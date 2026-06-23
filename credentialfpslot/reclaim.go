package credentialfpslot

// reclaim.go — background goroutine that proactively deletes idle slots
// before their Redis TTL expires. NOT WIRED: as of commit 96832f01 the
// slot TTL is 30 min and Redis auto-expiry is sufficient for the
// "30 min no request → free" requirement. The reclaim loop is kept as
// an opt-in escape hatch for tighter reclaim policies (e.g. < 30 min)
// that may come in the future.
//
// All functions in this file are dead code in production. They are
// exercised by reclaim_test.go to keep the reclaim logic verified.
// To enable: call Manager.reclaimLoopStart(ctx, reclaimConfig{...}) in
// cmd/gateway/main.go after constructing the fpSlots Manager.

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// reclaimSlotScript atomically deletes a slot + its pin when the slot
// TTL is at or below the idle threshold.
//
// KEYS[1] = slot key
// ARGV[1] = idle_after_seconds
// Returns: 1 if deleted, 0 if still fresh or absent.
//
// Implementation note: we DON'T track the holder name on the slot side —
// the slot key only has the holder as a value. The pin key (holder -> slot)
// will expire naturally on its own TTL (pin TTL == idle threshold). So
// reclaiming the slot is sufficient; the pin will follow on its own.
var reclaimSlotScript = redis.NewScript(`
    local slotKey = KEYS[1]
    local idle    = tonumber(ARGV[1])

    local slotTTL = redis.call('TTL', slotKey)
    if slotTTL == -1 or slotTTL == -2 then
        return 0
    end
    if slotTTL > idle then
        return 0
    end
    redis.call('DEL', slotKey)
    return 1
`)

// reclaimConfig holds the parameters for the background reclaim goroutine.
type reclaimConfig struct {
	// idleAfter is how long a slot can have its TTL below the threshold
	// before it gets reclaimed. Per user spec, default 15 min.
	idleAfter time.Duration

	// scanInterval is how often the goroutine wakes up. Default 30s.
	scanInterval time.Duration

	// totalTTL is the absolute upper bound on a slot's lifetime.
	// After this many hours, even an active slot is reclaimed. Default 24h.
	totalTTL time.Duration

	// clientTTL is how long an inactive client fingerprint is kept
	// before its slot is forcibly released. Per user spec, default 30 days.
	clientTTL time.Duration
}

func defaultReclaimConfig() reclaimConfig {
	return reclaimConfig{
		idleAfter:    15 * time.Minute,
		scanInterval: 30 * time.Second,
		totalTTL:     24 * time.Hour,
		clientTTL:    30 * 24 * time.Hour,
	}
}

// reclaimLoop is the background goroutine that periodically scans Redis
// for idle slots and deletes them. Started by StartReclaim and stopped
// by StopReclaim.
type reclaimLoop struct {
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	running bool
}

// reclaimLoopStart launches the background reclaim loop. Idempotent.
func (m *Manager) reclaimLoopStart(parent context.Context, cfg reclaimConfig) {
	m.reclaimLoopMu.Lock()
	defer m.reclaimLoopMu.Unlock()
	if m.reclaimLoop.running {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.reclaimLoop.cancel = cancel
	m.reclaimLoop.done = make(chan struct{})
	m.reclaimLoop.running = true

	go m.reclaimLoopRun(ctx, cfg)
	slog.Info("credentialfpslot: reclaim loop started",
		"idle_after", cfg.idleAfter,
		"scan_interval", cfg.scanInterval,
		"total_ttl", cfg.totalTTL,
		"client_ttl", cfg.clientTTL,
	)
}

// reclaimLoopStop signals the goroutine to exit and waits for it.
func (m *Manager) reclaimLoopStop() {
	m.reclaimLoopMu.Lock()
	if !m.reclaimLoop.running {
		m.reclaimLoopMu.Unlock()
		return
	}
	cancel := m.reclaimLoop.cancel
	done := m.reclaimLoop.done
	m.reclaimLoop.running = false
	m.reclaimLoopMu.Unlock()

	cancel()
	<-done
}

// reclaimLoopRun is the main loop. It performs one scan per tick.
func (m *Manager) reclaimLoopRun(ctx context.Context, cfg reclaimConfig) {
	defer close(m.reclaimLoop.done)
	ticker := time.NewTicker(cfg.scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			slots, err := m.reclaimIdleSlots(scanCtx, cfg)
			cancel()
			if err != nil {
				slog.Debug("credentialfpslot: reclaim scan failed", "error", err)
				continue
			}
			if slots > 0 {
				slog.Info("credentialfpslot: reclaimed idle slots",
					"count", slots,
					"idle_after", cfg.idleAfter,
				)
			}
		}
	}
}

