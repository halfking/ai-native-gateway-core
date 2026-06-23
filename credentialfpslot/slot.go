// Package credentialfpslot implements per-credential virtual fingerprint slot pools.
// Redis keys match the Python llm-gateway implementation for cross-runtime sharing.
package credentialfpslot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/identity"
	"github.com/redis/go-redis/v9"
)

const (
	// slotTTLSeconds 指纹槽的 TTL（30分钟无请求自动释放）
	// 最后一次请求后 30 分钟 Redis 自动过期 slot key，
	// 新客户端可以立即获取该槽位。
	slotTTLSeconds = 1800 // 30 minutes

	// sessionPinTTLSeconds pin 绑定的 TTL（24小时）
	// pin 记录该 holder 上次使用哪个槽位，即使 slot 已过期，
	// holder 回来后仍可快速重获同一槽位。
	sessionPinTTLSeconds = 86400 // 24 hours
)

// Config controls slot pool behaviour.
type Config struct {
	DefaultLimit int
	Enabled      bool

	// ActiveGateSeconds is the "active holder" threshold for slot
	// preemption. 2026-06-24: a slot whose holder has been active
	// within the last ActiveGateSeconds is considered active and
	// MUST NOT be preempted by a different holder. A slot whose
	// holder has been silent for at least ActiveGateSeconds is
	// eligible for preemption (LRU eviction or in-line steal).
	//
	// Per operator spec (2026-06-24): "5分钟内的会话不允许抢的".
	// Set to 0 to fall back to DefaultActiveGateSeconds (300).
	ActiveGateSeconds int

	// ReclaimIdleSeconds is the idle threshold for the BACKGROUND
	// reclaim goroutine. Different from ActiveGateSeconds: the
	// active gate is the in-line Acquire-time hard limit (active
	// vs idle), while reclaim is the asynchronous "this slot has
	// been silent for too long, just delete it" sweep.
	//
	// Per operator spec (2026-06-24): "自动清除无活动的时长，放到
	// 30分钟，这个可以作成常量，由系统设置中来设置". 30 min is the
	// default; in practice the goroutine only matters for slots
	// that are silently idle past the active gate AND have no
	// incoming traffic to trigger an in-line preempt. The slot
	// is otherwise "wasted" until Redis auto-expires it at 30 min
	// anyway, so reclaim is mostly a Redis-persistence safety net.
	// Set to 0 to fall back to DefaultReclaimIdleSeconds (1800).
	ReclaimIdleSeconds int
}

// DefaultActiveGateSeconds is the fallback when Config.ActiveGateSeconds
// is unset. Five minutes matches the operator's stated rule:
//
//	"如果slot满了，有人需要抢占，应该将最长时间没有交互的slot交出来"
//	"5分钟内的会话不允许抢的"
//
// Slot TTL is 30 min, so an active slot's remaining TTL is always
// > DefaultActiveGateSeconds (just refreshed → TTL ≈ 30 min > 5 min).
// An idle slot's TTL drops monotonically; once TTL - slotTTLSeconds
// <= -ActiveGateSeconds, i.e. remaining ≤ 30 - 5 = 25 min in
// absolute terms, the slot becomes preemptable. In practice the
// reclaim goroutine + Acquire path both gate on this.
const DefaultActiveGateSeconds = 300 // 5 minutes

// DefaultReclaimIdleSeconds is the fallback when
// Config.ReclaimIdleSeconds is unset. 30 minutes matches the slot
// TTL itself — reclaim is effectively a safety net for the case
// where the slot TTL is held alive by Redis persistence edge cases
// (AOF rewrite stalls, RDB snapshot pauses) and would otherwise
// outlive the documented 30 min.
//
// Per operator spec (2026-06-24): "自动清除无活动的时长，放到30
// 分钟，这个可以作成常量，由系统设置中来设置". The constant is
// here; the env var is in config.go.
const DefaultReclaimIdleSeconds = 1800 // 30 minutes

// Lease is one acquired fingerprint slot.
type Lease struct {
	SlotIndex    int
	Egress       *identity.EgressIdentity
	Unlimited    bool
	CredentialID int
	Holder       string
}

// resolveActiveGateSeconds returns the configured active gate, falling
// back to DefaultActiveGateSeconds for uninitialised / zero values.
func (c Config) resolveActiveGateSeconds() int {
	if c.ActiveGateSeconds <= 0 {
		return DefaultActiveGateSeconds
	}
	return c.ActiveGateSeconds
}

// resolveReclaimIdleSeconds returns the configured reclaim idle
// threshold, falling back to DefaultReclaimIdleSeconds for
// uninitialised / zero values.
func (c Config) resolveReclaimIdleSeconds() int {
	if c.ReclaimIdleSeconds <= 0 {
		return DefaultReclaimIdleSeconds
	}
	return c.ReclaimIdleSeconds
}

// Manager owns slot acquisition against Redis (or in-memory fallback).
type Manager struct {
	cfg      Config
	client   *redis.Client
	mu       sync.Mutex
	memSlots map[slotKey]memEntry
	memPins  map[string]memPinEntry

	// reclaimLoop / reclaimLoopMu track the background goroutine that
	// reclaims idle slots. See reclaim.go for the implementation.
	reclaimLoopMu sync.Mutex
	reclaimLoop   reclaimLoop
}

type slotKey struct {
	credentialID int
	slotIndex    int
}

type memEntry struct {
	holder string
	exp    time.Time
}

type memPinEntry struct {
	slot int
	exp  time.Time
}

// DefaultDefaultLimit is the fallback slot-pool size when neither
// the per-credential DB value nor the Config.DefaultLimit is set.
// 2026-06-24: bumped 5 → 20 per operator spec — wider pool avoids
// the "fp_slot contention" class of issues observed in production.
const DefaultDefaultLimit = 20

