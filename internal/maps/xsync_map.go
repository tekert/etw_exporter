package maps

import "github.com/puzpuzpuz/xsync/v4"

// XSyncMap is a generic, concurrent map that implements the ConcurrentMap interface
// using the highly optimized puzpuzpuz/xsync/v4 library.
type XSyncMap[K Integer, V any] struct {
	m *xsync.Map[K, V]
}

// NewXSyncMap creates a new XSyncMap, returning it as a ConcurrentMap.
func NewXSyncMap[K Integer, V any]() ConcurrentMap[K, V] {
	return &XSyncMap[K, V]{m: xsync.NewMap[K, V]()}
}

// Load returns the value for a given key.
func (m *XSyncMap[K, V]) Load(key K) (V, bool) {
	return m.m.Load(key)
}

// Store sets the value for a given key.
func (m *XSyncMap[K, V]) Store(key K, value V) {
	m.m.Store(key, value)
}

// Delete removes a key from the map.
func (m *XSyncMap[K, V]) Delete(key K) {
	m.m.Delete(key)
}

// LoadAndDelete deletes a key and returns the value it was associated with.
func (m *XSyncMap[K, V]) LoadAndDelete(key K) (V, bool) {
	return m.m.LoadAndDelete(key)
}

// LoadOrStore uses the efficient LoadOrCompute method for a factory-based get-or-create.
func (m *XSyncMap[K, V]) LoadOrStore(key K, valueFactory func() V) (V, bool) {
	// Use LoadOrCompute as it correctly returns the 'loaded' boolean that matches
	// our interface contract, unlike the 'Compute' method which returns an 'ok'
	// boolean (if the key exists AFTER the operation).
	return m.m.LoadOrCompute(key, func() (V, bool) {
		// The factory for LoadOrCompute returns (value, cancel).
		// We never want to cancel, so we always return false.
		return valueFactory(), false
	})
}

// Update uses the efficient Compute method to atomically update an entry.
func (m *XSyncMap[K, V]) Update(key K, updateFunc func(value V, exists bool) (newValue V, keep bool)) {
	m.m.Compute(key, func(oldValue V, loaded bool) (newValue V, op xsync.ComputeOp) {
		newVal, keep := updateFunc(oldValue, loaded)
		if keep {
			return newVal, xsync.UpdateOp
		}
		var zero V // Value is ignored on delete.
		return zero, xsync.DeleteOp
	})
}

// Range iterates over all items in the map.
func (m *XSyncMap[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(f)
}
