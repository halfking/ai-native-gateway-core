-- Migration 664: Human Annotation Workflow for Training Data
-- Part of: P2.1 - Human Annotation Workflow
-- Purpose: 支持人工标注低置信度AUTO路由样本，提升训练数据质量
--
-- Workflow:
--   1. Export low-confidence samples (confidence < 0.7) via llm-gw-exporter
--   2. Human annotators review and label via CSV or web interface
--   3. Import annotations via llm-gw-annotator
--   4. Use human labels as ground truth for ML training
--
-- Privacy: 标注系统只访问结构化特征，不接触prompt/messages/response
--
-- 2026-09-06

-- ============================================================================
-- 1. 人工标注数据表
-- ============================================================================

CREATE TABLE IF NOT EXISTS training_human_annotations (
  id BIGSERIAL PRIMARY KEY,
  
  -- 关联的请求
  request_id TEXT NOT NULL,
  -- 索引: 快速查找某个请求的标注
  
  -- ML预测结果（待验证）
  auto_label TEXT NOT NULL,           -- ML/规则引擎预测的provider
  auto_confidence FLOAT NOT NULL,     -- 预测置信度（0.0-1.0）
  
  -- 人工标注结果
  human_label TEXT NOT NULL,          -- 人工标注的正确provider
  is_correct BOOLEAN NOT NULL,        -- auto_label == human_label
  
  -- 标注原因和上下文
  annotation_reason TEXT,             -- 标注原因（performance, cost, availability, other）
  -- 示例:
  --   "better_latency": Anthropic在该region延迟更低
  --   "cost_effective": AWS Bedrock成本更低
  --   "availability": OpenAI当时不可用
  --   "quality": Claude 3在该任务上质量更好
  
  annotator TEXT NOT NULL,            -- 标注人员（username或email）
  annotated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  
  -- 额外元数据
  annotation_metadata JSONB,
  -- 示例:
  -- {
  --   "annotation_tool": "csv_v1",
  --   "annotation_session_id": "session-2026-09-06-001",
  --   "model_name": "gpt-4",
  --   "task_type": "chat",
  --   "original_confidence": 0.65
  -- }
  
  -- 时间戳
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 索引: 查找特定请求的标注
CREATE INDEX IF NOT EXISTS idx_training_human_annotations_request_id 
  ON training_human_annotations(request_id);

-- 索引: 按标注人员过滤
CREATE INDEX IF NOT EXISTS idx_training_human_annotations_annotator 
  ON training_human_annotations(annotator);

-- 索引: 按标注时间排序
CREATE INDEX IF NOT EXISTS idx_training_human_annotations_annotated_at 
  ON training_human_annotations(annotated_at DESC);

-- 索引: 查找错误标注（用于分析）
CREATE INDEX IF NOT EXISTS idx_training_human_annotations_is_correct 
  ON training_human_annotations(is_correct)
  WHERE is_correct = false;

COMMENT ON TABLE training_human_annotations IS 
  'P2.1人工标注数据。存储人工审核的AUTO路由样本标注结果，用于验证ML准确率和提供高质量训练标签。';

COMMENT ON COLUMN training_human_annotations.auto_label IS 
  'ML/规则引擎预测的provider（例如: openai, anthropic, aws_bedrock）';

COMMENT ON COLUMN training_human_annotations.human_label IS 
  '人工标注的正确provider。在训练时，human_label优先级高于auto_label。';

COMMENT ON COLUMN training_human_annotations.is_correct IS 
  'ML预测是否正确（auto_label == human_label）。用于统计ML准确率。';

COMMENT ON COLUMN training_human_annotations.annotation_reason IS 
  '标注原因类别: performance, cost, availability, quality, other';

-- ============================================================================
-- 2. 标注统计视图
-- ============================================================================

CREATE OR REPLACE VIEW annotation_stats AS
SELECT 
  COUNT(*) AS total_annotations,
  COUNT(*) FILTER (WHERE is_correct) AS correct_count,
  COUNT(*) FILTER (WHERE NOT is_correct) AS incorrect_count,
  ROUND(AVG(is_correct::int) * 100, 2) AS accuracy_percent,
  COUNT(DISTINCT annotator) AS num_annotators,
  MIN(annotated_at) AS first_annotation_at,
  MAX(annotated_at) AS last_annotation_at
FROM training_human_annotations;

COMMENT ON VIEW annotation_stats IS 
  'P2.1标注统计汇总。显示总标注数、准确率、标注人数等。';

-- ============================================================================
-- 3. 分provider准确率视图
-- ============================================================================

CREATE OR REPLACE VIEW annotation_accuracy_by_provider AS
SELECT 
  auto_label AS provider,
  COUNT(*) AS total_predictions,
  COUNT(*) FILTER (WHERE is_correct) AS correct_predictions,
  COUNT(*) FILTER (WHERE NOT is_correct) AS incorrect_predictions,
  ROUND(AVG(is_correct::int) * 100, 2) AS accuracy_percent,
  ROUND(AVG(auto_confidence)::numeric, 3) AS avg_confidence
FROM training_human_annotations
GROUP BY auto_label
ORDER BY total_predictions DESC;

COMMENT ON VIEW annotation_accuracy_by_provider IS 
  'P2.1分provider准确率。评估ML在不同provider上的预测准确性。';

-- ============================================================================
-- 4. 标注原因分布视图
-- ============================================================================

CREATE OR REPLACE VIEW annotation_reason_distribution AS
SELECT 
  annotation_reason,
  COUNT(*) AS count,
  ROUND(COUNT(*) * 100.0 / SUM(COUNT(*)) OVER (), 2) AS percent
FROM training_human_annotations
WHERE annotation_reason IS NOT NULL
GROUP BY annotation_reason
ORDER BY count DESC;

COMMENT ON VIEW annotation_reason_distribution IS 
  'P2.1标注原因分布。识别ML预测错误的主要原因（性能、成本、可用性等）。';

-- ============================================================================
-- 5. 标注人员统计视图
-- ============================================================================

CREATE OR REPLACE VIEW annotation_stats_by_annotator AS
SELECT 
  annotator,
  COUNT(*) AS total_annotations,
  COUNT(*) FILTER (WHERE is_correct) AS correct_count,
  COUNT(*) FILTER (WHERE NOT is_correct) AS incorrect_count,
  ROUND(AVG(is_correct::int) * 100, 2) AS accuracy_percent,
  MIN(annotated_at) AS first_annotation_at,
  MAX(annotated_at) AS last_annotation_at,
  EXTRACT(EPOCH FROM (MAX(annotated_at) - MIN(annotated_at))) / 3600.0 AS hours_span
FROM training_human_annotations
GROUP BY annotator
ORDER BY total_annotations DESC;

COMMENT ON VIEW annotation_stats_by_annotator IS 
  'P2.1标注人员统计。评估不同标注人员的工作量和标注质量。';

-- ============================================================================
-- 6. 辅助函数: 获取请求的标注历史
-- ============================================================================

CREATE OR REPLACE FUNCTION get_annotation_history(p_request_id TEXT)
RETURNS TABLE(
  annotation_id BIGINT,
  auto_label TEXT,
  auto_confidence FLOAT,
  human_label TEXT,
  is_correct BOOLEAN,
  annotator TEXT,
  annotated_at TIMESTAMPTZ
) AS $$
BEGIN
  RETURN QUERY
  SELECT 
    id,
    a.auto_label,
    a.auto_confidence,
    a.human_label,
    a.is_correct,
    a.annotator,
    a.annotated_at
  FROM training_human_annotations a
  WHERE a.request_id = p_request_id
  ORDER BY a.annotated_at DESC;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION get_annotation_history(TEXT) IS 
  'P2.1获取特定请求的标注历史。支持查看同一请求的多次标注（如果存在）。';

-- ============================================================================
-- 7. 辅助函数: 计算标注一致性（多人标注同一样本）
-- ============================================================================

CREATE OR REPLACE FUNCTION calculate_annotation_agreement()
RETURNS TABLE(
  request_id TEXT,
  num_annotators BIGINT,
  unique_labels BIGINT,
  most_common_label TEXT,
  agreement_rate FLOAT
) AS $$
BEGIN
  RETURN QUERY
  WITH multi_annotated AS (
    SELECT 
      a.request_id,
      COUNT(DISTINCT a.annotator) AS num_annotators,
      COUNT(DISTINCT a.human_label) AS unique_labels,
      MODE() WITHIN GROUP (ORDER BY a.human_label) AS most_common_label,
      COUNT(*) FILTER (WHERE a.human_label = (
        MODE() WITHIN GROUP (ORDER BY a.human_label)
      )) AS agree_count,
      COUNT(*) AS total_count
    FROM training_human_annotations a
    GROUP BY a.request_id
    HAVING COUNT(DISTINCT a.annotator) > 1
  )
  SELECT 
    m.request_id,
    m.num_annotators,
    m.unique_labels,
    m.most_common_label,
    ROUND((m.agree_count::FLOAT / m.total_count)::numeric, 3) AS agreement_rate
  FROM multi_annotated m
  ORDER BY m.num_annotators DESC, m.agreement_rate ASC;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION calculate_annotation_agreement() IS 
  'P2.1计算标注一致性。识别多人标注不一致的样本（可能是困难样本或标注指南需改进）。';

-- ============================================================================
-- 8. 数据完整性约束
-- ============================================================================

-- 约束1: auto_confidence必须在[0, 1]范围内
ALTER TABLE training_human_annotations
  ADD CONSTRAINT chk_auto_confidence_range
  CHECK (auto_confidence >= 0.0 AND auto_confidence <= 1.0);

-- 约束2: auto_label和human_label必须是有效的provider名称
-- （暂不添加外键约束，因为provider列表可能动态变化）
-- 在应用层验证: openai, anthropic, aws_bedrock, google_vertex, azure_openai等

-- 约束3: annotation_reason必须是预定义类别之一（可选，可通过应用层验证）
-- 预定义类别: performance, cost, availability, quality, other

-- ============================================================================
-- 9. 隐私合规注释
-- ============================================================================

-- PRIVACY COMPLIANCE NOTES:
--
-- 1. 标注数据表（training_human_annotations）:
--    - ✅ 只存储request_id引用，不复制prompt/messages/response
--    - ✅ auto_label和human_label是provider名称，无隐私风险
--    - ✅ annotation_metadata只包含结构化特征（model_name, task_type），不包含用户输入
--
-- 2. 标注导出（CSV）:
--    - ✅ 导出时从auto_route_selections JOIN结构化特征，不导出原始内容
--    - ✅ CSV文件只包含15个特征字段 + provider标签
--    - ✅ 标注人员看不到用户的prompt/response
--
-- 3. 审计追踪:
--    - ✅ annotator字段记录标注人员
--    - ✅ annotated_at记录标注时间
--    - ✅ annotation_metadata记录标注工具版本和会话ID
--
-- 4. 访问控制（应用层实现）:
--    - 只有数据团队（data-team role）可以导出标注样本
--    - 只有标注人员（annotator role）可以导入标注结果
--    - 普通用户无法访问标注系统

-- ============================================================================
-- 10. 示例数据（测试用）
-- ============================================================================

-- 插入测试标注数据（可选，用于验证视图）
-- INSERT INTO training_human_annotations (
--   request_id, auto_label, auto_confidence, human_label, is_correct,
--   annotation_reason, annotator
-- ) VALUES
--   ('req_001', 'openai', 0.65, 'openai', true, 'correct', 'alice'),
--   ('req_002', 'anthropic', 0.62, 'aws_bedrock', false, 'cost', 'bob'),
--   ('req_003', 'aws_bedrock', 0.58, 'anthropic', false, 'quality', 'alice'),
--   ('req_004', 'openai', 0.71, 'openai', true, 'correct', 'charlie');

-- Migration完成标记
-- Version: 664
-- Author: AUTO Route Team
-- Date: 2026-09-06
