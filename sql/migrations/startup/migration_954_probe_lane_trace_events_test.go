package startup

import (
	"strings"
	"testing"
)

func TestMigration954ProbeTraceContract(t *testing.T) {
	root := mig951Tree(t)
	name := "954_probe_lane_trace_events"
	up := mig951Read(t, root, "sql/migrations/startup/"+name+".sql")
	for _, want := range []string{
		"BEGIN;", "SET LOCAL lock_timeout", "SET LOCAL statement_timeout",
		"ALTER TABLE public.session_turns_probe_hot",
		"ADD COLUMN IF NOT EXISTS trace_events jsonb;",
		"INSERT INTO public.schema_migrations (version, description)", "'954'", "COMMIT;",
	} {
		if !strings.Contains(up, want) {
			t.Fatalf("954 missing %q", want)
		}
	}
	down := mig951Read(t, root, "sql/migrations/startup/"+name+".down.sql")
	for _, want := range []string{"DROP COLUMN IF EXISTS trace_events", "DELETE FROM public.schema_migrations WHERE version = '954'"} {
		if !strings.Contains(down, want) {
			t.Fatalf("954 down missing %q", want)
		}
	}
	for _, suffix := range []string{".sql", ".down.sql"} {
		canonical := mig951Read(t, root, "sql/migrations/startup/"+name+suffix)
		embedded := mig951Read(t, root, "installer/cmd/llm-gw-installer/embeddata/startup/"+name+suffix)
		if canonical != embedded {
			t.Fatalf("954 %s installer copy differs", suffix)
		}
	}
	for _, path := range []string{"scripts/apply-db-revision-sequence.sh", "installer/internal/dbinit/runner.go", "installer/cmd/llm-gw-installer/main.go", "sql/schema/installed_startup_migrations.tsv"} {
		if !strings.Contains(mig951Read(t, root, path), name+".sql") {
			t.Fatalf("954 missing from %s", path)
		}
	}
	trace := mig951Read(t, root, "internal/trace/trace.go")
	for _, want := range []string{
		"UPDATE session_turns_probe_hot SET trace_events = $1::jsonb WHERE request_id = $2",
		"SELECT trace_events FROM session_turns_probe_hot WHERE request_id = $1",
	} {
		if !strings.Contains(trace, want) {
			t.Fatalf("trace write/read missing: %q", want)
		}
	}
}
