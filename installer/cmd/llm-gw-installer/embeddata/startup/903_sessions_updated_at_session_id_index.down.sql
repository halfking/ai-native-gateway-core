-- Migration 903 down: 删除 public.sessions 的 (updated_at DESC, session_id DESC) 索引。
--
-- ★ 回滚后 GET /api/admin/turns/sessions 立刻回到慢形态：排序键
--   (updated_at DESC, session_id DESC) 再次没有索引可提供 ⇒ 规划器退回
--   Seq Scan + top-N Sort ⇒ 439,106 行物化（2026-10-24 实测
--   Execution Time 32,089.911 ms ⇒ 端点回到 500@30s）。
-- ⇒ 这是**不可逆的一步回退**（如实登记，不是缺陷）。

BEGIN;

DROP INDEX IF EXISTS public.idx_sessions_updated_at_session_id;

COMMIT;
