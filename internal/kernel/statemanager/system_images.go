package statemanager

import (
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// --- Image Management ---

// ImageInfo holds the minimal information needed for high-frequency
// address-to-name lookups, with an interned (shared) image name string.
type ImageInfo struct {
	ImageBase uint64 // Base address of the loaded image.
	ImageSize uint64
	ImageName string // Interned file name
	RefCount  atomic.Int32
}

// internImageName returns a canonical, shared string for the given image name.
// This function must be called while holding a lock on internMutex.
func (ss *SystemState) internImageName(name string) string {
	if interned, exists := ss.internedImageNames[name]; exists {
		return interned
	}
	ss.internedImageNames[name] = name
	return name
}

// AddImage adds image information to the store, handles reference counting, and triggers
// process enrichment if the image is identified as a process's main executable. This
// is the central hub for all image load logic.
func (ss *SystemState) AddImage(
	pid uint32,
	imageBase,
	imageSize uint64,
	fileName string,
	loadTimestamp uint32,
	imageChecksum uint32) {

	// --- Part 1: Store the image and handle reference counting ---
	if imageBase != 0 && imageSize != 0 {
		ss.imagesMutex.Lock()
		// Check for existence to handle duplicate events and manage ref count.
		if info, exists := ss.images[imageBase]; exists {
			info.RefCount.Add(1)
		} else {
			// This is a new image, so we create and store it.
			ss.internMutex.Lock()
			imageName := ss.internImageName(filepath.Base(fileName))
			ss.internMutex.Unlock()
			info := ss.imageInfoPool.Get().(*ImageInfo)
			info.ImageBase = imageBase
			info.ImageSize = imageSize
			info.ImageName = imageName
			info.RefCount.Store(1)
			ss.images[imageBase] = info
		}
		ss.imagesMutex.Unlock()
	}

	// --- Part 2: Process Enrichment Logic ---
	// Any Image/Load event for an executable (.exe) is a candidate for enriching
	// the process name. We pass it to the process manager, which contains the
	// logic to decide if the new name is better than the existing one. This is more
	// reliable than using the name from Process/Start alone.
	if strings.HasSuffix(strings.ToLower(fileName), ".exe") {
		ss.processManager.EnrichProcessWithImageData(pid, imageChecksum, fileName)
	}

	// --- Part 3: Logging ---
	ss.log.Trace().Uint32("pid", pid).
		Uint64("image_base", imageBase).
		Uint64("image_size", imageSize).
		Str("image_name", fileName).
		Uint32("load_timestamp", loadTimestamp).
		Uint32("image_checksum", imageChecksum).
		Msg("Image loaded")
}

// UnloadImage decrements the reference count for an image and marks it for deletion
// if the count reaches zero.
func (ss *SystemState) UnloadImage(imageBase uint64) {
	ss.imagesMutex.RLock()
	info, exists := ss.images[imageBase]
	ss.imagesMutex.RUnlock()

	if exists {
		// If the ref count drops to zero or below, mark it for deletion.
		if info.RefCount.Add(-1) <= 0 {
			ss.terminatedImages.Store(imageBase, time.Now())

			ss.log.Trace().Uint64("image_base", imageBase).
				Str("image_name", info.ImageName).
				Msg("Image marked for deletion (ref count zero)")
		}
	}
}

// MarkImageForDeletion flags an image for cleanup after the next scrape.
func (ss *SystemState) MarkImageForDeletion(imageBase uint64) {
	ss.imagesMutex.RLock()
	_, exists := ss.images[imageBase]
	ss.imagesMutex.RUnlock()
	if exists {
		ss.terminatedImages.Store(imageBase, time.Now())
	}
}

// RemoveImage handles the explicit unloading of an image.
func (ss *SystemState) RemoveImage(imageBase uint64) {
	ss.imagesMutex.Lock()
	defer ss.imagesMutex.Unlock()
	info, exists := ss.images[imageBase]
	if !exists {
		// Also remove it from the terminated list to make the cleanup idempotent.
		ss.terminatedImages.Delete(imageBase)
		return
	}

	// Remove from the primary map.
	delete(ss.images, imageBase)

	// Invalidate the point-address cache. This is a slow operation (O(N_cache)),
	// but it happens on the less frequent image unload path. We iterate the
	// entire cache and remove any entries that point to the unloaded image.
	for addr, cachedInfo := range ss.addressCache {
		if cachedInfo == info { // Pointer comparison is fast and correct here.
			delete(ss.addressCache, addr)
		}
	}
	ss.imageInfoPool.Put(info)

	// Also remove it from the terminated list to make the cleanup idempotent.
	ss.terminatedImages.Delete(imageBase)

	ss.log.Trace().Uint64("image_base", imageBase).
		Str("image_name", info.ImageName).
		Msg("Image removed from store")
}

// GetImageForAddress resolves a memory address to its corresponding image info.
// This is a hot-path function that uses a point-address cache for O(1) lookups
// on repeated requests, falling back to a linear scan for new addresses.
func (ss *SystemState) GetImageForAddress(address uint64) (*ImageInfo, bool) {
	// First, try a fast read-locked lookup in the cache. This is the hot path.
	ss.imagesMutex.RLock()
	if info, exists := ss.addressCache[address]; exists {
		ss.imagesMutex.RUnlock()
		return info, true
	}
	ss.imagesMutex.RUnlock()

	// Cache miss. We need a full write lock to scan and potentially update the cache.
	ss.imagesMutex.Lock()
	defer ss.imagesMutex.Unlock()

	// Double-check the cache. Another goroutine might have populated it
	// while we were waiting for the write lock.
	if info, exists := ss.addressCache[address]; exists {
		return info, true
	}

	// Still a miss. Perform the linear scan over the source of truth.
	for _, image := range ss.images {
		if address >= image.ImageBase && address < (image.ImageBase+image.ImageSize) {
			// Found it. Populate the cache for next time.
			ss.addressCache[address] = image
			return image, true
		}
	}

	// Not found in any loaded image.
	return nil, false
}

// GetImageCount returns the current number of loaded images.
func (ss *SystemState) GetImageCount() int {
	ss.imagesMutex.RLock()
	defer ss.imagesMutex.RUnlock()
	return len(ss.images)
}

// RangeImages iterates over images for the debug http handler.
func (ss *SystemState) RangeImages(f func(info *ImageInfo)) {
	ss.imagesMutex.RLock()
	defer ss.imagesMutex.RUnlock()

	for _, imgInfo := range ss.images {
		f(imgInfo)
	}
}
