-- 867_session_summaries_last_request_at_idx.sql
-- 2026-10-16：session_summaries 无租户前导的 (last_request_at DESC, session_key DESC) 部分索引。
--
-- 缺陷（2026-10-16 24h 复审轮，245/252 实测）：
--   GET /api/admin/sessions/online 的超管路径（admin/session_online.go，
--   IsSuperAdminOrLegacy 为真时不加 tenant_id 谓词）是
--     WHERE last_request_at IS NOT NULL AND last_request_at >= $1
--     ORDER BY last_request_at DESC, session_key DESC LIMIT n
--   而现有索引里 last_request_at 全都不在前导位：
--     · idx_session_summaries_tenant_time (tenant_id, last_request_at DESC)
--       —— 无租户等值时只能整扫全索引（252 EXPLAIN：3,974 缓冲页，
--       估 74 行实 970 行，再 top-N 排序）；
--     · idx_session_summaries_health_pending (last_request_at DESC)
--       WHERE health_score IS NULL —— 部分谓词与消费方不一致，不可用。
--   暖缓存 102ms，但 252 负载 15–22 时冷缓存要从盘读整棵 25MB 索引，
--   处理器 5s 超时 ⇒ 245 上冷调用两次 500。
--
-- 改法：补 (last_request_at DESC, session_key DESC) WHERE last_request_at IS NOT NULL
--   ——键序与 ORDER BY 逐列同向，Index Scan 按序取前 n 行即停（无排序节点）；
--   部分谓词与消费方的 `last_request_at IS NOT NULL` 精确一致。租户路径
--   继续走 tenant_time，不受影响。
--
-- 上线方式（大表，刻意与 864 不同）：
--   252 生产共享库 73 万行 / 760MB、持续写入。本文件在 psql 单事务链内执行，
--   不能 CONCURRENTLY；普通 CREATE INDEX 会在构建期挡写。因此：
--   ① 生产先由运维 `CREATE INDEX CONCURRENTLY` 同名预建（见 db-changelog）；
--   ② 本文件按 to_regclass 判定**索引不在才建**——PG 的
--      CREATE INDEX IF NOT EXISTS 在发现同名索引之前就已拿到表级 ShareLock，
--      预建环境若直接执行它仍会短暂挡写，故外层先判存在；
--   ③ 新装/小库（211、host-first）走普通构建，秒级。
--
-- 回滚：.down.sql 按名删索引，不动数据。

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '60s';

DO $create_867_index$
BEGIN
  IF to_regclass('public.idx_session_summaries_last_request_recent') IS NULL THEN
    CREATE INDEX IF NOT EXISTS idx_session_summaries_last_request_recent
        ON public.session_summaries (last_request_at DESC, session_key DESC)
        WHERE last_request_at IS NOT NULL;
  END IF;
END
$create_867_index$;

COMMIT;

-- r1030 教训：凡「可能不存在的对象」的 COMMENT 都在 to_regclass 判定后下发。
DO $comment_867_index$
BEGIN
  IF to_regclass('public.idx_session_summaries_last_request_recent') IS NOT NULL THEN
    COMMENT ON INDEX idx_session_summaries_last_request_recent IS
      '867: session_summaries 无租户前导的近窗时间序索引 —— /api/admin/sessions/online 超管路径（无 tenant_id 等值）的承重索引；tenant_time 无前导只能整扫，health_pending 部分谓词不一致。';
  END IF;
END
$comment_867_index$;
