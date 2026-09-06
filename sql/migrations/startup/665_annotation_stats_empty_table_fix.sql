-- Migration 665: annotation_stats 空表 NULL 修复（P2.1 功能验证 hotfix）
--
-- Bug: 部署 664 后 /api/admin/annotations/stats 在空表上 500：
--   "can't scan into dest[3] (col: accuracy_percent): cannot scan NULL
--    into *float64"
--   ROUND(AVG(is_correct::int) * 100, 2) 在无行时返回 NULL，pgx 无法把
--   NULL scan 进 float64。生产刚部署时标注表必为空，统计页必然打不开。
--
-- Fix: accuracy_percent 用 COALESCE(..., 0)。first/last_annotation_at 在
--   空表时保持 NULL（语义正确），Go 侧改为 *time.Time 扫描。
--
-- Idempotent: CREATE OR REPLACE VIEW（664 已应用且 checksum 入账，不可原地改）。
-- 2026-09-06

CREATE OR REPLACE VIEW annotation_stats AS
SELECT
  COUNT(*) AS total_annotations,
  COUNT(*) FILTER (WHERE is_correct) AS correct_count,
  COUNT(*) FILTER (WHERE NOT is_correct) AS incorrect_count,
  COALESCE(ROUND(AVG(is_correct::int) * 100, 2), 0) AS accuracy_percent,
  COUNT(DISTINCT annotator) AS num_annotators,
  MIN(annotated_at) AS first_annotation_at,
  MAX(annotated_at) AS last_annotation_at
FROM training_human_annotations;

COMMENT ON VIEW annotation_stats IS
  'P2.1标注统计汇总。空表时 accuracy_percent=0、first/last_annotation_at=NULL（665）。';
