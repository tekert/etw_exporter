package windowsapi

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessInfo holds basic information about a running process obtained via the Toolhelp snapshot API.
type ProcessInfo struct {
	PID            uint32
	ParentPID      uint32
	ExeFile        string
	FullDevicePath string
}

// TODO: replace with NtQuerySystemInformation

// GetProcessSnapshot iterates through all running processes using the CreateToolhelp32Snapshot API
// and returns a map of PID to ProcessInfo. This provides a "synthetic rundown" of active processes.
func GetProcessSnapshot() (map[uint32]ProcessInfo, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	var pe32 windows.ProcessEntry32
	pe32.Size = uint32(unsafe.Sizeof(pe32))
	if err := windows.Process32First(snapshot, &pe32); err != nil {
		return nil, err
	}

	processes := make(map[uint32]ProcessInfo)
	for {
		exeFile := windows.UTF16ToString(pe32.ExeFile[:])
		fullPath, _ := GetProcessImageFileName(pe32.ProcessID)

		processes[pe32.ProcessID] = ProcessInfo{
			PID:            pe32.ProcessID,
			ParentPID:      pe32.ParentProcessID,
			ExeFile:        exeFile,
			FullDevicePath: fullPath,
		}

		if err := windows.Process32Next(snapshot, &pe32); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				break
			}
			return nil, err
		}
	}
	return processes, nil
}
