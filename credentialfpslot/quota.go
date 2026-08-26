package credentialfpslot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/redis/go-redis/v9"
)

// QuotaMode controls whether a per-client fingerprint quota is observed or enforced.
type QuotaMode string

const (
	QuotaModeOff     QuotaMode = "off"
	QuotaModeShadow  QuotaMode = "shadow"
	QuotaModeEnforce QuotaMode = "enforce"
)

// Quota is the dependency-free value passed to the slot manager. MaxFPSlots
// <= 0 means no per-client limit. The metadata is still maintained.
type Quota struct {
	Mode           QuotaMode
	MaxFPSlots     int
	FPEnforceAfter *time.Time
}

// AcquireStatus distinguishes physical-pool pressure, client quota pressure,
// infrastructure fail-open, and credentials that do not use a finite pool.
type AcquireStatus string

const (
	Acquired            AcquireStatus = "acquired"
	PoolSaturated       AcquireStatus = "pool_saturated"
	ClientQuotaExceeded AcquireStatus = "client_quota_exceeded"
	RedisFailOpen       AcquireStatus = "redis_fail_open"
	Unlimited           AcquireStatus = "unlimited"
)

func (q Quota) enforce(now time.Time) bool {
	if normalizeQuotaMode(q.Mode) != QuotaModeEnforce || q.MaxFPSlots <= 0 {
		return false
	}
	return q.FPEnforceAfter == nil || !now.Before(*q.FPEnforceAfter)
}

func normalizeQuotaMode(mode QuotaMode) QuotaMode {
	switch QuotaMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case QuotaModeShadow:
		return QuotaModeShadow
	case QuotaModeEnforce:
		return QuotaModeEnforce
	default:
		return QuotaModeOff
	}
}

// globalMetadataKey stores per-credential, per-client metadata for the
// per-client fingerprint quota. The hash contains slot-key → normalized
// client-type entries that the Lua acquire script consults to decide whether
// a new slot acquisition would violate max_fp_slots.
func globalMetadataKey(credentialID int) string {
	return fmt.Sprintf("llmgw:cred_fp_global:%d:meta", credentialID)
}

// AcquireWithQuota selects a slot for the given holder/tenant while keeping
// a credential-wide, per-client-type stable-slot count. The status
// differentiates physical pool pressure, client quota pressure, and
// infrastructure fail-open.
func (m *Manager) AcquireWithQuota(
	ctx context.Context,
	credentialID int,
	limit *int,
	holder, tenantID string,
	quota Quota,
) (*Lease, AcquireStatus) {
	if !ratelimitIsEnabled() || !m.Enabled() {
		return unlimitedLease(credentialID, holder, tenantID), Unlimited
	}
	eff := EffectiveLimit(limit, m.cfg.DefaultLimit)
	if eff == nil {
		return unlimitedLease(credentialID, holder, tenantID), Unlimited
	}
	if m.client == nil {
		recordAcquireRedisError()
		return unlimitedLease(credentialID, holder, tenantID), RedisFailOpen
	}

	clientType, _ := splitClientToken(holder)
	now := time.Now()
	lease, status, err := m.acquireRedisWithQuota(ctx, credentialID, *eff, holder, tenantID, clientType, quota, now)
	if err != nil {
		recordAcquireRedisError()
		slog.Warn("cred_fp_slot quota acquire failed open",
			"credential_id", credentialID,
			"client_type", clientType,
			"error", err,
		)
		return unlimitedLease(credentialID, holder, tenantID), RedisFailOpen
	}
	if status != Acquired {
		if status == PoolSaturated {
			recordAcquireSaturated()
		}
		return nil, status
	}
	m.trackSlotForReclaim(ctx, tenantID, credentialID, lease.SlotIndex)
	recordAcquireSuccess()
	return lease, Acquired
}

