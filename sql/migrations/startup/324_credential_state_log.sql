-- 324_credential_state_log.sql — Create credential_state_log table for Phase 2.x state writes
--
-- Purpose: Stop "batch writer: write failed ... relation \"credential_state_log\"
-- does not exist (SQLSTATE 42P01)" warnings on every state update from
-- domains/credentialstate/batch_writer.go.
--
-- Background:
--   - domains/credentialstate was wired in 2026-06-28 as Phase 2.x of the
--     state manager refactor (commit 0d5aec70). The Go BatchWriter.flush
--     method INSERTs into credential_state_log, but no migration created
--     the table. On 184 the writer has been failing every flush since
--     deploy, leaving state writes silently lost (operator-visible only
--     via the WARN log line).
--   - The intent of this table mirrors the application-level StateUpdate
--     struct (domains/credentialstate/state.go:StateUpdate) and the
--     INSERT statement at batch_writer.go:103-117. We keep the schema
--     faithful to that contract so the existing code works unchanged.
--
-- Design:
--   1. PRIMARY KEY (credential_id, raw_model_name) matches the ON CONFLICT
--      clause in batch_writer.go:109. UPSERT semantics: keep last-write
--      values for any column not explicitly overwritten.
--   2. updated_at uses now() so concurrent writers don't race the timestamp.
--   3. last_success_at / last_failure_at / last_error / recover_at are
--      nullable because the code sends NULLs when the event didn't fire.
--   4. available / health_status / latency_ms are nullable for the same
--      reason — and the ON CONFLICT clause uses COALESCE(EXCLUDED.x, …)
--      to preserve a previous non-null value when the new event didn't
--      touch it.
--   5. NO RLS: state writes come from a system background goroutine that
--      doesn't carry a tenant context (the state is global by design).
--      If RLS becomes required, switch writers to use withTenantTx and
--      add tenant_isolation policy on tenant_id (default 'default').
--
-- Idempotent: CREATE TABLE IF NOT EXISTS + CREATE INDEX IF NOT EXISTS.

BEGIN;

CREATE TABLE IF NOT EXISTS public.credential_state_log (
    credential_id     INTEGER      NOT NULL,
    raw_model_name    TEXT         NOT NULL,
    available         BOOLEAN,
    health_status     TEXT,
    latency_ms        INTEGER,
    last_success_at   TIMESTAMPTZ,
    last_failure_at   TIMESTAMPTZ,
    last_error        TEXT,
    recover_at        TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_id, raw_model_name)
);

CREATE INDEX IF NOT EXISTS idx_credential_state_log_updated_at
    ON public.credential_state_log (updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_credential_state_log_credential_id
    ON public.credential_state_log (credential_id);

COMMENT ON TABLE public.credential_state_log IS
    'Real-time per-(credential, model) state snapshots written by domains/credentialstate/batch_writer. Mirrors application-level StateUpdate struct. UPSERT by (credential_id, raw_model_name).';
COMMENT ON COLUMN public.credential_state_log.available IS
    'Last observed availability flag (true=serving, false=broken/disabled). NULL when the event did not report availability.';
COMMENT ON COLUMN public.credential_state_log.health_status IS
    'Last observed health status (healthy|warning|degraded|unreachable). NULL when not reported.';
COMMENT ON COLUMN public.credential_state_log.updated_at IS
    'Wall-clock time of the last UPSERT. Defaults to now() so concurrent writers do not race the timestamp.';

COMMIT;