-- 412_rca_ts_index.sql
-- 为 request_context_attrs 增加 ts 单列索引，支撑 TTL DELETE。
--
-- 背景：该侧表每业务请求写一行（与 request_logs 同速增长），由
-- PartitionManager.cleanupOldRequestContextAttrs() 周期性 DELETE
-- `WHERE ts < now() - ($days)`。现有索引 idx_rca_tenant_ts 前导列是
-- tenant_id，纯 ts 谓词走不上索引。本索引让 TTL 清理走索引扫描而非
-- 全表扫，对应设置 lifecycle.request_context_attrs_ttl_days（默认 7 天）。
-- node_probe_runs 已有 idx_node_probe_runs_started，无需新建。

CREATE INDEX IF NOT EXISTS idx_rca_ts
    ON public.request_context_attrs (ts);
