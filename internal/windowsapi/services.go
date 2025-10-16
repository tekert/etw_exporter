package windowsapi

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// SC_SERVICE_TAG_QUERY_TYPE
const (
	TAG_INFO_NAME_FROM_TAG            uint32 = 1 // From Tag Information
	TAG_INFO_NAMES_REFERENCING_MODULE uint32 = 2 // Referencing Module
	TAG_INFO_NAME_TAG_MAPPING         uint32 = 3 // Tag Mapping Information
)

// SC_SERVICE_TAG_QUERY is the structure passed to I_QueryTagInformation.
// This layout is based on reverse engineering by Alex Ionescu.
// Source: https://web.archive.org/web/20180104183311/http://www.wj32.org/wp/2010/03/30/howto-use-i_querytaginformation/
type SC_SERVICE_TAG_QUERY struct {
	ProcessId  uint32
	ServiceTag uint32
	Reserved   uint32  // Must be 0
	Buffer     *uint16 // Receives a pointer to the service name string
}

var (
	advapiDll = syscall.MustLoadDLL("advapi32.dll")

	// I_QueryTagInformation is an undocumented API for querying service information from tags
	// This API has been stable since Windows Vista
	i_QueryTagInformation = advapiDll.MustFindProc("I_QueryTagInformation")
)

// QueryServiceNameByTag resolves a service name from a SubProcessTag within a specific process context.
// The SubProcessTag is a unique identifier assigned by the Service Control Manager (SCM)
// to each service when it's registered.
//
// This function uses the undocumented but stable I_QueryTagInformation API to perform the lookup.
// The provided PID acts as the context for the lookup. This should be the PID of the process
// hosting the service (e.g., svchost.exe or a dedicated service executable).
//
// Implementation based on research from:
//
//   - https://wj32.org/wp/2010/03/30/howto-use-i_querytaginformation/
//
//   - https://github.com/3gstudent/Windows-EventLog-Bypass
//
//   - https://artofpwn.com/2017/06/05/phant0m-killing-windows-event-log.html
//
//     DWORD I_QueryTagInformation(
//     _In_opt_ PVOID Unknown,           // Always NULL
//     _In_     SC_SERVICE_TAG_QUERY_TYPE InfoLevel,
//     _Inout_  PVOID TagInfo
//
//     );
func QueryServiceNameByTag(pid, tag uint32) (string, error) {
	if tag == 0 {
		return "", fmt.Errorf("invalid tag: 0")
	}
	if pid == 0 {
		return "", fmt.Errorf("invalid pid: 0")
	}

	var query SC_SERVICE_TAG_QUERY
	query.ProcessId = pid
	query.ServiceTag = tag
	// query.Unknown is 0 by default
	// query.Buffer is nil by default

	ret, _, _ := syscall.SyscallN(
		i_QueryTagInformation.Addr(),
		uintptr(0), // NULL for first parameter
		uintptr(TAG_INFO_NAME_FROM_TAG),
		uintptr(unsafe.Pointer(&query)),
	)
	if ret != 0 {
		// Non-zero return indicates error. The return value is the Windows error code.
		// Common errors:
		// - ERROR_INVALID_PARAMETER (87): Tag is invalid for the given process, or the query struct is malformed.
		// - ERROR_SERVICE_DOES_NOT_EXIST (1060): Service has been deleted.
		return "", fmt.Errorf("I_QueryTagInformation failed: %w (pid=%d, tag=%d)", syscall.Errno(ret), pid, tag)
	}

	// Check if we got a valid name pointer
	if query.Buffer == nil {
		return "", fmt.Errorf("no service name returned for pid %d, tag %d", pid, tag)
	}

	// Convert the UTF-16 string to Go string
	serviceName := windows.UTF16PtrToString(query.Buffer)

	// Free the memory allocated by the API
	// The API uses LocalAlloc, so we must use LocalFree
	if query.Buffer != nil {
		syscall.LocalFree(syscall.Handle(unsafe.Pointer(query.Buffer)))
	}

	if serviceName == "" {
		return "", fmt.Errorf("empty service name for pid %d, tag %d", pid, tag)
	}

	return serviceName, nil
}

// GetRunningServicesSnapshot enumerates all services currently in the "Running" state and returns
// a map of their Process ID to their service name. This is useful for performing a one-time,
// proactive enrichment of process data at application startup, ensuring that service names
// are available even for dormant services whose threads have not yet emitted ETW events.
func GetRunningServicesSnapshot() (map[uint32]string, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to SCM: %w", err)
	}
	defer m.Disconnect()

	// Initial buffer size, will be resized if needed.
	var bytesNeeded, servicesReturned uint32
	bufSize := uint32(16384) // Start with 16KB
	buf := make([]byte, bufSize)

	// We are interested in all running services.
	const serviceType = windows.SERVICE_WIN32
	const serviceState = windows.SERVICE_ACTIVE // Corresponds to "Running"

	for {
		err = windows.EnumServicesStatusEx(
			m.Handle,
			windows.SC_ENUM_PROCESS_INFO,
			serviceType,
			serviceState,
			&buf[0],
			bufSize,
			&bytesNeeded,
			&servicesReturned,
			nil,
			nil,
		)
		if err == nil {
			break // Success
		}

		if err == syscall.ERROR_MORE_DATA {
			// If the buffer was too small, resize it and try again.
			bufSize = bytesNeeded
			buf = make([]byte, bufSize)
			continue
		}

		if err != nil {
			return nil, fmt.Errorf("EnumServicesStatusEx failed with unexpected error: %w", err)
		}
	}

	if servicesReturned == 0 {
		return make(map[uint32]string), nil // No running services found.
	}

	// The buffer contains an array of ENUM_SERVICE_STATUS_PROCESSW structs.
	// We need to interpret this byte slice as a slice of those structs.
	services := unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(
		unsafe.Pointer(&buf[0])), servicesReturned)

	serviceMap := make(map[uint32]string)
	for i := 0; i < int(servicesReturned); i++ {
		service := &services[i]
		pid := service.ServiceStatusProcess.ProcessId
		if pid != 0 {
			serviceName := windows.UTF16PtrToString(service.ServiceName)
			// A single process (like svchost.exe) can host multiple services.
			// This will overwrite previous entries for the same PID if found,
			// which is acceptable as we just need one valid service name for enrichment.
			serviceMap[pid] = serviceName
		}
	}

	return serviceMap, nil
}
