-- Dual-schema record_request: maintains the legacy and the canonical (k2)
-- key sets for one logical (tenant, credential, raw model) tuple as a
-- single atomic unit (doc 14 §5.2). The per-set state machine is the
-- record_request.lua logic verbatim; only the manual_hold and dedup gates
-- are cross-set, so a hold or a duplicate seen in either grammar dominates
-- both.
--
-- KEYS[1..5] = legacy node hash, win1m, win5m, win30m, dedup
-- KEYS[6..10] = canonical node hash, win1m, win5m, win30m, dedup;
--               KEYS[6] == "" when the tuple cannot be represented
--               canonically (empty tenant): the script then maintains the
--               legacy set only (doc 14 §2).
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
-- ARGV[14] = health_status (optional; rich health enum, display-only bridge)
-- ARGV[15] = backoff_cap_seconds (optional; caps cool_seconds × 2^disable_count)
--
-- Per-set apply_set mirrors record_request.lua: window counters
-- (samples_/successes_/sr_*) live in the node hash and stay coherent with
-- the ZSET members; the legacy request_id of "0" heuristic (doc 14 §5.2.2)
-- is preserved verbatim. Health bridge is display-only and runs before the
-- source-priority guard, so an older caller's unknown enum value can never
-- corrupt a live hash.

local legacy_node = KEYS[1]
local legacy_w1 = KEYS[2]
local legacy_w5 = KEYS[3]
local legacy_w30 = KEYS[4]
local legacy_dedup = KEYS[5]
local k2_node = KEYS[6]
local k2_w1 = KEYS[7]
local k2_w5 = KEYS[8]
local k2_w30 = KEYS[9]
local k2_dedup = KEYS[10]
local have_k2 = k2_node ~= ""
local success = ARGV[1]
local err_kind = ARGV[2]
local now_ms = tonumber(ARGV[3]) or 0
local lat = tonumber(ARGV[4]) or 0
local req_id = ARGV[5]
local is_empty_response = success == "0" and err_kind == "empty_response"
local empty_marker = is_empty_response and "e1" or "e0"
local node_ttl = tonumber(ARGV[6]) or 3600
local w5_ttl = tonumber(ARGV[7]) or 360
local w30_ttl = tonumber(ARGV[8]) or 2100
local admin_hold_arg = ARGV[9]
local cool_seconds = tonumber(ARGV[10]) or 300
local fail_streak_limit = tonumber(ARGV[11]) or 3
local dedup_enabled = ARGV[12] == "1"
local billing_mode = ARGV[13] or ""
local health_status = ARGV[14] or ""
local backoff_cap_seconds = tonumber(ARGV[15]) or 1800

local transient_kinds = {
  rate_limit = true,
  timeout = true,
  stream_timeout = true,
  upstream_down = true,
  empty_response = true,
  transient = true,
}
local free_transient = (billing_mode == "free") and (transient_kinds[err_kind] == true)

-- Cross-set gates. A manual hold or an already-seen request in EITHER
-- grammar must dominate both: writing one side of a duplicate or held
-- request would let the two states diverge (doc 14 §5.2.2).
local manual_hold_legacy = redis.call("HGET", legacy_node, "manual_hold")
local manual_hold_k2 = "0"
if have_k2 then
  manual_hold_k2 = redis.call("HGET", k2_node, "manual_hold")
end
if manual_hold_legacy == "1" or manual_hold_k2 == "1" or admin_hold_arg == "1" then
  return {"ignored_manual_hold", "0", "0"}
end

if dedup_enabled then
  local dup_k2 = true
  if have_k2 then
    dup_k2 = redis.call("SET", k2_dedup, "1", "NX", "EX", node_ttl)
  end
  local dup_legacy = redis.call("SET", legacy_dedup, "1", "NX", "EX", node_ttl)
  if dup_legacy == false or dup_k2 == false then
    return {"duplicate", "0", "0"}
  end
end

