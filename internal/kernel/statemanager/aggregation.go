package statemanager

// ProgramAggregationKey defines the unique fields used to group process instances
// into a single aggregated program (by name+hash) metric.
type ProgramAggregationKey struct {
	Name          string
	ImageChecksum uint32
	SessionID     uint32
}

// AggregatedProgramMetrics holds the aggregated metrics for a single program signature.
// This is the data structure that collectors will read from on the warm path.
type AggregatedProgramMetrics struct {
	Disk     *AggregatedDiskMetrics
	Memory   *AggregatedMemoryMetrics
	Network  *AggregatedNetworkMetrics
	Registry *AggregatedRegistryMetrics
	Threads  *AggregatedThreadMetrics
}

// AggregatedDiskMetrics holds the final, summed disk I/O metrics for a program.
type AggregatedDiskMetrics struct {
	// Keyed by physical disk number.
	Disks map[uint32]*FinalDiskMetrics
}

// FinalDiskMetrics holds the simple integer counters after aggregation from atomics.
type FinalDiskMetrics struct {
	IOCount      [DiskOpCount]int64
	BytesRead    int64
	BytesWritten int64
}

// AggregatedMemoryMetrics holds the final, summed memory metrics for a program.
type AggregatedMemoryMetrics struct {
	HardPageFaults uint64
}

// AggregatedNetworkMetrics holds the final, summed network metrics for a program.
type AggregatedNetworkMetrics struct {
	BytesSent            [protocolMax]uint64
	BytesReceived        [protocolMax]uint64
	ConnectionsAttempted [protocolMax]uint64
	ConnectionsAccepted  [protocolMax]uint64
	RetransmissionsTotal uint64
	ConnectionsFailed    map[uint32]uint64 // Key: (protocol << 16) | failureCode
}

// AggregatedRegistryMetrics holds the final, summed registry metrics for a program.
type AggregatedRegistryMetrics struct {
	Operations map[RegistryOperationKey]uint64
}

// AggregatedThreadMetrics holds the final, summed thread metrics for a program.
type AggregatedThreadMetrics struct {
	ContextSwitches uint64
}

// newAggregatedProgramMetrics creates a new, empty aggregated metrics object.
func newAggregatedProgramMetrics() *AggregatedProgramMetrics {
	return &AggregatedProgramMetrics{}
}

