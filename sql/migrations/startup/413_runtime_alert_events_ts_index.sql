-- 413_runtime_alert_events_ts_index.sql
-- 为 runtime_alert_events 增加 detected_at 单列索引，支撑 TTL DELETE。
--
-- 背景：该表为告警事件追加表（无分区策略），由
-- PartitionManager.cleanupOldRuntimeAlertEvents() 周期性 DELETE
-- `WHERE detected_at < now() - ($days)`。现有索引 idx_rae_instance_status
-- 前导列是 instance_id、idx_rae_rule_open 是 rule_key，纯 detected_at 谓词
-- 走不上。本索引让 TTL 清理走索引扫描。对应设置
-- lifecycle.runtime_alert_events_ttl_days（默认 30 天）。

CREATE INDEX IF NOT EXISTS idx_rae_detected_at
    ON public.runtime_alert_events (detected_at DESC);
