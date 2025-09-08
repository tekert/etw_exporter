package statemanager

import (
	"path/filepath"
	"sync"
	"time"
)

// ImageInfo holds information about a loaded module (driver, DLL, etc.).
type ImageInfo struct {
	ImageBase uint64
	ImageSize uint64
	FileName  string // Full path
	ImageName string // Extracted file name (e.g., "ntoskrnl.exe")
}

// --- Image Management ---

// AddImage adds information about a loaded image and associates it with a process.
// This is the central method for tracking all loaded modules in the system.
func (sm *KernelStateManager) AddImage(pid uint32, imageBase, imageSize uint64, fileName string) {
	// Store the primary image information, keyed by its base address.
	imageName := filepath.Base(fileName)
	sm.images.Store(imageBase, &ImageInfo{
		ImageBase: imageBase,
		ImageSize: imageSize,
		FileName:  fileName,
		ImageName: imageName,
	})

	// Associate the image with the process that loaded it.
	// This is crucial for cascading cleanup when the process terminates.
	pidImages, _ := sm.pidToImages.LoadOrStore(pid, &sync.Map{})
	pidImages.(*sync.Map).Store(imageBase, struct{}{})

	// Create the reverse mapping for efficient cleanup on image unload.
	sm.imageToPid.Store(imageBase, pid)

	sm.log.Trace().
		Uint32("pid", pid).
		Str("image_name", imageName).
		Uint64("base", imageBase).
		Msg("Image loaded and associated with process")
}

// MarkImageForDeletion flags an image for cleanup after the next scrape.
// It does not immediately delete the image, allowing in-flight scrapes to resolve
// addresses within the image.
func (sm *KernelStateManager) MarkImageForDeletion(imageBase uint64) {
	// Check if we are actually tracking this image to avoid polluting the map.
	if _, exists := sm.images.Load(imageBase); exists {
		sm.terminatedImages.Store(imageBase, time.Now())
	}
}

// RemoveImage handles the explicit unloading of an image.
// It cleans up all state associated with the given image base address.
// This should only be called from post-scrape cleanup routines.
func (sm *KernelStateManager) RemoveImage(imageBase uint64) {
	// Find the owning process to clean up the pid->image association.
	if pidVal, ok := sm.imageToPid.Load(imageBase); ok {
		pid := pidVal.(uint32)
		if imagesVal, ok := sm.pidToImages.Load(pid); ok {
			imagesVal.(*sync.Map).Delete(imageBase)
		}
	}

	// Remove the primary image info and the reverse mapping.
	sm.images.Delete(imageBase)
	sm.imageToPid.Delete(imageBase)

	// Also remove it from the terminated list to make the cleanup idempotent.
	sm.terminatedImages.Delete(imageBase)
}

// GetImageForAddress resolves a memory address to the image that contains it.
// This is used by collectors like PerfInfo to map routine addresses to driver names.
func (sm *KernelStateManager) GetImageForAddress(address uint64) (*ImageInfo, bool) {
	var foundImage *ImageInfo
	// This Range is not ideal for performance, but image loads are infrequent.
	// A more complex data structure (like an interval tree) would be overkill.
	sm.images.Range(func(key, value any) bool {
		info := value.(*ImageInfo)
		if address >= info.ImageBase && address < (info.ImageBase+info.ImageSize) {
			foundImage = info
			return false // Stop iterating
		}
		return true
	})

	if foundImage != nil {
		return foundImage, true
	}

	return nil, false
}
