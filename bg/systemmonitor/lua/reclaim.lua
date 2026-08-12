-- ============================================================================
-- reclaim.lua — 把 processing 中 lease 已过期的任务原子重新入队
-- 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.2
-- ============================================================================
--
-- worker 在 claim 后、complete 前崩溃, 任务已不在 ready 队列 (claim 时移走),
-- 只残留在 processing 车道。本脚本扫描 processing, 把 lease 存活 key 已消失
-- (llmgw:monitor:lease:{id} 被 TTL 回收 = worker 死亡/卡住超 lease_ttl) 的成员
-- 重新 RPUSH 回 ready 队列, 并清掉 hash 上残留的 lease_token (避免下一个 worker
-- complete 时被 stale token 误判)。
--
-- 用原生 TTL key (而非 redis.call('TIME') 算术) 作为过期权威: miniredis 的
-- FastForward 推进 TTL 过期但不改变 TIME 返回值, 生产行为同样正确。
--
-- 双执行安全: reclaim 把过期任务重新入队后仍留在 processing, 直到新 claim
-- 把它 LPOP+LPUSH 重写 (产生新 lease_token + 新 lease key), 或 complete 按 id
-- 移除。原 worker 迟到 complete 会被 complete.lua 的 lease_token fencing 拒绝。
--
-- KEYS[1] = llmgw:monitor:processing   (LIST)
-- KEYS[2] = llmgw:monitor:queue        (LIST, ready)
-- KEYS[3] = llmgw:monitor:tasks:       (HASH key 前缀)
-- ARGV[1] = batch_limit (单次最多恢复多少条, 防止 processing 异常膨胀时阻塞)
--
-- 返回值: 恢复条数 (字符串)
-- ============================================================================

local processing_key = KEYS[1]
local queue_key      = KEYS[2]
local hash_prefix    = KEYS[3]
local batch_limit    = tonumber(ARGV[1]) or 256

local items = redis.call('LRANGE', processing_key, 0, -1)
local recovered = 0

for _i, raw in ipairs(items) do
    if recovered >= batch_limit then
        break
    end
    local ok, task = pcall(cjson.decode, raw)
    if ok and type(task) == 'table' and task.id ~= nil then
        local lease_key = 'llmgw:monitor:lease:' .. tostring(task.id)
        -- lease key 不存在 = worker 已不再持有 (崩溃或卡住超过 lease_ttl)
        if redis.call('EXISTS', lease_key) == 0 then
            -- 重新入队: 清掉本次 lease 标记, 让下一轮 claim 产生新 lease
            task.lease_token = nil
            task.lease_until_ms = nil
            local requeue_raw = cjson.encode(task)
            redis.call('RPUSH', queue_key, requeue_raw)
            redis.call('LREM', processing_key, 1, raw)
            -- 清 hash 上的 stale lease_token, complete fencing 不再误拒新 worker
            redis.call('HDEL', hash_prefix .. tostring(task.id), 'lease_token')
            recovered = recovered + 1
        end
    end
end

return tostring(recovered)
