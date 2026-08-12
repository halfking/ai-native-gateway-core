-- ============================================================================
-- complete.lua — fenced 完成原子清理 (lease 校验 + processing 移除 + running 移除 + hash 更新)
-- 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.2
-- ============================================================================
--
-- Fencing (round 3 审计修复):
--   旧版任何人都能 complete 任何任务。当 worker A 卡住、lease 过期后任务被
--   reclaim 重新派给 worker B 并由 B 完成时, A 的迟到 complete 仍会覆写状态。
--   现版用 lease_token fencing: 只有持有当前 lease_token 的 worker 才能把
--   running 转为终态; 迟到/stale worker 的 complete 被拒绝 (返回 0), 由调用方
--   视作可接受的 "lease lost, 跳过终态写入"。
--
-- 可靠性:
--   complete 同步从 processing 列表移除该任务 (按 id 匹配 JSON 成员), 使
--   reclaim 不再把已完成任务重新入队。
--
-- KEYS[1] = llmgw:monitor:tasks:{id}              (HASH, 任务权威定义)
-- KEYS[2] = llmgw:monitor:processing               (LIST, 恢复车道)
-- KEYS[3] = llmgw:monitor:running                  (SET)
-- ARGV[1] = status (success/failed/expired/skipped/timeout/network_error)
-- ARGV[2] = extras json (err_code/latency_ms/http_status/skip_reason/...)
-- ARGV[3] = task_id (字符串, 用于从 processing 按 id 匹配移除)
-- ARGV[4] = lease_token (claim 时签发; 与 hash 中的当前 token 不符则拒绝)
--
-- 返回值: 1 = 完成 (或任务已不在 running, 幂等); 0 = stale lease, 拒绝
--
-- inflight token 不在此释放 (让 30s 窗口自然过期), 与旧版一致。
-- ============================================================================

local task_key       = KEYS[1]
local processing_key = KEYS[2]
local running_key    = KEYS[3]
local status         = ARGV[1]
local extras_json    = ARGV[2]
local task_id        = ARGV[3]
local lease_token    = ARGV[4]

-- 1. Fencing: 校验 lease_token (任务不存在 / 已终态 → 视作幂等成功)
local cur_token = redis.call('HGET', task_key, 'lease_token')
if cur_token and cur_token ~= '' and lease_token ~= '' and cur_token ~= lease_token then
    -- 当前 lease 已属于另一个 worker (被 reclaim 重派): 拒绝迟到 complete
    return 0
end

-- 2. 更新任务 hash: status / finished_at / attempt+1
local cur_attempt = redis.call('HGET', task_key, 'attempt')
local next_attempt = 1
if cur_attempt then
    local n = tonumber(cur_attempt)
    if n then next_attempt = n + 1 end
end
redis.call('HSET', task_key, 'status', status, 'finished_at', tostring(redis.call('TIME')[1]), 'attempt', tostring(next_attempt))

-- 应用 extras (json → 逐字段 HSET)
local ok, extras = pcall(cjson.decode, extras_json)
if ok and type(extras) == 'table' then
    for k, v in pairs(extras) do
        if type(v) == 'string' or type(v) == 'number' then
            redis.call('HSET', task_key, k, tostring(v))
        end
    end
end

-- 3. 从 processing 列表按 id 移除恢复副本 (LRANGE + LREM, 处理中列表很小)
if task_id ~= '' then
    local items = redis.call('LRANGE', processing_key, 0, -1)
    for _i, raw in ipairs(items) do
        local dok, dt = pcall(cjson.decode, raw)
        if dok and dt and tostring(dt.id) == task_id then
            redis.call('LREM', processing_key, 1, raw)
            break
        end
    end
end

-- 4. 从 running 集合移除
if task_id ~= '' then
    redis.call('SREM', running_key, task_id)
    -- 释放 lease 存活 key, 使 reclaim 立刻看到任务不再被持有
    redis.call('DEL', 'llmgw:monitor:lease:' .. task_id)
end

return 1
