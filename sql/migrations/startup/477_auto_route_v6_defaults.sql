-- 477_auto_route_v6_defaults.sql
-- Phase: Auto 智能路由 — V6 默认路由矩阵 seed
--
-- 背景：task_default_routing 表自创建（388/421/431 migration）以来没有任何
-- seed 数据，生产路由完全依赖运营通过 admin API 手填，代码库无法审计。
-- V5 矩阵仅存在于 autoroute/v4_routing_matrix_test.go 单元测试。本 migration
-- 首次为该表写入权威 seed（V6 矩阵），让新环境开箱即用。
--
-- V6 相对 V5 的修正（见 auto 路由审计报告）：
--   1. long_context/smart: gemini-1.5-pro(陈旧) → claude-sonnet-5(高智商)
--   2. reasoning/speed_first: 新增 o5-preview 槽（V5 缺失）
--   3. intent_classification: 从误判到 chat/claude-sonnet-5(高成本) →
--      gemini-2.0-flash-exp(成本低、速度快、不过时)
--   4. 新增 planning 任务（编写计划/方案/任务拆解，用高智商模型）
--   5. fallback 全面替换陈旧模型：qwen-max→qwen3-235b, qwen-turbo→deepseek-chat
--   6. 所有模型均为 models_canonical 真实 seed 行（无虚构/幽灵模型）
--
-- Idempotent: ON CONFLICT DO NOTHING — 不覆盖运营已手填的行。
-- 幂等性说明：uq_task_default_routing 唯一索引含 COALESCE(tenant_id,0) 表达式，
-- 故使用不带冲突目标的 ON CONFLICT DO NOTHING（兼容表达式索引）。
--
-- Rollback: DELETE FROM task_default_routing WHERE reason LIKE 'v6 default%';

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO task_default_routing (task_type, profile, tier, canonical_model, priority, reason)
VALUES
    -- ── chat ───────────────────────────────────────────────────────
    ('chat', 'smart',       'primary', 'claude-sonnet-5',      100, 'v6 default'),
    ('chat', 'speed_first', 'primary', 'gemini-2.0-flash-exp', 100, 'v6 default'),
    ('chat', 'cost_first',  'primary', 'deepseek-chat',        100, 'v6 default'),
    -- ── reasoning ──────────────────────────────────────────────────
    ('reasoning', 'smart',       'primary', 'claude-fable-5',  100, 'v6 default'),
    ('reasoning', 'speed_first', 'primary', 'o5-preview',      100, 'v6 default'),
    ('reasoning', 'cost_first',  'primary', 'qwq-32b-preview', 100, 'v6 default'),
    -- ── code ───────────────────────────────────────────────────────
    ('code', 'smart',       'primary', 'claude-sonnet-5', 100, 'v6 default'),
    ('code', 'speed_first', 'primary', 'codestral',       100, 'v6 default'),
    ('code', 'cost_first',  'primary', 'deepseek-coder',  100, 'v6 default'),
    -- ── agent ──────────────────────────────────────────────────────
    ('agent', 'smart',       'primary', 'claude-sonnet-5',      100, 'v6 default'),
    ('agent', 'speed_first', 'primary', 'gemini-2.0-flash-exp', 100, 'v6 default'),
    ('agent', 'cost_first',  'primary', 'deepseek-chat',        100, 'v6 default'),
    -- ── creative ───────────────────────────────────────────────────
    ('creative', 'smart',       'primary', 'claude-opus-4-8',      100, 'v6 default'),
    ('creative', 'speed_first', 'primary', 'gemini-2.0-flash-exp', 100, 'v6 default'),
    ('creative', 'cost_first',  'primary', 'deepseek-chat',        100, 'v6 default'),
    -- ── long_context ───────────────────────────────────────────────
    ('long_context', 'smart',       'primary', 'claude-sonnet-5',  100, 'v6 default'),
    ('long_context', 'speed_first', 'primary', 'moonshot-v1-128k', 100, 'v6 default'),
    ('long_context', 'cost_first',  'primary', 'deepseek-chat',    100, 'v6 default'),
    -- ── vision ─────────────────────────────────────────────────────
    ('vision', 'smart',       'primary', 'claude-sonnet-5',      100, 'v6 default'),
    ('vision', 'speed_first', 'primary', 'gemini-2.0-flash-exp', 100, 'v6 default'),
    ('vision', 'cost_first',  'primary', 'glm-4v-plus',          100, 'v6 default'),
    -- ── function_call ──────────────────────────────────────────────
    ('function_call', 'smart',       'primary', 'claude-sonnet-5',      100, 'v6 default'),
    ('function_call', 'speed_first', 'primary', 'gemini-2.0-flash-exp', 100, 'v6 default'),
    ('function_call', 'cost_first',  'primary', 'deepseek-chat',        100, 'v6 default'),
    -- ── code_audit ─────────────────────────────────────────────────
    ('code_audit', 'smart',       'primary', 'claude-fable-5',   100, 'v6 default'),
    ('code_audit', 'speed_first', 'primary', 'claude-sonnet-5',  100, 'v6 default'),
    ('code_audit', 'cost_first',  'primary', 'deepseek-coder',   100, 'v6 default'),
    -- ── intent_classification（成本低、速度快，不用高成本 claude）─────
    ('intent_classification', 'smart',       'primary', 'gemini-2.0-flash-exp', 100, 'v6 default'),
    ('intent_classification', 'speed_first', 'primary', 'minimax-m3',           100, 'v6 default'),
    ('intent_classification', 'cost_first',  'primary', 'deepseek-chat',        100, 'v6 default'),
    -- ── planning（编写计划/方案/任务拆解，用智商最高的模型）─────────────
    ('planning', 'smart',       'primary', 'claude-opus-4-8',  100, 'v6 default'),
    ('planning', 'speed_first', 'primary', 'claude-sonnet-5',  100, 'v6 default'),
    ('planning', 'cost_first',  'primary', 'qwen3-235b',       100, 'v6 default'),
    -- ── 通用 fallback（profile='' 兜底，替换陈旧 qwen-max/qwen-turbo）──
    ('chat',                  '', 'fallback', 'qwen3-235b',     100, 'v6 default'),
    ('reasoning',             '', 'fallback', 'qwen3-235b',     100, 'v6 default'),
    ('code',                  '', 'fallback', 'deepseek-coder', 100, 'v6 default'),
    ('agent',                 '', 'fallback', 'qwen3-235b',     100, 'v6 default'),
    ('creative',              '', 'fallback', 'qwen3-235b',     100, 'v6 default'),
    ('long_context',          '', 'fallback', 'qwen3-235b',     100, 'v6 default'),
    ('vision',                '', 'fallback', 'qwen3-235b',     100, 'v6 default'),
    ('function_call',         '', 'fallback', 'deepseek-chat',  100, 'v6 default'),
    ('code_audit',            '', 'fallback', 'deepseek-coder', 100, 'v6 default'),
    ('intent_classification', '', 'fallback', 'deepseek-chat',  100, 'v6 default'),
    ('planning',              '', 'fallback', 'qwen3-235b',     100, 'v6 default')
ON CONFLICT DO NOTHING;

COMMIT;
