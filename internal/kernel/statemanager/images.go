package statemanager

import (
	"path/filepath"
	"time"
)

// ImageInfo holds the minimal information needed for high-frequency
// address-to-name lookups, with an interned (shared) image name string.
type ImageInfo struct {
	ImageBase uint64 // Base address of the loaded image.
	ImageSize uint64
	ImageName string // Interned file name
}

// --- Unified Image Management ---

// internImageName returns a canonical, shared string for the given image name.
// This function must be called while holding a lock on internMutex.
func (sm *KernelStateManager) internImageName(name string) string {
	if interned, exists := sm.internedImageNames[name]; exists {
		return interned
	}
	sm.internedImageNames[name] = name
	return name
}

// AddImage adds minimal image information to the store.
func (sm *KernelStateManager) AddImage(imageBase, imageSize uint64, fileName string) {
	if imageBase == 0 || imageSize == 0 {
		return // Ignore invalid image data
	}

	sm.imagesMutex.Lock()
	defer sm.imagesMutex.Unlock()

	// Check for existence to handle duplicate events.
	if _, exists := sm.images[imageBase]; exists {
		return
	}

	// This is a new image, so we create and store it.
	// Lock the interning mutex separately to avoid lock-ordering issues.
	sm.internMutex.Lock()
	imageName := sm.internImageName(filepath.Base(fileName))
	sm.internMutex.Unlock()

	info := sm.imageInfoPool.Get().(*ImageInfo)
	info.ImageBase = imageBase
	info.ImageSize = imageSize
	info.ImageName = imageName

	sm.images[imageBase] = info
}

// MarkImageForDeletion flags an image for cleanup after the next scrape.
func (sm *KernelStateManager) MarkImageForDeletion(imageBase uint64) {
	sm.imagesMutex.RLock()
	_, exists := sm.images[imageBase]
	sm.imagesMutex.RUnlock()

	if exists {
		sm.terminatedImages.Store(imageBase, time.Now())
	}
}

// RemoveImage handles the explicit unloading of an image.
func (sm *KernelStateManager) RemoveImage(imageBase uint64) {
	sm.imagesMutex.Lock()
	defer sm.imagesMutex.Unlock()

	info, exists := sm.images[imageBase]
	if !exists {
		// Also remove it from the terminated list to make the cleanup idempotent.
		sm.terminatedImages.Delete(imageBase)
		return
	}

	// Remove from the primary map.
	delete(sm.images, imageBase)

	// Invalidate the point-address cache. This is a slow operation (O(N_cache)),
	// but it happens on the less frequent image unload path. We iterate the
	// entire cache and remove any entries that point to the unloaded image.
	for addr, cachedInfo := range sm.addressCache {
		if cachedInfo == info { // Pointer comparison is fast and correct here.
			delete(sm.addressCache, addr)
		}
	}

	// Return the object to the pool.
	sm.imageInfoPool.Put(info)

	// Also remove it from the terminated list to make the cleanup idempotent.
	sm.terminatedImages.Delete(imageBase)
}

// GetImageForAddress resolves a memory address to its corresponding image info.
// This is a hot-path function that uses a point-address cache for O(1) lookups
// on repeated requests, falling back to a linear scan for new addresses.
func (sm *KernelStateManager) GetImageForAddress(address uint64) (*ImageInfo, bool) {
	// First, try a fast read-locked lookup in the cache. This is the hot path.
	sm.imagesMutex.RLock()
	if info, exists := sm.addressCache[address]; exists {
		sm.imagesMutex.RUnlock()
		return info, true
	}
	sm.imagesMutex.RUnlock()

	// Cache miss. We need a full write lock to scan and potentially update the cache.
	sm.imagesMutex.Lock()
	defer sm.imagesMutex.Unlock()

	// Double-check the cache. Another goroutine might have populated it
	// while we were waiting for the write lock.
	if info, exists := sm.addressCache[address]; exists {
		return info, true
	}

	// Still a miss. Perform the linear scan over the source of truth.
	for _, image := range sm.images {
		if address >= image.ImageBase && address < (image.ImageBase+image.ImageSize) {
			// Found it. Populate the cache for next time.
			sm.addressCache[address] = image
			return image, true
		}
	}

	// Not found in any loaded image.
	return nil, false
}
