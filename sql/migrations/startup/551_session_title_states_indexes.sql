-- Migration 551: title state constraints and lookup indexes.

BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.session_title_states'::regclass
          AND conname = 'session_title_states_token_nonnegative'
    ) THEN
        ALTER TABLE public.session_title_states
            ADD CONSTRAINT session_title_states_token_nonnegative
            CHECK (fencing_token >= 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.session_title_states'::regclass
          AND conname = 'session_title_states_priority_nonnegative'
    ) THEN
        ALTER TABLE public.session_title_states
            ADD CONSTRAINT session_title_states_priority_nonnegative
            CHECK (source_priority >= 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.session_title_states'::regclass
          AND conname = 'session_title_states_deleted_consistency'
    ) THEN
        ALTER TABLE public.session_title_states
            ADD CONSTRAINT session_title_states_deleted_consistency
            CHECK ((deleted AND deleted_at IS NOT NULL) OR NOT deleted);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_session_title_states_active
    ON public.session_title_states (tenant_id, scoped_session_id)
    WHERE deleted = false AND title IS NOT NULL AND title <> '';
CREATE INDEX IF NOT EXISTS idx_session_title_states_tombstone
    ON public.session_title_states (tenant_id, updated_at DESC)
    WHERE deleted = true;
CREATE INDEX IF NOT EXISTS idx_session_title_states_lease
    ON public.session_title_states (lease_expires_at)
    WHERE lease_expires_at IS NOT NULL;

COMMIT;
