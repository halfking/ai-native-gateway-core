package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigration568CredentialPriorityUpContract pins the 568 up contract:
// idempotent column add, view exposure, INSTEAD OF UPDATE persistence,
// cache-refresh predicate, and scope-hash inclusion for the priority flag.
func TestMigration568CredentialPriorityUpContract(t *testing.T) {
	up := string(migrationFile(t, "568_credential_priority_flag.sql"))

	for _, want := range []string{
		"BEGIN;",
		"column_name = 'priority'",
		"ADD COLUMN priority boolean NOT NULL DEFAULT false",
		"COMMENT ON COLUMN public.credential_model_bindings.priority",
		"CREATE OR REPLACE VIEW public.model_offers",
		"cmb.priority",
		"CREATE OR REPLACE FUNCTION public.model_offers_update_trigger()",
		"priority = COALESCE(NEW.priority, credential_model_bindings.priority)",
		"OR old.priority IS DISTINCT FROM new.priority",
		"EXECUTE FUNCTION public.notify_auto_route_refresh()",
		"b.priority::text || '|' ||",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_insert()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_delete()",
		"CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_update()",
		"CREATE TRIGGER model_offers_insert INSTEAD OF INSERT",
		"CREATE TRIGGER model_offers_update INSTEAD OF UPDATE",
		"CREATE TRIGGER model_offers_delete INSTEAD OF DELETE",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 568 up missing %q", want)
		}
	}

	// The scope-hash string must include priority between manual_priority and
	// updated_at in every bump function (3 trigger fns + the one-off recompute).
	if got := strings.Count(up, "b.priority::text"); got < 4 {
		t.Errorf("migration 568 up: b.priority::text appears %d times, want >= 4 (3 bump fns + recompute)", got)
	}
	// The update-scope predicate must treat priority flips as scope-affecting.
	if got := strings.Count(up, "o.priority        IS DISTINCT FROM n.priority"); got < 4 {
		t.Errorf("migration 568 up: update predicate priority clause appears %d times, want >= 4", got)
	}
}

// TestMigration568CredentialPriorityDownContract pins the down contract:
// view rebuilt without priority, predicate reverted, column dropped, and
// view triggers recreated after the CASCADE drop.
func TestMigration568CredentialPriorityDownContract(t *testing.T) {
	down := string(migrationFile(t, "568_credential_priority_flag.down.sql"))

	for _, want := range []string{
		"BEGIN;",
		"DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb_update ON public.credential_model_bindings",
		"DROP VIEW IF EXISTS public.model_offers CASCADE",
		"CREATE VIEW public.model_offers",
		"DROP COLUMN IF EXISTS priority",
		"CREATE TRIGGER model_offers_insert INSTEAD OF INSERT ON public.model_offers",
		"CREATE TRIGGER model_offers_update INSTEAD OF UPDATE ON public.model_offers",
		"CREATE TRIGGER model_offers_delete INSTEAD OF DELETE ON public.model_offers",
		"COMMIT;",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("migration 568 down missing %q", want)
		}
	}
	// The down view must not expose cmb.priority while the column is being
	// dropped, and the down predicate must not reference it.
	if strings.Contains(down, "cmb.priority,") {
		t.Error("migration 568 down: view still selects cmb.priority")
	}
	if strings.Contains(down, "old.priority IS DISTINCT FROM new.priority") {
		t.Error("migration 568 down: notify predicate still references priority")
	}
}

// TestMigration570InsertPriorityPassthrough pins the 570 contract: the
// model_offers INSERT trigger persists context_window_override and priority
// on both the insert and the upsert path, and the down migration restores
// the pre-570 body without them.
func TestMigration570InsertPriorityPassthrough(t *testing.T) {
	up := string(migrationFile(t, "570_model_offers_insert_priority_passthrough.sql"))
	for _, want := range []string{
		"BEGIN;",
		"CREATE OR REPLACE FUNCTION public.model_offers_insert_trigger()",
		"admin_protected, context_window_override, priority",
		"NEW.context_window_override, COALESCE(NEW.priority, FALSE)",
		"context_window_override = COALESCE(EXCLUDED.context_window_override, credential_model_bindings.context_window_override)",
		"priority = COALESCE(EXCLUDED.priority, credential_model_bindings.priority)",
		"COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 570 up missing %q", want)
		}
	}

	down := string(migrationFile(t, "570_model_offers_insert_priority_passthrough.down.sql"))
	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION public.model_offers_insert_trigger()",
		"COALESCE(NEW.admin_protected, FALSE)\n    )",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("migration 570 down missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"COALESCE(NEW.priority, FALSE)",
		"EXCLUDED.priority",
	} {
		if strings.Contains(down, forbidden) {
			t.Errorf("migration 570 down must not pass through %q", forbidden)
		}
	}
}

// TestMigration568FreshInstallArtifacts verifies both fresh-install paths:
// deploy uses the post-migration baseline while the installer applies the
// versioned startup artifact after its embedded base schema.
func TestMigration568FreshInstallArtifacts(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	artifacts := map[string][]string{
		"deploy/sql/schemas/baseline/01-schema.sql": {
			"priority boolean DEFAULT false NOT NULL",
			"cmb.priority\n   FROM (public.credential_model_bindings cmb",
			"priority = COALESCE(NEW.priority, credential_model_bindings.priority)",
			"admin_protected, context_window_override, priority",
			"NEW.context_window_override, COALESCE(NEW.priority, FALSE)",
			"priority = COALESCE(EXCLUDED.priority, credential_model_bindings.priority)",
			"OR (old.priority IS DISTINCT FROM new.priority)",
		},
		"installer/cmd/llm-gw-installer/embeddata/startup/568_credential_priority_flag.sql": {
			"ADD COLUMN priority boolean NOT NULL DEFAULT false",
			"cmb.priority\n   FROM (public.credential_model_bindings cmb",
			"priority = COALESCE(NEW.priority, credential_model_bindings.priority)",
			"OR old.priority IS DISTINCT FROM new.priority",
		},
		"installer/cmd/llm-gw-installer/embeddata/startup/570_model_offers_insert_priority_passthrough.sql": {
			"admin_protected, context_window_override, priority",
			"NEW.context_window_override, COALESCE(NEW.priority, FALSE)",
			"priority = COALESCE(EXCLUDED.priority, credential_model_bindings.priority)",
		},
	}
	for name, wants := range artifacts {
		contents, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read schema mirror %s: %v", name, err)
		}
		text := string(contents)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("fresh-install artifact %s missing %q", name, want)
			}
		}
	}
}

// TestModelOffersInsertTriggerObjectSync pins the sql/objects snapshot of
// model_offers_insert_trigger to the post-570 shape, so object
// regeneration cannot silently drop the priority passthrough.
func TestModelOffersInsertTriggerObjectSync(t *testing.T) {
	obj, err := os.ReadFile(filepath.Join("..", "..", "..", "sql", "objects", "functions", "model_offers_insert_trigger.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(obj)
	for _, want := range []string{
		"admin_protected, context_window_override, priority",
		"NEW.context_window_override, COALESCE(NEW.priority, FALSE)",
		"priority = COALESCE(EXCLUDED.priority, credential_model_bindings.priority)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("objects model_offers_insert_trigger.sql missing %q", want)
		}
	}
}
