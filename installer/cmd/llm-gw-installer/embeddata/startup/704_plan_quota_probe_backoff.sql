-- Migration 704: plan-probe failure backoff stamp for balance_floor_guard.
--
-- Feature (2026-09-14, audit R28 #12a): bg/balance_floor_guard.go 的套餐
-- （zhipu/minimax plan quota）探测失败时 fail-open 保留旧状态，但失败的行
-- 仍会在每个 5 分钟 sweep 周期被再次探测 —— 套餐端点长期故障时就是稳定的
-- 2min/5min 节奏轰炸（与 f8322dc04 R4 / A-P1-1 同形的死上游轰炸面）。
--
-- plan_quota_probe_failed_at 记录最近一次探测失败时刻：
--   - 失败分支（handlePlanCredential error 路径）写 now()；
--   - 扫描 SQL 凭它把失败行冷却 15 分钟（COALESCE(failed_at, to_timestamp(0))
--     < now() - interval '15 minutes' 才入选），并以
--     COALESCE(failed_at, checked_at) 排序让到期行沉底优先重试；
--   - 成功探测（persistPlanState）置 NULL，退避只作用于连续失败。
--   - 该列绝不参与 plan_quota_checked_at 的新鲜度语义：checked_at 的陈旧度
--     是 #4 逃生门（LLM_GATEWAY_BALANCE_FLOOR_ESCAPE_HOURS）的判据，两者
--     必须独立。
--
-- Compatibility: 单列 ADD COLUMN IF NOT EXISTS，对既有行零回填、零锁风险
-- （credentials 热表，与 631/701 同样的元数据级变更）。配套
-- 704_plan_quota_probe_backoff.down.sql 仅 DROP 该列（账本行保留，
-- append-only，与 703 down 同惯例）。

BEGIN;

ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS plan_quota_probe_failed_at timestamp with time zone;

-- 双账本自登记(695/701/703 定式,2026-09-14 审计):升级通道库此前只落
-- gateway_db_revision_sequences :704 标记、无 schema_migrations 行,账本对账
-- 审机会误报漂移。幂等:重跑安全。
INSERT INTO public.schema_migrations (version, description)
VALUES ('704', 'plan quota probe failure backoff stamp (credentials.plan_quota_probe_failed_at)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
