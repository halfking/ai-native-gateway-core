-- KEYS[1] = source hash
-- KEYS[2] = canonical target hash
-- KEYS[3] = idempotency marker
-- ARGV[1] = expected generation from preflight
-- ARGV[2] = requested remaining TTL in milliseconds (0 = persistent)
-- ARGV[3] = expected field count
-- ARGV[4..] = field/value pairs captured immediately before EVAL
--
-- The caller may read HGETALL to construct ARGV, but this script verifies
-- every source field, HLEN and generation before HSET. It is therefore not
-- a client-side HGETALL -> unconditional HSET overwrite: any live mutation
-- fences the copy. RESTORE/DUMP is deliberately avoided because miniredis
-- does not implement DUMP for hashes; the field-level CAS is executable by
-- both the substitute and real Redis.
if redis.call('EXISTS', KEYS[2]) == 1 then
  if redis.call('EXISTS', KEYS[3]) == 1 then
    return 'already'
  end
  return 'conflict'
end

local source_ttl = redis.call('PTTL', KEYS[1])
if source_ttl == -2 then
  return 'expired'
end
if redis.call('HGET', KEYS[1], 'generation') ~= ARGV[1] then
  return 'generation_changed'
end

local expected_count = tonumber(ARGV[3])
if redis.call('HLEN', KEYS[1]) ~= expected_count then
  return 'checksum_changed'
end
for i = 4, #ARGV, 2 do
  if redis.call('HGET', KEYS[1], ARGV[i]) ~= ARGV[i + 1] then
    return 'checksum_changed'
  end
end

local requested_ttl = tonumber(ARGV[2])
local target_ttl = requested_ttl
if source_ttl == -1 then
  target_ttl = 0
elseif requested_ttl == nil or requested_ttl <= 0 then
  return 'expired'
elseif requested_ttl > source_ttl then
  target_ttl = source_ttl
end

local write_args = {}
for i = 4, #ARGV do
  table.insert(write_args, ARGV[i])
end
redis.call('HSET', KEYS[2], unpack(write_args))
if target_ttl > 0 then
  redis.call('PEXPIRE', KEYS[2], target_ttl)
  redis.call('SET', KEYS[3], '1', 'PX', target_ttl)
else
  redis.call('SET', KEYS[3], '1')
end
return 'applied'
