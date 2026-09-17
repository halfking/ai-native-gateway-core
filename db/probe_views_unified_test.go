package db

import (
	"os"
	"strings"
	"testing"
)

func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// TestNodeProbeCompatViewSQL_Contract guards the derived-state vocabulary: the
// compat projection must keep emitting the legacy state words and the
// manual_offline sticky lock, and routing-consistent column aliases.
func TestNodeProbeCompatViewSQL_Contract(t *testing.T) {
	sql := NodeProbeCompatViewSQL()
	for _, want := range []string{
		"CREATE OR REPLACE VIEW v_node_probe_state_compat",
		"FROM node_probe_state nps",
		"AS last_status",
		"AS total_attempts",
		"AS probe_priority",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("compat view SQL missing %q", want)
		}
	}
	cse := NodeProbeStateCaseSQL("nps")
	for _, want := range []string{
		"manual_offline", "unknown", "probing", "healthy_confirmed",
		"broken_confirmed", "suspicious",
	} {
		if !strings.Contains(cse, want) {
			t.Errorf("state derivation missing branch %q", want)
		}
	}
}

// TestNodeProbeStateCaseSQL_MigrationSync fails when the startup migration or
// the baseline view file drifts from the canonical derivation — the gateway
// rebuilds these views at every boot from the Go source, so a drifted file
// would flip-flop definitions between migrate and boot.
func TestNodeProbeStateCaseSQL_MigrationSync(t *testing.T) {
	canonical := collapseWS(NodeProbeStateCaseSQL("nps"))
	for _, f := range []string{
		"../sql/migrations/startup/716_unify_probe_health_views.sql",
		"../sql/objects/views/v_node_probe_state_compat.sql",
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !strings.Contains(collapseWS(string(b)), canonical) {
			t.Errorf("%s is out of sync with NodeProbeStateCaseSQL", f)
		}
	}
}

// normalizeSQLForEquiv strips line comments and collapses whitespace so the
// two hand-maintained copies of the probe-health view family can be compared
// full-text (A-2 guard, R36): no `--` remark and no indentation difference
// can mask a real drift between the Go ensure channel and migration 716.
func normalizeSQLForEquiv(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return collapseWS(b.String())
}

// TestProbeHealthDashboardViewsSQL_MigrationSync upgrades the fragment-level
// guard to a full-text equivalence check: the gateway rebuilds the whole
// view family from probeHealthDashboardViewsSQL at every boot while startup
// migration 716 applies the same objects at install/upgrade time. A drift
// between the two channels would flip-flop definitions between migrate and
// boot, so any byte-level (post-normalization) difference fails here.
func TestProbeHealthDashboardViewsSQL_MigrationSync(t *testing.T) {
	goSQL := normalizeSQLForEquiv(probeHealthDashboardViewsSQL())
	b, err := os.ReadFile("../sql/migrations/startup/716_unify_probe_health_views.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	migSQL := normalizeSQLForEquiv(string(b))
	if goSQL != migSQL {
		// Locate the first divergence for a actionable failure message.
		n := len(goSQL)
		if len(migSQL) < n {
			n = len(migSQL)
		}
		cut := n
		for i := 0; i < n; i++ {
			if goSQL[i] != migSQL[i] {
				cut = i
				break
			}
		}
		lo := cut - 120
		if lo < 0 {
			lo = 0
		}
		hi := cut + 160
		if hi > n {
			hi = n
		}
		t.Fatalf("716 migration and ensure SQL diverged (go len=%d, mig len=%d, first diff at %d)\nGO : ...%s\nMIG: ...%s",
			len(goSQL), len(migSQL), cut, goSQL[lo:min(cut+80, len(goSQL))], migSQL[lo:min(cut+80, len(migSQL))])
	}
}

// TestProbeHealthDashboardViewsDown_CoversAllObjects guards the 716 down
// migration against a partial rollback (A-1, R36): it must recreate every
// object the up migration touches, otherwise a post-rollback restart gap
// would 42P01 on /probe/model/{model}/nodes, /timeline and /summary until
// the gateway's next ensure pass.
func TestProbeHealthDashboardViewsDown_CoversAllObjects(t *testing.T) {
	b, err := os.ReadFile("../sql/migrations/startup/716_unify_probe_health_views.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	down := string(b)
	// v_node_probe_state_compat 是 716 新增对象：down 必须DROP它（恢复 716 前
	// 的缺省形态），而不是重建——所以它只允许出现在 DROP 子句里。
	if !strings.Contains(down, "DROP VIEW IF EXISTS v_node_probe_state_compat CASCADE") {
		t.Error("716 down must drop v_node_probe_state_compat (object did not exist pre-716)")
	}
	for _, obj := range []string{
		"CREATE OR REPLACE VIEW v_model_health_dashboard",
		"CREATE OR REPLACE VIEW v_probe_queue_snapshot",
		"CREATE OR REPLACE VIEW v_model_priority_details",
		"CREATE OR REPLACE VIEW v_probe_system_health",
		"CREATE OR REPLACE VIEW v_model_availability_timeline",
		"CREATE OR REPLACE FUNCTION get_model_state_summary",
	} {
		if !strings.Contains(down, obj) {
			t.Errorf("716 down missing old-body definition: %s", obj)
		}
	}
}
