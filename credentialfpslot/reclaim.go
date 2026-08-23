package credentialfpslot

// reclaim.go — background goroutine that proactively deletes idle slots
// before their Redis TTL expires.
//
// 2026-06-24: ENABLED by default. The previous comment ("NOT WIRED")
// was correct in 2026-06-18 but the operator rule "5分钟内的会话
// 不允许抢的" + "5min idle → reclaim" makes the reclaim goroutine
// load-bearing: in-line Acquire-time preemption is fine for one
// stealing event, but slots that have been idle past the active
// gate but have no incoming traffic (i.e. nobody tries to steal)
// would otherwise stick around for the full 30-min Redis TTL.
// The reclaim goroutine sweeps them every scanInterval.
//
// Tuning knobs live in reclaimConfig (defaults via
// defaultReclaimConfig). The active gate is shared with the
// Manager.Config.ActiveGateSeconds (single source of truth) — see
// reclaimConfigFromManager. To enable from a custom call site,
// invoke Manager.reclaimLoopStart(ctx, cfg) directly.

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

// reclaimSlotScript atomically deletes a slot when the holder has
// been idle for at least idle_after seconds.
//
// KEYS[1] = slot key
// ARGV[1] = idle_after_seconds  (active gate)
// ARGV[2] = slot_ttl_seconds   (used to compute "real" idle time
//
//	from remaining TTL; we want to
//	reclaim when remaining TTL has
//	dropped by at least idle_after)
//
// Returns: 1 if deleted, 0 if still fresh or absent.
//
// 2026-06-24: behaviour was "remaining TTL < idle → reclaim" which
// is wrong (it would reclaim a fresh slot whose remaining TTL
// happens to be < idle). The correct condition is "real idle
// since last refresh ≥ active gate", i.e. slotTTL - remaining
// ≥ idle_after.
//
// We do NOT track the holder name on the slot side — the slot
// key only has the holder as a value. The pin key (holder -> slot)
// expires on its own TTL (pin TTL = 24h). So reclaiming the slot
// is sufficient; the pin will follow naturally. If a stale pin
// still points at this slot index, the new holder's acquire
// path treats it as a stale pin and either re-pins or evicts.
var reclaimSlotScript = redis.NewScript(`
    local slotKey    = KEYS[1]
    local idle_after = tonumber(ARGV[1])
    local slot_ttl   = tonumber(ARGV[2])

    local remaining = redis.call('TTL', slotKey)
    if remaining == -1 or remaining == -2 then
        return 0
    end
    local idle = slot_ttl - remaining
    if idle < idle_after then
        return 0
    end
    redis.call('DEL', slotKey)
    return 1
`)

// reclaimConfig holds the parameters for the background reclaim goroutine.
type reclaimConfig struct {
	// idleAfter is the idle threshold above which a slot is
	// reclaimed. 2026-06-24: this is DECOUPLED from the active
	// gate. The active gate (default 5 min) governs in-line
	// Acquire-time preemption; the reclaim idle (default 30 min)
	// governs the background sweep. 30 min matches the slot TTL,
	// so reclaim is effectively a Redis-persistence safety net.
	// Per operator spec (2026-06-24): "自动清除无活动的时长，放到
	// 30分钟，这个可以作成常量，由系统设置中来设置".
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
		// 30 min — same as the slot TTL itself. Reclaim is now a
		// safety net for slots that would otherwise outlive the
		// documented 30 min due to Redis persistence edge cases
		// (AOF rewrite stalls, RDB snapshot pauses).
		idleAfter:    30 * time.Minute,
		scanInterval: 30 * time.Second,
		totalTTL:     24 * time.Hour,
		clientTTL:    30 * 24 * time.Hour,
	}
}

// reclaimConfigFromManager builds a reclaim config from the
// Manager's ReclaimIdleSeconds, leaving the other knobs at their
// defaults. Operators tune Config.ReclaimIdleSeconds; the other
// knobs (scanInterval, totalTTL, clientTTL) are operational
// tuning that lives in reclaimConfig directly.
func (m *Manager) reclaimConfigFromManager() reclaimConfig {
	cfg := defaultReclaimConfig()
	if m != nil && m.cfg.ReclaimIdleSeconds > 0 {
		cfg.idleAfter = time.Duration(m.cfg.ReclaimIdleSeconds) * time.Second
	}
	return cfg
}

