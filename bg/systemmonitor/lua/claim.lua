-- ============================================================================
-- claim.lua — 原子抢占 + 30s dedup + 可恢复 processing lane + lease fencing
-- 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.2
-- ============================================================================
--
-- 可靠性模型 (round 3 审计修复):
--   旧版用 LPOP 把任务从 queue 永久移除 —— worker 在 claim 后、complete 前
--   崩溃, 任务即永久丢失 (inflight TTL 过期后没有任何机制把它重新入队)。
--   现版改为 "move, not pop": 抢占时把队头任务原子移入 processing 列表,
--   complete 时从 processing 移除; reclaim 周期把 processing 中 lease 过期
--   的任务重新入队。worker 崩溃后任务由 reclaim 恢复, 不再丢失。
--
-- KEYS[1] = llmgw:monitor:queue        (LIST, ready 队列)
-- KEYS[2] = llmgw:monitor:processing   (LIST, 已抢占待完成恢复车道)
-- KEYS[3] = llmgw:monitor:running      (SET,  运行中, 供 SSE/指标)
-- KEYS[4] = llmgw:monitor:tasks:       (HASH key 前缀, 拼接 id)
-- ARGV[1] = worker_id
-- ARGV[2] = inflight_ttl_seconds       (默认 30, 同 (cred,model) 短时去重)
-- ARGV[3] = lease_ttl_seconds          (默认 90, 必须 > inflight_ttl)
--
-- 返回值:
--   "":          队列空 / 队头应跳过 (本轮继续)
--   {json}:      抢占成功 (含 worker_id / claimed_at_ms / lease_until_ms / lease_token)
--
-- 副作用:
--   1. 队头任务 → 队尾 (RPOPLPUSH)  当 inflight 占位 或 scheduled 未到
--   2. 队头任务 → processing 列表 (LPOP queue + LPUSH processing), 不再销毁
--   3. 标记 inflight (SET EX), 写 lease_token 到 task hash + JSON
--   4. SADD running
--
-- inflight key 只承担 (credential, model) 30s 去重, 不再承担任务可靠性
-- (可靠性由 processing lane + lease 接管)。
-- ============================================================================

local queue_key      = KEYS[1]
local processing_key = KEYS[2]
local running_key    = KEYS[3]
local hash_prefix    = KEYS[4]
local worker_id      = ARGV[1]
local inflight_ttl   = tonumber(ARGV[2]) or 30
local lease_ttl      = tonumber(ARGV[3]) or 90

-- 队头任务 JSON
local raw = redis.call('LINDEX', queue_key, 0)
if not raw then
    return ""
end

local ok, task = pcall(cjson.decode, raw)
if not ok or type(task) ~= 'table' then
    -- 损坏 JSON: 直接弹出丢弃, 避免死循环
    redis.call('LPOP', queue_key)
    return ""
end

-- 从任务自身字段构造 inflight key (修复 Claim(0,"") 导致的全局 dedup 折叠)
local cred_id = task.credential_id
local raw_model = task.raw_model
if cred_id == nil or raw_model == nil or raw_model == '' then
    redis.call('LPOP', queue_key)
    return ""
end
local inflight_key = 'llmgw:monitor:inflight:' .. tostring(cred_id) .. ':' .. tostring(raw_model)

-- 30s dedup: 探测中则把任务转队尾
if redis.call('EXISTS', inflight_key) == 1 then
    redis.call('RPOPLPUSH', queue_key, queue_key)
    return ""
end

-- scheduled_at_ms 未到也转队尾
local time_arr = redis.call('TIME')
local now_ms = tonumber(time_arr[1]) * 1000 + math.floor(tonumber(time_arr[2]) / 1000)

if task.scheduled_at_ms and type(task.scheduled_at_ms) == 'number' and task.scheduled_at_ms > now_ms then
    redis.call('RPOPLPUSH', queue_key, queue_key)
    return ""
end

-- 抢占: 标记 worker / claimed_at / lease, 重写队头 JSON
local lease_until_ms = now_ms + lease_ttl * 1000
local lease_token = worker_id .. ':' .. tostring(now_ms)
task.worker_id = worker_id
task.claimed_at_ms = now_ms
task.lease_until_ms = lease_until_ms
task.lease_token = lease_token

local new_raw = cjson.encode(task)
redis.call('LSET', queue_key, 0, new_raw)
redis.call('LPOP', queue_key)               -- 从 ready 移除
redis.call('LPUSH', processing_key, new_raw) -- 移入恢复车道 (不销毁)

-- 写 lease_token 到任务 hash (complete.lua fencing 用)
local task_key = hash_prefix .. tostring(task.id)
redis.call('HSET', task_key,
    'status', 'running',
    'worker_id', worker_id,
    'claimed_at_ms', tostring(now_ms),
    'lease_until_ms', tostring(lease_until_ms),
    'lease_token', lease_token)

-- lease 存活 key: reclaim.lua 用 EXISTS 判断 worker 是否仍持有。
-- 用原生 TTL (而非 TIME 算术) 作为过期权威, 保证 miniredis FastForward
-- 与生产 Redis 行为一致 (FastForward 推进 TTL 过期, 但不改变 TIME 返回值)。
redis.call('SET', 'llmgw:monitor:lease:' .. tostring(task.id), lease_token, 'EX', lease_ttl)

-- 写 inflight token (TTL = inflight_ttl_seconds, (cred,model) 短时去重)
redis.call('SET', inflight_key, worker_id, 'EX', inflight_ttl)

-- 加入 running 集合 (for SSE / 指标)
redis.call('SADD', running_key, tostring(task.id))

return new_raw
