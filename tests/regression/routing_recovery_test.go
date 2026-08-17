package routing_test

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultRoutingSeedsUseRoutableFallbackModels(t *testing.T) {
	body, err := os.ReadFile("../../sql/migrations/startup/480_model_iq_cost_calibration.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "'fallback',  'minimax-m2.5'") {
		t.Fatal("default routing still seeds minimax-m2.5 as a fallback")
	}
	if !strings.Contains(s, "'fallback',  'deepseek-v4-flash'") {
		t.Fatal("default routing must retain a routable deepseek-v4-flash fallback")
	}

	body, err = os.ReadFile("../../sql/migrations/startup/491_work_type_default_routes.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "minimax-m2.5") {
		t.Fatal("work-type defaults still seed stale minimax-m2.5")
	}
}

func TestHealthyProbeReconciliationClearsNodeRouteGate(t *testing.T) {
	body, err := os.ReadFile("../../bg/model_probe_reconcile_healthy.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		"UPDATE node_probe_state",
		"last_direct_ok = TRUE",
		"paused = FALSE",
		"next_retry_at = now() + interval '1 hour'",
		"mps2.state = 'healthy_confirmed'",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("healthy reconciliation missing %q", want)
		}
	}
}

func TestWorkTypeSeedIsPerRouteIdempotent(t *testing.T) {
	body, err := os.ReadFile("../../sql/migrations/startup/491_work_type_default_routes.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "WHERE NOT EXISTS (\n    SELECT 1\n    FROM work_type_model_route") {
		t.Fatal("seed must not suppress all defaults when one route already exists")
	}
	if !strings.Contains(s, "AND NOT EXISTS (\n    SELECT 1 FROM work_type_model_route existing") {
		t.Fatal("seed must be idempotent per work-type/model pair")
	}
}
