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

if admin_hold == "1" then
  return {"ignored_manual_hold", "0", "0"}
end

local member = req_id .. ":" .. success .. ":" .. lat
redis.call("ZADD", w1, now_ms, member)
redis.call("EXPIRE", w1, math.max(60, math.floor(node_ttl/2)))
redis.call("ZADD", w5, now_ms, member)
redis.call("EXPIRE", w5, w5_ttl)
redis.call("ZADD", w30, now_ms, member)
redis.call("EXPIRE", w30, w30_ttl)

redis.call("HINCRBY", node_key, "generation", 1)
redis.call("HSET", node_key,
  "source_priority", "10",
  "updated_at_ms", tostring(now_ms),
  "last_err", err_kind)
redis.call("EXPIRE", node_key, node_ttl)

if success == "1" then
  redis.call("HINCRBY", node_key, "success_count", 1)
  redis.call("HSET", node_key, "available", "1", "fail_streak", "0")
else
  redis.call("HINCRBY", node_key, "failure_count", 1)
  redis.call("HINCRBY", node_key, "fail_streak", 1)
end

return {"applied", "0", "0"}
