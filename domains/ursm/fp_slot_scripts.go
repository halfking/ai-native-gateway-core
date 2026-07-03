package ursm

import "github.com/redis/go-redis/v9"

// 指纹槽 Redis Lua 脚本
// 复用 credentialfpslot 的逻辑，适配到 URSM 包

// acquireLRUScript 实现 LRU 抢占逻辑
// 返回: {acquired (0|1), slotIndex, oldHolder}
var acquireLRUScript = redis.NewScript(`
	local prefix = KEYS[1]  -- llmgw:cred_fp_slot:{credentialID}
	local limit  = tonumber(ARGV[1])
	local holder = ARGV[2]
	local slotTTL = tonumber(ARGV[3])
	local pinTTL  = tonumber(ARGV[4])
	local gate    = tonumber(ARGV[5])  -- activeGate (默认300秒)
	local pinKey  = ARGV[6]
	local credID  = tonumber(ARGV[7])

	local bestSlot = -1
	local bestIdle = -1
	local bestOldHolder = nil

	-- 遍历所有槽位
	for slot = 0, limit - 1 do
		local key = prefix .. ':' .. tostring(slot)
		local current = redis.call('GET', key)
		
		-- 情况1: 空闲槽，直接获取
		if current == false then
			redis.call('SET', key, holder, 'EX', slotTTL)
			if pinKey ~= '' and pinTTL > 0 then
				redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
			end
			return {1, slot, ''}
		end
		
		-- 情况2: 已持有该槽，刷新TTL（防止同一会话并发请求消耗多个槽）
		if current == holder then
			redis.call('EXPIRE', key, slotTTL)
			if pinKey ~= '' and pinTTL > 0 then
				redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
			end
			return {1, slot, ''}
		end
		
		-- 情况3: 无TTL槽（异常情况），直接获取
		local remaining = redis.call('TTL', key)
		if remaining == -1 or remaining == -2 then
			redis.call('SET', key, holder, 'EX', slotTTL)
			if pinKey ~= '' and pinTTL > 0 then
				redis.call('SET', pinKey, tostring(slot), 'EX', pinTTL)
			end
			return {1, slot, ''}
		end
		
		-- 情况4: 计算空闲时间，找出LRU槽
		local idle = slotTTL - remaining
		if idle >= gate and idle > bestIdle then
			bestSlot = slot
			bestIdle = idle
			bestOldHolder = current
		end
	end

	-- 没有可抢占的槽（全部活跃）
	if bestSlot == -1 then
		return {0, '', ''}
	end

	-- 抢占LRU槽
	local bestKey = prefix .. ':' .. tostring(bestSlot)
	redis.call('SET', bestKey, holder, 'EX', slotTTL)
	if pinKey ~= '' and pinTTL > 0 then
		redis.call('SET', pinKey, tostring(bestSlot), 'EX', pinTTL)
	end
	
	-- 删除旧holder的pin（防止旧holder回来时冲突）
	if bestOldHolder then
		local oldPinKey = 'llmgw:sess_cred_fp:' .. bestOldHolder .. ':' .. tostring(credID)
		if redis.call('GET', oldPinKey) == tostring(bestSlot) then
			redis.call('DEL', oldPinKey)
		end
	end
	
	return {1, bestSlot, bestOldHolder or ''}
`)

// releaseSlotScript 释放指纹槽（刷新TTL，保留pin）
var releaseSlotScript = redis.NewScript(`
	local slotKey = KEYS[1]
	local pinKey  = KEYS[2]
	local holder  = ARGV[1]
	local slotTTL = tonumber(ARGV[2])
	local pinTTL  = tonumber(ARGV[3])

	local current = redis.call('GET', slotKey)
	
	-- 只有当前holder才能释放
	if current == holder then
		-- 刷新TTL而不是删除（保留槽位归属）
		redis.call('EXPIRE', slotKey, slotTTL)
		
		-- 刷新pin TTL
		if pinKey ~= '' and pinTTL > 0 then
			redis.call('EXPIRE', pinKey, pinTTL)
		end
		return 1
	end
	
	return 0
`)

// forceUnpinScript 强制解绑pin
var forceUnpinScript = redis.NewScript(`
	local pinKey = KEYS[1]
	return redis.call('DEL', pinKey)
`)

// tryPinReuseScript 尝试Pin复用（快速路径）
var tryPinReuseScript = redis.NewScript(`
	local pinKey   = KEYS[1]
	local slotKey  = KEYS[2]
	local holder   = ARGV[1]
	local slotTTL  = tonumber(ARGV[2])
	local pinTTL   = tonumber(ARGV[3])

	local pinnedSlot = redis.call('GET', pinKey)
	if pinnedSlot == false then
		return {0, '', ''}  -- 无pin记录
	end

	local current = redis.call('GET', slotKey)
	
	-- Pin指向的槽是空闲的或者是自己持有的
	if current == false or current == holder then
		redis.call('SET', slotKey, holder, 'EX', slotTTL)
		redis.call('EXPIRE', pinKey, pinTTL)
		return {1, pinnedSlot, ''}
	end
	
	-- Pin指向的槽已被其他holder占用
	return {0, '', ''}
`)
