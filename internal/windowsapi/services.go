package windowsapi

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
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

// QueryServiceNameByPid finds the name of a service running with a specific process ID.
// This function uses documented SCM APIs and is a reliable way to find a service name
// when a tag is not available. It is slower than QueryServiceNameByTag because it
// must enumerate all system services.
func QueryServiceNameByPid(pid uint32) (string, error) {
	if pid == 0 {
		return "", fmt.Errorf("invalid pid: 0")
	}

	m, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("failed to connect to SCM: %w", err)
	}
	defer m.Disconnect()

	services, err := m.ListServices()
	if err != nil {
		return "", fmt.Errorf("failed to list services: %w", err)
	}

	for _, serviceName := range services {
		s, err := m.OpenService(serviceName)
		if err != nil {
			continue // Can't open, might be permissions or service is being deleted
		}

		status, err := s.Query()
		s.Close() // Close handle immediately after use
		if err != nil {
			continue
		}

		if status.State == svc.Running && status.ProcessId == uint32(pid) {
			// Found a running service with the matching PID.
			return serviceName, nil
		}
	}

	return "", fmt.Errorf("no running service found for pid %d", pid)
}

// ResolveServiceName attempts to find a service name using the most efficient methods first.
// It is the recommended function for resolving service names from ETW events that provide
// both a Process ID and a SubProcessTag. (But if we don't lose events then this is not needed.)
//
// It first tries the fast, tag-based lookup. If that fails (e.g., the tag is 0),
// it falls back to enumerating all system services to find a PID match.
// It returns an empty string if no service can be found.
func ResolveServiceName(pid, tag uint32) string {
	if pid == 0 {
		return ""
	}

	// Strategy 1: Fast path using the process-specific tag. This works for both
	// shared-host (svchost.exe) and own-process services.
	if tag != 0 {
		name, err := QueryServiceNameByTag(pid, tag)
		if err == nil {
			return name
		}
	}

	// Strategy 2: Fallback for when tag lookup fails or tag is unavailable.
	// This is slower but reliable.
	name, err := QueryServiceNameByPid(pid)
	if err != nil {
		return "" // Return empty string on failure
	}
	return name
}
