package ursm

import "github.com/redis/go-redis/v9"

// 并发槽 Redis Lua 脚本

// acquireConcurrencyScript 原子获取并发槽
// 返回: 1=成功, 0=达到限额
var acquireConcurrencyScript = redis.NewScript(`
	local limKey = KEYS[1]   -- llmgw:conc_slot:{credentialID}
	local sessKey = KEYS[2]  -- llmgw:conc_session:{credentialID}:{sessionID}
	local limit = tonumber(ARGV[1])

	-- 检查全局并发数
	local current = tonumber(redis.call('GET', limKey) or '0')
	if current >= limit then
		return 0
	end

	-- 增加全局计数
	redis.call('INCR', limKey)
	redis.call('EXPIRE', limKey, 300)  -- 5分钟过期

	-- 增加会话计数
	redis.call('INCR', sessKey)
	redis.call('EXPIRE', sessKey, 300)

	return 1
`)

// releaseConcurrencyScript 原子释放并发槽
// 返回: 1=成功, 0=失败
var releaseConcurrencyScript = redis.NewScript(`
	local limKey = KEYS[1]   -- llmgw:conc_slot:{credentialID}
	local sessKey = KEYS[2]  -- llmgw:conc_session:{credentialID}:{sessionID}

	-- 减少全局计数
	local globalCount = tonumber(redis.call('GET', limKey) or '0')
	if globalCount > 0 then
		redis.call('DECR', limKey)
	end

	-- 减少会话计数
	local sessCount = tonumber(redis.call('GET', sessKey) or '0')
	if sessCount > 0 then
		redis.call('DECR', sessKey)
		-- 如果会话计数归零，删除key
		if sessCount == 1 then
			redis.call('DEL', sessKey)
		end
	end

	return 1
`)
