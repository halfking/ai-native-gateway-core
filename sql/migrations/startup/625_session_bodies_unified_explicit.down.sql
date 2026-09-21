-- Rollback for migration 625: restore the SELECT-* style view that
-- migration 614 originally created. Five Go readers (unified_detail,
-- session_turns_v2, session_detail_v2, session_summary_v2, and
-- bodies_writer) all depend on this view existing, so we cannot leave
-- it absent — that would surface as 500s on every admin detail /
-- summary request. The restored view is byte-equivalent to the one
-- 614 created: SELECT * from both stores, hot first.
--
-- Why not DROP: rolling back 625 after 614 has already been applied
-- historically used SELECT *, and downstream readers expect the union
-- to cover hot + partition. If 614 itself is rolled back separately,
-- the `public.session_bodies_hot` table will be absent and this
-- recreate will fail at parse time — that is intentional, because the
-- downgrade path is then incomplete and must be done as a unit.

CREATE OR REPLACE VIEW public.session_bodies_unified AS
SELECT * FROM public.session_bodies_hot
UNION ALL
SELECT * FROM public.session_bodies
WHERE partition_date <= CURRENT_DATE - INTERVAL '1 day';

-- Restore migration 614's original view options as well as its SELECT *
-- definition. CREATE OR REPLACE preserves reloptions, so an explicit RESET
-- is needed to undo 625's security_invoker=true attribute.
ALTER VIEW public.session_bodies_unified RESET (security_invoker);
