-- 850 down: 删除 session_project_backfill 窗口化扫描的 (created_at, session_key) 部分索引。
-- 回滚后窗口化查询退回 idx_session_summaries_project_null 全水库顺序扫描
-- （12.8 万行/tick，~40s 空扫 × 288 tick/天），仅在明确指令下执行。
-- 代码侧的 48h 窗口谓词与 850 是一体修复，回滚索引时须同步回滚
-- bg/project_backfill_worker.go 的窗口谓词，否则窗口过滤退化为逐行 heap 判断。
BEGIN;
DROP INDEX IF EXISTS idx_session_summaries_project_null_recent;
COMMIT;
