package reconciler

import (
	"sync"
	"time"
)

// CrashTracker tracks plugin crashes within a time window.
type CrashTracker struct {
	mu          sync.RWMutex
	crashes     map[string]*CrashRecord
	resetWindow time.Duration
}

// CrashRecord maintains crash history for a plugin.
type CrashRecord struct {
	Count     int       // Number of crashes in current window
	FirstTime time.Time // Time of first crash in window
	LastTime  time.Time // Time of most recent crash
}

// NewCrashTracker creates a new CrashTracker with the specified reset window.
func NewCrashTracker(resetWindow time.Duration) *CrashTracker {
	return &CrashTracker{
		crashes:     make(map[string]*CrashRecord),
		resetWindow: resetWindow,
	}
}

// RecordCrash records a crash for the specified plugin.
func (ct *CrashTracker) RecordCrash(pluginID string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	now := time.Now()
	record, exists := ct.crashes[pluginID]

	if !exists {
		// First crash for this plugin
		ct.crashes[pluginID] = &CrashRecord{
			Count:     1,
			FirstTime: now,
			LastTime:  now,
		}
		return
	}

	// Check if we should reset the window
	if now.Sub(record.FirstTime) > ct.resetWindow {
		// Outside reset window - start new window
		ct.crashes[pluginID] = &CrashRecord{
			Count:     1,
			FirstTime: now,
			LastTime:  now,
		}
		return
	}

	// Within window - increment count
	record.Count++
	record.LastTime = now
}

// GetCount returns the crash count for the specified plugin within the current window.
func (ct *CrashTracker) GetCount(pluginID string) int {
	ct.mu.RLock()
	defer ct.mu.RUnlock()

	record, exists := ct.crashes[pluginID]
	if !exists {
		return 0
	}

	// Check if record is still within window
	if time.Since(record.FirstTime) > ct.resetWindow {
		return 0
	}

	return record.Count
}

// Reset clears the crash record for the specified plugin.
func (ct *CrashTracker) Reset(pluginID string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.crashes, pluginID)
}

// GetRecord returns the crash record for the specified plugin (for testing).
func (ct *CrashTracker) GetRecord(pluginID string) *CrashRecord {
	ct.mu.RLock()
	defer ct.mu.RUnlock()
	return ct.crashes[pluginID]
}

// Cleanup removes expired crash records (beyond reset window).
func (ct *CrashTracker) Cleanup() {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	now := time.Now()
	for pluginID, record := range ct.crashes {
		if now.Sub(record.FirstTime) > ct.resetWindow {
			delete(ct.crashes, pluginID)
		}
	}
}
