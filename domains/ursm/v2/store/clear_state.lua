-- domains/ursm/v2/store/clear_state.lua
-- KEYS[1] = node hash
-- ARGV[1] = issued_at_ms
-- Clears cooling state, error counters, and disable ladder in Redis.
-- Used by emergency repair "clear_circuit" and "reset_errors" actions.
--
-- C5 fix: also HDEL disable_count / disabled / disabled_reason / last_err
-- so that record_request.lua cannot extend the backoff ladder based on
-- stale counts. After this script runs the next failure will start the
-- fail_streak from 1 and the disable ladder from 0.

local node_key = KEYS[1]
local now_ms = ARGV[1]

-- Check if key exists
if redis.call("EXISTS", node_key) == 0 then
    return {"key_not_found"}
end

-- Clear cooling state
redis.call("HDEL", node_key, "cool_until_ms", "cool_start_ms")

-- Clear fail counters
redis.call("HDEL", node_key, "fail_streak", "fail_count")

-- Clear disable ladder so backoff resets to base on next failure
redis.call("HDEL", node_key,
    "disabled",
    "disable_count",
    "disabled_reason",
    "disabled_at_ms",
    "last_err",
    "last_err_at_ms",
    "failure_count",
    "success_count")

-- Increment generation to signal state change
redis.call("HINCRBY", node_key, "generation", 1)

-- Return success with generation
local generation = redis.call("HGET", node_key, "generation")
return {"cleared", generation}
