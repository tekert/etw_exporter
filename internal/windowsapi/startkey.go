package windowsapi

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// bootId2 = uint64(*(*uint32)(unsafe.Pointer(uintptr(0x7ffe02C4))))
var bootIdShift = bootId << 48

// The full start key is a combination of the boot session ID and a process-specific
// sequence number.
func GetProcessStartKey(handle windows.Handle) (startkey uint64, err error) {
	// Get the process-specific sequence number.
	var sequenceNumber uint64
	var returnLength uint32
	err = windows.NtQueryInformationProcess(
		handle,
		processSequenceNumber,
		unsafe.Pointer(&sequenceNumber),
		uint32(unsafe.Sizeof(sequenceNumber)),
		&returnLength)
	if err != nil { // NT_SUCCESS is 0
		return 0, err
	}
	// Combine with the system boot ID to form the full key.
	startkey = bootIdShift | sequenceNumber

	return startkey, nil
}

// The full start key is a combination of the boot session ID and a process-specific
// sequence number
func GetStartKeyFromSequence(sequenceNumber uint64) (startkey uint64) {
	// Combine with the system boot ID to form the full key.
	return  bootIdShift | sequenceNumber
}
