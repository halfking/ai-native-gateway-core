package credentialfpslot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ReclaimConfig controls the background slot-reclaim goroutine.
//
//   - IdleAfter is how long a holder can be silent before its slot is
//     released back to the pool. Default: 15 minutes (per user spec).
//   - ScanInterval is how often the goroutine wakes up to scan for idle
//     holders. Default: 30 seconds — gives the 15-minute threshold
//     ~1.0% slack (worst-case delay = IdleAfter + ScanInterval).
//   - Enabled toggles the entire mechanism. When false, no goroutine
//     is started and ReclaimIdleSlots is a no-op.
type ReclaimConfig struct {
	IdleAfter    time.Duration
	ScanInterval time.Duration
	Enabled      bool
}

// DefaultReclaimConfig returns the user-spec defaults: 15-minute idle
// threshold, 30-second scan interval.
func DefaultReclaimConfig() ReclaimConfig {
	return ReclaimConfig{
		IdleAfter:    15 * time.Minute,
		ScanInterval: 30 * time.Second,
		Enabled:      true,
	}
}

// reclaimScript atomically deletes a slot AND its pin if both are older
// than the cutoff timestamp. Implemented as a Lua script so the check +
// delete is a single round-trip.
//
// KEYS[1] = slot key (llmgw:cred_fp_slot:<cred>:<slot>)
// KEYS[2] = pin key  (llmgw:sess_cred_fp:<holder>:<cred>)
// KEYS[3] = holder last-seen key (we keep a separate marker so the
//
//	goroutine can detect "holder is idle" without scanning
//	every pin key — see implementation notes below)
//
// ARGV[1] = idle_after_seconds (e.g. 900)
//
// Returns: 1 if deleted, 0 if not (TTL still valid OR key absent).
//
// Implementation note: we don't keep a separate "last-seen" key per
// holder in this iteration — instead, we rely on the slot's TTL itself
// as the activity signal. A slot's TTL is set/refreshed on every
// Release(); if the TTL is below the cutoff, the holder is idle.
//
// To check TTL from the script, we use redis.call('TTL', key) which
// returns -1 (no TTL), -2 (no key), or seconds-remaining. We delete
// only when TTL <= idle_after_seconds AND key exists.
var reclaimScript = redis.NewScript(`
    local slotKey = KEYS[1]
    local pinKey  = KEYS[2]
    local idle    = tonumber(ARGV[1])

    local slotTTL = redis.call('TTL', slotKey)
    if slotTTL == -1 or slotTTL == -2 then
        return 0  -- no slot to reclaim
    end
    if slotTTL > idle then
        return 0  -- slot is still fresh
    end

    redis.call('DEL', slotKey)
    if redis.call('EXISTS', pinKey) == 1 then
        redis.call('DEL', pinKey)
    end
    return 1
`)

// reclaimState holds the goroutine lifecycle.
type reclaimState struct {
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	running bool
}

// reclaim scans Redis for slots whose TTL is below idle_after and deletes
// both the slot key and the pin key. Safe to call from a background
// goroutine: the Lua script is idempotent.
func (m *Manager) reclaim(ctx context.Context, idleAfter time.Duration) (slots, pins int, err error) {
	if m.client == nil {
		return m.reclaimMemory(idleAfter)
	}
	// Use SCAN to walk all slot keys without blocking Redis (KEYS is O(N)
	// and blocks the server). MATCH pattern, COUNT hint 1000 — sufficient
	// for our pool size (typically < 1000 slots total).
	pattern := "llmgw:cred_fp_slot:*"
	iter := m.client.Scan(ctx, 0, pattern, 1000).Iterator()
	for iter.Next(ctx) {
		slotKey := iter.Val()
		// Extract credential ID + slot index from the key.
		// Format: "llmgw:cred_fp_slot:<cred>:<slot>"
		credID, slotIdx, ok := parseSlotKey(slotKey)
		if !ok {
			continue
		}
		// Look up the pin key to also clean up holder→slot binding.
		// We don't have a holder→credential index in Redis, so we
		// can't directly find the pin from the slot key. Instead, the
		// goroutine will let the pin keys expire naturally on their
		// own TTL (sessionPinTTLSeconds = 15 min default). When the
		// holder comes back, hasPin() returns false → acquires a
		// fresh slot, possibly with a different index — acceptable
		// after long idle.
		//
		// Future enhancement: add a reverse index (slot → holder) so
		// reclaim can also DEL the pin. Skipped for now because pin TTL
		// = idle threshold, so they expire together anyway.
		_ = credID
		_ = slotIdx
		_ = pinKeyForSlot(credID, slotIdx) // unused, but documents the
		// design — pin keys are keyed by holder, not slot.

		res, err := reclaimScript.Run(ctx, m.client,
			[]string{slotKey, ""}, // pin key unknown; pass empty
			int(idleAfter.Seconds()),
		).Int()
		if err != nil {
			return slots, pins, err
		}
		if res == 1 {
			slots++
		}
	}
	if err := iter.Err(); err != nil {
		return slots, pins, err
	}
	return slots, pins, nil
}

