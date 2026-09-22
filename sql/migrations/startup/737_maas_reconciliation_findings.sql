-- 737 (Wave 3 B8, 2026-09-22): 内部对账 job 差异落表 — maas_reconciliation_findings。
--
-- 设计差距（§3 B8）：usage_ledger↔credit_ledger 只有网关↔供应商侧对账
-- （providerprofile.reconciliation），内部两本账的一致性无人校验。
-- bg.LedgerReconciler 周期校验两类差异并落本表 + 告警计数：
--   1. balance_after 链完整性：同租户 credit_ledger 按时间链式回放，
--      balance_after[n] != balance_after[n-1] + amount[n] 即断链；
--   2. usage↔credit 一致性：usage_ledger.credits_charged 汇总与
--      credit_ledger 中 ref_type='request' 的扣减额按 request_id 对拍。
--
-- 幂等：CREATE TABLE IF NOT EXISTS，重跑 no-op。清理策略：由 job 自身
-- 删除 window 外的旧 findings（表小，不进分区族）。

CREATE TABLE IF NOT EXISTS maas_reconciliation_findings (
    id          BIGSERIAL PRIMARY KEY,
    run_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    check_kind  TEXT NOT NULL, -- 'balance_chain' | 'usage_credit_mismatch'
    tenant_id   TEXT NOT NULL DEFAULT '',
    ref_id      TEXT NOT NULL DEFAULT '',
    expected    NUMERIC,
    actual      NUMERIC,
    detail      JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_maas_recon_findings_run
    ON maas_reconciliation_findings (run_at DESC);

COMMENT ON TABLE maas_reconciliation_findings IS
'737: usage_ledger<->credit_ledger internal reconciliation findings (bg.LedgerReconciler).';
