-- Migration 356: Session Health Score Columns
-- 2026-07-06: 为 session_summaries 增加健康评分、等级、结果分类列
-- Ref: docs/session-management-analytics-plan.md 第 4.4 节

-- 1. 增加健康评分相关列
ALTER TABLE session_summaries
    ADD COLUMN IF NOT EXISTS health_score INT,
    ADD COLUMN IF NOT EXISTS health_grade CHAR(1),
    ADD COLUMN IF NOT EXISTS outcome VARCHAR(20),
    ADD COLUMN IF NOT EXISTS last_health_at TIMESTAMPTZ;

-- 2. 列注释
COMMENT ON COLUMN session_summaries.health_score IS '会话健康评分（0-100），100 起扣 penalty 模型';
COMMENT ON COLUMN session_summaries.health_grade IS '健康等级（A-F）：A=90-100, B=75-89, C=60-74, D=40-59, F=0-39';
COMMENT ON COLUMN session_summaries.outcome IS '会话结果分类：completed（正常完成）/error（错误主导）/abandoned（被放弃）/unknown';
COMMENT ON COLUMN session_summaries.last_health_at IS '上次健康分计算时间（用于版本追踪与重算判定）';

-- 3. 约束（可选，保证数据质量）
ALTER TABLE session_summaries
    ADD CONSTRAINT chk_health_score_range CHECK (health_score IS NULL OR (health_score >= 0 AND health_score <= 100)),
    ADD CONSTRAINT chk_health_grade_enum CHECK (health_grade IS NULL OR health_grade IN ('A', 'B', 'C', 'D', 'F')),
    ADD CONSTRAINT chk_outcome_enum CHECK (outcome IS NULL OR outcome IN ('completed', 'error', 'abandoned', 'unknown'));
