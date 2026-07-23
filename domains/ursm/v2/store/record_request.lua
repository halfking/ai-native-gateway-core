-- KEYS[1] = node hash
-- KEYS[2] = window 1m
-- KEYS[3] = window 5m
-- KEYS[4] = window 30m
-- ARGV[1] = "1"|"0"   (success)
-- ARGV[2] = error_kind
-- ARGV[3] = event_ts_ms
-- ARGV[4] = latency_ms
-- ARGV[5] = request_id
-- ARGV[6] = node_ttl_sec
-- ARGV[7] = window_5m_ttl_sec
-- ARGV[8] = window_30m_ttl_sec
-- ARGV[9] = admin_hold_flag ("1"|"0")
-- ARGV[10] = cool_seconds (default 300 = 5min)
-- ARGV[11] = fail_streak_limit (default 3)

local node_key = KEYS[1]
local w1 = KEYS[2]
local w5 = KEYS[3]
local w30 = KEYS[4]
local success = ARGV[1]
local err_kind = ARGV[2]
local now_ms = tonumber(ARGV[3])
local lat = tonumber(ARGV[4])
local req_id = ARGV[5]
local node_ttl = tonumber(ARGV[6])
local w5_ttl = tonumber(ARGV[7])
local w30_ttl = tonumber(ARGV[8])
local admin_hold = ARGV[9]
local cool_seconds = tonumber(ARGV[10]) or 300
local fail_streak_limit = tonumber(ARGV[11]) or 3

if admin_hold == "1" then
  return {"ignored_manual_hold", "0", "0"}
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

    -- Check if should disable
    if new_streak >= fail_streak_limit then
      local new_cool_seconds = cool_seconds
      redis.call("HSET", node_key,
        "disabled", "1",
        "available", "0",
        "cool_until_ms", tostring(now_ms + (new_cool_seconds * 1000)),
        "disable_count", tostring(disable_count + 1),
        "disabled_reason", string.format("fail_streak_%d", new_streak))
    end
  end

  return {"applied", "0", "0"}
end
