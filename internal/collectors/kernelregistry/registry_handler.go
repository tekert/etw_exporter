package kernelregistry

import (
	"etw_exporter/internal/config"
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// Handler handles registry events from ETW providers.
type Handler struct {
	customCollector *RegistryCollector
	config          *config.RegistryConfig
	stateManager    *statemanager.KernelStateManager
	log             *phusluadapter.SampledLogger
	opcodeMap       [256]string // Opcode lookup table, encapsulated within the handler.
}

// NewRegistryHandler creates a new registry handler.
func NewRegistryHandler(config *config.RegistryConfig, sm *statemanager.KernelStateManager) *Handler {
	h := &Handler{
		customCollector: nil, // Will be set via AttachCollector
		config:          config,
		stateManager:    sm,
		log:             logger.NewSampledLoggerCtx("registry_handler"),
	}

	// Initialize the opcode lookup table for this handler instance.
	h.opcodeMap[10] = "create"
	h.opcodeMap[11] = "open"
	h.opcodeMap[12] = "delete"
	h.opcodeMap[13] = "query"
	h.opcodeMap[14] = "set_value"
	h.opcodeMap[15] = "delete_value"
	h.opcodeMap[16] = "query_value"
	h.opcodeMap[17] = "enumerate_key"
	h.opcodeMap[18] = "enumerate_value_key"
	h.opcodeMap[19] = "query_multiple_value"
	h.opcodeMap[20] = "set_information"
	h.opcodeMap[21] = "flush"
	h.opcodeMap[22] = "kcb_create"
	h.opcodeMap[23] = "kcb_delete"
	h.opcodeMap[24] = "kcb_rundown_begin"
	h.opcodeMap[25] = "kcb_rundown_end"
	h.opcodeMap[26] = "virtualize"
	h.opcodeMap[27] = "close"
	h.opcodeMap[28] = "set_security"
	h.opcodeMap[29] = "query_security"
	h.opcodeMap[30] = "commit"
	h.opcodeMap[31] = "prepare"
	h.opcodeMap[32] = "rollback"
	h.opcodeMap[33] = "mount_hive"

	return h
}

// AttachCollector allows a metrics collector to subscribe to the handler's events.
func (c *Handler) AttachCollector(collector *RegistryCollector) {
	c.log.Debug().Msg("Attaching metrics collector to registry handler.")
	c.customCollector = collector
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *Handler) RegisterRoutes(router handlers.Router) {
	// NOTE: These routes are not used and are handled on the fast path.

	// Provider: NT Kernel Logger (Registry) ({ae53722e-c863-11d2-8659-00c04fa321a1})
	for i := 10; i <= 50; i++ {
		router.AddRoute(*guids.RegistryKernelGUID, uint16(i), h.HandleRegistryEvent) // 10-33
		router.AddRoute(*guids.SystemRegistryProviderGuid, uint16(i), h.HandleRegistryEvent) // 10-33
	}
}

// GetCustomCollector returns the custom Prometheus collector.
func (h *Handler) GetCustomCollector() *RegistryCollector {
	return h.customCollector
}

// evntrace.h (v10.0.19041.0) defines the following event types:
//
// Event Types for Registry subsystem
//
/*
	#define EVENT_TRACE_TYPE_REGCREATE                  0x0A     // NtCreateKey
	#define EVENT_TRACE_TYPE_REGOPEN                    0x0B     // NtOpenKey
	#define EVENT_TRACE_TYPE_REGDELETE                  0x0C     // NtDeleteKey
	#define EVENT_TRACE_TYPE_REGQUERY                   0x0D     // NtQueryKey
	#define EVENT_TRACE_TYPE_REGSETVALUE                0x0E     // NtSetValueKey
	#define EVENT_TRACE_TYPE_REGDELETEVALUE             0x0F     // NtDeleteValueKey
	#define EVENT_TRACE_TYPE_REGQUERYVALUE              0x10     // NtQueryValueKey
	#define EVENT_TRACE_TYPE_REGENUMERATEKEY            0x11     // NtEnumerateKey
	#define EVENT_TRACE_TYPE_REGENUMERATEVALUEKEY       0x12     // NtEnumerateValueKey
	#define EVENT_TRACE_TYPE_REGQUERYMULTIPLEVALUE      0x13     // NtQueryMultipleValueKey
	#define EVENT_TRACE_TYPE_REGSETINFORMATION          0x14     // NtSetInformationKey
	#define EVENT_TRACE_TYPE_REGFLUSH                   0x15     // NtFlushKey
	#define EVENT_TRACE_TYPE_REGKCBCREATE               0x16     // KcbCreate
	#define EVENT_TRACE_TYPE_REGKCBDELETE               0x17     // KcbDelete
	#define EVENT_TRACE_TYPE_REGKCBRUNDOWNBEGIN         0x18     // KcbRundownBegin
	#define EVENT_TRACE_TYPE_REGKCBRUNDOWNEND           0x19     // KcbRundownEnd
	#define EVENT_TRACE_TYPE_REGVIRTUALIZE              0x1A     // VirtualizeKey
	#define EVENT_TRACE_TYPE_REGCLOSE                   0x1B     // NtClose (KeyObject)
	#define EVENT_TRACE_TYPE_REGSETSECURITY             0x1C     // SetSecurityDescriptor (KeyObject)
	#define EVENT_TRACE_TYPE_REGQUERYSECURITY           0x1D     // QuerySecurityDescriptor (KeyObject)
	#define EVENT_TRACE_TYPE_REGCOMMIT                  0x1E     // CmKtmNotification (TRANSACTION_NOTIFY_COMMIT)
	#define EVENT_TRACE_TYPE_REGPREPARE                 0x1F     // CmKtmNotification (TRANSACTION_NOTIFY_PREPARE)
	#define EVENT_TRACE_TYPE_REGROLLBACK                0x20     // CmKtmNotification (TRANSACTION_NOTIFY_ROLLBACK)
	#define EVENT_TRACE_TYPE_REGMOUNTHIVE               0x21     // NtLoadKey variations + system hives
*/

// HandleRegistryEventRaw processes registry events directly from the EVENT_RECORD.
//
// This is a high-performance "fast path" that reads directly from the UserData
// buffer using known offsets, bypassing the creation of an EventRecordHelper and
// the associated parsing overhead.
//
// This handler processes events from the NT Kernel Logger for registry activity,
// providing insights into key creation, deletion, value setting, and other interactions.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {ae53722e-c863-11d2-8659-00c04fa321a1}
//   - Event ID(s) (Opcodes): 10-33 (0x0A - 0x21)
//   - Event Name(s): Create, Open, Delete, Query, SetValue, DeleteValue, etc.
//   - Event Version(s): 1
//   - Schema: MOF
//
// Schema (from MOF - Registry_TypeGroup1):
//   - InitialTime (sint64): Initial time of the registry operation. Offset 0
//   - Status (uint32): NTSTATUS value of the registry operation. Offset 8
//   - Index (uint32): The subkey index for the registry operation. Offset 12
//   - KeyHandle (uint32): Handle to the registry key.  Offset 16
//   - KeyName (string): Name of the registry key.  Offset 20 StringTermination("NullTerminated")
//
// Event Opcodes and Descriptions (from evntrace.h):
//   - 10 (0x0A): Create (NtCreateKey)
//   - 11 (0x0B): Open (NtOpenKey)
//   - 12 (0x0C): Delete (NtDeleteKey)
//   - 13 (0x0D): Query (NtQueryKey)
//   - 14 (0x0E): SetValue (NtSetValueKey)
//   - 15 (0x0F): DeleteValue (NtDeleteValueKey)
//   - 16 (0x10): QueryValue (NtQueryValueKey)
//   - 17 (0x11): EnumerateKey (NtEnumerateKey)
//   - 18 (0x12): EnumerateValueKey (NtEnumerateValueKey)
//   - 19 (0x13): QueryMultipleValue (NtQueryMultipleValueKey)
//   - 20 (0x14): SetInformation (NtSetInformationKey)
//   - 21 (0x15): Flush (NtFlushKey)
//   - 22 (0x16): KCBCreate (Kernel Control Block Create)
//   - 23 (0x17): KCBDelete (Kernel Control Block Delete)
//   - 24 (0x18): KCBRundownBegin (Kernel Control Block Rundown Begin)
//   - 25 (0x19): KCBRundownEnd (Kernel Control Block Rundown End)
//   - 26 (0x1A): Virtualize (VirtualizeKey)
//   - 27 (0x1B): Close (NtClose on a KeyObject)
//   - 28 (0x1C): SetSecurity (SetSecurityDescriptor on a KeyObject)
//   - 29 (0x1D): QuerySecurity (QuerySecurityDescriptor on a KeyObject)
//   - 30 (0x1E): Commit (Transactional commit notification)
//   - 31 (0x1F): Prepare (Transactional prepare notification)
//   - 32 (0x20): Rollback (Transactional rollback notification)
//   - 33 (0x21): MountHive (NtLoadKey variations and system hive loading)
func (h *Handler) HandleRegistryEventRaw(er *etw.EventRecord) error {
	if h.customCollector == nil {
		return nil
	}

	opcode := er.EventHeader.EventDescriptor.Opcode

	// Bounds check for the array lookup.
	if int(opcode) >= len(h.opcodeMap) {
		return nil
	}

	operation := h.opcodeMap[opcode]
	if operation == "" {
		// This opcode is not mapped, so we ignore it.
		return nil
	}

	// Read Status property using direct offset.
	status, err := er.GetUint32At(8)
	if err != nil {
		h.log.SampledError("fail-status-raw").Err(err).Msg("Failed to read Status from raw registry event")
		return err
	}

	processID := er.EventHeader.ProcessId
	startKey, ok := h.stateManager.GetProcessStartKey(processID)
	if !ok {
		// Process is not known or has already terminated. We can't reliably attribute
		// this event, so we skip it.
		return nil
	}

	h.customCollector.RecordRegistryOperation(startKey, operation, status)

	return nil
}

// HandleRegistryEvent processes registry events to track operations.
//
// NOTE: This is now the fallback/slower path. The primary logic has been moved to
// HandleRegistryEventRaw for performance. This function will not be called
// for registry events due to the routing logic in EventRecordCallback.
//
// Same as HandleRegistryEventRaw
// Deprecated: Use HandleRegistryEventRaw instead for better performance.
func (h *Handler) HandleRegistryEvent(helper *etw.EventRecordHelper) error {
	if h.customCollector == nil {
		return nil
	}

	opcode := helper.EventRec.EventHeader.EventDescriptor.Opcode

	// Bounds check for the array lookup.
	if int(opcode) >= len(h.opcodeMap) {
		return nil
	}

	operation := h.opcodeMap[opcode]
	if operation == "" {
		// This opcode is not mapped, so we ignore it.
		return nil
	}

	status, err := helper.GetPropertyUint("Status")
	if err != nil {
		h.log.SampledError("fail-status").Err(err).Msg("Failed to get Status property for registry event")
		return err
	}

	processID := helper.EventRec.EventHeader.ProcessId
	startKey, ok := h.stateManager.GetProcessStartKey(processID)
	if !ok {
		return nil // Process not known, skip.
	}

	h.customCollector.RecordRegistryOperation(startKey, operation, uint32(status))

	return nil
}
