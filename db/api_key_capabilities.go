package db

import (
	"context"
	_ "embed"
	"fmt"
)

//go:embed migrations/20261125000001_api_keys_capabilities.sql
var apiKeyCapabilitiesSQL string

func (d *DB) ensureAPIKeyCapabilities(ctx context.Context) error {
	const readySQL = `
		SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_attribute a
			JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
			JOIN pg_catalog.pg_index i ON i.indrelid = a.attrelid AND i.indkey[0] = a.attnum
			JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
			JOIN pg_catalog.pg_am am ON am.oid = c.relam
			WHERE a.attrelid = 'public.api_keys'::regclass AND a.attname = 'capabilities'
			  AND NOT a.attisdropped AND a.atttypid = 'jsonb'::regtype AND a.attnotnull
			  AND pg_catalog.pg_get_expr(ad.adbin, ad.adrelid) = $$'[]'::jsonb$$
			  AND i.indexrelid = to_regclass('public.idx_api_keys_capabilities')
			  AND i.indisvalid AND i.indisready AND i.indnkeyatts = 1 AND i.indpred IS NULL
			  AND am.amname = 'gin'
		)`
	var ready bool
	err := d.pool.QueryRow(ctx, readySQL).Scan(&ready)
	if err != nil {
		return fmt.Errorf("inspect API key capabilities schema: %w", err)
	}
	if ready {
		return nil
	}
	if _, err := d.pool.Exec(ctx, "SET LOCAL lock_timeout = '2s';\n"+apiKeyCapabilitiesSQL); err != nil {
		return fmt.Errorf("ensure API key capabilities schema: %w", err)
	}
	// IF NOT EXISTS can accept an incompatible column or a same-name index.
	if err := d.pool.QueryRow(ctx, readySQL).Scan(&ready); err != nil {
		return fmt.Errorf("verify API key capabilities schema: %w", err)
	}
	if !ready {
		return fmt.Errorf("API key capabilities schema requires a non-null JSONB column with an empty array default and a valid GIN index on that column")
	}
	return nil
}
