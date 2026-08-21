package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigration553ApprovalResumeClaimContract(t *testing.T) {
	up := string(migrationFile(t, "553_approval_resume_claim.sql"))
	for _, want := range []string{
		"BEGIN;",
		"ADD COLUMN IF NOT EXISTS resume_state TEXT NOT NULL DEFAULT 'idle'",
		"ADD COLUMN IF NOT EXISTS resume_owner TEXT",
		"ADD COLUMN IF NOT EXISTS resume_lease_until TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS resume_fencing_token BIGINT NOT NULL DEFAULT 0",
		"ADD COLUMN IF NOT EXISTS resume_started_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS resume_completed_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS resume_error TEXT",
		"DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk",
		"ADD CONSTRAINT approval_queue_resume_state_chk",
		"resume_state IN ('idle', 'running', 'completed', 'failed')",
		"DROP CONSTRAINT IF EXISTS approval_queue_resume_fencing_token_chk",
		"ADD CONSTRAINT approval_queue_resume_fencing_token_chk",
		"resume_fencing_token >= 0",
		"CREATE INDEX IF NOT EXISTS idx_approval_queue_resume_claimable",
		"ON approval_queue (resume_lease_until, created_at)",
		"status = 'approved' AND resume_state IN ('idle', 'running', 'failed')",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 553 up missing %q", want)
		}
	}
	mustAppearBefore(t, up, "BEGIN;", "ADD COLUMN IF NOT EXISTS resume_state")
	mustAppearBefore(t, up, "ADD COLUMN IF NOT EXISTS resume_error", "DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk")
	mustAppearBefore(t, up, "DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk", "ADD CONSTRAINT approval_queue_resume_state_chk")
	mustAppearBefore(t, up, "DROP CONSTRAINT IF EXISTS approval_queue_resume_fencing_token_chk", "ADD CONSTRAINT approval_queue_resume_fencing_token_chk")
	mustAppearBefore(t, up, "ADD CONSTRAINT approval_queue_resume_fencing_token_chk", "CREATE INDEX IF NOT EXISTS idx_approval_queue_resume_claimable")
	mustAppearBefore(t, up, "CREATE INDEX IF NOT EXISTS idx_approval_queue_resume_claimable", "COMMIT;")
}

func TestMigration553ApprovalResumeClaimSchemaMirrors(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, name := range []string{
		"sql/schema/01-schema.sql",
		"deploy/sql/schemas/baseline/01-schema.sql",
		"installer/cmd/llm-gw-installer/embeddata/01-schema.sql",
	} {
		contents, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read schema mirror %s: %v", name, err)
		}
		text := string(contents)
		for _, want := range []string{
			"resume_state text DEFAULT 'idle'::text NOT NULL",
			"approval_queue_resume_state_chk",
			"idx_approval_queue_resume_claimable",
			"resume_lease_until, created_at",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("schema mirror %s missing %q", name, want)
			}
		}
	}
}

func TestMigration553ApprovalResumeClaimDownContract(t *testing.T) {
	down := string(migrationFile(t, "553_approval_resume_claim.down.sql"))
	for _, want := range []string{
		"BEGIN;",
		"RAISE EXCEPTION",
		"549 rollback refused",
		"resume_state <> 'idle'",
		"resume_fencing_token <> 0",
		"DROP INDEX IF EXISTS public.idx_approval_queue_resume_claimable",
		"DROP CONSTRAINT IF EXISTS approval_queue_resume_fencing_token_chk",
		"DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk",
		"DROP COLUMN IF EXISTS resume_state",
		"DROP COLUMN IF EXISTS resume_owner",
		"DROP COLUMN IF EXISTS resume_lease_until",
		"DROP COLUMN IF EXISTS resume_fencing_token",
		"DROP COLUMN IF EXISTS resume_started_at",
		"DROP COLUMN IF EXISTS resume_completed_at",
		"DROP COLUMN IF EXISTS resume_error",
		"COMMIT;",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("migration 553 down missing %q", want)
		}
	}
	if strings.Contains(down, "DROP TABLE") {
		t.Error("migration 553 down must not drop approval_queue")
	}
	mustAppearBefore(t, down, "RAISE EXCEPTION", "DROP INDEX IF EXISTS public.idx_approval_queue_resume_claimable")
	mustAppearBefore(t, down, "DROP INDEX IF EXISTS public.idx_approval_queue_resume_claimable", "DROP COLUMN IF EXISTS resume_error")
	mustAppearBefore(t, down, "DROP CONSTRAINT IF EXISTS approval_queue_resume_fencing_token_chk", "DROP COLUMN IF EXISTS resume_error")
	mustAppearBefore(t, down, "DROP CONSTRAINT IF EXISTS approval_queue_resume_state_chk", "DROP COLUMN IF EXISTS resume_error")
}

func mustAppearBefore(t *testing.T, text, first, second string) {
	t.Helper()
	firstIdx := strings.Index(text, first)
	secondIdx := strings.Index(text, second)
	if firstIdx == -1 || secondIdx == -1 {
		t.Fatalf("missing order anchors %q and %q", first, second)
	}
	if firstIdx >= secondIdx {
		t.Errorf("%q must appear before %q", first, second)
	}
}
