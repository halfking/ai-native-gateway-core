-- Migration 674: training_human_annotations.request_id 唯一索引（P2.1 功能验证 hotfix）
--
-- Bug: 部署 669 后 POST /api/admin/annotations 与 /batch 一律 500：
--   "there is no unique or exclusion constraint matching the ON CONFLICT
--    specification (SQLSTATE 42P10)"
--   669 把 request_id 建成了普通索引（idx_training_human_annotations_
--   request_id），而所有写入路径（web create/batch、llm-gw-annotator
--   importer）都用 ON CONFLICT (request_id) DO NOTHING 做幂等 upsert。
--
-- Fix: 补唯一索引。ON CONFLICT (request_id) 接受唯一索引（无需约束），
--   同时保留原普通索引的查找作用（本索引可取代它，避免重复索引）。
--
-- Idempotent: IF NOT EXISTS。669/671 已应用且 checksum 入账，不可原地改。
-- 2026-09-06

DROP INDEX IF EXISTS idx_training_human_annotations_request_id;

CREATE UNIQUE INDEX IF NOT EXISTS uq_training_human_annotations_request_id
  ON training_human_annotations (request_id);
