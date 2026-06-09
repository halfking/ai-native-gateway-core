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
	slotTTLSeconds       = 3600
	sessionPinTTLSeconds = 1800
)

// Config controls slot pool behaviour.
type Config struct {
	DefaultLimit int
	Enabled      bool
}

// Lease is one acquired fingerprint slot.
type Lease struct {
	SlotIndex      int
	Egress         *identity.EgressIdentity
	Unlimited      bool
	CredentialID   int
	Holder         string
}

// Manager owns slot acquisition against Redis (or in-memory fallback).
type Manager struct {
	cfg    Config
	client *redis.Client
	mu     sync.Mutex
	memSlots map[slotKey]memEntry
	memPins  map[string]memPinEntry
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

// New creates a slot manager. client may be nil (memory fallback).
func New(cfg Config, client *redis.Client) *Manager {
	if cfg.DefaultLimit <= 0 {
		cfg.DefaultLimit = 5
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

// DefaultLimit returns configured default slot count.
func (m *Manager) DefaultLimit() int {
	if m == nil || m.cfg.DefaultLimit <= 0 {
		return 5
	}
	return m.cfg.DefaultLimit
}

// EffectiveLimit maps DB concurrency_limit to slot pool size.
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

// Release frees a previously acquired slot.
func (m *Manager) Release(ctx context.Context, lease *Lease) {
	if lease == nil || lease.Unlimited {
		return
	}
	if m.client != nil {
		key := slotRedisKey(lease.CredentialID, lease.SlotIndex)
		pinKey := pinRedisKey(lease.Holder, lease.CredentialID)
		released, err := releaseSlotScript.Run(ctx, m.client,
			[]string{key, pinKey},
			lease.Holder,
		).Bool()
		if err != nil {
			slog.Debug("cred_fp_slot redis release failed", "cred", lease.CredentialID, "error", err)
		}
		if !released {
			slog.Debug("cred_fp_slot redis release: slot not owned", "cred", lease.CredentialID, "slot", lease.SlotIndex)
		}
	}
	m.releaseMemory(lease.CredentialID, lease.SlotIndex, lease.Holder)
}

var releaseSlotScript = redis.NewScript(`
	local slotKey = KEYS[1]
	local pinKey = KEYS[2]
	local holder = ARGV[1]
	
	-- Check if slot is owned by this holder
	local current = redis.call('GET', slotKey)
	if current ~= holder then
		return false
	end
	
	-- Delete slot and pin
	redis.call('DEL', slotKey)
	if pinKey ~= "" then
		redis.call('DEL', pinKey)
	end
	
	return true
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

// AvailableCount returns free slots.
func (m *Manager) AvailableCount(ctx context.Context, credentialID int, limit *int) (int, error) {
	eff := EffectiveLimit(limit, m.cfg.DefaultLimit)
	if eff == nil {
		return 0, nil
	}
	if m.client != nil {
		used := 0
		for slot := 0; slot < *eff; slot++ {
			if err := m.client.Get(ctx, slotRedisKey(credentialID, slot)).Err(); err == nil {
				used++
			}
		}
		free := *eff - used
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

func (m *Manager) hasPin(ctx context.Context, holder string, credentialID int) bool {
	if m.client != nil {
		_, err := m.client.Get(ctx, pinRedisKey(holder, credentialID)).Result()
		return err == nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.memPins[pinRedisKey(holder, credentialID)]
	return ok
}

func (m *Manager) acquireRedis(ctx context.Context, credentialID, limit int, holder, tenantID string) (*Lease, bool) {
	pinKey := pinRedisKey(holder, credentialID)
	if pinned, err := m.client.Get(ctx, pinKey).Result(); err == nil {
		slot, parseErr := strconv.Atoi(strings.TrimSpace(pinned))
		if parseErr == nil && slot >= 0 && slot < limit {
			if m.tryRedisLock(ctx, credentialID, slot, holder) {
				eg := identity.BuildEgressIdentity(credentialID, slot, tenantID)
				return &Lease{SlotIndex: slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
			}
		}
	}

	for slot := 0; slot < limit; slot++ {
		acquired, err := acquireSlotScript.Run(ctx, m.client,
			[]string{slotRedisKey(credentialID, slot), pinKey},
			holder, slotTTLSeconds, sessionPinTTLSeconds, slot,
		).Bool()
		if err != nil {
			slog.Debug("cred_fp_slot redis acquire failed", "cred", credentialID, "slot", slot, "error", err)
			continue
		}
		if acquired {
			eg := identity.BuildEgressIdentity(credentialID, slot, tenantID)
			return &Lease{SlotIndex: slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
		}
	}
	return nil, false
}

func (m *Manager) tryRedisLock(ctx context.Context, credentialID, slot int, holder string) bool {
	acquired, err := acquireSlotScript.Run(ctx, m.client,
		[]string{slotRedisKey(credentialID, slot), ""},
		holder, slotTTLSeconds, 0, slot,
	).Bool()
	if err != nil {
		slog.Debug("cred_fp_slot redis lock failed", "cred", credentialID, "error", err)
		return false
	}
	return acquired
}

var acquireSlotScript = redis.NewScript(`
	local slotKey = KEYS[1]
	local pinKey = KEYS[2]
	local holder = ARGV[1]
	local slotTTL = tonumber(ARGV[2])
	local pinTTL = tonumber(ARGV[3])
	local slotIndex = tonumber(ARGV[4])
	
	-- Try to acquire slot lock
	local acquired = redis.call('SET', slotKey, holder, 'NX', 'EX', slotTTL)
	if not acquired then
		return false
	end
	
	-- Set pin if pinKey provided
	if pinKey ~= "" and pinTTL > 0 then
		redis.call('SET', pinKey, tostring(slotIndex), 'EX', pinTTL)
	end
	
	return true
`)

func (m *Manager) acquireMemory(credentialID, limit int, holder, tenantID string) (*Lease, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.purgeExpiredLocked(now)

	pinKey := pinRedisKey(holder, credentialID)
	if pin, ok := m.memPins[pinKey]; ok && pin.exp.After(now) {
		key := slotKey{credentialID: credentialID, slotIndex: pin.slot}
		cur, exists := m.memSlots[key]
		if !exists || cur.holder == holder || cur.exp.Before(now) {
			m.memSlots[key] = memEntry{holder: holder, exp: now.Add(time.Duration(slotTTLSeconds) * time.Second)}
			eg := identity.BuildEgressIdentity(credentialID, pin.slot, tenantID)
			return &Lease{SlotIndex: pin.slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
		}
	}

	for slot := 0; slot < limit; slot++ {
		key := slotKey{credentialID: credentialID, slotIndex: slot}
		cur, exists := m.memSlots[key]
		if !exists || cur.exp.Before(now) {
			m.memSlots[key] = memEntry{holder: holder, exp: now.Add(time.Duration(slotTTLSeconds) * time.Second)}
			m.memPins[pinKey] = memPinEntry{slot: slot, exp: now.Add(time.Duration(sessionPinTTLSeconds) * time.Second)}
			eg := identity.BuildEgressIdentity(credentialID, slot, tenantID)
			return &Lease{SlotIndex: slot, Egress: &eg, CredentialID: credentialID, Holder: holder}, true
		}
	}
	return nil, false
}

func (m *Manager) releaseMemory(credentialID, slotIndex int, holder string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := slotKey{credentialID: credentialID, slotIndex: slotIndex}
	if cur, ok := m.memSlots[key]; ok && cur.holder == holder {
		delete(m.memSlots, key)
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