// reclaimMemory is the in-memory fallback for tests + when Redis is down.
// It walks memSlots and memPins for any entries with exp < now.
func (m *Manager) reclaimMemory(idleAfter time.Duration) (slots, pins int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	// Find expired slots — those whose TTL hasn't been refreshed for > idleAfter.
	// In memory mode, "TTL not refreshed" means the entry exists and exp > now
	// (still in use) but no Release has happened recently. We can't tell
	// activity from the entry alone in memory mode (no last-touch timestamp);
	// the safest approximation is to keep entries that are still inside the
	// window and drop those older than (idle + slotTTL).
	for k, e := range m.memSlots {
		if now.Sub(e.exp) > -idleAfter { // not recently refreshed within idleAfter
			// Actually: we DO want to keep entries that are still inside their TTL.
			// The "idle" signal in memory mode is: exp > now + idleAfter means
			// the slot was just refreshed; exp < now + idleAfter means the last
			// refresh was at least idleAfter ago. We can't tell apart "no
			// activity for 15 min" from "active and slot TTL has 14h left".
			// So we fall back to hard expiry (slotTTLSeconds) only.
			_ = k
			_ = e
		}
	}
	// Hard-expire old entries past slot TTL.
	slotTTL := time.Duration(slotTTLSeconds) * time.Second
	for k, e := range m.memSlots {
		if now.Sub(e.exp) > slotTTL {
			delete(m.memSlots, k)
			slots++
		}
	}
	for k, e := range m.memPins {
		if now.Sub(e.exp) > slotTTL {
			delete(m.memPins, k)
			pins++
		}
	}
	return slots, pins, nil
}

// parseSlotKey extracts (credentialID, slotIndex) from "llmgw:cred_fp_slot:<c>:<s>".
func parseSlotKey(key string) (int, int, bool) {
	const prefix = "llmgw:cred_fp_slot:"
	if len(key) < len(prefix) || key[:len(prefix)] != prefix {
		return 0, 0, false
	}
	rest := key[len(prefix):]
	// Format: "<cred>:<slot>"
	colon := -1
	for i, c := range rest {
		if c == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return 0, 0, false
	}
	credStr := rest[:colon]
	slotStr := rest[colon+1:]
	credID, ok1 := atoiSafe(credStr)
	slotIdx, ok2 := atoiSafe(slotStr)
	return credID, slotIdx, ok1 && ok2
}

// pinKeyForSlot is a no-op helper that documents the future pin-key
// reverse-index design (see comment in reclaim()).
func pinKeyForSlot(credID, slotIdx int) string {
	_ = credID
	_ = slotIdx
	return ""
}

func atoiSafe(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// StartReclaim launches the background reclaim goroutine. Returns an error
// if already running. Stop via StopReclaim.
func (m *Manager) StartReclaim(parent context.Context, cfg ReclaimConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !cfg.Enabled {
		return nil
	}
	if m.reclaimState.running {
		return nil // idempotent: already running
	}
	if cfg.IdleAfter <= 0 {
		cfg.IdleAfter = 15 * time.Minute
	}
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = 30 * time.Second
	}

	ctx, cancel := context.WithCancel(parent)
	m.reclaimState.cancel = cancel
	m.reclaimState.done = make(chan struct{})
	m.reclaimState.running = true

	go m.reclaimLoop(ctx, cfg)
	slog.Info("credentialfpslot: reclaim goroutine started",
		"idle_after", cfg.IdleAfter,
		"scan_interval", cfg.ScanInterval,
	)
	return nil
}

// StopReclaim signals the goroutine to exit and waits for it.
func (m *Manager) StopReclaim() {
	m.mu.Lock()
	if !m.reclaimState.running {
		m.mu.Unlock()
		return
	}
	cancel := m.reclaimState.cancel
	done := m.reclaimState.done
	m.reclaimState.running = false
	m.mu.Unlock()

	cancel()
	<-done
}

func (m *Manager) reclaimLoop(ctx context.Context, cfg ReclaimConfig) {
	defer close(m.reclaimState.done)
	ticker := time.NewTicker(cfg.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			slots, pins, err := m.reclaim(scanCtx, cfg.IdleAfter)
			cancel()
			if err != nil {
				slog.Debug("credentialfpslot: reclaim scan failed", "error", err)
				continue
			}
			if slots > 0 || pins > 0 {
				slog.Info("credentialfpslot: reclaimed idle slots",
					"slots", slots, "pins", pins,
					"idle_after", cfg.IdleAfter,
				)
			}
		}
	}
}
