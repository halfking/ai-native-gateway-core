-- domains/ursm/v2/store/apply_probe.lua
-- KEYS[1] = node hash
-- ARGV[1] = "1"|"0"   success
-- ARGV[2] = latency_ms
-- ARGV[3] = now_ms
-- ARGV[4] = admin_hold (1|0)
-- ARGV[5] = current_admin_hold (1|0)

if ARGV[4] == "1" or ARGV[5] == "1" then
  return {"ignored_manual_hold"}
end

redis.call("HINCRBY", KEYS[1], "generation", 1)
redis.call("HSET", KEYS[1],
  "source_priority", "20",
  "updated_at_ms", ARGV[3],
  "last_probe_at_ms", ARGV[3],
  "last_probe_latency_ms", ARGV[2],
  "available", ARGV[1])
return {"applied"}