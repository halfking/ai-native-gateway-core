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
