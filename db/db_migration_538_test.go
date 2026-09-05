package db

import (
	"os"
	"strings"
	"testing"
)

func TestApplyMigrationsIncludesMigration538Ensure(t *testing.T) {
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"ensureNodeProbeTriggerKindSchema(migCtx)",
		"func (d *DB) ensureNodeProbeTriggerKindSchema",
		"to_regclass('public.node_probe_runs')",
		"to_regclass('public.credential_probe_queue')",
		"node_probe_runs_trigger_kind_check",
		"credential_probe_queue_source_check",
		"'selfcheck'",
		"'external_async'",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("db.go missing migration 538 contract %q", want)
		}
	}
}
