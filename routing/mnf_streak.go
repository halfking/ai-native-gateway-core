package routing

import (
	"sync"
	"time"
)

// MnfStreak tracks consecutive model_not_found occurrences per
// (stickyKey, credentialID) pair. When the count reaches the configured
// threshold (default 3), the executor breaks the sticky binding so the
// next request re-selects a credential instead of repeatedly hitting
// the same broken one.
//
// Design notes (2026-06-18):
//   - key = stickyKey + ":" + strconv.Itoa(credentialID). Counters are
//     isolated per credential; A failing 2x and B failing 1x are
//     independent streaks.
//   - Reset must be called on the same key the Increment used.
//   - LRU eviction at capacity to bound memory. Default cap 10000.
//   - Intentionally in-memory only. Pod restart resets counts, which
//     is acceptable — the worst case is a one-pod re-occurrence before
//     a 3-streak is rebuilt.
//
// Thread-safety: all methods are safe for concurrent use.
type MnfStreak struct {
	mu       sync.Mutex
	items    map[string]*mnfEntry
	lruOrder []string
	cap      int
}

type mnfEntry struct {
	count  int
	lastAt time.Time
}

// NewMnfStreak creates a new MnfStreak. If capacity <= 0, default 10000 is used.
func NewMnfStreak(capacity int) *MnfStreak {
	if capacity <= 0 {
		capacity = 10000
	}
	return &MnfStreak{
		items: make(map[string]*mnfEntry),
		cap:   capacity,
	}
}

// Increment atomically bumps the counter for key and returns the new count.
// Triggers LRU eviction when the map exceeds capacity.
func (m *MnfStreak) Increment(key string) int {
	if key == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[key]
	if !ok {
		e = &mnfEntry{}
		m.items[key] = e
		m.lruOrder = append(m.lruOrder, key)
		m.evictIfNeededLocked()
	}
	e.count++
	e.lastAt = time.Now()
	return e.count
}

// Reset clears the counter for key. No-op if key is not present.
func (m *MnfStreak) Reset(key string) {
	if key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, key)
	for i, k := range m.lruOrder {
		if k == key {
			m.lruOrder = append(m.lruOrder[:i], m.lruOrder[i+1:]...)
			break
		}
	}
}

// Get returns the current count for key (0 if not present).
func (m *MnfStreak) Get(key string) int {
	if key == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.items[key]; ok {
		return e.count
	}
	return 0
}

// Len returns the number of tracked keys.
func (m *MnfStreak) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// BuildMnfStreakKey returns the canonical key for a (stickyKey, credentialID)
// pair. Centralized here so executor.go and tests stay in sync.
func BuildMnfStreakKey(stickyKey string, credentialID int) string {
	return stickyKey + ":" + itoa(credentialID)
}

// itoa is a small local helper to avoid importing strconv in callers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// evictIfNeededLocked removes the oldest entries until the map is at or
// below capacity. Caller must hold m.mu.
func (m *MnfStreak) evictIfNeededLocked() {
	for len(m.items) > m.cap && len(m.lruOrder) > 0 {
		oldest := m.lruOrder[0]
		m.lruOrder = m.lruOrder[1:]
		delete(m.items, oldest)
	}
}
