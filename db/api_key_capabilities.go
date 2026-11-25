package db

import (
	"context"
	_ "embed"
	"fmt"
)

//go:embed migrations/20261125000001_api_keys_capabilities.sql
var apiKeyCapabilitiesSQL string

func (d *DB) ensureAPIKeyCapabilities(ctx context.Context) error {
	var ready bool
	err := d.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_attribute
			WHERE attrelid = 'public.api_keys'::regclass
			  AND attname = 'capabilities' AND NOT attisdropped
		) AND EXISTS (
			SELECT 1 FROM pg_catalog.pg_index
			WHERE indexrelid = to_regclass('public.idx_api_keys_capabilities')
			  AND indrelid = 'public.api_keys'::regclass AND indisvalid
		)
	`).Scan(&ready)
	if err != nil {
		return fmt.Errorf("inspect API key capabilities schema: %w", err)
	}
	if ready {
		return nil
	}
	if _, err := d.pool.Exec(ctx, "SET LOCAL lock_timeout = '2s';\n"+apiKeyCapabilitiesSQL); err != nil {
		return fmt.Errorf("ensure API key capabilities schema: %w", err)
	}
	return nil
}
