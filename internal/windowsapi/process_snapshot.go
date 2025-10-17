package windowsapi

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// NOTE: not used currently, but kept for reference.

// EtwProcessInfo holds basic information about a running process. 	windows.ProcessInformation
type EtwProcessInfo struct {
	PID              uint32
	ParentPID        uint32
	SessionID        uint32
	ExeFile          string
	FullDevicePath   string
	CreateTime       int64   // Windows FILETIME
	UniqueProcessKey uintptr // EPROCESS pointer
}

// SYSTEM_PROCESS_INFORMATION is a corrected, local definition of the
// SYSTEM_PROCESS_INFORMATION struct. We use this instead of the version from
// golang.org/x/sys/windows, which has an incorrect type for UniqueProcessKey
// in older versions. This layout matches the actual in-memory structure
// returned by the kernel on modern Windows versions.
type SYSTEM_PROCESS_INFORMATION struct {
	NextEntryOffset              uint32                  // Offset to the next entry in the array; 0 for last entry.
	NumberOfThreads              uint32                  // Number of threads in the process.
	WorkingSetPrivateSize        uint64                  // Private memory allocated and resident (since Vista).
	HardFaultCount               uint32                  // Number of hard faults (since Win7).
	NumberOfThreadsHighWatermark uint32                  // Peak number of threads ever running.
	CycleTime                    uint64                  // Sum of cycle time for all threads.
	CreateTime                   int64                   // 100-nanosecond intervals since process creation.
	UserTime                     int64                   // 100-nanosecond intervals in user mode.
	KernelTime                   int64                   // 100-nanosecond intervals in kernel mode.
	ImageName                    windows.NTUnicodeString // Executable image file name.
	BasePriority                 int32                   // Starting priority of the process.
	UniqueProcessID              uintptr                 // Process identifier.
	InheritedFromUniqueProcessID uintptr                 // Creator process identifier.
	HandleCount                  uint32                  // Number of open handles.
	SessionID                    uint32                  // Remote Desktop session identifier.
	UniqueProcessKey             uintptr                 // EPROCESS pointer (since Vista).
	PeakVirtualSize              uintptr                 // Peak virtual memory size (bytes).
	VirtualSize                  uintptr                 // Current virtual memory size (bytes).
	PageFaultCount               uint32                  // Total number of page faults.
	PeakWorkingSetSize           uintptr                 // Peak working set size (bytes).
	WorkingSetSize               uintptr                 // Current working set size (bytes).
	QuotaPeakPagedPoolUsage      uintptr                 // Peak paged pool usage (bytes).
	QuotaPagedPoolUsage          uintptr                 // Current paged pool usage (bytes).
	QuotaPeakNonPagedPoolUsage   uintptr                 // Peak nonpaged pool usage (bytes).
	QuotaNonPagedPoolUsage       uintptr                 // Current nonpaged pool usage (bytes).
	PagefileUsage                uintptr                 // Current pagefile usage (bytes).
	PeakPagefileUsage            uintptr                 // Peak pagefile usage (bytes).
	PrivatePageCount             uintptr                 // Number of private memory pages.
	ReadOperationCount           uint64                  // Total read operations.
	WriteOperationCount          uint64                  // Total write operations.
	OtherOperationCount          uint64                  // Total other I/O operations.
	ReadTransferCount            uint64                  // Total bytes read.
	WriteTransferCount           uint64                  // Total bytes written.
	OtherTransferCount           uint64                  // Total bytes transferred in other operations.
}

type KPRIORITY int32

type CLIENT_ID struct {
	UniqueProcess uintptr // HANDLE (pointer-sized)
	UniqueThread  uintptr // HANDLE (pointer-sized)
}

// SYSTEM_THREAD_INFORMATION contains information about a thread running on a system.
// https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-tsts/e82d73e4-cedb-4077-9099-d58f3459722f
type SYSTEM_THREAD_INFORMATION struct {
	KernelTime      int64     // Number of 100-nanosecond intervals spent executing kernel code.
	UserTime        int64     // Number of 100-nanosecond intervals spent executing user code.
	CreateTime      int64     // The date and time when the thread was created.
	WaitTime        uint32    // The current time spent in ready queue or waiting (depending on the thread state).
	StartAddress    uintptr   // The initial start address of the thread.
	ClientId        CLIENT_ID // The identifier of the thread and the process owning the thread.
	Priority        KPRIORITY // The dynamic priority of the thread.
	BasePriority    KPRIORITY // The starting priority of the thread.
	ContextSwitches uint32    // The total number of context switches performed.
	ThreadState     uint32    // The current state of the thread.
	WaitReason      uint32    // The current reason the thread is waiting.
}

