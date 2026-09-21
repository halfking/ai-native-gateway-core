-- migrations/orphan-in-progress-cleanup-20260827.sql
-- 
-- 审计任务：修复 279 条卡住的 in_progress 孤儿记录
-- 
-- 背景：
-- 2026-08-27 审计发现近 7 天有 279 条 request_status='in_progress' 且超过 5 分钟未更新的记录，
-- 集中在 gpt-5.6-terra (147条) 和 minimax-m3 (94条)。这些记录的 success=false 是正确的——
-- 它们确实从未成功完成，原因可能是 panic/context cancel/重启等导致终态 UPDATE 未执行。
--
-- 修复策略：
-- 1. 将超过 5 分钟仍为 in_progress 的记录标记为 'stuck'（新状态值，前端可识别并展示为异常）
-- 2. 或者根据 stream_done_received 等字段推导：若有 done 信号则标记 success，否则标记 failure
--
-- 本次采用方案 2（保守推导），避免引入新状态值。

BEGIN;

-- Step 1: 预览将被修复的记录
SELECT
  request_id,
  ts,
  success,
  request_status,
  error_kind,
  latency_ms,
  stream_done_received,
  stream_done_sent,
  stream_interrupted,
  outbound_model
FROM request_logs
WHERE request_status = 'in_progress'
  AND success = false
  AND ts < now() - interval '5 minutes'
  AND ts > now() - interval '30 days'
ORDER BY ts DESC
LIMIT 20;

-- Step 2: 统计待修复记录的分布
SELECT
  outbound_model,
  stream_done_received,
  stream_interrupted,
  count(*) AS cnt
FROM request_logs
WHERE request_status = 'in_progress'
  AND success = false
  AND ts < now() - interval '5 minutes'
  AND ts > now() - interval '30 days'
GROUP BY outbound_model, stream_done_received, stream_interrupted
ORDER BY cnt DESC;

-- Step 3: 执行修复（幂等，可重复执行）
-- 
-- 规则：
-- 1. 若 stream_done_received=true，说明上游已完成，标记为 success
-- 2. 若 stream_interrupted=true，说明中断，保留 request_status='in_progress' 但补充 error_kind='client_disconnected'
-- 3. 否则，标记为 failure + error_kind='timeout_or_panic'（兜底）
UPDATE request_logs
SET
  request_status = CASE
    WHEN stream_done_received = true THEN 'success'
    WHEN stream_interrupted = true THEN 'failure'
    ELSE 'failure'
  END,
  error_kind = CASE
    WHEN stream_done_received = true THEN NULL
    WHEN stream_interrupted = true THEN 'client_disconnected'
    ELSE 'timeout_or_panic'
  END,
  success = CASE
    WHEN stream_done_received = true THEN true
    ELSE false
  END
WHERE request_status = 'in_progress'
  AND success = false
  AND ts < now() - interval '5 minutes'
  AND ts > now() - interval '30 days';

-- Step 4: 验证修复结果
SELECT
  request_status,
  error_kind,
  success,
  count(*) AS cnt
FROM request_logs
WHERE ts > now() - interval '30 days'
GROUP BY request_status, error_kind, success
ORDER BY cnt DESC;

-- Step 5: 确认没有遗留的卡住记录
SELECT count(*) AS remaining_stuck_count
FROM request_logs
WHERE request_status = 'in_progress'
  AND success = false
  AND ts < now() - interval '5 minutes'
  AND ts > now() - interval '30 days';

COMMIT;

-- 执行后预期结果：
-- - remaining_stuck_count = 0 或接近 0（只剩最近 5 分钟内的活跃请求）
-- - 修复后的记录按推导规则分布到 success/failure + 对应 error_kind
-- 
-- 注意：
-- 1. 本脚本幂等，可在 154 生产库上重复执行
-- 2. 修复后，dashboard 将正确显示这些请求的真实终态
-- 3. 若未来仍持续出现孤儿记录，需要在代码层面添加 defer cleanup 或后台清扫任务
