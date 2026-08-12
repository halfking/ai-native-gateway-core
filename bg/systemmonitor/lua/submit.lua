-- ============================================================================
-- submit.lua — 原子入队: 分配 ID + 写任务 hash + 入 ready 队列
-- 设计依据: docs/会话优化v2/32-系统监测模块设计.md §3.2
-- ============================================================================
--
-- 把原先 Submit 在 Go 端分三次 round-trip 的 INCR / HSET / LPUSH 收敛为
-- 单次原子 EVAL，消除 "hash 孤儿"(写了 hash 没入队) 与 "queue 孤儿"
-- (入了队但 hash 缺失，REST GET / SSE 重放会失败)。
--
-- KEYS[1] = llmgw:monitor:tasks:counter   (STRING, 自增计数器)
-- KEYS[2] = llmgw:monitor:tasks:           (HASH key 前缀, 拼接 id)
-- KEYS[3] = llmgw:monitor:queue            (LIST, FIFO ready 队列)
-- ARGV[1] = task JSON (id 字段可为 0/缺省, 脚本内覆写为真实 id)
-- ARGV[2] = hash fields JSON (由 Go 端 writeTaskHash 逻辑镜像, 逐字段 HSET)
--
-- 返回值: {id, taskJSON}  (id 为字符串, taskJSON 为填入 id 后的最终 JSON)
-- ============================================================================

local counter_key = KEYS[1]
local hash_prefix = KEYS[2]
local queue_key   = KEYS[3]

local task_raw = ARGV[1]
local fields_raw = ARGV[2]

-- 1. 分配全局唯一 id
local id = redis.call('INCR', counter_key)

-- 2. 解码 task JSON, 覆写 id, 重新编码 (供 claim.lua 消费)
local ok, task = pcall(cjson.decode, task_raw)
if not ok or type(task) ~= 'table' then
    -- 编码非法: 计数器已自增但无法入队 —— 回退计数避免空洞不可怕,
    -- 任务本身非法必须拒绝。
    return redis.error_reply('submit: invalid task json')
end
task.id = id
local out_raw = cjson.encode(task)

-- 3. 写任务 hash (REST GET + SSE 重放的权威来源)
local task_key = hash_prefix .. tostring(id)
redis.call('HSET', task_key, 'id', tostring(id), 'status', 'ready', 'attempt', '0')

local fok, fields = pcall(cjson.decode, fields_raw)
if fok and type(fields) == 'table' then
    for k, v in pairs(fields) do
        redis.call('HSET', task_key, tostring(k), tostring(v))
    end
end

-- 4. 入 ready 队列 (与 claim.lua 的消费端对称: LPUSH 入头)
redis.call('LPUSH', queue_key, out_raw)

return {tostring(id), out_raw}
