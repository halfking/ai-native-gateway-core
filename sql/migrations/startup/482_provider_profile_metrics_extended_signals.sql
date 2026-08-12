-- Migration 482: Add extended metric signal columns to provider_profile_metrics
--
-- 日期: 2026-08-13
--
-- Background
-- ──────────
-- 2026-08-12 审计发现（生产 154）: provider profile 聚合每 5 分钟报
--   column "rate_limit_hits" does not exist (SQLSTATE 42703)
-- 影响 13 个 credential 的 profile 快照写入失败。
--
-- Root cause:
--   domains/providerprofile/pg_store.go:112,154 在 INSERT / SELECT
--   provider_profile_metrics 时显式列出 rate_limit_hits, rate_limit_total_requests,
--   concurrency_limit, concurrency_limit_auto, concurrency_eff_limit,
--   concurrency_is_capped, downtime_buckets, downtime_total_buckets,
--   longest_downtime_run, quality_stability_mean, quality_stability_stddev,
--   quality_stability_cv, quality_stability_is_volatile, quality_stability_sample_n
--   共 14 列。sql/objects/tables/provider_profile_metrics.sql canonical
--   定义包含这些列，但 154 / 245 / 252 上 154 的 base schema 启动时
--   该表的 inline CREATE 不含这 14 列。
--
--   deploy/sql/migrations/V356__provider_profile_extended_metric_signals.sql
--   包含同样 ADD COLUMN 的迁移，但该迁移文件位于 deploy/sql/migrations/
--   旧路径，deploy-seamless 仅扫描 sql/migrations/startup/ + domain/，
--   V356 从未在 154 跑过（schema_migrations 表无 V356 记录）。
--
-- Fix
-- ────
-- 在新路径 (sql/migrations/startup/482) 重做同样的 ADD COLUMN，
-- IF NOT EXISTS 幂等。注释对齐 sql/objects/tables/provider_profile_metrics.sql
-- 的 canonical 定义。
--
-- Idempotent: 是（IF NOT EXISTS）
-- Down: 见 482_*.down.sql

BEGIN;

ALTER TABLE provider_profile_metrics
    ADD COLUMN IF NOT EXISTS rate_limit_hits INTEGER,
    ADD COLUMN IF NOT EXISTS rate_limit_total_requests INTEGER,
    ADD COLUMN IF NOT EXISTS concurrency_limit INTEGER,
    ADD COLUMN IF NOT EXISTS concurrency_limit_auto INTEGER,
    ADD COLUMN IF NOT EXISTS concurrency_eff_limit INTEGER,
    ADD COLUMN IF NOT EXISTS concurrency_is_capped BOOLEAN,
    ADD COLUMN IF NOT EXISTS downtime_buckets INTEGER,
    ADD COLUMN IF NOT EXISTS downtime_total_buckets INTEGER,
    ADD COLUMN IF NOT EXISTS longest_downtime_run INTEGER,
    ADD COLUMN IF NOT EXISTS quality_stability_mean DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS quality_stability_stddev DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS quality_stability_cv DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS quality_stability_is_volatile BOOLEAN,
    ADD COLUMN IF NOT EXISTS quality_stability_sample_n INTEGER;

COMMIT;