// New creates a slot manager. client may be nil (memory fallback).
func New(cfg Config, client *redis.Client) *Manager {
	if cfg.DefaultLimit <= 0 {
		cfg.DefaultLimit = DefaultDefaultLimit
	}
	if cfg.ActiveGateSeconds <= 0 {
		cfg.ActiveGateSeconds = DefaultActiveGateSeconds
	}
	if cfg.ReclaimIdleSeconds <= 0 {
		cfg.ReclaimIdleSeconds = DefaultReclaimIdleSeconds
	}
	return &Manager{
		cfg:      cfg,
		client:   client,
		memSlots: make(map[slotKey]memEntry),
		memPins:  make(map[string]memPinEntry),
	}
}

func (m *Manager) Enabled() bool {
	return m != nil && m.cfg.Enabled
}

// StartReclaim launches the background goroutine that proactively
// reclaims idle fingerprint slots before their Redis TTL expires.
// Idempotent: a second call is a no-op.
//
// 2026-06-24: reclaim is now load-bearing (not opt-in). Without it,
// slots that have been silent for ActiveGateSeconds but have no
// incoming traffic would stick around for the full 30-min Redis
// TTL, blocking later arrivals that would otherwise be eligible
// for an in-line Acquire-time preempt.
//
// The reclaim config is derived from the Manager's ActiveGateSeconds
// (single source of truth — no operator has to remember to set two
// knobs). To override scanInterval / totalTTL / clientTTL, call
// reclaimLoopStart directly with a custom reclaimConfig.
func (m *Manager) StartReclaim(parent context.Context) {
	m.reclaimLoopStart(parent, m.reclaimConfigFromManager())
}

// DefaultLimit returns configured default slot count.
func (m *Manager) DefaultLimit() int {
	if m == nil || m.cfg.DefaultLimit <= 0 {
		return 5
	}
	return m.cfg.DefaultLimit
}

// slotTTLSeconds returns the slot TTL. Hard-coded to 30 min; production
// uses Redis-side TTL so the Go value is only relevant for the
// in-memory fallback path.
func (m *Manager) slotTTLSeconds() int {
	return slotTTLSeconds
}

// EffectiveFpSlotLimit maps DB credentials.fp_slot_limit (the fingerprint
// slot pool size — how many distinct virtual identities this credential
// can simulate) to the value used at runtime.
//
// Returns:
//   - nil when the credential has no fingerprint slot limit (unlimited pool).
//     Callers should treat a nil return as "no upper bound; pick a free slot
//     or allocate a new one without rejection".
//   - *int when there is a finite pool. The pointer points to a copy of the
//     limit value (1..N); callers should not retain the pointer.
//
// IMPORTANT — distinct from concurrency_limit:
//   - concurrency_limit = max in-flight REQUESTS (managed by Limiter pkg)
//   - fp_slot_limit    = max distinct USER IDENTITIES (managed here)
//
// The two must NEVER be conflated. The previous EffectiveLimit() took
// the wrong argument (concurrency_limit) and is preserved below only
// for callers that have not migrated yet; new code MUST call
// EffectiveFpSlotLimit with the fp_slot_limit value from the DB.
//
// Mapping rules (per-credential):
//   - nil input         → defaultLimit  (fallback when DB row was added
//     before this column existed or
//     caller did not load the column)
//   - *limit == 0       → nil (unlimited pool)
//   - *limit  > 0       → &*limit
func EffectiveFpSlotLimit(fpSlotLimit *int, defaultLimit int) *int {
	if fpSlotLimit == nil {
		v := defaultLimit
		return &v
	}
	if *fpSlotLimit <= 0 {
		return nil
	}
	v := *fpSlotLimit
	return &v
}

// EffectiveLimit is the legacy mapping from concurrency_limit to slot
// pool size. RETAINED for backward compatibility only — the previous
// implementation incorrectly conflated the two concepts.
//
// New code must call EffectiveFpSlotLimit with the credentials.fp_slot_limit
// value from the DB. Callers of EffectiveLimit should migrate.
func EffectiveLimit(limit *int, defaultLimit int) *int {
	if limit == nil {
		v := defaultLimit
		return &v
	}
	if *limit <= 0 {
		return nil
	}
	v := *limit
	return &v
}

func slotRedisKey(credentialID, slotIndex int) string {
	return fmt.Sprintf("llmgw:cred_fp_slot:%d:%d", credentialID, slotIndex)
}

func pinRedisKey(holder string, credentialID int) string {
	return fmt.Sprintf("llmgw:sess_cred_fp:%s:%d", holder, credentialID)
}

// RoutingEligible reports whether holder can acquire a slot (prefilter).
func (m *Manager) RoutingEligible(ctx context.Context, credentialID int, limit *int, holder string) bool {
	if !m.Enabled() {
		return true
	}
	eff := EffectiveLimit(limit, m.cfg.DefaultLimit)
	if eff == nil {
		return true
	}
	if m.hasPin(ctx, holder, credentialID) {
		return true
	}
	free, _ := m.AvailableCount(ctx, credentialID, limit)
	return free > 0
}

// Acquire tries to take one slot. ok=false means saturated.
func (m *Manager) Acquire(ctx context.Context, credentialID int, limit *int, holder, tenantID string) (*Lease, bool) {
	if !m.Enabled() {
		return &Lease{Unlimited: true, CredentialID: credentialID, Holder: holder}, true
	}
	eff := EffectiveLimit(limit, m.cfg.DefaultLimit)
	if eff == nil {
		return &Lease{Unlimited: true, CredentialID: credentialID, Holder: holder}, true
	}
	if m.client != nil {
		if lease, ok := m.acquireRedis(ctx, credentialID, *eff, holder, tenantID); ok {
			return lease, true
		}
	}
	if lease, ok := m.acquireMemory(credentialID, *eff, holder, tenantID); ok {
		return lease, true
	}
	return nil, false
}

