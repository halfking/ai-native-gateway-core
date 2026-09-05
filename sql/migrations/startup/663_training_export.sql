-- Migration 663: Training Data Export Infrastructure
-- Part of: P2.2 - Training Data Export Pipeline
-- Purpose: 支持从auto_route_selections导出结构化特征+标签，用于ML模型训练
--
-- Privacy: 导出系统只访问15个结构化特征字段，不导出prompt/messages/response
--
-- Tables:
--   1. training_export_configs: 导出配置模板（时间范围、质量过滤、去重策略）
--   2. training_exports: 导出执行记录（状态、行数、输出路径）
--
-- 2026-09-06

-- ============================================================================
-- 1. 导出配置表
-- ============================================================================

CREATE TABLE IF NOT EXISTS training_export_configs (
  id SERIAL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  description TEXT, -- 配置说明（用途、实验名称等）
  
  -- 数据范围
  feature_version TEXT NOT NULL, -- v1, v2（对应structured_features.go的版本）
  time_range_start DATE NOT NULL,
  time_range_end DATE NOT NULL,
  
  -- 质量过滤条件
  quality_filters JSONB NOT NULL DEFAULT '{}'::jsonb,
  -- 示例:
  -- {
  --   "min_confidence": 0.7,          // 最低置信度
  --   "exclude_explore": true,        // 排除探索模式
  --   "require_settled": true,        // 只导出已结算的行
  --   "min_reward": 0.5,              // 最低reward（如果require_settled=true）
  --   "allowed_task_types": ["chat"]  // 只导出特定task_type
  -- }
  
  -- 去重策略
  dedup_strategy TEXT NOT NULL DEFAULT 'content_hash',
  -- 选项:
  --   content_hash: 基于content_hash去重（默认，推荐）
  --   request_id: 基于request_id去重
  --   none: 不去重（谨慎使用，可能导致训练偏差）
  
  -- 输出配置
  output_format TEXT NOT NULL DEFAULT 'parquet', -- parquet, csv, jsonl
  compression TEXT NOT NULL DEFAULT 'snappy', -- snappy, gzip, none
  
  -- 元数据
  created_by TEXT, -- 操作人员
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_training_export_configs_name 
  ON training_export_configs(name);

COMMENT ON TABLE training_export_configs IS 
  'P2.2导出配置模板。定义数据范围、质量过滤、去重策略等。隐私保证：只配置结构化特征导出，不包含prompt/messages。';

-- ============================================================================
-- 2. 导出执行记录表
-- ============================================================================

CREATE TABLE IF NOT EXISTS training_exports (
  id SERIAL PRIMARY KEY,
  config_id INTEGER NOT NULL REFERENCES training_export_configs(id) ON DELETE CASCADE,
  
  -- 执行状态
  status TEXT NOT NULL DEFAULT 'pending',
  -- 状态流转: pending → running → completed/failed
  -- 状态说明:
  --   pending: 已创建，等待执行
  --   running: 正在导出
  --   completed: 导出成功
  --   failed: 导出失败
  
  -- 数据统计
  row_count_raw INTEGER, -- 查询到的原始行数
  row_count_filtered INTEGER, -- 质量过滤后的行数
  row_count_deduped INTEGER, -- 去重后的最终行数
  
  -- 导出结果
  output_path TEXT, -- 输出路径（本地文件或S3 URI）
  -- 示例:
  --   /data/exports/2026-09-06-v1-balanced.parquet
  --   s3://llm-training-data/exports/2026-09-06-v1-balanced.parquet
  
  file_size_bytes BIGINT, -- 文件大小（字节）
  
  -- 时间戳
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  duration_seconds INTEGER GENERATED ALWAYS AS (
    EXTRACT(EPOCH FROM (completed_at - started_at))::INTEGER
  ) STORED,
  
  -- 错误信息
  error_message TEXT, -- 失败原因（仅当status=failed时）
  
  -- 元数据
  triggered_by TEXT, -- 触发来源（manual, scheduled, api）
  export_metadata JSONB, -- 额外元数据
  -- 示例:
  -- {
  --   "cli_version": "1.2.3",
  --   "operator": "data-team",
  --   "experiment_id": "exp-2026-09-06-001"
  -- }
  
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_training_exports_config_id 
  ON training_exports(config_id);

CREATE INDEX IF NOT EXISTS idx_training_exports_status 
  ON training_exports(status);

CREATE INDEX IF NOT EXISTS idx_training_exports_created_at 
  ON training_exports(created_at DESC);

COMMENT ON TABLE training_exports IS 
  'P2.2导出执行记录。跟踪每次导出任务的状态、数据量、输出路径、执行时间等。隐私保证：不记录原始数据，只记录统计和路径。';

-- ============================================================================
-- 3. 默认导出配置（示例）
-- ============================================================================

-- 配置1: 平衡模式 + 高质量数据（用于训练）
INSERT INTO training_export_configs (
  name, description, feature_version, 
  time_range_start, time_range_end, 
  quality_filters, dedup_strategy
) VALUES (
  'balanced-high-quality-v1',
  '平衡模式高质量数据（置信度≥0.7，已结算，reward≥0.5），用于模型训练',
  'v1',
  '2026-08-01',
  '2026-09-01',
  '{
    "min_confidence": 0.7,
    "exclude_explore": true,
    "require_settled": true,
    "min_reward": 0.5,
    "allowed_profiles": ["balanced"]
  }'::jsonb,
  'content_hash'
) ON CONFLICT (name) DO NOTHING;

-- 配置2: 全量数据（用于分析）
INSERT INTO training_export_configs (
  name, description, feature_version, 
  time_range_start, time_range_end, 
  quality_filters, dedup_strategy
) VALUES (
  'all-data-v1',
  '全量数据（不过滤），用于数据分析和探索',
  'v1',
  '2026-08-01',
  '2026-09-01',
  '{}'::jsonb,
  'content_hash'
) ON CONFLICT (name) DO NOTHING;

-- 配置3: 低置信度样本（用于标注）
INSERT INTO training_export_configs (
  name, description, feature_version, 
  time_range_start, time_range_end, 
  quality_filters, dedup_strategy
) VALUES (
  'low-confidence-v1',
  '低置信度样本（<0.7），用于人工标注优先级排序',
  'v1',
  '2026-08-01',
  '2026-09-01',
  '{
    "max_confidence": 0.7
  }'::jsonb,
  'content_hash'
) ON CONFLICT (name) DO NOTHING;

