-- ===========================================================================
-- File:          sql/migrations/startup/606_session_summaries_agent_expert_tags.sql
-- Migration:     606
-- Database:      llm_gateway
-- Purpose:       session_summaries 增加 agent_type / expert_type / tags 三列，
--                承载会话总结 LLM 输出中的智能体类型、专家类型与标签
--
-- Status:        active
-- Idempotent:    YES (ADD COLUMN IF NOT EXISTS + 单事务)
-- Dependencies:  358 (title/summary/key_topics/user_intent 基线)
--
-- Background:
--   会话总结 LLM prompt 此前只含对话消息，输出 {title,summary,key_topics,
--   user_intent}；现在 prompt 增加系统提示词前缀节选，输出 JSON 扩展出
--   agent_type（cursor/zcode/opencode/...）、expert_type（software_engineering/
--   security/...）与 tags，需要一个持久化落点。
--
-- View considerations (rule 49 §9.2):
--   - session_summaries 无任何挂载 VIEW（2026-08-26 grep sql/ 确认），
--     ADD COLUMN 无需配对 view 重建。
--   - 非分区表（非 rule 33 管辖范围）。
--
-- Affected Rows: 0 (纯 ADD COLUMN 带默认值，PG11+ 快速路径不重写表)
-- Breaking Change: NO (新列带 DEFAULT，既有写入路径不引用即兼容)
-- Rollback Script: 606_session_summaries_agent_expert_tags.down.sql
--
-- Date: 2026-08-26
-- Author: OpenCode
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;

ALTER TABLE public.session_summaries
    ADD COLUMN IF NOT EXISTS agent_type  TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS expert_type TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tags        TEXT[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN public.session_summaries.agent_type  IS 'canonical agent/client name (cursor|zcode|opencode|claude-code|...)';
COMMENT ON COLUMN public.session_summaries.expert_type IS 'expert specialty (software_engineering|security|data_science|...)';
COMMENT ON COLUMN public.session_summaries.tags        IS 'LLM-extracted session tags (normalized, capped)';

COMMIT;