// Release frees a previously acquired slot while preserving the session pin.
//
// The pin survives release so the same holder's next request re-uses the same
// slot within sessionPinTTLSeconds (24 h). If the slot was taken by another
// holder meanwhile, acquireRedis's pin path migrates to a new free slot and
// updates the pin atomically (see acquireSlotScript). This gives us:
//   - stability for the common case (low contention, no credential death)
//   - graceful migration under contention (slot can change, but only when forced)
//   - clean cleanup of stale pins when the credential is force-unpinned
//
// Call ForceUnpin explicitly when a credential is dead (auth revoked, quota
// permanent) so the next request doesn't try to re-acquire a slot in a dead
// credential.
func (m *Manager) Release(ctx context.Context, lease *Lease) {
	if lease == nil || lease.Unlimited {
		return
	}
	if m.client != nil {
		key := slotRedisKey(lease.CredentialID, lease.SlotIndex)
		pinKey := pinRedisKey(lease.Holder, lease.CredentialID)

		// Refresh TTLs: slot for 30 min (reclaimable sooner), pin for 24 h
		// (stable identity across the longer session).
		refreshed, err := releaseSlotScript.Run(ctx, m.client,
			[]string{key, pinKey},
			lease.Holder,
			slotTTLSeconds,
			sessionPinTTLSeconds,
			lease.SlotIndex,
		).Bool()
		if err != nil {
			slog.Debug("cred_fp_slot redis release failed", "cred", lease.CredentialID, "error", err)
		}
		if !refreshed {
			slog.Debug("cred_fp_slot redis release: slot not owned", "cred", lease.CredentialID, "slot", lease.SlotIndex)
		}
	}
	m.releaseMemory(lease.CredentialID, lease.SlotIndex, lease.Holder)
}

// ForceUnpin removes a holder's pin for a credential, regardless of slot state.
//
// Called by the executor when the credential is dead (auth revoked, quota
// permanent) so the next request doesn't try to re-acquire a slot in a dead
// credential. The slot itself is untouched; this only clears the pinning hint.
func (m *Manager) ForceUnpin(ctx context.Context, holder string, credentialID int) {
	if holder == "" {
		return
	}
	pinKey := pinRedisKey(holder, credentialID)
	if m.client != nil {
		if _, err := forceUnpinScript.Run(ctx, m.client, []string{pinKey}).Result(); err != nil {
			slog.Debug("cred_fp_slot force-unpin redis failed", "cred", credentialID, "holder", holder, "error", err)
		}
	}
	m.mu.Lock()
	delete(m.memPins, pinKey)
	m.mu.Unlock()
}

var releaseSlotScript = redis.NewScript(`
	local slotKey = KEYS[1]
	local pinKey = KEYS[2]
	local holder = ARGV[1]
	local slotTTL = tonumber(ARGV[2])
	local pinTTL = tonumber(ARGV[3])
	local slotIndex = tonumber(ARGV[4])

	-- Check if slot is owned by this holder
	local current = redis.call('GET', slotKey)
	if current ~= holder then
		return false
	end

	-- DO NOT delete the slot key. Instead, refresh its TTL to keep
	-- the fingerprint identity alive for 24 hours. This allows the
	-- same holder's next request to reuse the same slot, and other
	-- sessions to see this slot as "occupied by a stable identity".
	redis.call('EXPIRE', slotKey, slotTTL)

	-- Also refresh the pin TTL so the holder can reuse this slot
	if pinKey ~= "" and pinTTL > 0 then
		redis.call('SET', pinKey, tostring(slotIndex), 'EX', pinTTL)
	end

	return true
`)

var forceUnpinScript = redis.NewScript(`
	local pinKey = KEYS[1]
	if redis.call('EXISTS', pinKey) == 0 then
		return 0
	end
	redis.call('DEL', pinKey)
	return 1
`)

// Stats returns occupancy snapshot for admin dashboards.
func (m *Manager) Stats(ctx context.Context, credentialID int, limit *int) (slotLimit, used, free *int) {
	eff := EffectiveLimit(limit, m.cfg.DefaultLimit)
	if eff == nil {
		return nil, nil, nil
	}
	avail, _ := m.AvailableCount(ctx, credentialID, limit)
	u := *eff - avail
	if u < 0 {
		u = 0
	}
	l, u2, f := *eff, u, avail
	return &l, &u2, &f
}

// SlotDetail describes a single fingerprint slot's state for monitoring.
type SlotDetail struct {
	Index      int    `json:"index"`
	Holder     string `json:"holder"`
	TTLSeconds int    `json:"ttl_seconds"`
	Expired    bool   `json:"expired"`
	MemoryMode bool   `json:"memory_mode"`
}

// DetailedStats returns per-slot occupancy for monitoring and diagnostics.
//
// This method is intended for admin dashboards and debugging tools that need
// to inspect the actual state of each fingerprint slot — e.g. diagnosing
// the "cred-11/minimax-m3 alternating success/failure" issue where one
// session bounces between credentials due to intermittent failures.
func (m *Manager) DetailedStats(ctx context.Context, credentialID int, limit *int) (slotLimit *int, holders []string, details []SlotDetail, healthySlots int) {
	if !m.Enabled() {
		return nil, nil, nil, 0
	}
	eff := EffectiveLimit(limit, m.cfg.DefaultLimit)
	if eff == nil {
		return nil, nil, nil, 0
	}
	limitVal := *eff
	slotLimit = &limitVal

	if m.client != nil {
		holders, details, healthySlots = m.detailedStatsRedis(ctx, credentialID, limitVal)
		return slotLimit, holders, details, healthySlots
	}

	holders, details, healthySlots = m.detailedStatsMemory(credentialID, limitVal)
	return slotLimit, holders, details, healthySlots
}

