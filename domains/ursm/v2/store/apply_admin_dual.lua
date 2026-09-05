-- Dual-schema admin mutation for legacy and K2 node hashes.
-- KEYS[1] = legacy node hash, KEYS[2] = canonical K2 node hash.
-- ARGV layout is identical to apply_admin.lua.

local disabled = ARGV[1]
local actor = ARGV[2]
local reason = ARGV[3]
local now_ms = ARGV[4]

local function apply_set(node_key)
  redis.call("HSET", node_key,
    "manual_hold", disabled,
    "manual_actor", actor,
    "manual_reason", reason,
    "manual_at_ms", now_ms,
    "source_priority", "40",
    "available", disabled == "1" and "0" or "1")
  redis.call("HINCRBY", node_key, "generation", 1)
end

apply_set(KEYS[1])
apply_set(KEYS[2])
return {"applied"}