func unlimitedLease(credentialID int, holder, tenantID string) *Lease {
	return &Lease{Unlimited: true, CredentialID: credentialID, Holder: holder, TenantID: tenantID}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// splitClientToken local override intentionally removed: package-level
// splitClientToken in metrics.go is reused.

func (m *Manager) acquireRedisWithQuota(
	ctx context.Context,
	credentialID, limit int,
	holder, tenantID, clientType string,
	quota Quota,
	now time.Time,
) (*Lease, AcquireStatus, error) {
	prefix := tenantSlotRedisPrefix(tenantID, credentialID)
	pinKey := tenantPinRedisKey(tenantID, holder, credentialID)
	result, err := acquireQuotaSlotScript.Run(ctx, m.client,
		[]string{
			prefix,
			pinKey,
			globalMetadataKey(credentialID),
		},
		limit,
		holder,
		slotTTLSeconds,
		sessionPinTTLSeconds,
		m.cfg.resolveActiveGateSeconds(),
		credentialID,
		normalizeMetricClientType(clientType),
		quota.MaxFPSlots,
		boolInt(quota.enforce(now)),
		now.Unix(),
	).Result()
	if err != nil {
		return nil, RedisFailOpen, err
	}
	values, ok := result.([]interface{})
	if !ok || len(values) < 2 {
		return nil, RedisFailOpen, fmt.Errorf("unexpected quota acquire result %T", result)
	}
	code, _ := values[0].(int64)
	switch code {
	case 1:
		slot, _ := values[1].(int64)
		egress := buildEgressIdentity(credentialID, int(slot), tenantID)
		return &Lease{
			SlotIndex:    int(slot),
			Egress:       &egress,
			CredentialID: credentialID,
			Holder:       holder,
			TenantID:     tenantID,
		}, Acquired, nil
	case 2:
		return nil, PoolSaturated, nil
	case 3:
		return nil, ClientQuotaExceeded, nil
	default:
		return nil, RedisFailOpen, fmt.Errorf("unknown quota acquire code %d", code)
	}
}

// buildEgressIdentity defers to the package-level identity builder used by
// slot.go.
var buildEgressIdentity = identity.BuildEgressIdentity

// identityStub is no longer used locally.
type identityStub struct {
	CredentialID int
	SlotIndex    int
	TenantID     string
}

var _ = identityStub{}

// ActiveSlotCount returns the credential-global active slot count for one
// normalized client type. Expired metadata is pruned before counting.
func (m *Manager) ActiveSlotCount(ctx context.Context, credentialID int, clientType string) (int64, error) {
	if m == nil || m.client == nil {
		return 0, ErrRedisRequired
	}
	count, err := activeSlotCountScript.Run(ctx, m.client,
		[]string{globalMetadataKey(credentialID)},
		normalizeMetricClientType(clientType),
		time.Now().Unix(),
	).Int64()
	if err != nil {
		return 0, fmt.Errorf("redis active slot count failed: %w", err)
	}
	return count, nil
}

var activeSlotCountScript = redis.NewScript(`
local meta = KEYS[1]
local clientType = ARGV[1]
local function prune(key)
    if redis.call('EXISTS', key) == 0 then
        local oldType = redis.call('HGET', meta, key)
        if oldType then
            redis.call('HINCRBY', meta, oldType .. ':count', -1)
            if tonumber(redis.call('HGET', meta, oldType .. ':count') or '0') <= 0 then
                redis.call('HDEL', meta, oldType .. ':count')
            end
        end
        redis.call('HDEL', meta, key)
        redis.call('HDEL', meta, key .. ':exp')
    end
end

local entries = redis.call('HKEYS', meta)
for _, key in ipairs(entries) do
    if not string.find(key, ':exp') and not string.find(key, ':count') then
        prune(key)
    end
end
return tonumber(redis.call('HGET', meta, clientType .. ':count') or '0')
`)

// acquireQuotaSlotScript owns physical selection and global quota metadata in
// one Redis transaction. Existing tenant slot and pin key formats are
// unchanged; the credential-global metadata lives in a separate hash.
var acquireQuotaSlotScript = redis.NewScript(`
local prefix = KEYS[1]
local pinKey = KEYS[2]
local meta = KEYS[3]

local limit = tonumber(ARGV[1])
local holder = ARGV[2]
local slotTTL = tonumber(ARGV[3])
local pinTTL = tonumber(ARGV[4])
local gate = tonumber(ARGV[5])
local credentialID = tonumber(ARGV[6])
local clientType = ARGV[7]
local maxSlots = tonumber(ARGV[8])
local enforce = tonumber(ARGV[9]) == 1
local now = tonumber(ARGV[10])
local expires = now + slotTTL

local function remember(slotKey)
    local oldType = redis.call('HGET', meta, slotKey)
    if oldType == clientType then
        -- same client type, do not double-count.
        redis.call('HSET', meta, slotKey .. ':exp', tostring(expires))
        return
    end
    if oldType then
        redis.call('HINCRBY', meta, oldType .. ':count', -1)
        if tonumber(redis.call('HGET', meta, oldType .. ':count') or '0') <= 0 then
            redis.call('HDEL', meta, oldType .. ':count')
        end
    end
    redis.call('HSET', meta, slotKey, clientType)
    redis.call('HINCRBY', meta, clientType .. ':count', 1)
    redis.call('HSET', meta, slotKey .. ':exp', tostring(expires))
end

local function forget(slotKey)
    local oldType = redis.call('HGET', meta, slotKey)
    if oldType then
        redis.call('HDEL', meta, slotKey)
        redis.call('HDEL', meta, slotKey .. ':exp')
        redis.call('HINCRBY', meta, oldType .. ':count', -1)
        if tonumber(redis.call('HGET', meta, oldType .. ':count') or '0') <= 0 then
            redis.call('HDEL', meta, oldType .. ':count')
        end
    end
end

-- 1. prune expired metadata entries.
local entries = redis.call('HGETALL', meta)
for i = 1, #entries, 2 do
    local slotKey = entries[i]
    local val = entries[i + 1]
    if val
        and string.sub(slotKey, -4) ~= ':exp'
        and string.sub(slotKey, -6) ~= ':count'
        and redis.call('EXISTS', slotKey) == 0 then
        forget(slotKey)
    end
end

-- 2. same-holder reuse: any slot already mapped to the same holder counts
-- as a zero-add acquire and must never trip max_fp_slots.
for slot = 0, limit - 1 do
    local key = prefix .. ':' .. tostring(slot)
    if redis.call('GET', key) == holder then
        redis.call('EXPIRE', key, slotTTL)
        redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
        remember(key)
        return {1, slot}
    end
end

local pinnedSlot = tonumber(redis.call('GET', pinKey) or '-1')
if pinnedSlot and pinnedSlot >= 0 and pinnedSlot < limit then
    local pinnedKey = prefix .. ':' .. tostring(pinnedSlot)
    if redis.call('EXISTS', pinnedKey) == 0 then
        redis.call('DEL', pinKey)
        pinnedSlot = -1
    end
end

local function currentCount()
    return tonumber(redis.call('HGET', meta, clientType .. ':count') or '0')
end

local freeSlot = -1
	local bestSameSlot = -1
	local bestSameIdle = -1
	local bestSameHolder = ''
	local bestAnySlot = -1
	local bestAnyIdle = -1
	local bestAnyHolder = ''
	local bestAnyType = ''

	for slot = 0, limit - 1 do
		local key = prefix .. ':' .. tostring(slot)
		local holderValue = redis.call('GET', key)
		if not holderValue then
			if freeSlot == -1 then freeSlot = slot end
		else
			local exp = tonumber(redis.call('HGET', meta, key .. ':exp') or '0')
			local idle = 0
			if exp > 0 then idle = slotTTL - (exp - now) end
			-- bestSameSlot records the oldest eligible same-type slot
			-- already held by THIS holder; preempting another holder would
			-- change the holder count for that client type, so we ignore
			-- same-type slots held by a different holder when enforcing.
			if holderValue == holder then
				if idle > bestSameIdle then
					bestSameSlot = slot
					bestSameIdle = idle
					bestSameHolder = holderValue
				end
			end
			if idle >= gate then
				local oldType = redis.call('HGET', meta, key)
				if oldType == clientType and holderValue ~= holder and idle > bestSameIdle then
					bestSameSlot = slot
					bestSameIdle = idle
					bestSameHolder = holderValue
				end
				if idle > bestAnyIdle then
					bestAnySlot = slot
					bestAnyIdle = idle
					bestAnyHolder = holderValue
					bestAnyType = oldType or 'unknown'
				end
			end
		end
	end

local chosen = -1
local oldHolder = ''
local blockedByQuota = enforce and maxSlots > 0 and currentCount() >= maxSlots

if blockedByQuota then
    if bestSameSlot >= 0 then
        chosen = bestSameSlot
        oldHolder = bestSameHolder
    else
        return {3, -1}
    end
elseif bestSameSlot >= 0 then
    chosen = bestSameSlot
    oldHolder = bestSameHolder
elseif freeSlot >= 0 then
    chosen = freeSlot
elseif bestAnySlot >= 0 then
    chosen = bestAnySlot
    oldHolder = bestAnyHolder
else
    return {2, -1}
end

local chosenKey = prefix .. ':' .. tostring(chosen)
if oldHolder ~= '' then
    forget(chosenKey)
end
redis.call('SET', chosenKey, holder, 'EX', slotTTL)
redis.call('SET', pinKey, tostring(chosen), 'EX', pinTTL)
remember(chosenKey)
return {1, chosen}
`)

// recordAcquireRedisError is the package-internal hook for instrumentation.
// Other tests/users override record* symbols; declare as variables so they
// can be swapped at test time.
var ratelimitIsEnabled = ratelimitIsEnabledImpl

// ratelimit_IsRateLimitEnabled is defined in slot.go to avoid an import cycle.
var ratelimit_IsRateLimitEnabled = func() bool { return true }

// ratelimitOverride lets tests disable the gate. Production wiring reuses the
// ratelimit package via slot.go.
var ratelimitOverride func() bool

func ratelimitIsEnabledImpl() bool {
	if ratelimitOverride != nil {
		return ratelimitOverride()
	}
	return ratelimit_IsRateLimitEnabled()
}