func (m *Manager) detailedStatsRedis(ctx context.Context, credentialID, limit int) ([]string, []SlotDetail, int) {
	holders := make([]string, 0, limit)
	details := make([]SlotDetail, 0, limit)
	healthySlots := 0

	pipe := m.client.Pipeline()
	getCmds := make([]*redis.StringCmd, limit)
	ttlCmds := make([]*redis.DurationCmd, limit)
	for slot := 0; slot < limit; slot++ {
		key := slotRedisKey(credentialID, slot)
		getCmds[slot] = pipe.Get(ctx, key)
		ttlCmds[slot] = pipe.TTL(ctx, key)
	}
	pipe.Exec(ctx)

	for slot := 0; slot < limit; slot++ {
		holder, err := getCmds[slot].Result()
		ttl, _ := ttlCmds[slot].Result()
		ttlSeconds := int(ttl.Seconds())

		if err == redis.Nil {
			details = append(details, SlotDetail{Index: slot, Holder: "", TTLSeconds: 0, Expired: true})
			continue
		}
		if err != nil {
			details = append(details, SlotDetail{Index: slot, Holder: "", TTLSeconds: 0, Expired: true})
			continue
		}

		healthySlots++
		holders = append(holders, holder)
		details = append(details, SlotDetail{Index: slot, Holder: holder, TTLSeconds: ttlSeconds, Expired: ttlSeconds <= 0})
	}

	return holders, details, healthySlots
}

func (m *Manager) detailedStatsMemory(credentialID, limit int) ([]string, []SlotDetail, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.purgeExpiredLocked(now)

	holders := make([]string, 0, limit)
	details := make([]SlotDetail, 0, limit)
	healthySlots := 0

	for slot := 0; slot < limit; slot++ {
		key := slotKey{credentialID: credentialID, slotIndex: slot}
		cur, exists := m.memSlots[key]
		if !exists {
			details = append(details, SlotDetail{Index: slot, Holder: "", Expired: true, MemoryMode: true})
			continue
		}

		ttlSeconds := int(time.Until(cur.exp).Seconds())
		expired := ttlSeconds <= 0
		if !expired {
			healthySlots++
			holders = append(holders, cur.holder)
		}
		details = append(details, SlotDetail{Index: slot, Holder: cur.holder, TTLSeconds: ttlSeconds, Expired: expired, MemoryMode: true})
	}

	return holders, details, healthySlots
}

