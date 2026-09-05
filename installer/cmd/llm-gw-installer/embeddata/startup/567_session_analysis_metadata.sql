-- 567_session_analysis_metadata.sql
-- Persist sessionmeta.Result at arrival (status=provisional) and final worker (status=final).
-- Idempotent: guarded by information_schema.

BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM information_schema.tables
         WHERE table_schema = 'public'
           AND table_name = 'session_analysis_metadata'
    ) THEN
        CREATE TABLE public.session_analysis_metadata (
            tenant_id           text NOT NULL,
            scoped_session_id   text NOT NULL,
            schema_version      text NOT NULL DEFAULT 'session-analysis/v1',
            status              text NOT NULL CHECK (status IN ('provisional', 'final')),
            input_hash          text NOT NULL,
            payload             jsonb NOT NULL,
            source_task_id      text,
            created_at          timestamptz NOT NULL DEFAULT now(),
            updated_at          timestamptz NOT NULL DEFAULT now(),
            PRIMARY KEY (tenant_id, scoped_session_id, status)
        );
    END IF;
END $$;

COMMENT ON TABLE public.session_analysis_metadata IS
    'Session analysis metadata (provisional at arrival, final from LLM worker). One row per (tenant, session, status).';

CREATE INDEX IF NOT EXISTS idx_session_analysis_metadata_tenant_updated
    ON public.session_analysis_metadata (tenant_id, updated_at DESC);

COMMIT;
