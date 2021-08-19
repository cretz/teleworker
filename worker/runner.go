package worker

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"syscall"
)

// JobLimitConfig represents configuration for limiting jobs.
type JobLimitConfig struct {
	// Resource limits per job.
	ResourceLimits JobResourceLimits
	// Namespace isolation per job.
	Isolation JobIsolation
}

// JobResourceLimits represent per-job resource limits.
type JobResourceLimits struct {
	// Maximum amount of CPU microseconds to divy up.
	CPUMaxPeriod uint64 `json:"cpu_max_period,omitempty"`
	// Maximum amount of CPU microseconds, per period, that can be used.
	CPUMaxQuota uint64 `json:"cpu_max_quota,omitempty"`
	// Maximum amount of bytes, including swap, that can be allocated.
	MemoryMax uint64 `json:"memory_max,omitempty"`
	// Maximum read and write bytes per second per device. The key is
	// "major:minor" of the device, or empty string to default to the same device
	// this worker executable is running on.
	DeviceIOMax map[string]uint64 `json:"device_io_max,omitempty"`
}

// JobIsolation represents namespaces that should be isolated per job.
type JobIsolation struct {
	PID bool
	// TODO(cretz): Options for adding network interfaces?
	Network bool
	Mount   bool
}

type runner interface {
	// Guaranteed to have PID set on complete. The job is not used by any other
	// goroutines until this returns.
	start(*Job) error
}

type execRunner struct{}

func newRunner() *execRunner { return &execRunner{} }

func (e *execRunner) start(j *Job) error {
	// Cannot have root when calling exec runner direct
	if j.RootFS != "" {
		return fmt.Errorf("cannot have job root in non-limited runner")
	}
	return e.startCmd(j, exec.Command(j.Command, j.Args...))
}

func (e *execRunner) startCmd(j *Job, cmd *exec.Cmd) error {
	// Create pipes for stdout and stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("creating stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	j.PID = cmd.Process.Pid
	// Start pipes
	stdoutCh := startPipe(j, false /* stderr */, stdout)
	stderrCh := startPipe(j, true /* stderr */, stderr)
	// Asynchronously wait for completion
	go func() {
		// Wait for pipes to be complete before waiting for command to be complete.
		// Per docs if use Wait before waiting on pipes, we can lose data.
		<-stdoutCh
		<-stderrCh
		// Now wait on command completion
		var exitCode int
		err := cmd.Wait()
		if exitErr, _ := err.(*exec.ExitError); exitErr != nil {
			exitCode = exitErr.ExitCode()
		} else if err != nil {
			log.Printf("Child execution on job %v:%v failed without exit code: %v", j.Namespace, j.ID, err)
			exitCode = -1
		}
		// Mark done
		j.markDone(exitCode)
	}()
	// Asynchronously listen for stop requests
	go func() {
		for {
			var signal os.Signal
			select {
			case <-j.doneCtx.Done():
				return
			case <-j.stopCtx.Done():
				signal = syscall.SIGTERM
			case <-j.forceStopCtx.Done():
				signal = syscall.SIGKILL
			}
			// TODO(cretz): Handle unexpected error?
			cmd.Process.Signal(signal)
		}
	}()
	return nil
}

// Returns channel that is completed when done
func startPipe(j *Job, stderr bool, r io.Reader) <-chan struct{} {
	done := make(chan struct{})
	// Read asynchronously until error
	go func() {
		defer close(done)
		// TODO(cretz): Preferred buffer size?
		b := make([]byte, 1024)
		for {
			n, err := r.Read(b)
			// If any read, send it regardless of error
			if n > 0 {
				j.updateOutput(stderr, b[:n])
			}
			// If there's an error, we're done
			if err != nil {
				if err != io.EOF {
					log.Printf("Got non-EOF error on job %v:%v output: %v", j.Namespace, j.ID, err)
				}
				return
			}
		}
	}()
	return done
}
