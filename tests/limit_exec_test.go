// +build linux

package tests

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/cretz/teleworker/cmd"
	"github.com/stretchr/testify/require"
)

func TestExecLimit(t *testing.T) {
	// Try with limits first
	limitRes, err := execDiag(t, false, "--write-disk")
	if err != nil {
		t.Fatal(err)
	}
	// Try without limits
	noLimitRes, err := execDiag(t, true, "--write-disk")
	if err != nil {
		t.Fatal(err)
	}
	// Confirm limit settings
	require.Equal(t, 1, limitRes.PPID)
	require.False(t, limitRes.NetInterfaceAvail)
	require.Equal(t, "/", limitRes.Dir)
	// Make sure bps is less than 1MB/s
	require.Less(t, limitRes.DiskBPS, float64(1024*1024))
	// Confirm no-limit settings
	require.NotEqual(t, 1, noLimitRes.PPID)
	require.True(t, noLimitRes.NetInterfaceAvail)
	require.NotEqual(t, "/", noLimitRes.Dir)
	// Make sure bps is more than 1MB/s
	require.Greater(t, noLimitRes.DiskBPS, float64(1024*1024))
	// CPU on limited one should be at least 2x longer than unlimited (in
	// actuality it's 5x since it defaults at 0.2 cores)
	require.Greater(t, limitRes.CPUTaskNanos, noLimitRes.CPUTaskNanos*2)
	// Try to alloc 75MB with limits and without
	_, err = execDiag(t, false, "--alloc-mem", strconv.FormatUint(75*1024*1024, 10))
	require.Error(t, err)
	_, err = execDiag(t, true, "--alloc-mem", strconv.FormatUint(75*1024*1024, 10))
	require.NoError(t, err)
}

func execDiag(t *testing.T, withoutLimits bool, diagArgs ...string) (*cmd.DiagnosticResult, error) {
	_, currFile, _, _ := runtime.Caller(0)
	modDir := filepath.Join(currFile, "../..")
	// Create a temp dir and remove when done
	tmpDir, err := os.MkdirTemp("", "exec-test-")
	if err != nil {
		return nil, fmt.Errorf("failed making temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	exe := filepath.Join(tmpDir, "teleworker")
	// Compile the teleworker statically linked into the temp dir
	buildCmd := exec.Command("go", "build", "-tags", "osusergo,netgo", "-o", exe)
	buildCmd.Dir = modDir
	b, err := buildCmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed building static teleworker: %w\nOutput:\n%s", err, b)
	}
	// Prepare args to direct exec our own diag command
	args := []string{"direct-exec"}
	if withoutLimits {
		args = append(args, "--without-limits", "--", exe, "diag")
	} else {
		args = append(args, "--root", tmpDir, "--", "/teleworker", "diag")
	}
	args = append(args, diagArgs...)
	// Run and unmarshal result
	t.Logf("Running %v with args %v", exe, args)
	out, err := exec.Command(exe, args...).CombinedOutput()
	t.Logf("Output:\n%s", out)
	if err != nil {
		return nil, err
	}
	var diagRes cmd.DiagnosticResult
	if err := json.Unmarshal(out, &diagRes); err != nil {
		return nil, fmt.Errorf("failed unmarshaling output: %w\nCombined output:\n%s", err, out)
	}
	return &diagRes, nil
}
