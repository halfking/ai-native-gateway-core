-- 846 down: 删除回填候选承重部分索引。
-- 回滚后回填 worker 的 idle 空扫回到 pkey 全扫（>30s 击杀 ×~103/日），仅在明确指令下执行。
BEGIN;
DROP INDEX IF EXISTS idx_session_turns_req_status_backfill;
COMMIT;
