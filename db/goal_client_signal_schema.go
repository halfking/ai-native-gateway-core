package db

import (
	"context"
	"fmt"
	"log/slog"
)

// goalClientSignalColumns enumerates the columns migration 645 adds to
// goal_sessions, and goalClientSignalSummaryColumns the ones it adds to
// session_summaries. The catalog guard below reads these lists, so the probe and
// the DDL cannot drift apart.
var (
	goalClientSignalColumns = []string{
		"continue_attempt",
		"last_completion_judgement",
		"sub_agents_total",
		"sub_agents_completed",
		"sub_agents_pending",
		"last_sub_agents_report_at",
	}
	goalClientSignalSummaryColumns = []string{
		"parent_session_key",
		"handoff_reason",
	}
)

// goalClientSignalDDL mirrors startup migration 645.
//
// 2026-10-07: this DDL used to run unguarded on every boot. session_summaries
// carries 465 MB of heap behind 21 indexes and takes ~198k reads/day plus
// ~58k writes/day in the shared production database, and `ALTER TABLE … ADD
// COLUMN IF NOT EXISTS` takes ACCESS EXCLUSIVE on the table and every one of
// its indexes before it can decide the columns are already there. Measured on
// production, all six goal_sessions columns, both session_summaries columns and
// idx_session_summaries_parent are already in place — so every boot locked 22
// relations to change nothing.
//
// Same shape as credits_charged (§10.97), quality_fix_mode (§10.98.3) and
// provider soft delete (§10.98.7).
const goalClientSignalDDL = `
		ALTER TABLE public.goal_sessions
			ADD COLUMN IF NOT EXISTS continue_attempt INT NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS last_completion_judgement VARCHAR(32) DEFAULT '',
			ADD COLUMN IF NOT EXISTS sub_agents_total INT NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS sub_agents_completed INT NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS sub_agents_pending INT NOT NULL DEFAULT 0,
			ADD COLUMN IF NOT EXISTS last_sub_agents_report_at TIMESTAMPTZ;

		ALTER TABLE public.session_summaries
			ADD COLUMN IF NOT EXISTS parent_session_key VARCHAR(255) DEFAULT '',
			ADD COLUMN IF NOT EXISTS handoff_reason VARCHAR(64) DEFAULT '';

		CREATE INDEX IF NOT EXISTS idx_session_summaries_parent
			ON public.session_summaries(tenant_id, parent_session_key)
			WHERE parent_session_key <> '';`

// goalClientSignalCurrent reports whether goalClientSignalDDL would be a pure
// no-op: every column it adds is present and its index exists.
//
// Everything is checked together, not per table. The DDL is applied as one
// unit, so requiring all of it before skipping is the only shape that cannot
// leave a half-migrated database unfixed.
//
// Any probe error returns false so the DDL still runs: reading a failed probe
// as "already current" would permanently skip the schema on a database that
// genuinely needs it.
func (d *DB) goalClientSignalCurrent(ctx context.Context) bool {
	if d == nil || d.pool == nil {
		return false
	}
	if !d.columnsAllPresent(ctx, "goal_sessions", goalClientSignalColumns) {
		return false
	}
	if !d.columnsAllPresent(ctx, "session_summaries", goalClientSignalSummaryColumns) {
		return false
	}
	var found int
	if err := d.pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_indexes
		 WHERE schemaname='public' AND indexname='idx_session_summaries_parent'
	`).Scan(&found); err != nil {
		slog.Warn("goal client signal probe failed; applying DDL", "error", err)
		return false
	}
	return found > 0
}

// ensureGoalClientSignalSchema mirrors startup migration 645 for databases
// upgraded through db.Open rather than the installer.
func (d *DB) ensureGoalClientSignalSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	if d.goalClientSignalCurrent(ctx) {
		return nil
	}

	if _, err := d.pool.Exec(ctx, goalClientSignalDDL); err != nil {
		return fmt.Errorf("ensure goal client signal schema: %w", err)
	}
	return nil
}