// reclaimLoop is the background goroutine that periodically scans Redis
// for idle slots and deletes them. Started by StartReclaim and stopped
// by StopReclaim.
type reclaimLoop struct {
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex //nolint:unused
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
func (m *Manager) reclaimLoopStop() { //nolint:unused
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

// reclaimIdleSlots reclaims indexed slot keys. Existing deployments are
// seeded once from the legacy SCAN path, then subsequent ticks avoid walking
// the shared Redis keyspace.
func (m *Manager) reclaimIdleSlots(ctx context.Context, cfg reclaimConfig) (int, error) {
	if m.client == nil {
		return 0, nil
	}

	slotKeys, indexed, err := m.indexedSlotKeys(ctx)
	if err != nil {
		return 0, err
	}
	if !indexed {
		iter := m.client.Scan(ctx, 0, "llmgw:tenant:*:cred_fp_slot:*:*", 1000).Iterator()
		for iter.Next(ctx) {
			slotKeys = append(slotKeys, iter.Val())
		}
		if err := iter.Err(); err != nil {
			return 0, err
		}
		pipe := m.client.Pipeline()
		if len(slotKeys) > 0 {
			pipe.SAdd(ctx, slotReclaimIndexKey(), stringSliceToAny(slotKeys)...)
		}
		// Redis removes empty sets. A sentinel keeps the initialized marker so
		// an idle system does not repeat a full SCAN every reclaim tick.
		pipe.SAdd(ctx, slotReclaimIndexKey(), slotIndexSentinel)
		pipe.Expire(ctx, slotReclaimIndexKey(), time.Duration(slotIndexTTLSeconds)*time.Second)
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			return 0, err
		}
	}

	totalReclaimed := 0
	for _, slotKey := range slotKeys {
		// Skip if key disappeared between SCAN and the script call.
		res, err := reclaimSlotScript.Run(ctx, m.client,
			[]string{slotKey},
			int(cfg.idleAfter.Seconds()),
			slotTTLSeconds,
		).Int()
		if err != nil {
			return totalReclaimed, fmt.Errorf("reclaim slot %s: %w", slotKey, err)
		}
		if res == 1 {
			totalReclaimed++
			recordReclaim()
			pipe := m.client.Pipeline()
			pipe.SRem(ctx, slotReclaimIndexKey(), slotKey)
			pipe.SAdd(ctx, slotReclaimIndexKey(), slotIndexSentinel)
			pipe.Expire(ctx, slotReclaimIndexKey(), time.Duration(slotIndexTTLSeconds)*time.Second)
			_, _ = pipe.Exec(ctx)
		}

	}
	return totalReclaimed, nil
}

func (m *Manager) indexedSlotKeys(ctx context.Context) ([]string, bool, error) {
	exists, err := m.client.Exists(ctx, slotReclaimIndexKey()).Result()
	if err != nil {
		return nil, false, err
	}
	if exists == 0 {
		return nil, false, nil
	}
	members, err := m.client.SMembers(ctx, slotReclaimIndexKey()).Result()
	if err != nil {
		return nil, true, err
	}
	if len(members) == 0 {
		return nil, true, nil
	}
	pipe := m.client.Pipeline()
	commands := make([]*redis.IntCmd, len(members))
	for i, key := range members {
		commands[i] = pipe.Exists(ctx, key)
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, true, err
	}
	keys := make([]string, 0, len(members))
	stale := make([]any, 0)
	for i, command := range commands {
		if members[i] == slotIndexSentinel {
			continue
		}
		if command.Err() == nil && command.Val() > 0 {
			keys = append(keys, members[i])
		} else {
			stale = append(stale, members[i])
		}
	}
	if len(stale) > 0 {
		pipe := m.client.Pipeline()
		pipe.SRem(ctx, slotReclaimIndexKey(), stale...)
		pipe.SAdd(ctx, slotReclaimIndexKey(), slotIndexSentinel)
		pipe.Expire(ctx, slotReclaimIndexKey(), time.Duration(slotIndexTTLSeconds)*time.Second)
		_, _ = pipe.Exec(ctx)
	}
	return keys, true, nil
}

func stringSliceToAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// parseSlotKey extracts (credentialID, slotIndex) from a key like
// "llmgw:cred_fp_slot:123:4".
func parseSlotKey(key string) (credID, slotIdx int, ok bool) { //nolint:unused
	const marker = ":cred_fp_slot:"
	start := strings.Index(key, marker)
	if start < 0 {
		return 0, 0, false
	}
	rest := key[start+len(marker):]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return 0, 0, false
	}
	credID, err1 := strconv.Atoi(rest[:colon])
	slotIdx, err2 := strconv.Atoi(rest[colon+1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return credID, slotIdx, true
}
