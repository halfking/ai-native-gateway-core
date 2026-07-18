package credential

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const rpmWindowSeconds = 60.0

// RPMLimiter reserves a request slot in a per-credential RPM window.
type RPMLimiter interface {
	CheckAndReserve(ctx context.Context, providerID, credentialID int, limit int) (bool, int, error)
}

// MemoryRPMLimiter is the process-local fallback RPM implementation.
type MemoryRPMLimiter struct {
	credsRPM map[string]*rpmWindow
	mu       sync.Mutex
}

// NewMemoryRPMLimiter creates a process-local RPM limiter.
func NewMemoryRPMLimiter() *MemoryRPMLimiter {
	return &MemoryRPMLimiter{credsRPM: make(map[string]*rpmWindow)}
}

// CheckAndReserve checks and records one request in the sliding window.
func (m *MemoryRPMLimiter) CheckAndReserve(ctx context.Context, providerID, credentialID int, limit int) (bool, int, error) {
	if err := ctx.Err(); err != nil {
		return false, 0, nil
	}
	if limit <= 0 {
		return true, 0, nil
	}

	key := fmt.Sprintf("%d/%d", providerID, credentialID)
	now := float64(time.Now().UnixMilli()) / 1000.0
	cutoff := now - rpmWindowSeconds

	m.mu.Lock()
	defer m.mu.Unlock()

	w, ok := m.credsRPM[key]
	if !ok {
		w = &rpmWindow{}
		m.credsRPM[key] = w
	}
	filtered := w.timestamps[:0]
	for _, timestamp := range w.timestamps {
		if timestamp > cutoff {
			filtered = append(filtered, timestamp)
		}
	}
	w.timestamps = filtered
	if len(w.timestamps) >= limit {
		return false, len(w.timestamps), nil
	}
	w.timestamps = append(w.timestamps, now)
	return true, len(w.timestamps), nil
}
