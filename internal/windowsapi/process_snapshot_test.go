//go:build windows
// +build windows

package windowsapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetProcessSnapshot_Basic(t *testing.T) {
	procs, err := GetProcessSnapshot()
	if err != nil {
		t.Fatalf("GetProcessSnapshot returned error: %v", err)
	}
	if len(procs) == 0 {
		t.Fatalf("expected at least one process in snapshot, got 0")
	}
}

func TestGetProcessSnapshot_CurrentProcessPresent(t *testing.T) {
	procs, err := GetProcessSnapshot()
	if err != nil {
		t.Fatalf("GetProcessSnapshot returned error: %v", err)
	}

	pid := uint32(os.Getpid())
	info, ok := procs[pid]
	if !ok {
		// Provide some context listing a few PIDs to help debugging.
		sample := make([]uint32, 0, 10)
		i := 0
		for k := range procs {
			if i >= 10 {
				break
			}
			sample = append(sample, k)
			i++
		}
		t.Fatalf("current PID %d not found in snapshot; sample PIDs: %v", pid, sample)
	}

	if info.PID != pid {
		t.Fatalf("info.PID mismatch: got %d expected %d", info.PID, pid)
	}

	// ExeFile should contain the executable base name for the current process.
	exePath, err := os.Executable()
	if err != nil {
		t.Logf("os.Executable returned error: %v; skipping name check", err)
		return
	}
	base := filepath.Base(exePath)
	// Image name reported by NtQuerySystemInformation may be just the filename.
	if info.ExeFile == "" {
		t.Fatalf("expected ExeFile for current process to be non-empty")
	}
	if !strings.EqualFold(info.ExeFile, base) && !strings.HasSuffix(strings.ToLower(base), strings.ToLower(info.ExeFile)) && !strings.HasSuffix(strings.ToLower(info.ExeFile), strings.ToLower(base)) {
		t.Fatalf("ExeFile mismatch: got %q expected to match %q", info.ExeFile, base)
	}
}
