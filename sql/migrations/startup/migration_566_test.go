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
