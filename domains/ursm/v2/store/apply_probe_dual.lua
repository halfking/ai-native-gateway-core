-- Dual-schema probe mutation: updates legacy and K2 node hashes as one atomic
-- operation. The transition logic mirrors apply_probe.lua; cross-schema manual
-- hold is conservative so either held key prevents divergent probe evidence.
--
-- KEYS[1] = legacy node hash
-- KEYS[2] = canonical K2 node hash
-- ARGV layout is identical to apply_probe.lua.

local legacy_node = KEYS[1]
local k2_node = KEYS[2]
local success = ARGV[1]
local latency_ms = ARGV[2]
local now_ms = ARGV[3]
local node_ttl = tonumber(ARGV[6]) or 3600
local priority = tonumber(ARGV[7]) or 20
if priority <= 0 then
  priority = 20
end

if ARGV[4] == "1" or ARGV[5] == "1" then
  return {"ignored_manual_hold"}
end
if redis.call("HGET", legacy_node, "manual_hold") == "1" or
   redis.call("HGET", k2_node, "manual_hold") == "1" then
  return {"ignored_manual_hold"}
end

local function apply_set(node_key)
  local manual_hold = redis.call("HGET", node_key, "manual_hold")
  if manual_hold == "1" then
    return "ignored_manual_hold"
  end

  local cool_until_ms = tonumber(redis.call("HGET", node_key, "cool_until_ms") or "0")
  local disabled = redis.call("HGET", node_key, "disabled")
  local in_cool = (disabled == "1") and (cool_until_ms > tonumber(now_ms))

  redis.call("HINCRBY", node_key, "generation", 1)
  if success == "1" and in_cool then
    redis.call("HSET", node_key,
      "source_priority", tostring(priority),
      "updated_at_ms", now_ms,
      "last_probe_at_ms", now_ms,
      "last_probe_latency_ms", latency_ms,
      "available", "1",
      "disabled", "0",
      "cool_until_ms", "0",
      "fail_streak", "0",
      "last_err", "")
    redis.call("EXPIRE", node_key, node_ttl)
    return "recovered_from_cool"
  end

  redis.call("HSET", node_key,
    "source_priority", tostring(priority),
    "updated_at_ms", now_ms,
    "last_probe_at_ms", now_ms,
    "last_probe_latency_ms", latency_ms,
    "available", success)
  redis.call("EXPIRE", node_key, node_ttl)
  return "applied"
end

local legacy_result = apply_set(legacy_node)
local k2_result = apply_set(k2_node)
if legacy_result == "ignored_manual_hold" or k2_result == "ignored_manual_hold" then
  return {"ignored_manual_hold"}
end
if legacy_result == "recovered_from_cool" or k2_result == "recovered_from_cool" then
  return {"applied", "recovered_from_cool"}
end
return {"applied"}
