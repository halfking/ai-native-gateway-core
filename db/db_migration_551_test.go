package db

import (
	"os"
	"strings"
	"testing"
)

func TestApplyMigrationsIncludesMigration551Ensure(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"ensureApprovalResumeClaimSchema(migCtx)",
		"func (d *DB) ensureApprovalResumeClaimSchema",
		"to_regclass('public.approval_queue')",
		"ALTER TABLE public.approval_queue",
		"resume_state TEXT NOT NULL DEFAULT 'idle'",
		"resume_fencing_token BIGINT NOT NULL DEFAULT 0",
		"approval_queue_resume_state_chk",
		"approval_queue_resume_fencing_token_chk",
		"idx_approval_queue_resume_claimable",
		"ON public.approval_queue (resume_lease_until, created_at)",
		"tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("db.go missing migration 551 contract %q", want)
		}
	}
}
