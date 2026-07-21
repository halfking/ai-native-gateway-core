-- domains/ursm/v2/store/apply_decision.lua
-- KEYS[1] = node hash
-- ARGV[1] = incoming_generation
-- ARGV[2] = incoming_source_priority
-- ARGV[3] = incoming_available (0|1)
-- ARGV[4] = incoming_fail_streak
-- ARGV[5] = incoming_reason
-- ARGV[6] = current_admin_hold (0|1)

local node_key = KEYS[1]
local in_gen = tonumber(ARGV[1])
local in_pri = tonumber(ARGV[2])
local in_avail = tonumber(ARGV[3])
local in_streak = tonumber(ARGV[4])
local in_reason = ARGV[5]
local admin_hold = ARGV[6]

if admin_hold == "1" then
  return {"ignored_manual_hold", "0"}
end

local cur_gen = tonumber(redis.call("HGET", node_key, "generation") or "0")
local cur_pri = tonumber(redis.call("HGET", node_key, "source_priority") or "0")

if cur_gen > in_gen or (cur_gen == in_gen and cur_pri > in_pri) then
  return {"ignored_stale", tostring(cur_gen)}
end

redis.call("HSET", node_key,
  "available", tostring(in_avail),
  "fail_streak", tostring(in_streak),
  "last_err", in_reason,
  "source_priority", tostring(in_pri),
  "generation", tostring(in_gen))
return {"applied", tostring(in_gen)}