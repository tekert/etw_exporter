package statemanager

import (
	"sync/atomic"
)

// RegistryResult defines the outcome of a registry operation.
type RegistryResult uint8

const (
	ResultSuccess RegistryResult = iota
	ResultFailure
	ResultMax // Used for sizing arrays
)

// RegistryOperationKey is a memory-efficient key for storing per-process registry metrics.
// It uses integer constants instead of strings for high performance.
type RegistryOperationKey struct {
	Opcode uint8
	Result RegistryResult
}

const (
	MaxRegistryOpcodes = 256 // Opcodes are uint8
	MaxRegistryArray   = int(MaxRegistryOpcodes) * int(ResultMax)
)

// RegistryModule holds all registry-related metrics for a single process instance.
// It uses a fixed-size array for lock-free, high-performance writes on the hot path.
type RegistryModule struct {
	// Operations is an array where the index is calculated from the opcode and result.
	// Index = (opcode * resultMax) + result
	Operations [MaxRegistryArray]atomic.Uint64
}

// Reset zeroes out the counters, making the module ready for reuse.
func (rm *RegistryModule) Reset() {
	for i := range rm.Operations {
		rm.Operations[i].Store(0)
	}
}

// RegistryOpcodeToString maps registry event opcodes to their string representations for Prometheus labels.
// This is the single source of truth for opcode names, using an array for fast lookups.
var RegistryOpcodeToString [MaxRegistryOpcodes]string

func init() {
	// Initialize the opcode lookup table.
	RegistryOpcodeToString[10] = "create"
	RegistryOpcodeToString[11] = "open"
	RegistryOpcodeToString[12] = "delete"
	RegistryOpcodeToString[13] = "query"
	RegistryOpcodeToString[14] = "set_value"
	RegistryOpcodeToString[15] = "delete_value"
	RegistryOpcodeToString[16] = "query_value"
	RegistryOpcodeToString[17] = "enumerate_key"
	RegistryOpcodeToString[18] = "enumerate_value_key"
	RegistryOpcodeToString[19] = "query_multiple_value"
	RegistryOpcodeToString[20] = "set_information"
	RegistryOpcodeToString[21] = "flush"
	RegistryOpcodeToString[22] = "kcb_create"
	RegistryOpcodeToString[23] = "kcb_delete"
	RegistryOpcodeToString[24] = "kcb_rundown_begin"
	RegistryOpcodeToString[25] = "kcb_rundown_end"
	RegistryOpcodeToString[26] = "virtualize"
	RegistryOpcodeToString[27] = "close"
	RegistryOpcodeToString[28] = "set_security"
	RegistryOpcodeToString[29] = "query_security"
	RegistryOpcodeToString[30] = "commit"
	RegistryOpcodeToString[31] = "prepare"
	RegistryOpcodeToString[32] = "rollback"
	RegistryOpcodeToString[33] = "mount_hive"
}

// RecordRegistryOperation records a registry event for a process.
// It is optimized to be lock-free and allocation-free on the hot path.
func (pd *ProcessData) RecordRegistryOperation(opcode uint8, status uint32) {
	result := ResultSuccess
	if status != 0 {
		result = ResultFailure
	}

	// Calculate the index for the flat array. This is bounds-checked by the array definition.
	index := int(opcode)*int(ResultMax) + int(result)

	// Perform a simple, lock-free atomic increment.
	pd.Registry.Operations[index].Add(1)
}
