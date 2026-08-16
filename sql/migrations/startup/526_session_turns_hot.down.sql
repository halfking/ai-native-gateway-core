-- Migration 526 down: remove the independent session_turns hot path.
-- Fail closed: the hot table is locked and must be empty. Promote or otherwise
-- preserve every row before retrying this rollback.

BEGIN;

DO $$
DECLARE
    v_hot_rows BIGINT;
BEGIN
    IF to_regclass('public.session_turns_hot') IS NOT NULL THEN
        LOCK TABLE public.session_turns_hot IN ACCESS EXCLUSIVE MODE;
        SELECT count(*) INTO v_hot_rows FROM public.session_turns_hot;
        IF v_hot_rows <> 0 THEN
            RAISE EXCEPTION 'Migration 526 down refused: public.session_turns_hot contains % rows',
                v_hot_rows;
        END IF;
    END IF;
END $$;

DROP VIEW IF EXISTS public.session_turns_with_current_month;
DROP FUNCTION IF EXISTS public.promote_session_turns_hot_to_partition(INTERVAL, INTEGER);
DROP FUNCTION IF EXISTS public.session_turns_advisory_lock_key(TEXT, TEXT);
DROP TABLE IF EXISTS public.session_turns_hot;

DROP POLICY IF EXISTS session_turns_owner_filter ON public.session_turns;
CREATE POLICY session_turns_owner_filter ON public.session_turns
    AS RESTRICTIVE
    FOR ALL
    TO PUBLIC
    USING (
        EXISTS (
            SELECT 1
            FROM (
                SELECT DISTINCT ON (gw_session_id) gw_session_id, owner_user
                FROM public.request_logs
                WHERE gw_session_id IS NOT NULL
                ORDER BY gw_session_id, ts ASC
            ) first_rl
            WHERE first_rl.gw_session_id = public.session_turns.session_id
              AND (
                  first_rl.owner_user = current_setting('app.current_user', true)
                  OR current_setting('app.current_role', true) = 'super_admin'
                  OR current_setting('app.bypass_rls', true) = 'true'
              )
        )
    );

ALTER TABLE public.session_turns
    DROP CONSTRAINT IF EXISTS session_turns_submit_mode_check;
ALTER TABLE public.session_turns
    ADD CONSTRAINT session_turns_submit_mode_check
        CHECK (submit_mode IN ('full', 'delta', 'snapshot', 'inferred_compressed', 'attachment_only'));

COMMIT;
