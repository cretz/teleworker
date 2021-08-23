package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/ncw/directio"
	"github.com/spf13/cobra"
)

// DiagnosticResult contains results from RunDiag.
type DiagnosticResult struct {
	PID               int     `json:"pid"`
	PPID              int     `json:"ppid"`
	NetInterfaceAvail bool    `json:"net_interface_avail"`
	Dir               string  `json:"dir"`
	CPUTaskNanos      int64   `json:"cpu_task_nanos"`
	DiskBPS           float64 `json:"disk_bps,omitempty"`
}

func diagCmd() *cobra.Command {
	var allocMem int
	var writeDisk bool
	cmd := &cobra.Command{
		Use:          "diag",
		Short:        "Internal utility to perform diagnostics and dump result",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			d, err := RunDiag(allocMem, writeDisk)
			if err != nil {
				return err
			}
			b, err := json.MarshalIndent(d, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(b))
			return nil
		},
	}
	cmd.Flags().IntVar(&allocMem, "alloc-mem", 0, "Amount of bytes to attempt to allocate")
	cmd.Flags().BoolVar(&writeDisk, "write-disk", false, "Test disk write speed")
	return cmd
}

// RunDiag runs diagnostic tests and returns diagnostic info.
func RunDiag(allocMem int, writeDisk bool) (*DiagnosticResult, error) {
	// Get common info
	res := &DiagnosticResult{
		PID:  os.Getpid(),
		PPID: os.Getppid(),
	}
	// See if there are any avail interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("getting interfaces: %w", err)
	}
	for _, iface := range ifaces {
		// Flag not 0 (tunl/sit) or local only (so not up, broadcast, etc), then
		// it's "available" by our definition
		if iface.Flags != 0 && iface.Flags != net.FlagLoopback {
			res.NetInterfaceAvail = true
			break
		}
	}
	// Cwd
	if res.Dir, err = os.Getwd(); err != nil {
		return nil, fmt.Errorf("getting current working dir: %w", err)
	}
	// If alloc requested, attempt via byte slice
	if allocMem > 0 {
		var buf bytes.Buffer
		buf.Write(make([]byte, allocMem))
	}
	// Simulate some CPU
	runtime.GOMAXPROCS(1)
	start := time.Now()
	for i := uint64(0); i < 500000000; i++ {
	}
	res.CPUTaskNanos = time.Since(start).Nanoseconds()
	// Write 5MB to disk via direct IO
	if writeDisk {
		f, err := directio.OpenFile("temp-file", os.O_WRONLY|os.O_CREATE|os.O_SYNC, 0644)
		if err != nil {
			return nil, fmt.Errorf("opening temp file: %w", err)
		}
		block := directio.AlignedBlock(directio.BlockSize)
		start = time.Now()
		const bytesTotal = 5 * 1024 * 1024
		// Capture error and return later so we can close on complete
		err = nil
		for i := 0; i < bytesTotal; i += len(block) {
			if _, err = f.Write(block); err != nil {
				break
			}
		}
		// Close the file and remove it, ignoring errors
		f.Close()
		os.Remove(f.Name())
		if err != nil {
			return nil, fmt.Errorf("writing temp file: %w", err)
		}
		timeTaken := time.Since(start)
		res.DiskBPS = bytesTotal / timeTaken.Seconds()
	}
	return res, nil
}
