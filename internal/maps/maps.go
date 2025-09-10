package maps

// mapImplementation controls the default concurrent map used across the application.
// Valid options: "xsync", "sharded", "cornelk", "sync". (cornelk is not safe)
const mapImplementation = "xsync"

// Integer is a constraint that permits any integer type.
// All integer types are comparable.
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// ConcurrentMap defines a generic, thread-safe map interface for integer keys.
// This abstraction allows swapping the underlying implementation without
// changing the business logic in the collectors.
type ConcurrentMap[K Integer, V any] interface {
	// Load returns the value stored in the map for a key, or nil if no
	// value is present. The ok result is true if the key was found in the map.
	Load(key K) (value V, ok bool)

	// Store sets the value for a key.
	Store(key K, value V)

	// Delete deletes the value for a key.
	Delete(key K)

	// LoadAndDelete deletes the value for a key, returning the previous value if any.
	// The loaded result is true if the key was found in the map.
	LoadAndDelete(key K) (value V, loaded bool)

	// LoadOrStore returns the existing value for the key if present.
	// Otherwise, it calls the provided valueFactory function to generate a new value,
	// stores it in the map, and returns the new value. The entire call is atomic,
	// and the factory function is only called if the key is not present.
	LoadOrStore(key K, valueFactory func() V) (actual V)

	// Update provides an atomic read-modify-write operation for a key.
	// The updateFunc is called with the current value and its existence status.
	// It should return the new value and a boolean indicating whether to keep (true)
	// or delete (false) the entry. The entire operation is atomic.
	Update(key K, updateFunc func(value V, exists bool) (newValue V, keep bool))

	// Range calls f sequentially for each key and value present in the map.
	// If f returns false, range stops the iteration.
	//
	// Range does not necessarily correspond to a consistent snapshot of the Map's
	// contents: it is not guaranteed to see values stored concurrently, and it may
	// visit keys which are deleted concurrently.
	Range(f func(key K, value V) bool)
}

// NewConcurrentMap is a factory that returns the default concurrent map implementation
// for integer-keyed maps.
// The implementation can be changed by modifying the mapImplementation constant.
func NewConcurrentMap[K Integer, V any]() ConcurrentMap[K, V] {
	switch mapImplementation {
	case "xsync":
		return NewXSyncMap[K, V]()
	case "sharded":
		return NewShardedMap[K, V]()
	case "cornelk":
		return NewCornelkMap[K, V]()
	case "sync":
		return NewStdSyncMap[K, V]()
	default:
		// Default to the highest-performing implementation as a safe fallback.
		return NewXSyncMap[K, V]()
	}
}
