package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

type jobLimitArgs struct {
	JobResourceLimits
	RootMount string `json:"root-mount,omitempty"`
}

type limitedRunner struct {
	*JobLimitConfig
	*execRunner
}

func newLimitedRunner(config *JobLimitConfig) (runner, error) {
	if (config.ResourceLimits.CPUMaxPeriod == 0) != (config.ResourceLimits.CPUMaxQuota == 0) {
		return nil, fmt.Errorf("must set either both or neither CPU limit")
	}
	// Set the default device number for empty-string device as the device of the
	// executable
	if limit := config.ResourceLimits.DeviceIOMax[""]; limit > 0 {
		delete(config.ResourceLimits.DeviceIOMax, "")
		var stat syscall.Stat_t
		if err := syscall.Stat(os.Args[0], &stat); err != nil {
			return nil, fmt.Errorf("getting device info for executable: %w", err)
		}
		config.ResourceLimits.DeviceIOMax[fmt.Sprintf("%v:%v", uint64(stat.Dev/256), uint64(stat.Dev%256))] = limit
	}
	return &limitedRunner{JobLimitConfig: config, execRunner: newRunner()}, nil
}

func (l *limitedRunner) start(j *Job) error {
	// JSON marshal the args as the first parameter
	jsonLimitArgs, err := json.Marshal(&jobLimitArgs{JobResourceLimits: l.ResourceLimits, RootMount: j.RootFS})
	if err != nil {
		return err
	}
	// Build command for child with the first param as the limit args, then the
	// rest as the command and args
	args := append([]string{"child-exec", string(jsonLimitArgs), j.Command}, j.Args...)
	cmd := exec.Command("/proc/self/exe", args...)
	// Add syscall args
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUTS | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	if l.Isolation.PID {
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWPID
	}
	if l.Isolation.Network {
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNET
	}
	if l.Isolation.Mount {
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWNS
	}
	err = l.startCmd(j, cmd)
	return err
}

// ExecLimitedChild is called via internal child-exec. Only returns a nil error
// on completion of the child with a 0 exit code. Otherwise returns an error
// starting or running the child. The error may be *exec.ExitError if the child
// ran to completion and gave a non-zero exit code.
func ExecLimitedChild(args []string) error {
	// Make sure there is the right arg amount and unmarshal the first
	if len(args) < 2 {
		return fmt.Errorf("invalid arg count")
	}
	var limitArgs jobLimitArgs
	if err := json.Unmarshal([]byte(args[0]), &limitArgs); err != nil {
		return fmt.Errorf("invalid child exec args: %w", err)
	}
	// Create container ID (even if there are no limits)
	containerID := uuid.New().String()
	// If the max period and max quota are present, limit CPU
	if limitArgs.CPUMaxPeriod > 0 && limitArgs.CPUMaxQuota > 0 {
		dir, err := writeCGroupSettings(containerID, "cpu",
			[]string{"cpu.cfs_period_us", strconv.FormatUint(limitArgs.CPUMaxPeriod, 10)},
			[]string{"cpu.cfs_quota_us", strconv.FormatUint(limitArgs.CPUMaxQuota, 10)},
		)
		if dir != "" {
			defer os.RemoveAll(dir)
		}
		if err != nil {
			return err
		}
	}
	// If memory max present, limit it
	if limitArgs.MemoryMax > 0 {
		dir, err := writeCGroupSettings(containerID, "memory",
			[]string{"memory.limit_in_bytes", strconv.FormatUint(limitArgs.MemoryMax, 10)},
			[]string{"memory.memsw.limit_in_bytes", strconv.FormatUint(limitArgs.MemoryMax, 10)},
		)
		if dir != "" {
			defer os.RemoveAll(dir)
		}
		if err != nil {
			return err
		}
	}
	// If device maxes exist, apply them
	if len(limitArgs.DeviceIOMax) > 0 {
		var readLimits, writeLimits strings.Builder
		for dev, bps := range limitArgs.DeviceIOMax {
			if readLimits.Len() > 0 {
				readLimits.WriteByte('\n')
				writeLimits.WriteByte('\n')
			}
			str := dev + "  " + strconv.FormatUint(bps, 10)
			readLimits.WriteString(str)
			writeLimits.WriteString(str)
		}
		dir, err := writeCGroupSettings(containerID, "blkio",
			[]string{"blkio.throttle.read_bps_device", readLimits.String()},
			[]string{"blkio.throttle.write_bps_device", writeLimits.String()},
		)
		if dir != "" {
			defer os.RemoveAll(dir)
		}
		if err != nil {
			return err
		}
	}
	// Pivot root if there is a root mount
	if limitArgs.RootMount != "" {
		if err := pivotRoot(limitArgs.RootMount); err != nil {
			return err
		}
	}
	cmd := exec.Command(args[1], args[2:]...)
	// While we don't need stdin, we can't use /dev/null because it may not be
	// mounted after pivot root
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func pivotRoot(target string) error {
	// Create /proc inside of root mount and then mount it
	procDir := filepath.Join(target, "proc")
	if err := os.MkdirAll(procDir, 0755); err != nil {
		return fmt.Errorf("creating proc in root mount: %w", err)
	}
	if err := syscall.Mount("proc", procDir, "proc", 0, ""); err != nil {
		return fmt.Errorf("mounting proc: %w", err)
	}
	// Mount ourself (pivot_root won't let us use same mount as current)
	if err := syscall.Mount(target, target, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("mounting root: %w", err)
	}
	// Create place inside root for old, then pivot and chdir
	pivotOld := filepath.Join(target, ".pivot_old")
	if err := os.MkdirAll(pivotOld, 0755); err != nil {
		return fmt.Errorf("creating pivot old dir: %w", err)
	}
	if err := syscall.PivotRoot(target, pivotOld); err != nil {
		return fmt.Errorf("calling pivot root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("changing root dir: %w", err)
	}
	// Unmount and remove the old pivot
	if err := syscall.Unmount("/.pivot_old", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmounting pivot old dir: %w", err)
	}
	if err := os.RemoveAll("/.pivot_old"); err != nil {
		return fmt.Errorf("removing pivot old dir: %w", err)
	}
	return nil
}

// If dir non-empty, regardless of error, caller should remove it when done.
// Each setting is two-string tuple (and slice can be mutated internally).
func writeCGroupSettings(containerID, controller string, settings ...[]string) (dir string, err error) {
	// Create dir if not there
	dir = filepath.Join("/sys/fs/cgroup", controller, "teleworker", containerID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating dir %v: %w", dir, err)
	}
	// Add my PID to settings as procs
	settings = append(settings, []string{"cgroup.procs", "0"})
	// Write each setting as a file
	for _, settingSet := range settings {
		if err := os.WriteFile(filepath.Join(dir, settingSet[0]), []byte(settingSet[1]), 0644); err != nil {
			return dir, fmt.Errorf("writing file %v: %w", filepath.Join(dir, settingSet[0]), err)
		}
	}
	return
}