type SYSTEM_EXTENDED_PROCESS_INFORMATION struct {
	SYSTEM_PROCESS_INFORMATION
	Threads [1]SYSTEM_THREAD_INFORMATION
}

// GetProcessSnapshot iterates through all running processes using NtQuerySystemInformation
// and returns a map of PID to ProcessInfo. This provides a fast and reliable "synthetic rundown"
// of active processes, replacing the older Toolhelp snapshot API.
func GetProcessSnapshot() (map[uint32]EtwProcessInfo, error) {
	var bufferSize uint32
	// First call to get the required buffer size. It is expected to fail.
	status := windows.NtQuerySystemInformation(windows.SystemExtendedProcessInformation, nil, 0, &bufferSize)
	if status != windows.STATUS_INFO_LENGTH_MISMATCH {
		return nil, fmt.Errorf("NtQuerySystemInformation failed to get buffer size: 0x%x", status)
	}

	// Allocate buffer. Loop in case the process list changes between calls, requiring a larger buffer.
	var buffer []byte
	for {
		buffer = make([]byte, bufferSize)
		status = windows.NtQuerySystemInformation(windows.SystemExtendedProcessInformation, unsafe.Pointer(&buffer[0]), bufferSize, &bufferSize)
		if status == windows.STATUS_INFO_LENGTH_MISMATCH {
			// Buffer was too small, resize and try again.
			continue
		}
		if status != nil { // 0 is STATUS_SUCCESS
			return nil, fmt.Errorf("NtQuerySystemInformation failed with status: 0x%x", status)
		}
		break
	}

	processes := make(map[uint32]EtwProcessInfo)
	offset := 0
	for {
		// Cast the current position in the buffer to our struct.
		pInfo := (*SYSTEM_PROCESS_INFORMATION)(unsafe.Pointer(&buffer[offset]))

		pid := uint32(pInfo.UniqueProcessID)
		// The Idle process (PID 0) and System process (PID 4) often have empty names; skip them.
		if pid != 0 {
			var imageName string
			if pInfo.ImageName.Length > 0 && pInfo.ImageName.Buffer != nil {
				// Create a slice of uint16s pointing to the string data within the buffer
				chars := unsafe.Slice(pInfo.ImageName.Buffer, pInfo.ImageName.Length/2)
				imageName = windows.UTF16ToString(chars)
			}

			// Getting the full path is a separate operation. We can keep using GetProcessImageFileName for this.
			fullPath, _ := GetProcessImageFileName(pid)

			processes[pid] = EtwProcessInfo{
				PID:              pid,
				ParentPID:        uint32(pInfo.InheritedFromUniqueProcessID),
				SessionID:        pInfo.SessionID,
				ExeFile:          imageName,
				FullDevicePath:   fullPath,
				CreateTime:       pInfo.CreateTime,
				UniqueProcessKey: uintptr(unsafe.Pointer(pInfo.UniqueProcessKey)),
			}
		}

		// Move to the next entry in the linked list. If offset is 0, we're at the end.
		if pInfo.NextEntryOffset == 0 {
			break
		}
		offset += int(pInfo.NextEntryOffset)
	}

	return processes, nil
}

// ProcessInfo holds basic information about a running process.
type EtwProcessInfo_old struct {
	PID            uint32
	ParentPID      uint32
	ExeFile        string
	FullDevicePath string
}

// GetProcessSnapshot iterates through all running processes using the CreateToolhelp32Snapshot API
// and returns a map of PID to ProcessInfo. This provides a "synthetic rundown" of active processes.
func GetProcessSnapshot_old() (map[uint32]EtwProcessInfo_old, error) {
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

	processes := make(map[uint32]EtwProcessInfo_old)
	for {
		exeFile := windows.UTF16ToString(pe32.ExeFile[:])
		fullPath, _ := GetProcessImageFileName(pe32.ProcessID)

		processes[pe32.ProcessID] = EtwProcessInfo_old{
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
