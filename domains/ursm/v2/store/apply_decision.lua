-- domains/ursm/v2/store/apply_decision.lua
-- KEYS[1] = node hash
-- ARGV[1] = incoming_generation
-- ARGV[2] = incoming_source_priority
-- ARGV[3] = incoming_available (0|1)
-- ARGV[4] = incoming_fail_streak
-- ARGV[5] = incoming_reason
-- ARGV[6] = caller-supplied admin_hold (0|1) [DEPRECATED: lua now reads manual_hold directly]

local node_key = KEYS[1]
-- Defensive defaults: a missing/empty ARGV would make tonumber() return nil,
-- and the comparison at L34 would then throw a raw Lua error ("attempt to
-- compare number with nil") that surfaces as a hard EVAL failure. Mirror the
-- "or \"0\"" guard used for the in-script HGET reads below.
local in_gen = tonumber(ARGV[1]) or 0
local in_pri = tonumber(ARGV[2]) or 0
local in_avail = tonumber(ARGV[3]) or 0
local in_streak = tonumber(ARGV[4]) or 0
local in_reason = ARGV[5]

-- M3-style fix (2026-08-29): read manual_hold INSIDE the script so the
-- short-circuit observes the live value at write time. Eliminates the
-- prior TOCTOU race where the Go caller pre-read manual_hold via HGet
-- and could hand a stale value to this script if ApplyAdmin flipped
-- the flag in between. Also drops one hot-path RTT. Mirrors the
-- apply_probe.lua M3 fix from 2026-07-28.
local manual_hold = redis.call("HGET", node_key, "manual_hold")
if manual_hold == "1" then
  return {"ignored_manual_hold", "0"}
end
-- Deprecated ARGV[6] retained for ABI parity with older callers; the
-- live-read above is the source of truth. Admin priority still wins
-- because a fresh HGET sees whatever ApplyAdmin just wrote.

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