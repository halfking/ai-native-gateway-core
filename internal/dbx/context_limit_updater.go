// Package dbx — context_limit_updater.go
//
// ContextLimitUpdater implements async persistence of discovered context limits
// to credential_model_bindings.context_window_override with source='discovery'.
// Called by executors.handleContextLengthRecovery when the upstream error body
// carries the real limit and it differs from the configured value by >5%.
//
// Migration 523 added the per-credential override columns; migration 524 added
// the NOTIFY trigger so the update fans out to all gateway instances' caches.
package dbx

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
)

// DBExecutor is the minimal interface for executing queries, satisfied by both
// *pgxpool.Pool and pgxmock.PgxPoolIface.
type DBExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// ContextLimitUpdater persists discovered context limits to the database.
// The interface allows test doubles and no-op implementations.
type ContextLimitUpdater interface {
	// UpdateContextLimit writes a discovered limit and reports whether a row was
	// actually updated. A false result with nil error means the binding was not
	// eligible (missing or protected by a manual/probe source).
	UpdateContextLimit(ctx context.Context, credentialID int, rawModel string, limit int) (bool, error)
}

// DBContextLimitUpdater writes discovered limits to credential_model_bindings.
type DBContextLimitUpdater struct {
	db DBExecutor
}

// NewDBContextLimitUpdater returns a production updater backed by the given executor.
func NewDBContextLimitUpdater(db DBExecutor) *DBContextLimitUpdater {
	return &DBContextLimitUpdater{db: db}
}

// UpdateContextLimit writes the discovered limit to credential_model_bindings
// .context_window_override with source='discovery' and updated_at=NOW().
//
// The UPDATE is keyed by (credential_id, provider_model_id), where provider_model_id
// is resolved from provider_models.raw_model_name = rawModel. Manual and probe
// sources are protected and are never overwritten by automatic discovery.
func (u *DBContextLimitUpdater) UpdateContextLimit(ctx context.Context, credentialID int, rawModel string, limit int) (bool, error) {
	if limit <= 0 || rawModel == "" {
		return false, nil
	}
	const q = `
		UPDATE credential_model_bindings cmb
		SET context_window_override = $3,
		    context_window_source = 'discovery',
		    context_window_updated_at = NOW()
		FROM provider_models pm
		WHERE cmb.provider_model_id = pm.id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.context_window_source, 'catalog') IN ('catalog', 'discovery')
	`
	tag, err := u.db.Exec(ctx, q, credentialID, rawModel, limit)
	if err != nil {
		slog.ErrorContext(ctx, "failed to update discovered context limit",
			"credential_id", credentialID, "raw_model", rawModel, "limit", limit, "error", err)
		return false, err
	}

	updated := tag.RowsAffected() > 0
	if !updated {
		slog.InfoContext(ctx, "context_limit_discovery: no eligible binding (silent no-op)",
			"credential_id", credentialID, "raw_model", rawModel, "limit", limit)
		return false, nil
	}
	slog.InfoContext(ctx, "context_limit_discovery: updated credential_model_bindings",
		"credential_id", credentialID, "raw_model", rawModel, "limit", limit,
		"rows_affected", tag.RowsAffected())
	return true, nil
}
