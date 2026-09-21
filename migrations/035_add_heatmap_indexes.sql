-- Migration: 035 — credential heatmap 索引（2026-09-07 重写）
--
-- 历史：初版（2026-09-06）有三重缺陷，在任何库上都执行不成功：
--   1. 谓词引用不存在的 is_self_test 列（42703）——自检流量过滤已改为
--      task_type='probe_triggered' + 'probe'=ANY(quality_flags)，
--      见 admin/credential_monitor_heatmap.go；
--   2. 谓词含 NOW()-INTERVAL（STABLE），PG 要求索引谓词 IMMUTABLE；
--   3. CREATE INDEX CONCURRENTLY 作用于分区父表 request_logs，会向全部
--      月度分区传播，Citus columnar 分区不支持 btree。
--   因此本迁移从未在任何库生效，原地重写是安全的。
--
-- 重写原则：
--   - 只建 request_logs_hot（heap，8h 保留），避开 columnar 分区；
--     更大窗口的查询走 request_logs_with_current_month 视图，
--     由 idx_request_logs_hot_credential_model_ts（schema 内建）驱动；
--   - 谓词只用真实列 + IMMUTABLE 表达式；
--   - 只补现有索引没有覆盖的形态：失败样本子查询
--     （heatmap 的 failed-samples CTE：NOT success AND error_kind IS NOT NULL，
--     按 credential_id + 时间桶聚合）；
--   - 热表体量小，普通 CREATE INDEX 即可，无需 CONCURRENTLY。

-- 防御性清理：若某环境手工建过初版的残留索引则移除（正常不存在）。
DROP INDEX IF EXISTS idx_request_logs_heatmap_core;
DROP INDEX IF EXISTS idx_request_logs_heatmap_covering;
DROP INDEX IF EXISTS idx_request_logs_tenant_heatmap;

-- 失败样本子查询专用部分索引（IMMUTABLE 谓词，真实列）。
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_failures_cred_ts
ON request_logs_hot (
  credential_id,
  ts DESC
)
WHERE credential_id IS NOT NULL
  AND success = FALSE
  AND error_kind IS NOT NULL;

ANALYZE request_logs_hot;
