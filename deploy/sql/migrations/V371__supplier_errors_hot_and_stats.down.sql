-- =============================================================================
-- V371__supplier_errors_hot_and_stats.down.sql
-- 回滚：删除 V371 建立的 supplier_errors 事实源三件套。
-- 注意：DROP 不可逆，回滚前确认 supplier_errors 历史数据已归档或可丢弃。
-- =============================================================================

\set ON_ERROR_STOP on

DROP VIEW IF EXISTS supplier_errors_unified;
DROP FUNCTION IF EXISTS promote_supplier_errors_hot_to_partition(interval, int);
DROP FUNCTION IF EXISTS ensure_supplier_errors_partition(timestamp with time zone);
DROP TABLE IF EXISTS supplier_error_stats;
DROP TABLE IF EXISTS supplier_errors CASCADE;
DROP TABLE IF EXISTS supplier_errors_hot CASCADE;
