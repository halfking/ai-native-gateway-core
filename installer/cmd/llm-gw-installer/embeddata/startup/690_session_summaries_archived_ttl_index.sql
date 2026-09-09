-- Migration 690: session_summaries 归档 TTL 索引
--
-- Background(2026-09-09 24h 审计第三轮 #4):
--   session_summaries 只有 471 加的 archived_at 标记(domains/sessionarchive
--   把 30d 不活跃的行 SET archived_at = NOW()),但归档行从不删除 → 表无界
--   增长。同批变更在 settings/spec_lifecycle.go 注册
--   lifecycle.session_summaries_ttl_days(默认 90),并由
--   bg/session_summaries_trimmer.go 分批 DELETE 已归档且过期的行。
--
-- 本迁移为其建部分索引(partial index):
--   - 删除路径:WHERE archived_at IS NOT NULL AND archived_at < cutoff,
--     leading 列 archived_at 的范围扫描正好走索引下沉;
--   - last_request_at 作为第二列,归档判定(adjacency 查询按会话活跃度
--     排序/过滤)可做 index-only 的补充过滤;
--   - partial(WHERE archived_at IS NOT NULL)只覆盖归档行,归档行占比低,
--     活跃行(archived_at IS NULL,占绝大多数)完全不入索引,写放大最小。
--
-- 列序 DESC 判断:删除路径对 leading 列是范围条件(< cutoff),ASC 与 DESC
-- 均可反向/正向扫描,无排序消费方,取默认 ASC。
--
-- Idempotent: YES(IF NOT EXISTS)。
-- Down: 无(660+ 惯例)。

CREATE INDEX IF NOT EXISTS idx_session_summaries_archived
    ON public.session_summaries (archived_at, last_request_at)
    WHERE archived_at IS NOT NULL;
