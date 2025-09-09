package maps

import "github.com/cornelk/hashmap"

// CornelkMap wraps the cornelk/hashmap to implement the ConcurrentMap interface.
type CornelkMap[K Integer, V any] struct {
	m *hashmap.Map[K, V]
}

// NewCornelkMap creates a new CornelkMap.
func NewCornelkMap[K Integer, V any]() ConcurrentMap[K, V] {
	return &CornelkMap[K, V]{m: hashmap.New[K, V]()}
}

func (m *CornelkMap[K, V]) Load(key K) (V, bool) {
	val, ok := m.m.Get(key)
	return val, ok
}
func (m *CornelkMap[K, V]) Store(key K, value V) { m.m.Set(key, value) }
func (m *CornelkMap[K, V]) Delete(key K)         { m.m.Del(key) }

// LoadAndDelete is a non-atomic simulation. It is vulnerable to race conditions.
func (m *CornelkMap[K, V]) LoadAndDelete(key K) (V, bool) {
	val, ok := m.m.Get(key)
	if ok {
		m.m.Del(key)
	}
	return val, ok
}

func (m *CornelkMap[K, V]) LoadOrStore(key K, valueFactory func() V) V {
	val, _ := m.m.GetOrInsert(key, valueFactory())
	return val
}

// Update is a non-atomic simulation. It is vulnerable to race conditions.
func (m *CornelkMap[K, V]) Update(key K, updateFunc func(value V, exists bool) (newValue V, keep bool)) {
	val, exists := m.m.Get(key)
	newVal, keep := updateFunc(val, exists)
	if keep {
		m.m.Set(key, newVal)
	} else if exists {
		m.m.Del(key)
	}
}

func (m *CornelkMap[K, V]) Range(f func(key K, value V) bool) { m.m.Range(f) }
