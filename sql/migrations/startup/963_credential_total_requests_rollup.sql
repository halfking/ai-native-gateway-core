-- ===========================================================================
-- File:          963_credential_total_requests_rollup.sql
-- Migration:     963
-- Purpose:       /api/credentials/monitor-summary 的 total_requests **预物化**
--                （2026-11-13 24h 审计轮，第五轮主档 §八第 12 条路径③兑现）。
--
--                ★ 背景（第五轮实测留痕）：端点对每个凭据跑一次
--                  `session_turns_hot ∪ session_turns` 两臂 COUNT——
--                  252 库母表 1.4M 行 / 7 分区，热凭据单次计数 ~12 s
--                  （Bitmap Heap Scan 要回表十几万行——「要数完」，不是
--                  「找不到」）⇒ 74 凭据 × 12 s 必越 15 s context ⇒
--                  245/154 恒 500。任何同口径实时改法都过不了
--                  （GROUP BY 聚合形态实测 23.4 s）。
--
--                ★ 本表 = 后台 job（bg.CredentialRollupRefresher）周期性把
--                  「全凭据 × 两臂计数」一次聚合写入（单语句原子：
--                  空表 → 全量行一次可见，无半刷新态）。读面 O(1)：
--                  CASE WHEN 本表非空 THEN 读本表 ELSE 回落旧两臂实时计数
--                  END——**口径零变化**（同一数字定义，仅新鲜度受刷新间隔
--                  约束；回落分支保证「job 尚未首跑/失败」时端点行为与
--                  历史完全一致，包括 brief 500 窗口的自愈语义）。
--
--                ★ 写入方只有 bg.CredentialRollupRefresher（间隔
--                  LLM_GATEWAY_CRED_ROLLUP_INTERVAL，默认 30m；2026-11-13
--                  252 真库实测：全凭据聚合形态 17.3 s 一次）。
--                ★ 空表 ≠ 故障：读面回落实时计数（与 961「空表=job 未跑」
--                  同纪律，但这里回落是有语义的回退而不是沉默）。
--
-- Down: 963_credential_total_requests_rollup.down.sql
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS public.credential_total_requests_rollup (
    credential_id  bigint PRIMARY KEY,
    total_requests bigint NOT NULL,
    refreshed_at   timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE public.credential_total_requests_rollup IS
    'Pre-materialized per-credential all-time request counts (963, 2026-11-13 audit round) for /api/credentials/monitor-summary. Writer = bg.CredentialRollupRefresher only (single-statement atomic upsert over session_turns_hot UNION session_turns, both arms counting db.SessionFamilyCredentialIDExpr). Read face (admin/credential_monitor.go) reads this table when non-empty and falls back to the legacy per-credential live COUNT when empty — zero semantic change, freshness bounded by the refresh interval. Empty table = job has not run yet (fresh install / pre-first-refresh), not a fault.';
COMMENT ON COLUMN public.credential_total_requests_rollup.total_requests IS
    'All-time request count for the credential across both storage arms (hot ∪ mother). Same definition as the legacy live COUNT — identical number, potentially stale by up to the refresher interval.';

-- ── 记账（≥952 自登记纪律，门 TestMigrationRegistryStaysContinuous / 自登记门）──
INSERT INTO public.schema_migrations (version, description)
VALUES ('963', 'credential_total_requests_rollup: pre-materialized per-credential all-time request counts for /api/credentials/monitor-summary (245/154 500 root fix, path 3 of round-5 finding #12; writer = bg.CredentialRollupRefresher, read face falls back to legacy live count when table is empty)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
