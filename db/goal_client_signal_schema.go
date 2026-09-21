package db

import (
	"context"
	"fmt"
)

// ensureGoalClientSignalSchema mirrors startup migration 645 for databases
// upgraded through db.Open rather than the installer.
func (d *DB) ensureGoalClientSignalSchema(ctx context.Context) error {
	if d == nil || d.pool == nil {
		return nil
	}

	_, err := d.pool.Exec(ctx, `
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
			WHERE parent_session_key <> '';
	`)
	if err != nil {
		return fmt.Errorf("ensure goal client signal schema: %w", err)
	}
	return nil
}
