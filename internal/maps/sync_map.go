package maps

import "sync"

// StdSyncMap wraps the standard library's sync.Map to implement the ConcurrentMap interface.
type StdSyncMap[K Integer, V any] struct {
	m sync.Map
}

// NewStdSyncMap creates a new StdSyncMap.
func NewStdSyncMap[K Integer, V any]() ConcurrentMap[K, V] {
	return &StdSyncMap[K, V]{}
}

func (m *StdSyncMap[K, V]) Load(key K) (V, bool) {
	val, ok := m.m.Load(key)
	if !ok {
		var zero V
		return zero, false
	}
	return val.(V), true
}

func (m *StdSyncMap[K, V]) Store(key K, value V) { m.m.Store(key, value) }
func (m *StdSyncMap[K, V]) Delete(key K)         { m.m.Delete(key) }

func (m *StdSyncMap[K, V]) LoadAndDelete(key K) (V, bool) {
	val, loaded := m.m.LoadAndDelete(key)
	if !loaded {
		var zero V
		return zero, false
	}
	return val.(V), true
}

func (m *StdSyncMap[K, V]) LoadOrStore(key K, valueFactory func() V) V {
	// This may call the factory function unnecessarily if the key already exists.
	val, _ := m.m.LoadOrStore(key, valueFactory())
	return val.(V)
}

// Update is a non-atomic simulation. It is vulnerable to race conditions.
func (m *StdSyncMap[K, V]) Update(key K, updateFunc func(value V, exists bool) (newValue V, keep bool)) {
	val, exists := m.m.Load(key)
	var oldVal V
	if exists {
		oldVal = val.(V)
	}
	newVal, keep := updateFunc(oldVal, exists)
	if keep {
		m.m.Store(key, newVal)
	} else if exists {
		m.m.Delete(key)
	}
}

func (m *StdSyncMap[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(func(key, value any) bool {
		return f(key.(K), value.(V))
	})
}
