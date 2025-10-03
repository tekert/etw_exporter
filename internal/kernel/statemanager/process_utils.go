package statemanager

// FNV-1a constants for 64-bit hash
const (
	fnv1_offset64 = 14695981039346656037
	fnv1_prime64  = 1099511628211
)

// // Boot-scoped globals (read once at startup)
// var (
// 	bootId        = uint64(*(*uint32)(unsafe.Pointer(uintptr(0x7ffe02C4))))
// 	bootStartTime = time.Now().UnixNano() // Nanosecond timestamp when this process started
// )

// GenerateStartKey creates a unique StartKey from ETW event data.
// This works even when the process is already terminated and cannot be opened.
// The key is unique within a boot session and handles PID reuse correctly by
// incorporating the unique EPROCESS kernel pointer.
// This implementation is a non-allocating version of FNV-1a.
func GenerateStartKey(pid uint32, parentPid uint32, eprocessPtr uint64) uint64 {
	if eprocessPtr == 0 {
		return 0 // Invalid
	}

	// This is a manual, non-allocating implementation of the FNV-1a hash algorithm.
	// It avoids heap allocations from `fnv.New64a()` and `binary.Write` which is
	// critical as this function can be on the hot path.
	hash := uint64(fnv1_offset64)

	// Add EPROCESS pointer (8 bytes)
	hash = hashUpdateUint64(hash, eprocessPtr)

	// Add PID (4 bytes)
	hash = hashUpdateUint32(hash, pid)

	// Add ParentPID (4 bytes)
	hash = hashUpdateUint32(hash, parentPid)

	return hash
}

// hashUpdateUint64 incorporates an 8-byte unsigned integer into the FNV-1a hash.
func hashUpdateUint64(hash uint64, v uint64) uint64 {
	hash ^= (v >> 0) & 0xff
	hash *= fnv1_prime64
	hash ^= (v >> 8) & 0xff
	hash *= fnv1_prime64
	hash ^= (v >> 16) & 0xff
	hash *= fnv1_prime64
	hash ^= (v >> 24) & 0xff
	hash *= fnv1_prime64
	hash ^= (v >> 32) & 0xff
	hash *= fnv1_prime64
	hash ^= (v >> 40) & 0xff
	hash *= fnv1_prime64
	hash ^= (v >> 48) & 0xff
	hash *= fnv1_prime64
	hash ^= (v >> 56) & 0xff
	hash *= fnv1_prime64
	return hash
}

// hashUpdateUint32 incorporates a 4-byte unsigned integer into the FNV-1a hash.
func hashUpdateUint32(hash uint64, v uint32) uint64 {
	hash ^= uint64((v >> 0) & 0xff)
	hash *= fnv1_prime64
	hash ^= uint64((v >> 8) & 0xff)
	hash *= fnv1_prime64
	hash ^= uint64((v >> 16) & 0xff)
	hash *= fnv1_prime64
	hash ^= uint64((v >> 24) & 0xff)
	hash *= fnv1_prime64
	return hash
}
