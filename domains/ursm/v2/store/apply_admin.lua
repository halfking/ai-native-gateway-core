-- domains/ursm/v2/store/apply_admin.lua
-- KEYS[1] = node hash
-- ARGV[1] = "1"|"0"   manual_disabled
-- ARGV[2] = actor
-- ARGV[3] = reason
-- ARGV[4] = issued_at_ms

local node_key = KEYS[1]
local disabled = ARGV[1]
local actor = ARGV[2]
local reason = ARGV[3]
local now_ms = ARGV[4]

redis.call("HSET", node_key,
  "manual_hold", disabled,
  "manual_actor", actor,
  "manual_reason", reason,
  "manual_at_ms", now_ms,
  "source_priority", "40",
  "available", disabled == "1" and "0" or "1")
redis.call("HINCRBY", node_key, "generation", 1)
return {"applied"}
