local total = 0
local expired = 0
local cursor = '0'
repeat
    local result = redis.call('SCAN', cursor, 'MATCH', 'session:gw_*', 'COUNT', 1000)
    cursor = result[1]
    for _, k in ipairs(result[2]) do
        total = total + 1
        local ttl = redis.call('TTL', k)
        if ttl == -2 then
            expired = expired + 1
        end
    end
until cursor == '0'
return 'total=' .. total .. ' expired=' .. expired