// AvailableCount returns free slots.
func (m *Manager) AvailableCount(ctx context.Context, credentialID int, limit *int) (int, error) {
	eff := EffectiveLimit(limit, m.cfg.DefaultLimit)
	if eff == nil {
		return 0, nil
	}
	if m.client != nil {
		result, err := availableCountScript.Run(ctx, m.client,
			[]string{fmt.Sprintf("llmgw:cred_fp_slot:%d", credentialID)},
			*eff,
		).Int()
		if err != nil {
			slog.Debug("cred_fp_slot available_count script failed", "cred", credentialID, "error", err)
			// fallback: count via pipeline
			pipe := m.client.Pipeline()
			cmds := make([]*redis.StringCmd, *eff)
			for slot := 0; slot < *eff; slot++ {
				cmds[slot] = pipe.Get(ctx, slotRedisKey(credentialID, slot))
			}
			if _, pipeErr := pipe.Exec(ctx); pipeErr != nil && pipeErr != redis.Nil {
				return *eff, pipeErr
			}
			used := 0
			for _, cmd := range cmds {
				if cmd.Err() == nil {
					used++
				}
			}
			free := *eff - used
			if free < 0 {
				free = 0
			}
			return free, nil
		}
		free := result
		if free < 0 {
			free = 0
		}
		return free, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.purgeExpiredLocked(now)
	used := 0
	for k, e := range m.memSlots {
		if k.credentialID == credentialID && e.exp.After(now) {
			used++
		}
	}
	free := *eff - used
	if free < 0 {
		free = 0
	}
	return free, nil
}

// availableCountScript counts free slots atomically via Lua.
// KEYS[1] = prefix "llmgw:cred_fp_slot:{credentialID}"
// ARGV[1] = limit
var availableCountScript = redis.NewScript(`
	local prefix = KEYS[1]
	local limit = tonumber(ARGV[1])
	local used = 0
	for slot = 0, limit - 1 do
		local key = prefix .. ':' .. tostring(slot)
		if redis.call('EXISTS', key) == 1 then
			used = used + 1
		end
	end
	return limit - used
`)

func (m *Manager) hasPin(ctx context.Context, holder string, credentialID int) bool {
	if m.client != nil {
		_, err := m.client.Get(ctx, pinRedisKey(holder, credentialID)).Result()
		return err == nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	pinKey := pinRedisKey(holder, credentialID)
	pin, ok := m.memPins[pinKey]
	if !ok {
		return false
	}
	if !pin.exp.After(now) {
		// Pin has expired (sessionPinTTLSeconds elapsed with no activity).
		// Drop the stale entry and report absence so the caller re-acquires.
		delete(m.memPins, pinKey)
		return false
	}
	return true
}

func (m *Manager) acquireRedis(ctx context.Context, credentialID, limit int, holder, tenantID string) (*Lease, bool) {
	pinKey := pinRedisKey(holder, credentialID)
	gate := m.cfg.resolveActiveGateSeconds()
	// Phase 1: pin-reuse path. The Lua script applies the active
	// gate for us — if our pin is on a slot that some other holder
	// is now sitting on, the gate decides whether to preempt.
	if pinned, err := m.client.Get(ctx, pinKey).Result(); err == nil {
		slot, parseErr := strconv.Atoi(strings.TrimSpace(pinned))
		if parseErr == nil && slot >= 0 && slot < limit {
			acquired, err := acquireSlotScript.Run(ctx, m.client,
				[]string{slotRedisKey(credentialID, slot), pinKey},
				holder, slotTTLSeconds, sessionPinTTLSeconds, slot, gate,
			).Bool()
			if err != nil {
				slog.Debug("cred_fp_slot redis pin-reuse failed", "cred", credentialID, "slot", slot, "error", err)
			} else if acquired {
				eg := identity.BuildEgressIdentity(credentialID, slot, tenantID)
				return &Lease{SlotIndex: slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
			}
		}
	}

	// Phase 2: LRU preempt — single atomic Lua call. Scans the
	// pool once and either grabs a free slot (free wins over LRU
	// preempt) or preempts the slot whose holder has been silent
	// the LONGEST (and is past the active gate). Per operator spec
	// (2026-06-24): "长时间占用的 slot 在 slot 满时，优先被抢占".
	res, err := acquireLRUScript.Run(ctx, m.client,
		[]string{fmt.Sprintf("llmgw:cred_fp_slot:%d", credentialID)},
		limit, holder, slotTTLSeconds, sessionPinTTLSeconds, gate, pinKey, credentialID,
	).Result()
	if err != nil {
		slog.Debug("cred_fp_slot redis LRU acquire failed", "cred", credentialID, "error", err)
		return nil, false
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) < 3 {
		return nil, false
	}
	acquired, _ := arr[0].(int64)
	if acquired != 1 {
		// No preemptable slot — all active. Later arrivals wait
		// (sync_retry in routing layer).
		return nil, false
	}
	slot, _ := arr[1].(int64)
	oldHolder, _ := arr[2].(string)
	if oldHolder != "" {
		slog.Info("cred_fp_slot LRU preempt",
			"cred", credentialID,
			"new_holder", holder,
			"old_holder", oldHolder,
			"slot", slot,
		)
	}
	eg := identity.BuildEgressIdentity(credentialID, int(slot), tenantID)
	return &Lease{SlotIndex: int(slot), Egress: &eg, CredentialID: credentialID, Holder: holder}, true
}

func (m *Manager) tryRedisLock(ctx context.Context, credentialID, slot int, holder string) bool {
	acquired, err := acquireSlotScript.Run(ctx, m.client,
		[]string{slotRedisKey(credentialID, slot), ""},
		holder, slotTTLSeconds, 0, slot, m.cfg.resolveActiveGateSeconds(),
	).Bool()
	if err != nil {
		slog.Debug("cred_fp_slot redis lock failed", "cred", credentialID, "error", err)
		return false
	}
	return acquired
}

// acquireSlotScript is the atomic single-slot acquire path used by
// the pin-reuse and the for-loop free-slot scans.
//
// Behaviour (2026-06-24 update):
//  1. Slot is free → take it (returns true).
//  2. Slot is owned by the same holder → refresh TTL (returns true).
//  3. Slot is owned by a different holder:
//     a. The current holder is ACTIVE (refresh happened within the
//     last ActiveGateSeconds) → refuse (return false). The
//     operator's rule "5分钟内的会话不允许抢的" applies —
//     newer activity is preserved.
//     b. The current holder is IDLE (refresh was at least
//     ActiveGateSeconds ago) → preempt: overwrite the slot with
//     the new holder, refresh TTL to slotTTLSeconds. Returns
//     true. Side effect: the old holder's pin (if any) is wiped
//     here. The Go side receives true; if it needs to call
//     ForceUnpin on the old holder (for symmetry with the
//     in-memory store), it has the slot key available and can
//     resolve oldHolder from the slot value.
//  4. Slot key has no TTL (shouldn't happen, defensive) → refuse.
//
// KEYS[1] = slot key
// KEYS[2] = pin key
// ARGV[1] = holder
// ARGV[2] = slotTTL
// ARGV[3] = pinTTL
// ARGV[4] = slotIndex
// ARGV[5] = activeGateSeconds (5 min default)
// Returns: 1 on success (including preemption), 0 on refuse.
var acquireSlotScript = redis.NewScript(`
	local slotKey   = KEYS[1]
	local pinKey    = KEYS[2]
	local holder    = ARGV[1]
	local slotTTL   = tonumber(ARGV[2])
	local pinTTL    = tonumber(ARGV[3])
	local slotIndex = tonumber(ARGV[4])
	local gate      = tonumber(ARGV[5])

	local currentHolder = redis.call('GET', slotKey)

	if currentHolder == false then
		-- Slot is free, acquire it
		redis.call('SET', slotKey, holder, 'EX', slotTTL)
	elseif currentHolder == holder then
		-- Same holder: refresh TTL
		redis.call('EXPIRE', slotKey, slotTTL)
	else
		-- Owned by a different holder. Active gate check.
		local remaining = redis.call('TTL', slotKey)
		if remaining == -1 or remaining == -2 then
			-- No TTL set: defensive, refuse.
			return 0
		end
		-- idle = (slotTTL - remaining), the time since last refresh.
		local idle = slotTTL - remaining
		if idle < gate then
			-- Recent activity within gate window: do not preempt.
			return 0
		end
		-- Preempt: overwrite slot with new holder, refresh TTL.
		redis.call('SET', slotKey, holder, 'EX', slotTTL)
	end

	-- Set pin if pinKey provided
	if pinKey ~= "" and pinTTL > 0 then
		redis.call('SET', pinKey, tostring(slotIndex), 'EX', pinTTL)
	end

	return 1
`)

// acquireLRUScript is the LRU-aware preempt path used when the
// per-slot scan in acquireRedis finds no free slot and no pin
// match. It scans the pool ONCE atomically and preempts the slot
// whose holder has been silent the LONGEST (but only if that
// holder is past the active gate).
//
// Per operator spec (2026-06-24): "长时间占用的 slot 在 slot 满
// 时，优先被抢占". The "longest idle first" order is the LRU
// implementation of that rule. Without this, the in-line scan
// would preempt the first idle slot it found in index order
// (slot 0 before slot 1), which can starve a heavily-used
// (re-acquired often) credential pool.
//
// KEYS[1] = slot key prefix (e.g. "llmgw:cred_fp_slot:42")
// ARGV[1] = limit
// ARGV[2] = holder
// ARGV[3] = slotTTL
// ARGV[4] = pinTTL
// ARGV[5] = activeGateSeconds
// ARGV[6] = pinKey (caller's pin)
// ARGV[7] = credentialID (for wiping the old holder's pin)
// Returns: {1, slotIndex, oldHolder} on preempt, {0, "", ""} on
// no preemptable slot.
var acquireLRUScript = redis.NewScript(`
	local prefix = KEYS[1]
	local limit  = tonumber(ARGV[1])
	local holder = ARGV[2]
	local slotTTL = tonumber(ARGV[3])
	local pinTTL  = tonumber(ARGV[4])
	local gate    = tonumber(ARGV[5])
	local pinKey  = ARGV[6]
	local credID  = tonumber(ARGV[7])

	local bestSlot = -1
	local bestIdle = -1
	local bestOldHolder = nil

	for slot = 0, limit - 1 do
		local key = prefix .. ':' .. tostring(slot)
		local current = redis.call('GET', key)
		if current == false then
			-- Free slot — take it (no LRU bookkeeping needed)
			redis.call('SET', key, holder, 'EX', slotTTL)
			if pinKey ~= '' and pinTTL > 0 then
				redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
			end
			return {1, slot, ''}
		end
		local remaining = redis.call('TTL', key)
		if remaining == -1 or remaining == -2 then
			-- No TTL: take it.
			redis.call('SET', key, holder, 'EX', slotTTL)
			if pinKey ~= '' and pinTTL > 0 then
				redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
			end
			return {1, slot, ''}
		end
		local idle = slotTTL - remaining
		if idle < gate then
			-- Active holder: skip.
		elseif idle > bestIdle then
			bestSlot = slot
			bestIdle = idle
			bestOldHolder = current
		end
	end

	if bestSlot == -1 then
		-- No free slot and no idle slot; later arrivals wait.
		return {0, '', ''}
	end

	-- Preempt the LRU-most-idle slot.
	local bestKey = prefix .. ':' .. tostring(bestSlot)
	redis.call('SET', bestKey, holder, 'EX', slotTTL)
	if pinKey ~= '' and pinTTL > 0 then
		redis.call('SET', pinKey, tostring(bestSlot), 'EX', pinTTL)
	end
	-- Wipe the old holder's pin if it still points at the
	-- preempted slot. Without this the old holder's next
	-- Acquire would either re-take the slot (racing the new
	-- holder) or fail spuriously.
	if bestOldHolder then
		local oldPinKey = 'llmgw:sess_cred_fp:' .. bestOldHolder .. ':' .. tostring(credID)
		if redis.call('GET', oldPinKey) == tostring(bestSlot) then
			redis.call('DEL', oldPinKey)
		end
	end
	return {1, bestSlot, bestOldHolder or ''}
`)

func (m *Manager) acquireMemory(credentialID, limit int, holder, tenantID string) (*Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.purgeExpiredLocked(now)

	gate := time.Duration(m.cfg.resolveActiveGateSeconds()) * time.Second
	// "Active" means: the holder refreshed their slot within the
	// last `gate` seconds, so the slot's remaining TTL is at
	// least (slotTTLSeconds - gate). Anything below that bound is
	// idle and preemptable.
	activeRemaining := time.Duration(slotTTLSeconds)*time.Second - gate
	pinKey := pinRedisKey(holder, credentialID)

	// Phase 1: pin-reuse path. Active gate applies — if our pin
	// points at a slot that some other holder is now sitting on,
	// the gate decides whether to preempt. "Same holder" branch
	// always succeeds (we own it).
	if pin, ok := m.memPins[pinKey]; ok && pin.exp.After(now) {
		key := slotKey{credentialID: credentialID, slotIndex: pin.slot}
		cur, exists := m.memSlots[key]
		if !exists || cur.exp.Before(now) {
			// Slot is free (or expired) — take it.
			m.memSlots[key] = memEntry{holder: holder, exp: now.Add(time.Duration(slotTTLSeconds) * time.Second)}
			eg := identity.BuildEgressIdentity(credentialID, pin.slot, tenantID)
			return &Lease{SlotIndex: pin.slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
		}
		if cur.holder == holder {
			// Same holder, refresh TTL.
			m.memSlots[key] = memEntry{holder: holder, exp: now.Add(time.Duration(slotTTLSeconds) * time.Second)}
			m.memPins[pinKey] = memPinEntry{slot: pin.slot, exp: now.Add(time.Duration(sessionPinTTLSeconds) * time.Second)}
			eg := identity.BuildEgressIdentity(credentialID, pin.slot, tenantID)
			return &Lease{SlotIndex: pin.slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
		}
		// Different holder on our pinned slot. Active gate?
		if cur.exp.Sub(now) >= activeRemaining {
			// Active: don't preempt. 5 min 内不允许抢的。
			return nil, false
		}
		// Idle: preempt. (This case is rare — pin path is usually
		// taken when the holder has been active recently; preemption
		// here is the safety net for the "we left a pin pointing at
		// a slot, then went away, then came back" case.)
		//
		// Wipe the old holder's pin: it would otherwise still
		// point at this slot, leading to a future "same pin
		// → same slot" Acquire that races against the new
		// holder and either overwrites it or fails.
		oldHolder := cur.holder
		oldPinKey := pinRedisKey(oldHolder, credentialID)
		delete(m.memPins, oldPinKey)
		m.memSlots[key] = memEntry{holder: holder, exp: now.Add(time.Duration(slotTTLSeconds) * time.Second)}
		m.memPins[pinKey] = memPinEntry{slot: pin.slot, exp: now.Add(time.Duration(sessionPinTTLSeconds) * time.Second)}
		eg := identity.BuildEgressIdentity(credentialID, pin.slot, tenantID)
		return &Lease{SlotIndex: pin.slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
	}

	// Phase 2 + 3: find the best slot to take.
	//  2a. First free slot wins (cost = 0).
	//  2b. Otherwise: LRU preempt — pick the slot whose holder has
	//       been silent the LONGEST, but only if its remaining TTL
	//       has dropped below the active-gate bound (i.e. idle ≥
	//       gate). Per operator spec (2026-06-24): "长时间占用的
	//       slot 在 slot 满时，优先被抢占". The "longest idle
	//       first" order is the LRU implementation of that rule.
	type candidate struct {
		slot int
		idle time.Duration
		old  string
		exp  time.Time
	}
	var best *candidate
	for slot := 0; slot < limit; slot++ {
		key := slotKey{credentialID: credentialID, slotIndex: slot}
		cur, exists := m.memSlots[key]

		if !exists || cur.exp.Before(now) {
			// Free slot — take it immediately. No LRU bookkeeping
			// needed; free is cheaper than preempt.
			m.memSlots[key] = memEntry{holder: holder, exp: now.Add(time.Duration(slotTTLSeconds) * time.Second)}
			m.memPins[pinKey] = memPinEntry{slot: slot, exp: now.Add(time.Duration(sessionPinTTLSeconds) * time.Second)}
			eg := identity.BuildEgressIdentity(credentialID, slot, tenantID)
			return &Lease{SlotIndex: slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
		}
		if cur.holder == holder {
			// Same holder, refresh TTL.
			m.memSlots[key] = memEntry{holder: holder, exp: now.Add(time.Duration(slotTTLSeconds) * time.Second)}
			m.memPins[pinKey] = memPinEntry{slot: slot, exp: now.Add(time.Duration(sessionPinTTLSeconds) * time.Second)}
			eg := identity.BuildEgressIdentity(credentialID, slot, tenantID)
			return &Lease{SlotIndex: slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
		}
		// Occupied by a different holder. Eligible for preempt
		// only if idle ≥ gate. Track the LRU-most-idle one.
		idle := time.Duration(slotTTLSeconds)*time.Second - cur.exp.Sub(now)
		if idle < gate {
			// Active: skip.
			continue
		}
		if best == nil || idle > best.idle {
			best = &candidate{slot: slot, idle: idle, old: cur.holder, exp: cur.exp}
		}
	}
	if best == nil {
		// All slots active (or all slots owned by us, which would
		// have been caught above). No preempt possible; later
		// arrivals wait (sync_retry in routing layer).
		return nil, false
	}
	// LRU preempt. Wipe the old holder's pin so it doesn't race
	// against the new one on its next Acquire.
	oldPinKey := pinRedisKey(best.old, credentialID)
	delete(m.memPins, oldPinKey)
	m.memSlots[slotKey{credentialID: credentialID, slotIndex: best.slot}] = memEntry{
		holder: holder,
		exp:    now.Add(time.Duration(slotTTLSeconds) * time.Second),
	}
	m.memPins[pinKey] = memPinEntry{
		slot: best.slot,
		exp:  now.Add(time.Duration(sessionPinTTLSeconds) * time.Second),
	}
	eg := identity.BuildEgressIdentity(credentialID, best.slot, tenantID)
	return &Lease{SlotIndex: best.slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
}

func (m *Manager) releaseMemory(credentialID, slotIndex int, holder string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	key := slotKey{credentialID: credentialID, slotIndex: slotIndex}
	if cur, ok := m.memSlots[key]; ok && cur.holder == holder {
		// DO NOT delete the slot. Refresh its TTL to keep the fingerprint
		// identity alive for 24 hours (same as Redis mode).
		m.memSlots[key] = memEntry{
			holder: holder,
			exp:    now.Add(time.Duration(slotTTLSeconds) * time.Second),
		}
		// Refresh pin TTL as well
		pinKey := pinRedisKey(holder, credentialID)
		m.memPins[pinKey] = memPinEntry{
			slot: slotIndex,
			exp:  now.Add(time.Duration(sessionPinTTLSeconds) * time.Second),
		}
	}
}

func (m *Manager) purgeExpiredLocked(now time.Time) {
	for k, e := range m.memSlots {
		if !e.exp.After(now) {
			delete(m.memSlots, k)
		}
	}
	for k, p := range m.memPins {
		if !p.exp.After(now) {
			delete(m.memPins, k)
		}
	}
}

// ResetSlots clears all slot and pin keys for a credential, resetting occupancy to zero.
// Used by admin UI "复位" button when slots appear stuck due to:
//   - Gateway restart before defer cleanup
//   - Redis key expiry delays
//   - Program panic during request handling
//
// Returns (deleted_slots, deleted_pins, error).
func (m *Manager) ResetSlots(ctx context.Context, credentialID int, limit *int) (int, int, error) {
	if !m.Enabled() {
		return 0, 0, nil
	}
	eff := EffectiveLimit(limit, m.cfg.DefaultLimit)
	if eff == nil {
		return 0, 0, nil
	}

	if m.client != nil {
		// Delete all slot keys and pin keys via Lua script for atomicity
		result, err := resetSlotsScript.Run(ctx, m.client,
			[]string{fmt.Sprintf("llmgw:cred_fp_slot:%d", credentialID)},
			*eff,
			credentialID,
		).Result()
		if err != nil {
			return 0, 0, fmt.Errorf("redis reset failed: %w", err)
		}
		// Lua returns two integers
		results := result.([]interface{})
		slots := int(results[0].(int64))
		pins := int(results[1].(int64))
		slog.Info("cred_fp_slot reset completed",
			"credential_id", credentialID,
			"deleted_slots", slots,
			"deleted_pins", pins,
		)
		return slots, pins, nil
	}

	// Memory fallback
	m.mu.Lock()
	defer m.mu.Unlock()
	deletedSlots := 0
	deletedPins := 0
	for k := range m.memSlots {
		if k.credentialID == credentialID {
			delete(m.memSlots, k)
			deletedSlots++
		}
	}
	// Pin keys contain credentialID at the end: "llmgw:sess_cred_fp:{holder}:{credentialID}"
	pinSuffix := fmt.Sprintf(":%d", credentialID)
	for k := range m.memPins {
		if strings.HasSuffix(k, pinSuffix) {
			delete(m.memPins, k)
			deletedPins++
		}
	}
	return deletedSlots, deletedPins, nil
}

// ReleaseSlot frees a single fingerprint slot (and its pin) for a credential.
// Returns true if the slot was actually occupied and released.
func (m *Manager) ReleaseSlot(ctx context.Context, credentialID, slotIndex int) (bool, error) {
	if !m.Enabled() {
		return false, nil
	}

	if m.client != nil {
		result, err := releaseFpSlotScript.Run(ctx, m.client,
			[]string{
				slotRedisKey(credentialID, slotIndex),
			},
			credentialID,
		).Result()
		if err != nil {
			return false, fmt.Errorf("redis release slot failed: %w", err)
		}
		released := result.(int64) == 1
		if released {
			slog.Info("fp_slot released",
				"credential_id", credentialID,
				"slot_index", slotIndex,
			)
		}
		return released, nil
	}

	// Memory fallback
	m.mu.Lock()
	defer m.mu.Unlock()
	key := slotKey{credentialID: credentialID, slotIndex: slotIndex}
	entry, exists := m.memSlots[key]
	if !exists {
		return false, nil
	}
	delete(m.memSlots, key)
	// Also remove associated pin
	pinSuffix := fmt.Sprintf(":%d", credentialID)
	for k := range m.memPins {
		if strings.HasSuffix(k, pinSuffix) {
			delete(m.memPins, k)
			break
		}
	}
	slog.Info("fp_slot released (memory)",
		"credential_id", credentialID,
		"slot_index", slotIndex,
		"holder", entry.holder,
	)
	return true, nil
}

// releaseFpSlotScript Lua: GET slot key → DEL it + DEL its pin.
var releaseFpSlotScript = redis.NewScript(`
	local slotKey = KEYS[1]
	local credentialID = tonumber(ARGV[1])
	
	local holder = redis.call('GET', slotKey)
	if not holder then
		return 0  -- slot was already free
	end
	
	redis.call('DEL', slotKey)
	
	-- Also delete the associated pin key
	local pinKey = 'llmgw:sess_cred_fp:' .. holder .. ':' .. tostring(credentialID)
	redis.call('DEL', pinKey)
	
	return 1
`)

var resetSlotsScript = redis.NewScript(`
	local prefix = KEYS[1]
	local limit = tonumber(ARGV[1])
	local credentialID = tonumber(ARGV[2])
	
	local deletedSlots = 0
	local deletedPins = 0
	
	-- Delete all slot keys (llmgw:cred_fp_slot:{credentialID}:{slot})
	for slot = 0, limit - 1 do
		local slotKey = prefix .. ':' .. tostring(slot)
		if redis.call('DEL', slotKey) > 0 then
			deletedSlots = deletedSlots + 1
		end
	end
	
	-- Delete all pin keys (llmgw:sess_cred_fp:*:{credentialID})
	-- Use SCAN to find matching pin keys
	local pinPattern = 'llmgw:sess_cred_fp:*:' .. tostring(credentialID)
	local cursor = '0'
	repeat
		local result = redis.call('SCAN', cursor, 'MATCH', pinPattern, 'COUNT', 100)
		cursor = result[1]
		local keys = result[2]
		for _, key in ipairs(keys) do
			if redis.call('DEL', key) > 0 then
				deletedPins = deletedPins + 1
			end
		end
	until cursor == '0'
	
	return {deletedSlots, deletedPins}
`)

// ApplyEgressHeaders overwrites upstream-facing identity headers.
func ApplyEgressHeaders(hdr httpHeader, egress *identity.EgressIdentity) {
	if egress == nil {
		return
	}
	hdr.Set("X-Device-Seed", egress.EgressSeed)
	hdr.Set("X-Virtual-Client-Id", egress.VirtualClientID)
	hdr.Set("X-Virtual-IP", egress.VirtualIP)
	hdr.Set("X-Virtual-MAC", egress.VirtualMAC)
}

// httpHeader is satisfied by http.Header.
type httpHeader interface {
	Set(key, value string)
}
