-- KEYS[1] = ready key
-- KEYS[2] = epoch hash
-- ARGV[1] = operation: close | open_if_epoch
-- ARGV[2] = close reason (close only)
-- ARGV[3] = timestamp
-- ARGV[4] = observed epoch (open_if_epoch only)
-- ARGV[5] = observed node count (open_if_epoch only)

if ARGV[1] == "close" then
  redis.call("SET", KEYS[1], "0")
  redis.call("HINCRBY", KEYS[2], "counter", 1)
  redis.call("HSET", KEYS[2], "reason", ARGV[2], "started_at", ARGV[3])
  return "closed"
end

local current = redis.call("HGET", KEYS[2], "counter") or ""
if current ~= ARGV[4] then
  return "superseded"
end
if redis.call("GET", KEYS[1]) == "1" then
  return "already_open"
end
redis.call("HINCRBY", KEYS[2], "recovery_counter", 1)
redis.call("HSET", KEYS[2], "recovered_at", ARGV[3], "recovered_keys_count", ARGV[5])
redis.call("SET", KEYS[1], "1")
return "opened"
