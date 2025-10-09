package statemanager

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ImageInfo holds the minimal information needed for high-frequency
// address-to-name lookups, with an interned (shared) image name string.
type ImageInfo struct {
	ImageBase uint64 // Base address of the loaded image.
	ImageSize uint64
	ImageName string // Interned file name
	RefCount  atomic.Int32
}

// ImageManager is responsible for the lifecycle of loaded images (DLLs, EXEs),
// handling address lookups, reference counting, and process name enrichment.
type ImageManager struct {
	images             map[uint64]*ImageInfo
	addressCache       map[uint64]*ImageInfo // Point-address cache for hot-path lookups
	imagesMutex        sync.RWMutex
	terminatedImages   maps.ConcurrentMap[uint64, uint64] // key: ImageBase, value: scrape generation
	imageInfoPool      sync.Pool
	internedImageNames map[string]string
	internMutex        sync.Mutex

	// Collaborators
	processManager *ProcessManager

	// Back-reference to the orchestrator to access shared state like the scrape counter.
	stateManager *KernelStateManager

	log *phusluadapter.SampledLogger
}

// newImageManager creates and initializes a new ImageManager.
func newImageManager(log *phusluadapter.SampledLogger) *ImageManager {
	return &ImageManager{
		log:                log,
		images:             make(map[uint64]*ImageInfo),
		addressCache:       make(map[uint64]*ImageInfo, 4096),
		terminatedImages:   maps.NewConcurrentMap[uint64, uint64](),
		internedImageNames: make(map[string]string),
		imageInfoPool: sync.Pool{
			New: func() any { return new(ImageInfo) },
		},
	}
}

// setStateManager sets the reference to the KernelStateManager to resolve circular dependencies
// and allow access to shared state.
func (im *ImageManager) setStateManager(sm *KernelStateManager) {
	im.stateManager = sm
}

// setProcessManager sets the reference to the ProcessManager to resolve circular dependencies.
func (im *ImageManager) setProcessManager(pm *ProcessManager) {
	im.processManager = pm
}

// internImageName returns a canonical, shared string for the given image name.
func (im *ImageManager) internImageName(name string) string {
	im.internMutex.Lock()
	defer im.internMutex.Unlock()
	if interned, exists := im.internedImageNames[name]; exists {
		return interned
	}
	im.internedImageNames[name] = name
	return name
}

// AddImage adds image information to the store, handles reference counting, and triggers
// process enrichment if the image is identified as a process's main executable. This
// is the central hub for all image load logic.
func (im *ImageManager) AddImage(
	pid uint32,
	imageBase,
	imageSize uint64,
	fileName string,
	peTimestamp uint32,
	peChecksum uint32) {

	// --- Store the image and handle reference counting ---
	if imageBase != 0 && imageSize != 0 {
		im.imagesMutex.Lock()
		// Check for existence to handle duplicate events and manage ref count.
		if info, exists := im.images[imageBase]; exists {
			info.RefCount.Add(1)
		} else {
			// This is a new image, so we create and store it.
			imageName := im.internImageName(filepath.Base(fileName))
			info := im.imageInfoPool.Get().(*ImageInfo)
			info.ImageBase = imageBase
			info.ImageSize = imageSize
			info.ImageName = imageName
			info.RefCount.Store(1)
			im.images[imageBase] = info
		}
		im.imagesMutex.Unlock()
	}

	// --- Process Enrichment Logic ---
	// Any Image/Load event for an executable (.exe) is a candidate for enriching
	// the process name. We pass it to the process manager, which contains the
	// logic to decide if the new name is better than the existing one. This is more
	// reliable than using the name from Process/Start alone.
	if im.processManager.Enabled {
		if strings.HasSuffix(strings.ToLower(fileName), ".exe") {
			im.processManager.EnrichProcessWithImageData(pid, uint64(peChecksum), fileName)
		}
	}

	// --- Part 3: Logging ---
	im.log.Trace().Uint32("pid", pid).
		Uint64("image_base", imageBase).
		Str("image_name", fileName).
		Msg("Image loaded")
}

// UnloadImage decrements the reference count for an image and marks it for deletion
// if the count reaches zero.
func (im *ImageManager) UnloadImage(imageBase uint64) {
	im.imagesMutex.RLock()
	info, exists := im.images[imageBase]
	im.imagesMutex.RUnlock()

	if exists {
		// If the ref count drops to zero or below, mark it for deletion.
		if info.RefCount.Add(-1) <= 0 {
			currentScrape := im.stateManager.scrapeCounter.Load()
			im.terminatedImages.Store(imageBase, currentScrape)

			im.log.Trace().Uint64("image_base", imageBase).
				Str("image_name", info.ImageName).
				Msg("Image marked for deletion (ref count zero)")
		}
	}
}

