package statemanager

import "sync"

// processIdentifier holds the PID and unique StartKey for a process.
type processIdentifier struct {
	PID      uint32
	StartKey uint64
}

// AddFileObjectMapping creates a mapping from a FileObject to a process.
func (sm *KernelStateManager) AddFileObjectMapping(fileObject uint64, pid uint32, startKey uint64) {
	ident := processIdentifier{PID: pid, StartKey: startKey}
	sm.fileObjectToProcess.Store(fileObject, ident)

	// Store the reverse mapping for efficient cleanup when a process terminates.
	fileObjects, _ := sm.processToFileObjects.LoadOrStore(pid, &sync.Map{})
	fileObjects.(*sync.Map).Store(fileObject, struct{}{})
}

// RemoveFileObjectMapping removes a mapping when a file is closed or deleted.
func (sm *KernelStateManager) RemoveFileObjectMapping(fileObject uint64) {
	// Find the owning process to clean up the reverse map.
	if identVal, ok := sm.fileObjectToProcess.Load(fileObject); ok {
		ident := identVal.(processIdentifier)
		if fos, ok := sm.processToFileObjects.Load(ident.PID); ok {
			fos.(*sync.Map).Delete(fileObject)
		}
	}
	sm.fileObjectToProcess.Delete(fileObject)
}

// GetProcessForFileObject resolves a FileObject to a process identifier.
func (sm *KernelStateManager) GetProcessForFileObject(fileObject uint64) (pid uint32, startKey uint64, ok bool) {
	identVal, exists := sm.fileObjectToProcess.Load(fileObject)
	if !exists {
		return 0, 0, false
	}
	ident := identVal.(processIdentifier)
	return ident.PID, ident.StartKey, true
}
