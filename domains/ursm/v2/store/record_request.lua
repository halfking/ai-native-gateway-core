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
-- ARGV[9] = admin_hold_flag ("1"|"0") [deprecated; live hash value wins]
-- ARGV[10] = cool_seconds (default 300 = 5min)
-- ARGV[11] = fail_streak_limit (default 3)
-- ARGV[12] = dedup enabled ("1"|"0")
-- ARGV[13] = billing_mode (optional; "free" enables transient tolerance)

local node_key = KEYS[1]
local w1 = KEYS[2]
local w5 = KEYS[3]
local w30 = KEYS[4]
local dedup_key = KEYS[5]
local success = ARGV[1]
local err_kind = ARGV[2]
local now_ms = tonumber(ARGV[3]) or 0
local lat = tonumber(ARGV[4]) or 0
local req_id = ARGV[5]
local node_ttl = tonumber(ARGV[6]) or 3600
local w5_ttl = tonumber(ARGV[7]) or 360
local w30_ttl = tonumber(ARGV[8]) or 2100
local admin_hold_arg = ARGV[9]
local cool_seconds = tonumber(ARGV[10]) or 300
local fail_streak_limit = tonumber(ARGV[11]) or 3
local dedup_enabled = ARGV[12] == "1"
local billing_mode = ARGV[13] or ""

local transient_kinds = {
  rate_limit = true,
  timeout = true,
  stream_timeout = true,
  upstream_down = true,
  empty_response = true,
  transient = true,
}
local free_transient = (billing_mode == "free") and (transient_kinds[err_kind] == true)

-- Admin holds dominate both telemetry-derived routing state and request state.
-- Read the live value in the script to avoid a Go-side TOCTOU race.
local manual_hold = redis.call("HGET", node_key, "manual_hold")
if manual_hold == "1" or admin_hold_arg == "1" then
  return {"ignored_manual_hold", "0", "0"}
end

if dedup_enabled then
  if redis.call("SET", dedup_key, "1", "NX", "EX", node_ttl) == false then
    return {"duplicate", "0", "0"}
  end
end

-- A request event is unique even when request IDs are empty or repeated. The
-- leading success flag lets the counter maintenance below avoid parsing IDs.
local event_seq = redis.call("HINCRBY", node_key, "event_seq", 1)
local member = success .. ":" .. tostring(now_ms) .. ":" .. tostring(event_seq) .. ":" .. req_id

-- Keep exact window counts and rates atomically with the event append. Existing
-- counters are adjusted by the members trimmed from the ZSET; if a window was
-- created by an older writer or counters are absent, rebuild once from it.
local function update_window(window_key, suffix, ttl, cutoff_ms)
  local existed = redis.call("EXISTS", window_key) == 1
  local counters_exist = existed and
    redis.call("HEXISTS", node_key, "samples_" .. suffix) == 1 and
    redis.call("HEXISTS", node_key, "successes_" .. suffix) == 1

  redis.call("ZADD", window_key, now_ms, member)
  local removed = redis.call("ZRANGEBYSCORE", window_key, "-inf", tostring(cutoff_ms))
  redis.call("ZREMRANGEBYSCORE", window_key, "-inf", tostring(cutoff_ms))

  local function member_success(member_value)
    -- New members are success:timestamp:sequence:request_id; legacy members
    -- are request_id:success:latency. Check the second delimiter so a legacy
    -- request ID of "0" is not mistaken for a failed new-format event.
    local first_colon = string.find(member_value, ":", 1, true)
    local second_colon = first_colon and string.find(member_value, ":", first_colon + 1, true)
    local third_colon = second_colon and string.find(member_value, ":", second_colon + 1, true)
    if first_colon == 2 and third_colon ~= nil then
      local timestamp = tonumber(string.sub(member_value, first_colon + 1, second_colon - 1))
      local sequence = tonumber(string.sub(member_value, second_colon + 1, third_colon - 1))
      if timestamp ~= nil and sequence ~= nil then
        return string.sub(member_value, 1, 1) == "1"
      end
    end
    return first_colon ~= nil and string.sub(member_value, first_colon + 1, first_colon + 1) == "1"
  end

  local current_retained = now_ms > cutoff_ms

  local samples = 0
  local successes = 0
  if counters_exist then
    samples = tonumber(redis.call("HGET", node_key, "samples_" .. suffix) or "0") or 0
    successes = tonumber(redis.call("HGET", node_key, "successes_" .. suffix) or "0") or 0
    samples = samples - #removed
    if current_retained then
      samples = samples + 1
    end
    for _, old_member in ipairs(removed) do
      if member_success(old_member) then
        successes = successes - 1
      end
    end
    if current_retained and success == "1" then
      successes = successes + 1
    end
    if samples < 0 then samples = 0 end
    if successes < 0 then successes = 0 end
  else
    local current = redis.call("ZRANGE", window_key, 0, -1)
    samples = #current
    for _, current_member in ipairs(current) do
      if member_success(current_member) then
        successes = successes + 1
      end
    end
  end

  local rate = 0.5
  if samples > 0 then
    rate = successes / samples
  end
  redis.call("HSET", node_key,
    "samples_" .. suffix, tostring(samples),
    "successes_" .. suffix, tostring(successes),
    "sr_" .. suffix, tostring(rate))
  redis.call("EXPIRE", window_key, ttl)