// AggregateMetrics performs the on-demand aggregation of all instance metrics
// into program-level metrics. This is called once per scrape on the warm path,
// ensuring collectors read from a fresh, consistent snapshot.
func (sm *KernelStateManager) AggregateMetrics() {
	// 1. Get a fresh map from the pool for the new aggregation results.
	newAggregatedData := sm.aggregationDataPool.Get().(map[ProgramAggregationKey]*AggregatedProgramMetrics)

	// 2. Iterate over all active process instances on the hot path.
	sm.instanceData.Range(func(startKey uint64, pData *ProcessData) bool {
		// Create the aggregation key from the process's static info.
		pData.Info.mu.Lock()
		key := ProgramAggregationKey{
			Name:          pData.Info.Name,
			ImageChecksum: pData.Info.ImageChecksum,
			SessionID:     pData.Info.SessionID,
		}
		pData.Info.mu.Unlock()

		// Get or create the aggregated metrics entry for this program.
		aggMetrics, exists := newAggregatedData[key]
		if !exists {
			aggMetrics = newAggregatedProgramMetrics()
			newAggregatedData[key] = aggMetrics
		}

		// --- Aggregate Disk Module ---
		if diskModule := pData.Disk; diskModule != nil {
			// Lazily initialize the aggregated disk module if this is the first time we see it.
			if aggMetrics.Disk == nil {
				aggMetrics.Disk = &AggregatedDiskMetrics{
					Disks: make(map[uint32]*FinalDiskMetrics),
				}
			}

			diskModule.mu.RLock()
			for diskNum, instanceDiskMetrics := range diskModule.Disks {
				// Get or create the final disk metrics entry for this disk number.
				finalDiskMetrics, diskExists := aggMetrics.Disk.Disks[diskNum]
				if !diskExists {
					finalDiskMetrics = &FinalDiskMetrics{}
					aggMetrics.Disk.Disks[diskNum] = finalDiskMetrics
				}

				// Add the atomic values from the instance to the final counters.
				finalDiskMetrics.BytesRead += instanceDiskMetrics.BytesRead.Load()
				finalDiskMetrics.BytesWritten += instanceDiskMetrics.BytesWritten.Load()
				for op := 0; op < int(DiskOpCount); op++ {
					finalDiskMetrics.IOCount[op] += instanceDiskMetrics.IOCount[op].Load()
				}
			}
			diskModule.mu.RUnlock()
		}

		// --- Aggregate Memory Module ---
		if memModule := pData.Memory; memModule != nil {
			// Lazily initialize the aggregated memory module.
			if aggMetrics.Memory == nil {
				aggMetrics.Memory = &AggregatedMemoryMetrics{}
			}
			aggMetrics.Memory.HardPageFaults += memModule.Metrics.HardPageFaults.Load()
		}

		// --- Aggregate Network Module ---
		if netModule := pData.Network; netModule != nil {
			// Lazily initialize the aggregated network module.
			if aggMetrics.Network == nil {
				aggMetrics.Network = &AggregatedNetworkMetrics{
					ConnectionsFailed: make(map[uint32]uint64),
				}
			}
			aggMetrics.Network.RetransmissionsTotal += netModule.Metrics.RetransmissionsTotal.Load()
			for i := 0; i < protocolMax; i++ {
				aggMetrics.Network.BytesSent[i] += netModule.Metrics.BytesSent[i].Load()
				aggMetrics.Network.BytesReceived[i] += netModule.Metrics.BytesReceived[i].Load()
				aggMetrics.Network.ConnectionsAttempted[i] += netModule.Metrics.ConnectionsAttempted[i].Load()
				aggMetrics.Network.ConnectionsAccepted[i] += netModule.Metrics.ConnectionsAccepted[i].Load()
			}

			netModule.mu.RLock()
			for key, counter := range netModule.ConnectionsFailed {
				aggMetrics.Network.ConnectionsFailed[key] += counter.Load()
			}
			netModule.mu.RUnlock()
		}

		// --- Aggregate Registry Module ---
		if regModule := pData.Registry; regModule != nil {
			// Lazily initialize the aggregated registry module.
			if aggMetrics.Registry == nil {
				aggMetrics.Registry = &AggregatedRegistryMetrics{
					Operations: make(map[RegistryOperationKey]uint64),
				}
			}

			for i := range &regModule.Operations {
				count := regModule.Operations[i].Load()
				if count > 0 {
					// Deconstruct the index back into opcode and result.
					opcode := uint8(i / int(ResultMax))
					result := RegistryResult(i % int(ResultMax))
					key := RegistryOperationKey{Opcode: opcode, Result: result}
					aggMetrics.Registry.Operations[key] += count
				}
			}
		}

		// --- Aggregate Thread Module ---
		// The ThreadModule is pre-allocated, so we can access it directly.
		if threadModule := pData.Threads; threadModule != nil {
			count := threadModule.ContextSwitches.Load()
			if count > 0 {
				// Lazily initialize the aggregated thread module.
				if aggMetrics.Threads == nil {
					aggMetrics.Threads = &AggregatedThreadMetrics{}
				}
				aggMetrics.Threads.ContextSwitches += count
			}
		}

		// --- Aggregation for other modules will be added here in future steps ---

		return true // Continue iteration
	})

	// 3. Atomically swap the live map with the newly prepared one.
	sm.aggregationMu.Lock()
	oldAggregatedData := sm.aggregatedData
	sm.aggregatedData = newAggregatedData
	sm.aggregationMu.Unlock()

	// 4. Clear the old map and return it to the pool for reuse in the next scrape.
	if oldAggregatedData != nil {
		for k := range oldAggregatedData {
			delete(oldAggregatedData, k)
		}
		sm.aggregationDataPool.Put(oldAggregatedData)
	}
}

// RangeAggregatedMetrics allows a collector to safely iterate over the most recently
// aggregated program metrics. The lock ensures the map isn't swapped during iteration.
func (sm *KernelStateManager) RangeAggregatedMetrics(f func(key ProgramAggregationKey,
	metrics *AggregatedProgramMetrics) bool) {

	sm.aggregationMu.Lock()
	data := sm.aggregatedData
	sm.aggregationMu.Unlock()

	if data == nil {
		return
	}

	for key, metrics := range data {
		if !f(key, metrics) {
			break
		}
	}
}
