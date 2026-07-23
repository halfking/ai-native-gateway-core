-- domains/ursm/v2/store/clear_state.lua
-- KEYS[1] = node hash
-- ARGV[1] = issued_at_ms
-- Clears cooling state and error counters in Redis.
-- Used by emergency repair "clear_circuit" and "reset_errors" actions.

local node_key = KEYS[1]
local now_ms = ARGV[1]

-- Check if key exists
if redis.call("EXISTS", node_key) == 0 then
    return {"key_not_found"}
end

-- Clear cool_until_ms and cool_start_ms
redis.call("HDEL", node_key, "cool_until_ms", "cool_start_ms")

-- Clear fail streak and fail count
redis.call("HDEL", node_key, "fail_streak", "fail_count")

-- Increment generation to signal state change
redis.call("HINCRBY", node_key, "generation", 1)

-- Return success with generation
local generation = redis.call("HGET", node_key, "generation")
return {"cleared", generation}
