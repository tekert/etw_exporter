package maps

// mapImplementation controls the default concurrent map used across the application.
// Valid options: "xsync", "sharded", "cornelk", "sync".
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
	Load(key K) (V, bool)
	Store(key K, value V)
	Delete(key K)
	LoadAndDelete(key K) (V, bool)
	LoadOrStore(key K, valueFactory func() V) V
	Update(key K, updateFunc func(value V, exists bool) (newValue V, keep bool))
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
