-- Migration 618: durable JournalSnapshot idempotency receipts.
-- Stores only integrity and lease metadata; snapshot bodies remain in existing
-- request journey/body stores.
BEGIN;

CREATE TABLE IF NOT EXISTS public.journal_snapshot_receipts (
    tenant_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    snapshot_version BIGINT NOT NULL,
    payload_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'processing',
    claim_owner TEXT,
    claim_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT journal_snapshot_receipts_identity_uq UNIQUE (tenant_id, request_id, snapshot_version),
    CONSTRAINT journal_snapshot_receipts_version_chk CHECK (snapshot_version > 0),
    CONSTRAINT journal_snapshot_receipts_status_chk CHECK (status IN ('processing', 'completed')),
    CONSTRAINT journal_snapshot_receipts_processing_lease_chk CHECK (
        status <> 'processing' OR (claim_owner IS NOT NULL AND claim_until IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_journal_snapshot_receipts_claim
    ON public.journal_snapshot_receipts (claim_until, updated_at)
    WHERE status = 'processing';
CREATE INDEX IF NOT EXISTS idx_journal_snapshot_receipts_tenant
    ON public.journal_snapshot_receipts (tenant_id, updated_at DESC);

ALTER TABLE public.journal_snapshot_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.journal_snapshot_receipts FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS journal_snapshot_receipts_tenant_isolation ON public.journal_snapshot_receipts;
CREATE POLICY journal_snapshot_receipts_tenant_isolation
    ON public.journal_snapshot_receipts
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT)
    WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS journal_snapshot_receipts_super_admin_bypass ON public.journal_snapshot_receipts;
CREATE POLICY journal_snapshot_receipts_super_admin_bypass
    ON public.journal_snapshot_receipts
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true')
    WITH CHECK (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMIT;
