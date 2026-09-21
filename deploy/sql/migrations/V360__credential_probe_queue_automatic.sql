-- Persist whether a probe queue task is automatic or explicitly manual.
-- Existing rows are manual by default to preserve operator-created and legacy work.
ALTER TABLE public.credential_probe_queue
    ADD COLUMN IF NOT EXISTS automatic BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN public.credential_probe_queue.automatic IS
    'TRUE for scheduler/system-generated probes; FALSE for explicit manual probes. Automatic tasks are eligibility-gated at enqueue, claim, and execution.';

CREATE INDEX IF NOT EXISTS idx_credential_probe_queue_automatic_claim
    ON public.credential_probe_queue (automatic, priority DESC, next_run_at, id)
    WHERE status = 'ready';