end

update_window(w1, "1m", math.max(60, math.floor(node_ttl / 2)), now_ms - 60000)
update_window(w5, "5m", w5_ttl, now_ms - 300000)
update_window(w30, "30m", w30_ttl, now_ms - 1800000)

-- Request telemetry is still useful while probe/admin state is authoritative.
-- Keep EWMA in the telemetry channel; the priority guard below only protects
-- routing/adjudication fields.
if lat >= 0 then
  local old_lat = tonumber(redis.call("HGET", node_key, "lat_ewma_ms") or "0") or 0
  local ewma = lat
  if old_lat > 0 then
    ewma = math.floor((old_lat * 3 + lat) / 4 + 0.5)
  end
  redis.call("HSET", node_key, "lat_ewma_ms", tostring(ewma))
end

local current_priority = tonumber(redis.call("HGET", node_key, "source_priority") or "0") or 0
if current_priority > 10 then
  redis.call("EXPIRE", node_key, node_ttl)
  return {"applied", "0", "0"}
end

local disabled = redis.call("HGET", node_key, "disabled")
local cool_until_ms = tonumber(redis.call("HGET", node_key, "cool_until_ms") or "0")
local disable_count = tonumber(redis.call("HGET", node_key, "disable_count") or "0")
local in_cool = (disabled == "1") and (cool_until_ms > now_ms)

if in_cool then
  if success == "1" then
    redis.call("HSET", node_key,
      "disabled", "0",
      "available", "1",
      "fail_streak", "0",
      "cool_until_ms", "0",
      "disable_count", "0",
      "last_err", "",
      "disabled_reason", "recovered_with_success_during_cool",
      "updated_at_ms", tostring(now_ms),
      "source_priority", "10")
    redis.call("HINCRBY", node_key, "success_count", 1)
    redis.call("HINCRBY", node_key, "generation", 1)
    redis.call("EXPIRE", node_key, node_ttl)
    return {"applied", "0", "0"}
  else
    local new_cool_seconds = cool_seconds * math.pow(2, disable_count)
    new_cool_seconds = math.min(new_cool_seconds, 3600)
    redis.call("HSET", node_key,
      "cool_until_ms", tostring(now_ms + (new_cool_seconds * 1000)),
      "last_err", err_kind,
      "updated_at_ms", tostring(now_ms),
      "source_priority", "10")
    redis.call("HINCRBY", node_key, "failure_count", 1)
    redis.call("HINCRBY", node_key, "fail_streak", 1)
    redis.call("HINCRBY", node_key, "generation", 1)
    redis.call("EXPIRE", node_key, node_ttl)
    return {"applied", "0", "0"}
  end
end

if disabled == "1" then
  redis.call("HSET", node_key, "disabled", "0")
end

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
  local new_streak = tonumber(redis.call("HINCRBY", node_key, "fail_streak", 1))
  if new_streak >= fail_streak_limit and not free_transient then
    redis.call("HSET", node_key,
      "disabled", "1",
      "available", "0",
      "cool_until_ms", tostring(now_ms + (cool_seconds * 1000)),
      "disable_count", tostring(disable_count + 1),
      "disabled_reason", string.format("fail_streak_%d", new_streak))
  elseif new_streak >= fail_streak_limit and free_transient then
    redis.call("HSET", node_key, "disabled_reason", "free_transient_tolerated")
  end
end

return {"applied", "0", "0"}