// reclaimIdleSlots walks all slot keys via SCAN and reclaims those whose
// TTL is at or below idleAfter.
func (m *Manager) reclaimIdleSlots(ctx context.Context, cfg reclaimConfig) (int, error) {
	if m.client == nil {
		return m.reclaimIdleSlotsMemory(ctx, cfg)
	}

	totalReclaimed := 0
	iter := m.client.Scan(ctx, 0, "llmgw:cred_fp_slot:*", 1000).Iterator()
	for iter.Next(ctx) {
		slotKey := iter.Val()
		// Skip if key disappeared between SCAN and the script call.
		res, err := reclaimSlotScript.Run(ctx, m.client,
			[]string{slotKey},
			int(cfg.idleAfter.Seconds()),
		).Int()
		if err != nil {
			return totalReclaimed, fmt.Errorf("reclaim slot %s: %w", slotKey, err)
		}
		if res == 1 {
			totalReclaimed++
		}
	}
	if err := iter.Err(); err != nil {
		return totalReclaimed, err
	}
	return totalReclaimed, nil
}

// reclaimIdleSlotsMemory is the in-memory fallback (used when Redis is
// unavailable, e.g., in unit tests). It walks memSlots and drops entries
// that have been idle for at least idleAfter.
func (m *Manager) reclaimIdleSlotsMemory(ctx context.Context, cfg reclaimConfig) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	cutoff := cfg.idleAfter
	totalReclaimed := 0

	// A slot is "idle" if its expiry is older than (now - cutoff).
	// In memory mode, exp is the absolute expiry; an entry that was
	// refreshed at time T has exp = T + slotTTLSeconds.
	// So idle duration = (now - (exp - slotTTLSeconds)) = now - (last refresh).
	// We approximate by checking if (slotTTL - remaining) > cutoff.
	slotTTL := m.slotTTLSeconds()
	for k, e := range m.memSlots {
		lastRefresh := e.exp.Add(-time.Duration(slotTTL) * time.Second)
		if now.Sub(lastRefresh) > cutoff {
			delete(m.memSlots, k)
			totalReclaimed++
		}
	}

	// Also drop pins older than idleAfter (mirror of slot reclaim).
	// A pin is expired when: now > exp (absolute expiry passed)
	// We want: last activity (exp - pinTTL) was > cutoff ago
	// So: now - (exp - pinTTL) > cutoff
	// Which means: exp < now - cutoff + pinTTL
	// But pins have same TTL as slots in this implementation, so we just check absolute expiry.
	for k, e := range m.memPins {
		if now.Before(e.exp) {
			// pin still fresh (exp is in the future)
			continue
		}
		// Pin has expired (now >= exp), delete it
		delete(m.memPins, k)
	}

	return totalReclaimed, nil
}

// parseSlotKey extracts (credentialID, slotIndex) from a key like
// "llmgw:cred_fp_slot:123:4".
func parseSlotKey(key string) (credID, slotIdx int, ok bool) {
	const prefix = "llmgw:cred_fp_slot:"
	if !strings.HasPrefix(key, prefix) {
		return 0, 0, false
	}
	rest := key[len(prefix):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return 0, 0, false
	}
	c1, err1 := strconv.Atoi(rest[:colon])
	c2, err2 := strconv.Atoi(rest[colon+1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return c1, c2, true
}
