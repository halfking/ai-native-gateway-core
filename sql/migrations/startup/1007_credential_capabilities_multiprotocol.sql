-- Migration 1007: expand credential_model_capabilities CHECK to support
-- multi-protocol capability keys (A5 供应商协议自适应配套).
--
-- 背景：migration 613 把 capability 限定为两个 Responses 族值
-- ('native_responses_nonstream', 'native_responses_stream')。自适应协议
-- 引入后，需要按协议维度存储能力证据（anthropic-messages / gemini-generate
-- / ollama-native 等各有独立的能力位）。
--
-- 本迁移扩展 CHECK 约束为「协议前缀 + 下划线 + 能力类型」的通用模式，
-- 向后兼容既有两值。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE public.credential_model_capabilities
    DROP CONSTRAINT IF EXISTS credential_model_capabilities_capability_check;

-- 新 CHECK：{protocol_prefix}_{capability_type}
-- 协议前缀：小写字母、数字、连字符（与 provider_catalog.Protocols 口径一致）
-- 能力类型：nonstream | stream（当前两种，扩展时在此枚举追加）
--
-- 示例合法值：
--   native_responses_nonstream  （既有，向后兼容）
--   native_responses_stream     （既有，向后兼容）
--   anthropic_messages_nonstream
--   gemini_generate_stream
--   ollama_native_nonstream
ALTER TABLE public.credential_model_capabilities
    ADD CONSTRAINT credential_model_capabilities_capability_check
    CHECK (capability ~ '^[a-z0-9_-]+_(nonstream|stream)$');

COMMENT ON CONSTRAINT credential_model_capabilities_capability_check
    ON public.credential_model_capabilities IS
    'Multi-protocol capability key pattern: {protocol}_{nonstream|stream}. Migration 1007 (A5).';

COMMIT;
