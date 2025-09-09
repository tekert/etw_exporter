package maps

import (
	"sync"
)

// --- ShardedMap Implementation ---

const numShards = 64 // Default shard count, must be a power of 2.

// shard represents a single partition of the map, protected by its own lock.
type shard[K Integer, V any] struct {
	sync.RWMutex
	m map[K]V
}

// ShardedMap is a generic, concurrent, sharded map optimized for mixed read/write workloads.
// It uses generics to provide type safety and avoids the overhead of interface conversions
// found in sync.Map. The key type K must be an integer.
// It implements the ConcurrentMap interface.
type ShardedMap[K Integer, V any] struct {
	shards [numShards]shard[K, V]
}

// NewShardedMap creates and initializes a new ShardedMap, returning it as a ConcurrentMap.
func NewShardedMap[K Integer, V any]() ConcurrentMap[K, V] {
	m := &ShardedMap[K, V]{}
	for i := 0; i < numShards; i++ {
		m.shards[i].m = make(map[K]V)
	}
	return m
}

// getShard returns the specific shard for a given key.
// It uses a fast bitwise AND operation on the key's hash.
func (m *ShardedMap[K, V]) getShard(key K) *shard[K, V] {
	// Since K is constrained to Integer, this conversion is safe and fast.
	return &m.shards[uint64(key)&(numShards-1)]
}

// Load returns the value for a given key.
func (m *ShardedMap[K, V]) Load(key K) (V, bool) {
	shard := m.getShard(key)
	shard.RLock()
	defer shard.RUnlock()
	val, exists := shard.m[key]
	return val, exists
}

// Store sets the value for a given key.
func (m *ShardedMap[K, V]) Store(key K, value V) {
	shard := m.getShard(key)
	shard.Lock()
	defer shard.Unlock()
	shard.m[key] = value
}

// Delete removes a key from the map.
func (m *ShardedMap[K, V]) Delete(key K) {
	shard := m.getShard(key)
	shard.Lock()
	defer shard.Unlock()
	delete(shard.m, key)
}

// LoadAndDelete deletes a key and returns the value it was associated with.
// The loaded result is true if the key was found in the map.
func (m *ShardedMap[K, V]) LoadAndDelete(key K) (V, bool) {
	shard := m.getShard(key)
	shard.Lock()
	defer shard.Unlock()
	val, exists := shard.m[key]
	if exists {
		delete(shard.m, key)
	}
	return val, exists
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it calls the valueFactory to create a new value, stores it, and returns it.
// This is useful for "get or create" patterns.
func (m *ShardedMap[K, V]) LoadOrStore(key K, valueFactory func() V) V {
	shard := m.getShard(key)
	shard.RLock()
	val, exists := shard.m[key]
	shard.RUnlock()

	if exists {
		return val
	}

	// Fallback to a full lock to create the entry.
	shard.Lock()
	defer shard.Unlock()
	// Double-check in case another goroutine created it while we were waiting for the lock.
	if val, exists := shard.m[key]; exists {
		return val
	}

	val = valueFactory()
	shard.m[key] = val
	return val
}

// Update provides a safe way to read, modify, and write a value for a key under a lock.
// The updateFunc receives the current value (or zero value if not present) and its existence.
// It should return the new value and a boolean indicating whether to keep (true) or delete (false) the entry.
func (m *ShardedMap[K, V]) Update(key K, updateFunc func(value V, exists bool) (newValue V, keep bool)) {
	shard := m.getShard(key)
	shard.Lock()
	defer shard.Unlock()
	oldVal, exists := shard.m[key]
	newVal, keep := updateFunc(oldVal, exists)
	if keep {
		shard.m[key] = newVal
	} else if exists {
		delete(shard.m, key)
	}
}

// Range iterates over all items in the map and calls the provided function for each.
// Iteration stops if the function returns false.
// To prevent deadlocks, it does not hold the lock while calling the function.
func (m *ShardedMap[K, V]) Range(f func(key K, value V) bool) {
	for i := 0; i < numShards; i++ {
		shard := &m.shards[i]
		shard.RLock()

		// Create copies to iterate over without holding the lock.
		keys := make([]K, 0, len(shard.m))
		values := make([]V, 0, len(shard.m))
		for k, v := range shard.m {
			keys = append(keys, k)
			values = append(values, v)
		}
		shard.RUnlock()

		for j := range keys {
			if !f(keys[j], values[j]) {
				return
			}
		}
	}
}
