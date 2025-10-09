package kerneimage

import (
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// Handler handles interrupt-related ETW events for performance monitoring
// It implements the event handler interfaces needed for interrupt latency tracking
type Handler struct {
	stateManager *statemanager.KernelStateManager
	log          *phusluadapter.SampledLogger
}

// NewImageHandler creates a new interrupt event handler
func NewImageHandler(sm *statemanager.KernelStateManager) *Handler {
	return &Handler{
		stateManager: sm,
		log:          logger.NewSampledLoggerCtx("image_handler"),
	}
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *Handler) RegisterRoutes(router handlers.Router) {

	// Provider: NT Kernel Logger (Image) ({2cb15d1d-5fc1-11d2-abe1-00a0c911f518})
	// Provider: System Process Provider ({151f55dc-467d-471f-83b5-5f889d46ff66}) - Windows 11+
	imageRoutes := map[uint8]handlers.EventHandlerFunc{
		etw.EVENT_TRACE_TYPE_LOAD:     h.HandleImageLoadEvent,   // Image Load
		etw.EVENT_TRACE_TYPE_DC_START: h.HandleImageLoadEvent,   // Image Rundown
		etw.EVENT_TRACE_TYPE_DC_END:   h.HandleImageLoadEvent,   // Image Rundown End
		etw.EVENT_TRACE_TYPE_END:      h.HandleImageUnloadEvent, // Image Unload
	}
	handlers.RegisterRoutesForGUID(router, guids.ImageKernelGUID, imageRoutes)
	handlers.RegisterRoutesForGUID(router, etw.SystemProcessProviderGuid, imageRoutes) // Win11+

	h.log.Debug().Msg("image collector enabled and routes registered")
}

// HandleImageLoadEvent processes image load events to map routine addresses to drivers.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (Image)
//   - Provider GUID: {2cb15d1d-5fc1-11d2-abe1-00a0c911f518}
//   - Event ID(s): 10, 3, 4
//   - Event Name(s): Load, DCStart, DCEnd
//   - Event Version(s): 2, 3
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go):
//   - ImageBase (pointer): Base address of the loaded image.
//   - ImageSize (uint32): Size of the loaded image in bytes.
//   - ProcessId (uint32): ID of the process loading the image.
//   - ImageChecksum (uint32): The checksum of the image.
//   - TimeDateStamp (uint32): Linker build time.
//   - Reserved0 (uint32): Reserved.
//   - DefaultBase (pointer): The default base address of the image.
//   - Reserved1 (uint32): Reserved.
//   - Reserved2 (uint32): Reserved.
//   - Reserved3 (uint32): Reserved.
//   - Reserved4 (uint32): Reserved.
//   - FileName (string): Full path to the image file.
//
// This handler collects information about loaded modules (drivers, executables, DLLs)
// to resolve routine addresses from DPC/ISR events to a driver name.
func (h *Handler) HandleImageLoadEvent(helper *etw.EventRecordHelper) error {
	// Extract image load properties using optimized methods
	var imageBase uint64
	var imageSize uint64
	var fileName string
	var processID uint32
	var timeDateStamp uint32
	var imageChecksum uint32

	// Extract properties using proper helper methods
	if base, err := helper.GetPropertyUint("ImageBase"); err == nil {
		imageBase = base
	}
	if size, err := helper.GetPropertyUint("ImageSize"); err == nil {
		imageSize = size
	}
	if name, err := helper.GetPropertyString("FileName"); err == nil {
		fileName = name
	}
	if pid, err := helper.GetPropertyUint("ProcessId"); err == nil {
		processID = uint32(pid)
	}
	if ts, err := helper.GetPropertyUint("TimeDateStamp"); err == nil {
		timeDateStamp = uint32(ts)
	}
	if ts, err := helper.GetPropertyUint("ImageChecksum"); err == nil {
		imageChecksum = uint32(ts)
	}

	// The state manager's AddImage method now contains all logic for storing the image,
	// reference counting, and triggering process enrichment for main executables.
	h.stateManager.AddImage(processID, imageBase, imageSize, fileName, timeDateStamp, imageChecksum)

	return nil
}

// HandleImageUnloadEvent processes image unload events to remove driver mappings.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (Image)
//   - Provider GUID: {2cb15d1d-5fc1-11d2-abe1-00a0c911f518}
//   - Event ID(s): 2
//   - Event Name(s): Unload
//   - Event Version(s): 2, 3
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go):
//   - ImageBase (pointer): Base address of the unloaded image.
//   - ImageSize (uint32): Size of the unloaded image in bytes.
//   - ProcessId (uint32): ID of the process unloading the image.
//   - ImageChecksum (uint32): The checksum of the image.
//   - TimeDateStamp (uint32): The timestamp from the image header.
//   - Reserved0 (uint32): Reserved.
//   - DefaultBase (pointer): The default base address of the image.
//   - Reserved1 (uint32): Reserved.
//   - Reserved2 (uint32): Reserved.
//   - Reserved3 (uint32): Reserved.
//   - Reserved4 (uint32): Reserved.
//   - FileName (string): Full path to the image file.
//
// This handler removes the address-to-driver mapping when a module is unloaded
// to prevent stale data.
func (h *Handler) HandleImageUnloadEvent(helper *etw.EventRecordHelper) error {
	var imageBase uint64
	if base, err := helper.GetPropertyUint("ImageBase"); err == nil {
		imageBase = base
	} else {
		return nil // Cannot process without image base
	}

	// Decrement the image's reference count. The state manager facade will handle
	// notifying any interested listeners before marking the image for deletion.
	h.stateManager.UnloadImage(imageBase)

	return nil
}
