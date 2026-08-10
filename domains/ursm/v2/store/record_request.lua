-- KEYS[1] = node hash
-- KEYS[2] = window 1m
-- KEYS[3] = window 5m
-- KEYS[4] = window 30m
-- KEYS[5] = optional dedup key for a non-empty request ID
-- ARGV[1] = "1"|"0"   (success)
-- ARGV[2] = error_kind
-- ARGV[3] = event_ts_ms
-- ARGV[4] = latency_ms
-- ARGV[5] = request_id
-- ARGV[6] = node_ttl_sec
-- ARGV[7] = window_5m_ttl_sec
-- ARGV[8] = window_30m_ttl_sec
-- ARGV[9] = admin_hold_flag ("1"|"0") [DEPRECATED: lua now reads manual_hold directly]
-- ARGV[10] = cool_seconds (default 300 = 5min)
-- ARGV[11] = fail_streak_limit (default 3)
-- ARGV[12] = dedup enabled ("1"|"0")
-- ARGV[13] = billing_mode (optional; "free" enables transient tolerance below)

local node_key = KEYS[1]
local w1 = KEYS[2]
local w5 = KEYS[3]
local w30 = KEYS[4]
local dedup_key = KEYS[5]
local success = ARGV[1]
local err_kind = ARGV[2]
local now_ms = tonumber(ARGV[3])
local lat = tonumber(ARGV[4])
local req_id = ARGV[5]
local node_ttl = tonumber(ARGV[6])
local w5_ttl = tonumber(ARGV[7])
local w30_ttl = tonumber(ARGV[8])
local admin_hold_arg = ARGV[9]
local cool_seconds = tonumber(ARGV[10]) or 300
local fail_streak_limit = tonumber(ARGV[11]) or 3
local dedup_enabled = ARGV[12] == "1"
local billing_mode = ARGV[13] or ""

-- Free-billing transient tolerance (2026-08-10): a free-tier credential
-- failing on infra noise (timeout/rate_limit/upstream_down/empty_response/
-- stream_timeout/generic "transient") must not be hard-disabled the same
-- way a paid credential is. This set mirrors
-- domains/ursm/v2/reducer/reducer.go's transientErrors map so the (dead,
-- pending-migration) pure reducer and this live script agree on the
-- policy. Permanent errors (auth/auth_revoked/model_not_found/
-- quota_permanent) and all non-free billing modes are unaffected.
local transient_kinds = {
  rate_limit = true,
  timeout = true,
  stream_timeout = true,
  upstream_down = true,
  empty_response = true,
  transient = true,
}
local free_transient = (billing_mode == "free") and (transient_kinds[err_kind] == true)

-- M3 (2026-07-28): read manual_hold INSIDE the script so the short-circuit
-- observes the live value at write time. Eliminates the prior TOCTOU race
-- where the Go caller pre-read manual_hold via HGet and could hand a stale
-- value to this script if ApplyAdmin flipped the flag in between. Also
-- drops one hot-path RTT (was: Go HGet + Lua Eval).
-- Precedence: live Redis manual_hold wins; the legacy ARGV[9] admin_hold_arg
-- stays as a redundant sanity check (kept for ABI; callers may pass "0").
local manual_hold = redis.call("HGET", node_key, "manual_hold")
if manual_hold == "1" or admin_hold_arg == "1" then
  return {"ignored_manual_hold", "0", "0"}
end

if dedup_enabled then
  if redis.call("SET", dedup_key, "1", "NX", "EX", node_ttl) == false then
    return {"duplicate", "0", "0"}
  end
end

-- Get current state
local disabled = redis.call("HGET", node_key, "disabled")
local cool_until_ms = tonumber(redis.call("HGET", node_key, "cool_until_ms") or "0")
local disable_count = tonumber(redis.call("HGET", node_key, "disable_count") or "0")

local in_cool = (disabled == "1") and (cool_until_ms > now_ms)

  if in_cool then
  -- Node is in cooling period
  if success == "1" then
    -- Success during cool -> recover immediately
    redis.call("HSET", node_key,
      "disabled", "0",
      "available", "1",
      "fail_streak", "0",
      "cool_until_ms", "0",
      "disable_count", "0",  -- Reset disable count on successful recovery
      "last_err", "",
      "disabled_reason", "recovered_with_success_during_cool",
      "updated_at_ms", tostring(now_ms))
    redis.call("HINCRBY", node_key, "success_count", 1)
    redis.call("HINCRBY", node_key, "generation", 1)
    redis.call("EXPIRE", node_key, node_ttl)
    return {"applied", "0", "0"}
  else
    -- Failure during cool -> extend cool period (exponential backoff)
    local new_cool_seconds = cool_seconds * math.pow(2, disable_count)
    new_cool_seconds = math.min(new_cool_seconds, 3600) -- cap at 1 hour
    redis.call("HSET", node_key,
      "cool_until_ms", tostring(now_ms + (new_cool_seconds * 1000)),
      "last_err", err_kind,
      "updated_at_ms", tostring(now_ms))
    redis.call("HINCRBY", node_key, "failure_count", 1)
    redis.call("HINCRBY", node_key, "fail_streak", 1)
    redis.call("HINCRBY", node_key, "generation", 1)
    redis.call("EXPIRE", node_key, node_ttl)
    return {"applied", "0", "0"}
  end
else
  -- Normal path: not in cooling
  -- Reset disabled state if we were
  if disabled == "1" then
    redis.call("HSET", node_key, "disabled", "0")
  end

  -- Add to sliding windows
  local member = req_id .. ":" .. success .. ":" .. lat
  redis.call("ZADD", w1, now_ms, member)
  redis.call("EXPIRE", w1, math.max(60, math.floor(node_ttl/2)))
  redis.call("ZADD", w5, now_ms, member)
  redis.call("EXPIRE", w5, w5_ttl)
  redis.call("ZADD", w30, now_ms, member)
  redis.call("EXPIRE", w30, w30_ttl)

  redis.call("HINCRBY", node_key, "generation", 1)
  redis.call("HSET", node_key,
    "available", "1",
    "source_priority", "10",
    "updated_at_ms", tostring(now_ms),
    "last_err", err_kind)
  redis.call("EXPIRE", node_key, node_ttl)

  if success == "1" then
    redis.call("HINCRBY", node_key, "success_count", 1)
    redis.call("HSET", node_key, "fail_streak", "0")
  else
    redis.call("HINCRBY", node_key, "failure_count", 1)
    local new_streak = redis.call("HINCRBY", node_key, "fail_streak", 1)
    new_streak = tonumber(new_streak)

    -- Check if should disable. Free-tier credentials failing on transient
    -- infra noise (see transient_kinds above) are tolerated: fail_streak
    -- and failure_count still accumulate (visible for observability/
    -- scoring), but the node is not hard-disabled the way a paid
    -- credential would be. This mirrors reducer.go's soft-demote path.
    if new_streak >= fail_streak_limit and not free_transient then
      local new_cool_seconds = cool_seconds
      redis.call("HSET", node_key,
        "disabled", "1",
        "available", "0",
        "cool_until_ms", tostring(now_ms + (new_cool_seconds * 1000)),
        "disable_count", tostring(disable_count + 1),
        "disabled_reason", string.format("fail_streak_%d", new_streak))
    elseif new_streak >= fail_streak_limit and free_transient then
      redis.call("HSET", node_key, "disabled_reason", "free_transient_tolerated")
    end
  end

  return {"applied", "0", "0"}
end
