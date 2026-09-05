-- domains/ursm/v2/store/apply_probe.lua
-- KEYS[1] = node hash
-- ARGV[1] = "1"|"0"   success
-- ARGV[2] = latency_ms
-- ARGV[3] = now_ms
-- ARGV[4] = admin_hold (1|0) [DEPRECATED: lua now reads manual_hold directly]
-- ARGV[5] = current_admin_hold (1|0) [DEPRECATED: see ARGV[4] note]
-- ARGV[6] = node_ttl_sec
-- ARGV[7] = source_priority (optional; default 20 = Probe)
--           会话优化 v4 T5 / R4.3 / UT-UR-08: callers may pass
--           api.SourcePriorityRecover (30) so the 36h lookback scan
--           (bg/credential_recovery.go) can write recovery evidence at
--           Recover priority. Absent/invalid keeps the legacy Probe=20.

if ARGV[4] == "1" or ARGV[5] == "1" then
  return {"ignored_manual_hold"}
end

-- M3 (2026-07-28): read manual_hold INSIDE the script so the short-circuit
-- observes the live value at write time. Eliminates the prior TOCTOU race
-- where the Go caller pre-read manual_hold via HGet and could hand a stale
-- value to this script if ApplyAdmin flipped the flag in between. Also
-- drops one hot-path RTT.
local manual_hold = redis.call("HGET", KEYS[1], "manual_hold")
if manual_hold == "1" then
  return {"ignored_manual_hold"}
end

local priority = tonumber(ARGV[7]) or 20
if priority <= 0 then
  priority = 20
end

local cool_until_ms = tonumber(redis.call("HGET", KEYS[1], "cool_until_ms") or "0")
local disabled = redis.call("HGET", KEYS[1], "disabled")
local now_ms = tonumber(ARGV[3])
local node_ttl = tonumber(ARGV[6]) or 3600

-- If node is in cooling period and probe succeeds, recover immediately
local in_cool = (disabled == "1") and (cool_until_ms > now_ms)

redis.call("HINCRBY", KEYS[1], "generation", 1)
if ARGV[1] == "1" and in_cool then
  -- Probe success during cooling -> recover node
  redis.call("HSET", KEYS[1],
    "source_priority", tostring(priority),
    "updated_at_ms", ARGV[3],
    "last_probe_at_ms", ARGV[3],
    "last_probe_latency_ms", ARGV[2],
    "available", "1",
    "disabled", "0",
    "cool_until_ms", "0",
    "fail_streak", "0",
    "last_err", "")
  redis.call("EXPIRE", KEYS[1], node_ttl)
  return {"applied", "recovered_from_cool"}
else
  redis.call("HSET", KEYS[1],
    "source_priority", tostring(priority),
    "updated_at_ms", ARGV[3],
    "last_probe_at_ms", ARGV[3],
    "last_probe_latency_ms", ARGV[2],
    "available", ARGV[1])
  redis.call("EXPIRE", KEYS[1], node_ttl)
  return {"applied"}
end
