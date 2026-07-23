-- ============================================================================
-- complete.lua — 完成任务原子清理 (inflight 释放 + running 移除 + 任务 hash 更新)
-- 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.2
-- ============================================================================
--
-- KEYS[1] = llmgw:monitor:tasks:{id}                  (HASH, 任务完整定义)
-- KEYS[2] = llmgw:monitor:inflight:{cred}:{model}     (STRING, 30s dedup)
-- ARGV[1] = status (success/failed/expired/skipped/timeout/network_error)
-- ARGV[2] = json_blob_with_extra_fields  (err_code/latency_ms/http_status/skip_reason/...)
--
-- 设计: complete 阶段不释放 inflight token (让 30s 窗口自然过期)，
--       否则同节点会被多 worker 同时抢占；只有 reset_inflight=1 才主动释放。
--
-- Phase 1: 不释放 inflight；Phase 2 评估是否需要主动 release (task.next_run_at > 30s 时)。
-- ============================================================================

local task_key = KEYS[1]
local inflight_key = KEYS[2]
local status = ARGV[1]
local extras_json = ARGV[2]

-- 更新任务 hash：status / finished_at / attempt+1
redis.call('HSET', task_key, 'status', status)
redis.call('HSET', task_key, 'finished_at', tostring(redis.call('TIME')[1]))

-- 应用 extras (json → 逐字段 HSET)
local ok, extras = pcall(cjson.decode, extras_json)
if ok and type(extras) == 'table' then
    for k, v in pairs(extras) do
        if type(v) == 'string' or type(v) == 'number' then
            redis.call('HSET', task_key, k, tostring(v))
        end
    end
end

-- 从 running 集合移除
local task_id = string.match(task_key, ':(%d+)$')
if task_id then
    redis.call('SREM', 'llmgw:monitor:running', task_id)
end

return 1