-- KEYS[1] is one exact source key.
-- ARGV[1] is the expected hash field count; remaining arguments are sorted
-- field/value pairs from the checksum-validated client snapshot.
local kind = redis.call('TYPE', KEYS[1]).ok
if kind == 'none' then
  return 'missing'
end
if kind ~= 'hash' then
  return 'wrong_type'
end

local expected_count = tonumber(ARGV[1])
if expected_count == nil or expected_count < 0 or (#ARGV - 1) ~= expected_count * 2 then
  return redis.error_reply('invalid cleanup compare-delete arguments')
end
if redis.call('HLEN', KEYS[1]) ~= expected_count then
  return 'changed'
end

for i = 2, #ARGV, 2 do
  if redis.call('HGET', KEYS[1], ARGV[i]) ~= ARGV[i + 1] then
    return 'changed'
  end
end

redis.call('DEL', KEYS[1])
return 'deleted'
