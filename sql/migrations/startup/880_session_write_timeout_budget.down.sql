-- Down migration for 880: restore the previous write-timeout budget.
--
-- The up migration raises a persisted budget that was BELOW 2000 and inserts
-- 2000 when the key is absent. The down migration therefore has two cases:
--   · key was absent before  ⇒ remove the row this migration inserted.
--   · key existed with a lower value ⇒ put that value back.
--
-- `settings_kv` keeps `prev_value` / `prev_updated_at` for exactly this
-- purpose (see 022_settings_kv.sql), so the restore is data-driven rather
-- than a guess. When there is no `prev_value` (the row was inserted by 880),
-- the row is deleted.
--
-- ⚠ This restores the *broken* 500ms budget on purpose: it is the inverse of
-- the up migration, and the defect it re-introduces is documented in
-- docs/audit/2026-10-20-request-logs-session-turns-复核轮.md §2.4.

BEGIN;

DO $down_880$
DECLARE
    v_prev TEXT;
BEGIN
    SELECT value #>> '{}' INTO v_prev
      FROM settings_kv
     WHERE key = 'sessions_v2.write_timeout_ms'
       AND prev_value IS NOT NULL;

    IF FOUND THEN
        UPDATE settings_kv
           SET value       = prev_value,
               value_type  = 'int',
               updated_by  = 'migration-880-down',
               updated_at  = now(),
               prev_value  = NULL,
               prev_updated_at = now()
         WHERE key = 'sessions_v2.write_timeout_ms';
    ELSE
        DELETE FROM settings_kv WHERE key = 'sessions_v2.write_timeout_ms';
    END IF;
END
$down_880$;

COMMIT;