-- Per-set state machine. Identical to record_request.lua so both grammars
-- evolve the same state for the same event stream; event_seq and generation
-- advance independently per set but stay in lockstep because every event is
-- applied to both inside this one script.
local function apply_set(node_key, w1, w5, w30)
  local event_seq = redis.call("HINCRBY", node_key, "event_seq", 1)
  local member = success .. ":" .. empty_marker .. ":" .. tostring(now_ms) .. ":" .. tostring(event_seq) .. ":" .. req_id

  local function update_window(window_key, suffix, ttl, cutoff_ms)
    local existed = redis.call("EXISTS", window_key) == 1
    local counters_exist = existed and
      redis.call("HEXISTS", node_key, "samples_" .. suffix) == 1 and
      redis.call("HEXISTS", node_key, "successes_" .. suffix) == 1 and
      redis.call("HEXISTS", node_key, "empty_responses_" .. suffix) == 1

    redis.call("ZADD", window_key, now_ms, member)
    local removed = redis.call("ZRANGEBYSCORE", window_key, "-inf", tostring(cutoff_ms))
    redis.call("ZREMRANGEBYSCORE", window_key, "-inf", tostring(cutoff_ms))

    local function member_success(member_value)
      local first_colon = string.find(member_value, ":", 1, true)
      local second_colon = first_colon and string.find(member_value, ":", first_colon + 1, true)
      local third_colon = second_colon and string.find(member_value, ":", second_colon + 1, true)
      if first_colon == 2 and third_colon ~= nil then
        local marker = string.sub(member_value, first_colon + 1, second_colon - 1)
        if marker == "e0" or marker == "e1" then
          return string.sub(member_value, 1, 1) == "1"
        end
        local timestamp = tonumber(string.sub(member_value, first_colon + 1, second_colon - 1))
        local sequence = tonumber(string.sub(member_value, second_colon + 1, third_colon - 1))
        if timestamp ~= nil and sequence ~= nil then
          return string.sub(member_value, 1, 1) == "1"
        end
      end
      return first_colon ~= nil and string.sub(member_value, first_colon + 1, first_colon + 1) == "1"
    end

    local function member_empty_response(member_value)
      local first_colon = string.find(member_value, ":", 1, true)
      local second_colon = first_colon and string.find(member_value, ":", first_colon + 1, true)
      if first_colon == 2 and second_colon ~= nil then
        return string.sub(member_value, first_colon + 1, second_colon - 1) == "e1"
      end
      return false
    end

    local current_retained = now_ms > cutoff_ms

    local samples = 0
    local successes = 0
    local empty_responses = 0
    if counters_exist then
      samples = tonumber(redis.call("HGET", node_key, "samples_" .. suffix) or "0") or 0
      successes = tonumber(redis.call("HGET", node_key, "successes_" .. suffix) or "0") or 0
      empty_responses = tonumber(redis.call("HGET", node_key, "empty_responses_" .. suffix) or "0") or 0
      samples = samples - #removed
      if current_retained then
        samples = samples + 1
      end
      for _, old_member in ipairs(removed) do
        if member_success(old_member) then
          successes = successes - 1
        end
        if member_empty_response(old_member) then
          empty_responses = empty_responses - 1
        end
      end
      if current_retained and success == "1" then
        successes = successes + 1
      end
      if current_retained and empty_marker == "e1" then
        empty_responses = empty_responses + 1
      end
      if samples < 0 then samples = 0 end
      if successes < 0 then successes = 0 end
      if empty_responses < 0 then empty_responses = 0 end
    else
      local current = redis.call("ZRANGE", window_key, 0, -1)
      samples = #current
      for _, current_member in ipairs(current) do
        if member_success(current_member) then
          successes = successes + 1
        end
        if member_empty_response(current_member) then
          empty_responses = empty_responses + 1
        end
      end
    end

    local rate = 0.5
    if samples > 0 then
      rate = successes / samples
    end
    local empty_rate = 0
    if samples > 0 then
      empty_rate = empty_responses / samples
    end
    redis.call("HSET", node_key,
      "samples_" .. suffix, tostring(samples),
      "successes_" .. suffix, tostring(successes),
      "empty_responses_" .. suffix, tostring(empty_responses),
      "empty_response_rate_" .. suffix, tostring(empty_rate),
      "sr_" .. suffix, tostring(rate))
    redis.call("EXPIRE", window_key, ttl)
  end

  update_window(w1, "1m", math.max(60, math.floor(node_ttl / 2)), now_ms - 60000)
  update_window(w5, "5m", w5_ttl, now_ms - 300000)
  update_window(w30, "30m", w30_ttl, now_ms - 1800000)

  if lat >= 0 then
    local old_lat = tonumber(redis.call("HGET", node_key, "lat_ewma_ms") or "0") or 0
    local ewma = lat
    if old_lat > 0 then
      ewma = math.floor((old_lat * 3 + lat) / 4 + 0.5)
    end
    redis.call("HSET", node_key, "lat_ewma_ms", tostring(ewma))
  end

  local valid_health = {
    healthy = true,
    suspect = true,
    degraded = true,
    quarantined = true,
    recovering = true,
  }
  if valid_health[health_status] == true then
    redis.call("HSET", node_key, "health", health_status)
  end

  local current_priority = tonumber(redis.call("HGET", node_key, "source_priority") or "0") or 0
  if current_priority > 10 then
    redis.call("EXPIRE", node_key, node_ttl)
    return
  end

  -- Empty responses are binding-scoped quality telemetry. Their windows above
  -- are authoritative for routing penalties, but they must never mutate cooldown
  -- or fail-streak state because that state would hard-exclude this node.
  if is_empty_response then
    redis.call("HINCRBY", node_key, "failure_count", 1)
    -- A first-ever observation needs the normal routable baseline; without it
    -- the read path treats an otherwise healthy node as unavailable forever.
    -- Once any writer has established availability, do not touch routing state:
    -- a cooldown, probe, or admin decision remains authoritative.
    if redis.call("HEXISTS", node_key, "available") == 0 then
      redis.call("HINCRBY", node_key, "generation", 1)
      redis.call("HSET", node_key,
        "available", "1",
        "source_priority", "10",
        "updated_at_ms", tostring(now_ms))
    end
    redis.call("HSET", node_key, "last_empty_response_at_ms", tostring(now_ms))
    redis.call("EXPIRE", node_key, node_ttl)
    return
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
      return
    else
      local new_cool_seconds = cool_seconds * math.pow(2, disable_count)
      new_cool_seconds = math.min(new_cool_seconds, backoff_cap_seconds)
      redis.call("HSET", node_key,
        "cool_until_ms", tostring(now_ms + (new_cool_seconds * 1000)),
        "last_err", err_kind,
        "updated_at_ms", tostring(now_ms),
        "source_priority", "10")
      redis.call("HINCRBY", node_key, "failure_count", 1)
      redis.call("HINCRBY", node_key, "fail_streak", 1)
      redis.call("HINCRBY", node_key, "generation", 1)
      redis.call("EXPIRE", node_key, node_ttl)
      return
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
    -- Empty responses are recorded for binding-scoped routing penalties but
    -- must never advance hard-disable state for this credential/model node.
    if err_kind == "empty_response" then
      redis.call("HSET", node_key, "disabled_reason", "empty_response_soft_penalty")
      return
    end
    local fatal = string.match(err_kind, "^auth") or string.match(err_kind, "^quota")
    if fatal then
      redis.call("HSET", node_key,
        "disabled", "1",
        "available", "0",
        "cool_until_ms", tostring(now_ms + (cool_seconds * 1000)),
        "disable_count", tostring(disable_count + 1),
        "disabled_reason", err_kind)
      redis.call("EXPIRE", node_key, node_ttl)
      return {"applied", "0", "0"}
    end
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
end

apply_set(legacy_node, legacy_w1, legacy_w5, legacy_w30)
if have_k2 then
  apply_set(k2_node, k2_w1, k2_w5, k2_w30)
end

return {"applied", "0", "0"}
