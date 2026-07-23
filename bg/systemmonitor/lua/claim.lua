-- ============================================================================
-- claim.lua — 原子抢占 + 30s dedup + scheduled_at 校验
-- 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.2
-- ============================================================================
--
-- KEYS[1] = llmgw:monitor:queue           (LIST, FIFO)
-- KEYS[2] = llmgw:monitor:inflight:{cred}:{model}  (STRING, 30s dedup token)
-- ARGV[1] = worker_id                     (标识抢占方)
-- ARGV[2] = inflight_ttl_seconds          (默认 30)
--
-- 返回值:
--   nil (false-like): 队列空，或队头任务应被跳过 (本轮循环继续)
--   {json}: 成功抢占的任务对象 (含 worker_id / claimed_at_ms 字段)
--
-- 副作用:
--   1. 队头任务 → 队尾 (RPOPLPUSH)  当 inflight 占位 或 scheduled 未到
--   2. 弹出队头 (LPOP) + 标记 inflight (SET EX)
--   3. 加入 llmgw:monitor:running SET
--
-- 多机一致性:
--   Redis 单实例下原子；Redis Cluster 下需保证 KEYS[1] 与 KEYS[2] 同 slot
--   （Phase 3 评估 hash tags: {llmgw:monitor}）。
-- ============================================================================

local queue_key = KEYS[1]
local inflight_key = KEYS[2]
local worker_id = ARGV[1]
local inflight_ttl = tonumber(ARGV[2])

-- 队头任务 JSON
local raw = redis.call('LINDEX', queue_key, 0)
if not raw then
    return false
end

local ok, task = pcall(cjson.decode, raw)
if not ok or type(task) ~= 'table' then
    -- 损坏 JSON：直接弹出丢弃，避免死循环
    redis.call('LPOP', queue_key)
    return false
end

-- 30s dedup：探测中则把任务转队尾
if redis.call('EXISTS', inflight_key) == 1 then
    redis.call('RPOPLPUSH', queue_key, queue_key)
    return false
end

-- scheduled_at_ms 未到也转队尾（即时任务 scheduled_at_ms = nil 或 <= now）
local time_arr = redis.call('TIME')
local now_ms = tonumber(time_arr[1]) * 1000 + math.floor(tonumber(time_arr[2]) / 1000)

if task.scheduled_at_ms and type(task.scheduled_at_ms) == 'number' and task.scheduled_at_ms > now_ms then
    redis.call('RPOPLPUSH', queue_key, queue_key)
    return false
end

-- 抢占：标记 worker / claimed_at，更新队头 JSON
task.worker_id = worker_id
task.claimed_at_ms = now_ms

local new_raw = cjson.encode(task)
redis.call('LSET', queue_key, 0, new_raw)
local popped = redis.call('LPOP', queue_key)

-- 写入 inflight token (TTL = inflight_ttl_seconds)
redis.call('SET', inflight_key, worker_id, 'EX', inflight_ttl)

-- 加入 running 集合 (for SSE / 指标)
redis.call('SADD', 'llmgw:monitor:running', tostring(task.id))

return new_raw