// RemoveImage handles the explicit unloading of an image from the store.
func (im *ImageManager) RemoveImage(imageBase uint64) {
	im.imagesMutex.Lock()
	defer im.imagesMutex.Unlock()
	info, exists := im.images[imageBase]
	if !exists {
		// Also remove it from the terminated list to make the cleanup idempotent.
		im.terminatedImages.Delete(imageBase)
		return
	}

	// Remove from the primary map.
	delete(im.images, imageBase)

	// Invalidate the point-address cache. This is a slow operation (O(N_cache)),
	// but it happens on the less frequent image unload path. We iterate the
	// entire cache and remove any entries that point to the unloaded image.
	for addr, cachedInfo := range im.addressCache {
		if cachedInfo == info { // Pointer comparison is fast and correct here.
			delete(im.addressCache, addr)
		}
	}
	im.imageInfoPool.Put(info)

	// Also remove it from the terminated list to make the cleanup idempotent.
	im.terminatedImages.Delete(imageBase)

	im.log.Trace().Uint64("image_base", imageBase).
		Str("image_name", info.ImageName).
		Msg("Image removed from store")
}

// GetImageForAddress resolves a memory address to its corresponding image info.
// This is a hot-path function that uses a point-address cache for O(1) lookups
// on repeated requests, falling back to a linear scan for new addresses.
func (im *ImageManager) GetImageForAddress(address uint64) (*ImageInfo, bool) {
	// First, try a fast read-locked lookup in the cache. This is the hot path.
	im.imagesMutex.RLock()
	if info, exists := im.addressCache[address]; exists {
		im.imagesMutex.RUnlock()
		return info, true
	}
	im.imagesMutex.RUnlock()

	// Cache miss. We need a full write lock to scan and potentially update the cache.
	im.imagesMutex.Lock()
	defer im.imagesMutex.Unlock()

	// Double-check the cache. Another goroutine might have populated it
	// while we were waiting for the write lock.
	if info, exists := im.addressCache[address]; exists {
		return info, true
	}

	// Still a miss. Perform the linear scan over the source of truth.
	for _, image := range im.images {
		if address >= image.ImageBase && address < (image.ImageBase+image.ImageSize) {
			// Found it. Populate the cache for next time.
			im.addressCache[address] = image
			return image, true
		}
	}

	// Not found in any loaded image.
	return nil, false
}

// cleanupTerminated handles the cleanup for individually unloaded images.
func (im *ImageManager) cleanupTerminated(currentGeneration uint64) (cleanedImages int) {
	imagesToClean := make([]uint64, 0)
	im.terminatedImages.Range(func(imageBase uint64, terminatedAtGeneration uint64) bool {
		if terminatedAtGeneration < currentGeneration {
			imagesToClean = append(imagesToClean, imageBase)
		}
		return true
	})
	for _, imageBase := range imagesToClean {
		im.RemoveImage(imageBase)
		cleanedImages++
	}
	return
}

// GetCleanedImages returns the names of all images currently marked for termination.
// DEPRECATED: This function is inefficient as it allocates a new slice of strings
// during the cleanup phase. It is preserved for compatibility but should be avoided.
// Collectors should manage their own state based on process termination events.
func (im *ImageManager) GetCleanedImages() []string {
	var cleanedNames []string

	// A read lock is sufficient to ensure a consistent view of both the terminated
	// list and the main image list from which we retrieve the names.
	im.imagesMutex.RLock()
	defer im.imagesMutex.RUnlock()

	im.terminatedImages.Range(func(imageBase uint64, _ uint64) bool {
		// Look up the image info using the base address. The image will still
		// exist in the main map at this point in the scrape cycle.
		if info, exists := im.images[imageBase]; exists {
			cleanedNames = append(cleanedNames, info.ImageName)
		}
		return true
	})

	return cleanedNames
}

// --- Debug Provider Methods ---

func (im *ImageManager) GetImageCount() int {
	im.imagesMutex.RLock()
	defer im.imagesMutex.RUnlock()
	return len(im.images)
}

func (im *ImageManager) GetTerminatedCount() (images int) {
	im.terminatedImages.Range(func(u uint64, u2 uint64) bool {
		images++
		return true
	})
	return
}

func (im *ImageManager) RangeImages(f func(info *ImageInfo)) {
	im.imagesMutex.RLock()
	defer im.imagesMutex.RUnlock()
	for _, imgInfo := range im.images {
		f(imgInfo)
	}
}
