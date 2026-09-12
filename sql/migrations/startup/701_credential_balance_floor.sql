-- Migration 701: per-credential balance floors + subscription-plan quota sensing.
--
-- Feature (2026-09-13): 自动感知原厂凭据余额并在打到下限前把它从路由池摘出，
-- 避免上游账号被真实扣到 0 而进入风控/停用。分两种额度语义：
--
-- 1) 货币余额下限（balance_floor_usd）
--    适用有公开余额 API 的厂商（openai/deepseek/siliconflow，经 providercap）。
--    bg/balance_floor_guard.go 在 balance_usd <= balance_floor_usd 时写
--    quota_state='balance_exhausted'（state_reason_code='balance_floor'），
--    v_routable_credential_models 与候选 SQL 天然排除该状态 = 摘出池子；
--    充值后余额 >= floor*1.1（滞回）自动恢复。绝不写 manual_disabled。
--
-- 2) 订阅套餐下限（quota_floor_tokens / quota_floor_percent）
--    zhipu GLM:  GET {origin}/api/monitor/usage/quota/limit
--                limits[] 携带绝对 remaining（TOKENS_LIMIT 为 token 数）
--                与 percentage（已用百分比，方向=已用）。
--    MiniMax:    GET {origin}/v1/api/openplatform/coding_plan/remains
--                （fallback /v1/token_plan/remains）只有剩余百分比，
--                需 100-x 反转成已用。
--    quota_floor_tokens:  剩余 token <= floor 时摘出（仅 TOKENS_LIMIT 可判，
--                         credit 计价的套餐请用百分比下限）。
--    quota_floor_percent: 任一窗口已用 >= floor 时摘出（订阅窗口会自动重置，
--                         等价于"留 buffer 不打满"）。
--
-- plan_quota_* 是探测结果的落库展示列，由 balance_floor_guard 周期刷新。
-- 所有 floor 列 NULL = 该凭据不启用对应下限（默认全关，worker 空转）。
--
-- Compatibility: 纯 ADD COLUMN IF NOT EXISTS，对既有行零回填、零锁风险
-- （credentials 是热表，与 631 同样的元数据级变更）。

BEGIN;

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS balance_floor_usd numeric(14,6);

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS quota_floor_tokens bigint;

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS quota_floor_percent numeric(5,2);

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS plan_quota_kind text;

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS plan_quota_windows jsonb;

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS plan_quota_remaining_tokens bigint;

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS plan_quota_used_percent numeric(5,2);

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS plan_quota_checked_at timestamp with time zone;

COMMIT;