-- ============================================================================
-- 4. 导出统计视图（便于监控）
-- ============================================================================

CREATE OR REPLACE VIEW training_export_stats AS
SELECT 
  c.name AS config_name,
  c.feature_version,
  COUNT(*) AS export_count,
  COUNT(*) FILTER (WHERE e.status = 'completed') AS completed_count,
  COUNT(*) FILTER (WHERE e.status = 'failed') AS failed_count,
  COUNT(*) FILTER (WHERE e.status = 'running') AS running_count,
  SUM(e.row_count_deduped) FILTER (WHERE e.status = 'completed') AS total_rows_exported,
  SUM(e.file_size_bytes) FILTER (WHERE e.status = 'completed') AS total_bytes_exported,
  AVG(e.duration_seconds) FILTER (WHERE e.status = 'completed') AS avg_duration_seconds,
  MAX(e.created_at) AS last_export_at
FROM training_export_configs c
LEFT JOIN training_exports e ON e.config_id = c.id
GROUP BY c.id, c.name, c.feature_version
ORDER BY last_export_at DESC NULLS LAST;

COMMENT ON VIEW training_export_stats IS 
  'P2.2导出统计汇总。监控各配置的导出次数、成功率、数据量、耗时等。';

-- ============================================================================
-- 5. 隐私合规注释
-- ============================================================================

-- PRIVACY COMPLIANCE NOTES:
--
-- 1. 导出配置（training_export_configs）:
--    - ✅ 只定义结构化特征的导出范围（时间、版本、质量过滤）
--    - ✅ 不存储任何prompt/messages/response内容
--    - ✅ quality_filters只包含结构化字段的过滤条件
--
-- 2. 导出执行记录（training_exports）:
--    - ✅ 只记录统计数据（行数、文件大小、耗时）
--    - ✅ output_path只是文件路径，不包含数据内容
--    - ✅ error_message只记录系统错误，不包含用户数据
--
-- 3. 导出文件（Parquet/CSV）:
--    - ✅ 由TrainingExporter生成，只包含15个结构化特征字段
--    - ✅ 不包含prompt、messages、response、summary、keywords字段
--    - ✅ 必须通过隐私测试才能导出（TestTrainingExporterNoContentLeakage）
--
-- 4. 审计追踪:
--    - ✅ created_by记录操作人员
--    - ✅ triggered_by记录触发来源
--    - ✅ export_metadata记录导出上下文（实验ID、团队等）

-- Migration完成标记
-- Version: 663
-- Author: AUTO Route Team
-- Date: 2026-09-06
