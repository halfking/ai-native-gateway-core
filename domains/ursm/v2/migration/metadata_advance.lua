-- KEYS[1] = <prefix>meta:migration
-- ARGV[1] = expected cutover_epoch
-- ARGV[2] = next checkpoint
-- ARGV[3] = next schema mode
-- ARGV[4] = updated_at RFC3339 timestamp
local current = redis.call('HGET', KEYS[1], 'cutover_epoch')
if current == false then
  return 'missing'
end
if tonumber(current) ~= tonumber(ARGV[1]) then
  return 'superseded'
end
redis.call('HSET', KEYS[1],
  'cutover_epoch', tostring(tonumber(current) + 1),
  'checkpoint', ARGV[2],
  'mode', ARGV[3],
  'updated_at', ARGV[4])
return 'advanced'
