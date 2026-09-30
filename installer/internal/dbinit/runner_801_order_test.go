package dbinit

import "testing"

// TestStartupFilesPlace801AfterItsDependencies pins the apply order of
// 801_session_turn_details_duplicate_drain.sql.
//
// 801 does `CREATE OR REPLACE FUNCTION public.promote_session_turn_details_hot_to_partition`,
// and that function body references `public.session_turn_details` plus its hot
// table, both created by 733. Runner.applySQL walks StartupFiles in slice order
// against a live database, so if 801 is registered before 733 the replace runs
// while its target tables do not exist yet.
//
// The 801 entry originally sat at 759's position (between 758 and 760) because
// the migration was numbered 759 on this branch. It was renumbered to 801 after
// upstream took 759 (759_report_snapshots_grain_dims); the slice position had to
// move with it, and this test is what keeps the two from drifting apart again.
func TestStartupFilesPlace801AfterItsDependencies(t *testing.T) {
	runner := NewRunner("citus", "user", "db", "/tmp/sql")

	pos := make(map[string]int, len(runner.StartupFiles))
	for i, name := range runner.StartupFiles {
		pos[name] = i
	}

	const target = "801_session_turn_details_duplicate_drain.sql"
	targetAt, ok := pos[target]
	if !ok {
		t.Fatalf("%s must be registered in dbinit.Runner.StartupFiles; "+
			"a fresh install would never replace the 733 promote function, so "+
			"duplicate session_turn_details_hot rows would never drain", target)
	}

	dependsOn := []string{
		"733_session_turn_details.sql",
		"734_request_logs_view_details_join.sql",
	}
	for _, dep := range dependsOn {
		depAt, ok := pos[dep]
		if !ok {
			t.Fatalf("%s missing from StartupFiles; cannot assert %s ordering", dep, target)
		}
		if depAt > targetAt {
			t.Errorf("%s is registered at position %d, after %s at %d; "+
				"applySQL executes in slice order, so 801 would replace the promote "+
				"function before its dependency tables exist",
				target, targetAt, dep, depAt)
		}
	}

	// The renumbering must not silently resurrect the 759 collision: 759 now
	// belongs to upstream's report-snapshots migration and must not appear here.
	if at, ok := pos["759_session_turn_details_duplicate_drain.sql"]; ok {
		t.Errorf("pre-renumber name 759_session_turn_details_duplicate_drain.sql is still "+
			"registered at position %d; it collides with upstream 759_report_snapshots_grain_dims", at)
	}
}
