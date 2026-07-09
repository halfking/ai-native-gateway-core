-- Migration 362 Down: Revert intent_classification_feedback table creation
-- Date: 2026-07-09

-- 删除触发器
DROP TRIGGER IF EXISTS trigger_intent_feedback_correctness ON intent_classification_feedback;

-- 删除函数
DROP FUNCTION IF EXISTS update_intent_feedback_correctness();

-- 删除索引
DROP INDEX IF EXISTS idx_feedback_annotated;
DROP INDEX IF EXISTS idx_feedback_tenant_intent;
DROP INDEX IF EXISTS idx_feedback_session;

-- 删除表
DROP TABLE IF EXISTS intent_classification_feedback CASCADE;
