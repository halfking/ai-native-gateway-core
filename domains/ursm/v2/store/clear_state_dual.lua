-- Dual-schema clear-state mutation for legacy and K2 node hashes.
-- KEYS[1] = legacy node hash, KEYS[2] = canonical K2 node hash.
-- ARGV layout is identical to clear_state.lua.

local function clear_set(node_key)
  if redis.call("EXISTS", node_key) == 0 then
    return false
  end
  redis.call("HDEL", node_key, "cool_until_ms", "cool_start_ms")
  redis.call("HDEL", node_key, "fail_streak", "fail_count")
  redis.call("HDEL", node_key,
    "disabled",
    "disable_count",
    "disabled_reason",
    "disabled_at_ms",
    "last_err",
    "last_err_at_ms",
    "failure_count",
    "success_count")
  redis.call("HINCRBY", node_key, "generation", 1)
  return true
end

local legacy_present = clear_set(KEYS[1])
local k2_present = clear_set(KEYS[2])
if not legacy_present and not k2_present then
  return {"key_not_found"}
end
return {"cleared"}
