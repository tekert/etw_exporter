package maps

import (
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	keySpace = 1024
)

// --- RWMutexMap (Benchmark Baseline Only) ---

type RWMutexMap[K Integer, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

func NewRWMutexMap[K Integer, V any]() ConcurrentMap[K, V] {
	return &RWMutexMap[K, V]{m: make(map[K]V)}
}
func (m *RWMutexMap[K, V]) Load(key K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	val, ok := m.m[key]
	return val, ok
}
func (m *RWMutexMap[K, V]) Store(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.m[key] = value
}
func (m *RWMutexMap[K, V]) Delete(key K) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, key)
}
func (m *RWMutexMap[K, V]) LoadAndDelete(key K) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, exists := m.m[key]
	if exists {
		delete(m.m, key)
	}
	return val, exists
}
func (m *RWMutexMap[K, V]) LoadOrStore(key K, valueFactory func() V) (V, bool) {
	m.mu.RLock()
	val, ok := m.m[key]
	m.mu.RUnlock()
	if ok {
		return val, true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Double-check in case another goroutine created it while we were waiting for the lock.
	val, ok = m.m[key]
	if ok {
		return val, true
	}
	val = valueFactory()
	m.m[key] = val
	return val, false
}
func (m *RWMutexMap[K, V]) Update(key K, updateFunc func(value V, exists bool) (newValue V, keep bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	oldVal, exists := m.m[key]
	newVal, keep := updateFunc(oldVal, exists)
	if keep {
		m.m[key] = newVal
	} else {
		delete(m.m, key)
	}
}
func (m *RWMutexMap[K, V]) Range(f func(key K, value V) bool) {
	m.mu.RLock()
	copiedMap := make(map[K]V, len(m.m))
	for k, v := range m.m {
		copiedMap[k] = v
	}
	m.mu.RUnlock()

	for k, v := range copiedMap {
		if !f(k, v) {
			return
		}
	}
}

// --- Benchmark Runners ---

// runMixedWorkloadBenchmark simulates N goroutines each performing a mix of operations.
func runMixedWorkloadBenchmark(b *testing.B, bm ConcurrentMap[uint32, *int64], readRatio int, writers int) {
	var v int64 = 1
	for i := range keySpace {
		bm.Store(uint32(i), &v)
	}
	b.ResetTimer()
	b.SetParallelism(writers)
	b.RunParallel(func(pb *testing.PB) {
		r := rand.New(rand.NewSource(rand.Int63()))
		for pb.Next() {
			key := r.Uint32() % keySpace
			if r.Intn(100) < readRatio {
				_, _ = bm.Load(key)
			} else {
				bm.Store(key, &v)
			}
		}
	})
}

// runLoadOrStoreBenchmark simulates the "context switch counter" pattern.
func runLoadOrStoreBenchmark(b *testing.B, bm ConcurrentMap[uint32, *atomic.Int64], writers int) {
	b.ResetTimer()
	b.SetParallelism(writers)
	b.RunParallel(func(pb *testing.PB) {
		r := rand.New(rand.NewSource(rand.Int63()))
		factory := func() *atomic.Int64 { return new(atomic.Int64) }
		for pb.Next() {
			key := r.Uint32() % keySpace
			counter, _ := bm.LoadOrStore(key, factory)
			counter.Add(1)
		}
	})
}

// runUpdateBenchmark simulates the "file object mapping" pattern.
func runUpdateBenchmark(b *testing.B, bm ConcurrentMap[uint32, map[uint64]struct{}], writers int) {
	b.ResetTimer()
	b.SetParallelism(writers)
	b.RunParallel(func(pb *testing.PB) {
		r := rand.New(rand.NewSource(rand.Int63()))
		for pb.Next() {
			key := r.Uint32() % keySpace
			fileObject := r.Uint64()
			bm.Update(key, func(val map[uint64]struct{}, exists bool) (map[uint64]struct{}, bool) {
				if !exists {
					val = make(map[uint64]struct{})
				}
				val[fileObject] = struct{}{}
				return val, true // Keep the entry
			})
		}
	})
}

// --- Main Benchmark Function ---

func BenchmarkMaps(b *testing.B) {
	workloads := []struct {
		name    string
		threads int
	}{
		{"1_Thread_(Win11_Sim)", 1},
		{"2_Threads_(Win10_Sim)", 2},
		{"Max_Threads_(Generic)", -1}, // -1 will use b.N
	}

	b.Run("Pattern_LoadOrStore_Counters", func(b *testing.B) {
		mapsToTest := []struct {
			name string
			m    ConcurrentMap[uint32, *atomic.Int64]
		}{
			{"SyncMap", NewStdSyncMap[uint32, *atomic.Int64]()},
			{"RWMutexMap", NewRWMutexMap[uint32, *atomic.Int64]()},
			{"ShardedMap", NewShardedMap[uint32, *atomic.Int64]()},
			{"CornelkHashMap", NewCornelkMap[uint32, *atomic.Int64]()},
			{"XSyncMapV4", NewXSyncMap[uint32, *atomic.Int64]()},
		}
		for _, wl := range workloads {
			b.Run(wl.name, func(b *testing.B) {
				for _, mt := range mapsToTest {
					b.Run(mt.name, func(b *testing.B) {
						runLoadOrStoreBenchmark(b, mt.m, wl.threads)
					})
				}
			})
		}
	})

	b.Run("Pattern_Update_NestedMap", func(b *testing.B) {
		mapsToTest := []struct {
			name string
			m    ConcurrentMap[uint32, map[uint64]struct{}]
		}{
			{"SyncMap", NewStdSyncMap[uint32, map[uint64]struct{}]()},
			{"RWMutexMap", NewRWMutexMap[uint32, map[uint64]struct{}]()},
			{"ShardedMap", NewShardedMap[uint32, map[uint64]struct{}]()},
			{"CornelkHashMap", NewCornelkMap[uint32, map[uint64]struct{}]()},
			{"XSyncMapV4", NewXSyncMap[uint32, map[uint64]struct{}]()},
		}
		for _, wl := range workloads {
			b.Run(wl.name, func(b *testing.B) {
				for _, mt := range mapsToTest {
					b.Run(mt.name, func(b *testing.B) {
						if mt.name == "SyncMap" || mt.name == "CornelkHashMap" {
							b.Skip("Skipping non-atomic Update implementation")
						}
						runUpdateBenchmark(b, mt.m, wl.threads)
					})
				}
			})
		}
	})

	b.Run("Pattern_LoadStore_Simple", func(b *testing.B) {
		mapsToTest := []struct {
			name string
			m    ConcurrentMap[uint32, *int64]
		}{
			{"SyncMap", NewStdSyncMap[uint32, *int64]()},
			{"RWMutexMap", NewRWMutexMap[uint32, *int64]()},
			{"ShardedMap", NewShardedMap[uint32, *int64]()},
			{"CornelkHashMap", NewCornelkMap[uint32, *int64]()},
			{"XSyncMapV4", NewXSyncMap[uint32, *int64]()},
		}
		for _, wl := range workloads {
			b.Run(wl.name, func(b *testing.B) {
				b.Run("ReadHeavy_90R_10W", func(b *testing.B) {
					for _, mt := range mapsToTest {
						b.Run(mt.name, func(b *testing.B) {
							runMixedWorkloadBenchmark(b, mt.m, 90, wl.threads)
						})
					}
				})
				b.Run("WriteHeavy_10R_90W", func(b *testing.B) {
					for _, mt := range mapsToTest {
						b.Run(mt.name, func(b *testing.B) {
							runMixedWorkloadBenchmark(b, mt.m, 10, wl.threads)
						})
					}
				})
			})
		}
	})
}
