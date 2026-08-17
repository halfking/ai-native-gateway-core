package db

import (
	"context"
	"fmt"
	"log/slog"
)

// ensureHandoffLogsHotColumnarSchema validates the contract installed by
// startup migration 532. Destructive legacy conversion belongs to the
// versioned migration; startup self-healing only restores safe, idempotent
// definitions and fails closed when the database is in an ambiguous state.
func (d *DB) ensureHandoffLogsHotColumnarSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	const contract = `
DO $do$
DECLARE
	parent_kind "char";
	hot_kind "char";
	partitioned boolean;
	view_exists boolean;
BEGIN
	SELECT c.relkind INTO parent_kind
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname='public' AND c.relname='handoff_logs';
	SELECT c.relkind INTO hot_kind
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	WHERE n.nspname='public' AND c.relname='handoff_logs_hot';
	SELECT EXISTS(
		SELECT 1 FROM pg_partitioned_table
		WHERE partrelid = to_regclass('public.handoff_logs')
	) INTO partitioned;
	SELECT to_regclass('public.handoff_logs_with_current_month') IS NOT NULL
	INTO view_exists;

	IF parent_kind IS DISTINCT FROM 'p' OR NOT partitioned THEN
		RAISE EXCEPTION 'handoff_logs schema contract requires a RANGE partitioned parent; run startup migration 532';
	END IF;
	IF hot_kind IS DISTINCT FROM 'r' THEN
		RAISE EXCEPTION 'handoff_logs_hot schema contract is missing or not a heap table; run startup migration 532';
	END IF;
	IF NOT view_exists THEN
		RAISE EXCEPTION 'handoff_logs_with_current_month is missing; run startup migration 532';
	END IF;
	IF to_regprocedure('public.ensure_handoff_logs_partition(timestamptz)') IS NULL
	   OR to_regprocedure('public.promote_handoff_logs_hot_to_partition(interval,integer)') IS NULL THEN
		RAISE EXCEPTION 'handoff partition functions are missing; run startup migration 532';
	END IF;
END
$do$;`

	if _, err := d.pool.Exec(ctx, contract); err != nil {
		return fmt.Errorf("handoff hot+columnar schema contract: %w", err)
	}

	if _, err := d.pool.Exec(ctx, `
		INSERT INTO public.settings_kv (key, value, value_type, scope, category, updated_at, updated_by)
		VALUES ('lifecycle.handoff_logs_hot_retention_hours', '8', 'int', 'platform', 'lifecycle', now(), 'startup-self-heal-532')
		ON CONFLICT (key) DO NOTHING`); err != nil {
		return fmt.Errorf("handoff hot retention setting: %w", err)
	}

	slog.Debug("handoff hot+columnar schema contract validated")
	return nil
}
