-- rpm_sliding_window.lua
-- Redis 滑动窗口 RPM 限流脚本
--
-- 用途：跨网关实例的全局 RPM 限流
-- 算法：ZSET 滑动窗口
--
-- KEYS[1] = "rpm:providerID:credentialID"
-- ARGV[1] = limit (int)
-- ARGV[2] = now (unix seconds, float)
-- ARGV[3] = window_seconds (default 60)
--
-- 返回值：
--   {1, count} -- 允许，count 为预留后的总数
--   {0, count} -- 拒绝，count 为当前窗口内总数

local key = KEYS[1]
local limit = tonumber(ARGV[1])
local now = tonumber(ARGV[2])
local window = tonumber(ARGV[3])

-- 1. 删除窗口外的旧记录
local cutoff = now - window
redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)

-- 2. 计数当前窗口内的请求数
local count = redis.call('ZCARD', key)

-- 3. 判断是否超限
if count >= limit then
    -- 超限，返回当前计数
    return {0, count}
end

-- 4. 未超限，添加当前时间戳
-- member 使用 now + 微秒随机数避免冲突
local microseconds = redis.call('TIME')[2]
local member = string.format("%.6f:%s", now, microseconds)
redis.call('ZADD', key, now, member)

-- 5. 设置过期时间（窗口 + 5s 余量）
redis.call('EXPIRE', key, window + 5)

-- 返回 {允许, 新计数}
return {1, count + 1}
