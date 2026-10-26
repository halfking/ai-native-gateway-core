-- 361b: provider_models.source 前置补列（host-first 部署通道 prelude）
--
-- 日期: 2026-10-11
--
-- Purpose
-- ───────
-- provider_models.source 由 domain/361 的防御性 ALTER 添加（NOT NULL
-- DEFAULT 'discovery'），投递路径是 scripts/init-local-db.sh / installer，
-- 从不经过 apply-db-revision-sequence 的 startup 通道。host-first 部署的
-- 库只走 startup 通道，列缺失时启动 ensure 链在 db open 即炸：
--   ensure asr catalog seed (provider_models glm-asr): column "source" of
--   relation "provider_models" does not exist (SQLSTATE 42703)
-- 本 prelude 只做 361 的同一句防御性 ALTER，不带 361 的 xai/moonshot/gemini
-- 预填行（那是 domain 层数据，由原通道负责）。
--
-- Idempotent: YES（ADD COLUMN IF NOT EXISTS）
-- Breaking: NO（纯加列，DEFAULT 兼容旧行）

BEGIN;

ALTER TABLE public.provider_models
    ADD COLUMN IF NOT EXISTS source text NOT NULL DEFAULT 'discovery';

COMMIT;
