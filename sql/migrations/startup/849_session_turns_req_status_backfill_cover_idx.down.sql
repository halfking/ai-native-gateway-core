-- 849 down: 删除 remaining-count gauge 的覆盖式部分索引。
-- 回滚后 gauge 的 t 侧退回 per-partition (id) btree + 逐行 heap 回表，
-- 30s 角色级击杀复发（gauge 永远 -1，D9 release gate 不可用），仅在明确指令下执行。
BEGIN;
DROP INDEX IF EXISTS idx_session_turns_req_status_backfill_cov;
COMMIT;
