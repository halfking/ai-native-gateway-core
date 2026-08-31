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
	// UpdateContextLimit writes the discovered limit to credential_model_bindings
	// .context_window_override with source='discovery'. This call should be async
	// and non-blocking — the caller (handleContextLengthRecovery) fires it in a
	// goroutine so a slow DB write doesn't block the retry.
	//
	// credentialID + rawModel must uniquely identify a credential_model_bindings
	// row. If no such row exists, the update is a silent no-op (the binding was
	// never created or has been removed).
	UpdateContextLimit(ctx context.Context, credentialID int, rawModel string, limit int) error
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
// is resolved from provider_models.raw_model_name = rawModel. If no matching row
// exists in credential_model_bindings, the statement affects 0 rows (silent no-op).
//
// The NOTIFY trigger (trg_notify_auto_route_cmb_update, migration 524) fires on
// context_window_override changes and invalidates all instances' candCaches, so
// the next request picks up the discovered limit.
func (u *DBContextLimitUpdater) UpdateContextLimit(ctx context.Context, credentialID int, rawModel string, limit int) error {
	// Resolve provider_model_id from raw_model_name, then UPDATE the binding row.
	// The WHERE clause ensures we only write when the binding exists (the model
	// was seen on this credential and a binding was created by the discovery or
	// offer sync process).
	const q = `
		UPDATE credential_model_bindings cmb
		SET context_window_override = $3,
		    context_window_source = 'discovery',
		    context_window_updated_at = NOW()
		FROM provider_models pm
		WHERE cmb.provider_model_id = pm.id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
	`
	tag, err := u.db.Exec(ctx, q, credentialID, rawModel, limit)
	if err != nil {
		slog.ErrorContext(ctx, "failed to update discovered context limit",
			"credential_id", credentialID,
			"raw_model", rawModel,
			"limit", limit,
			"error", err,
		)
		return err
	}

	rowsAffected := tag.RowsAffected()
	if rowsAffected == 0 {
		// No matching binding found. This is not an error — the credential may
		// have been removed, the model may not be bound, or the offer sync may
		// not have created a binding yet. Log at Info level for observability.
		slog.InfoContext(ctx, "context_limit_discovery: no binding found (silent no-op)",
			"credential_id", credentialID,
			"raw_model", rawModel,
			"limit", limit,
		)
		return nil
	}

	slog.InfoContext(ctx, "context_limit_discovery: updated credential_model_bindings",
		"credential_id", credentialID,
		"raw_model", rawModel,
		"limit", limit,
		"rows_affected", rowsAffected,
	)
	return nil
}
