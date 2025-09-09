package statemanager

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
	sm.processToFileObjects.Update(pid, func(fileObjects map[uint64]struct{}, exists bool) (map[uint64]struct{}, bool) {
		if !exists {
			fileObjects = make(map[uint64]struct{})
		}
		fileObjects[fileObject] = struct{}{}
		return fileObjects, true // Keep/store the updated map
	})
}

// RemoveFileObjectMapping removes a mapping when a file is closed or deleted.
func (sm *KernelStateManager) RemoveFileObjectMapping(fileObject uint64) {
	// Find the owning process to clean up the reverse map.
	if ident, ok := sm.fileObjectToProcess.Load(fileObject); ok {
		sm.processToFileObjects.Update(ident.PID, func(fos map[uint64]struct{}, exists bool) (map[uint64]struct{}, bool) {
			if !exists {
				return nil, false // Nothing to do, don't store
			}
			delete(fos, fileObject)
			if len(fos) == 0 {
				return nil, false // Delete the entry for this PID
			}
			return fos, true // Keep the modified map
		})
	}

	// Now remove the primary mapping.
	sm.fileObjectToProcess.Delete(fileObject)
}

// GetProcessForFileObject resolves a FileObject to a process identifier.
func (sm *KernelStateManager) GetProcessForFileObject(fileObject uint64) (pid uint32, startKey uint64, ok bool) {
	ident, exists := sm.fileObjectToProcess.Load(fileObject)
	if !exists {
		return 0, 0, false
	}
	return ident.PID, ident.StartKey, true
}
