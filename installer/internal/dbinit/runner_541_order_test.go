package dbinit

import (
	"os"
	"path/filepath"
	"testing"
)

// Round 42: migration 541 creates public.candidate_binding_scope_revision,
// which 568_credential_priority_flag.sql indexes into. 541 existed in the
// canonical startup directory but was never registered in the installer — not
// in embeddata, not in the go:embed list, not in StartupFiles — so a fresh
// install aborted at 568 with
//
//	relation "public.candidate_binding_scope_revision" does not exist
//
// Runner.applySQL walks StartupFiles in slice order against a live database,
// so the registration position must be earlier than every consumer, not merely
// present.

func startupIndex(t *testing.T) map[string]int {
	t.Helper()
	files := NewRunner("citus", "user", "db", "/tmp/sql").StartupFiles
	idx := make(map[string]int, len(files))
	for i, name := range files {
		if prev, dup := idx[name]; dup {
			t.Fatalf("StartupFiles lists %q twice (index %d and %d)", name, prev, i)
		}
		idx[name] = i
	}
	return idx
}

func requireStartupOrder(t *testing.T, before, after string) {
	t.Helper()
	idx := startupIndex(t)
	b, okB := idx[before]
	a, okA := idx[after]
	if !okB {
		t.Fatalf("StartupFiles no longer contains %q; this guard needs updating", before)
	}
	if !okA {
		t.Fatalf("StartupFiles no longer contains %q; this guard needs updating", after)
	}
	if b >= a {
		t.Errorf("StartupFiles order wrong: %q is at %d but must precede %q at %d", before, b, after, a)
	}
}

func TestStartupFilesPlace541BeforeItsConsumer568(t *testing.T) {
	requireStartupOrder(t,
		"541_candidate_binding_scope_revision.sql",
		"568_credential_priority_flag.sql")
}

// Every StartupFiles entry must resolve to a real embeddata file, or the fresh
// installer would apply a file that is not there.
func TestStartupFilesExistInEmbeddata(t *testing.T) {
	for _, name := range NewRunner("citus", "user", "db", "/tmp/sql").StartupFiles {
		p := filepath.Join("..", "..", "cmd", "llm-gw-installer", "embeddata", "startup", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("StartupFiles entry %q has no embeddata file: %v", name, err)
		}
	}
}

// Note: StartupFiles is deliberately NOT in ascending numeric order — apply
// order follows dependencies, not the migration number (801 is registered last
// because the function it replaces depends on 733). A global ascending-order
// guard would therefore encode a false invariant. Two genuine inversions were
// observed while writing this and are left for a dedicated review rather than
// pinned here:
//
//	560_session_summaries_tenant_uniqueness.sql after 655_session_summaries_schema_reconcile.sql
//	704_plan_quota_probe_backoff.sql            after 713_session_turns_cost_precision.sql
