package startup

import (
	"os"
	"strings"
	"testing"
)

func TestMigration566CredentialsGovernorRevisionUpContract(t *testing.T) {
	up, err := os.ReadFile("566_credentials_governor_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(up)
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0",
		"CREATE SEQUENCE IF NOT EXISTS public.credentials_governor_revision_seq",
		"CREATE INDEX IF NOT EXISTS credentials_revision_idx",
		"CREATE OR REPLACE FUNCTION public.bump_credentials_governor_revision()",
		"CREATE OR REPLACE FUNCTION public.notify_credentials_governor_revision()",
		"NEW.revision := nextval('public.credentials_governor_revision_seq')",
		"pg_notify('credentials_revision', NEW.revision::text)",
		"concurrency_mode",
		"rpm_limit",
		"tpm_limit",
		"fp_slot_limit",
		"max_queue_depth",
		"max_queue_wait_ms",
		"COMMIT;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("566 up missing %q", want)
		}
	}
	if !strings.Contains(body, "WHEN (OLD.revision IS DISTINCT FROM NEW.revision)") {
		t.Error("566 revision trigger must compare OLD.revision")
	}
	// setval must never rewind a sequence that already handed out a higher
	// value (rolled-back tx / deleted top-revision row), or revisions could
	// be reused across reruns.
	if !strings.Contains(body, "pg_sequence_last_value('public.credentials_governor_revision_seq')") {
		t.Error("566 setval must consider the sequence's current last_value via pg_sequence_last_value")
	}
	if strings.Count(body, "pg_sequence_last_value") < 2 {
		t.Errorf("566 setval must use pg_sequence_last_value in both the value and is_called branches (got %d references)", strings.Count(body, "pg_sequence_last_value"))
	}
	// Non-governor updates must preserve the stored revision so callers
	// cannot rewind or reorder history via a direct revision write.
	if !strings.Contains(body, "NEW.revision := OLD.revision;") {
		t.Error("566 bump function must preserve OLD.revision when no governor field changed")
	}
}

func TestMigration566CredentialsGovernorRevisionDownContract(t *testing.T) {
	down, err := os.ReadFile("566_credentials_governor_revision.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(down)
	for _, want := range []string{
		"DROP TRIGGER IF EXISTS trg_notify_credentials_governor_revision_insert",
		"DROP TRIGGER IF EXISTS trg_notify_credentials_governor_revision_update",
		"DROP TRIGGER IF EXISTS trg_bump_credentials_governor_revision",
		"DROP TRIGGER IF EXISTS trg_notify_auto_route_creds",
		// Rollback must restore the pre-566 auto-route trigger, not just
		// drop the widened one.
		"CREATE TRIGGER trg_notify_auto_route_creds AFTER UPDATE OF status, availability_state, quota_state, circuit_state, concurrency_limit, lifecycle_status, manual_disabled",
		"DROP FUNCTION IF EXISTS public.notify_credentials_governor_revision()",
		"DROP FUNCTION IF EXISTS public.bump_credentials_governor_revision()",
		"DROP SEQUENCE IF EXISTS public.credentials_governor_revision_seq",
		"DROP COLUMN IF EXISTS revision",
		"COMMIT;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("566 down missing %q", want)
		}
	}
}

// TestMigration566EnsureParity pins that the db.go runtime ensure keeps the
// same setval monotonicity and revision-preservation semantics as the SQL
// migration, so the two mirrors cannot drift silently.
func TestMigration566EnsureParity(t *testing.T) {
	src, err := os.ReadFile("../../../db/db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (d *DB) ensureCredentialGovernorRevision")
	if start == -1 {
		t.Fatal("ensureCredentialGovernorRevision not found in db.go")
	}
	end := strings.Index(body[start:], "\nfunc ")
	if end == -1 {
		end = len(body) - start
	}
	ensureSQL := body[start : start+end]
	for _, want := range []string{
		"pg_sequence_last_value('public.credentials_governor_revision_seq')",
		"NEW.revision := OLD.revision;",
		"pg_notify('credentials_revision', NEW.revision::text)",
	} {
		if !strings.Contains(ensureSQL, want) {
			t.Errorf("db.go ensureCredentialGovernorRevision missing %q (migration/ensure drift)", want)
		}
	}
}
