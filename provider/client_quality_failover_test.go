package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateQueryKeepsDegradedSiblingForFailover(t *testing.T) {
	path := filepath.Join("client.go")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read candidate query: %v", err)
	}

	query := string(body)
	if strings.Contains(query, "AND rsr.samples >= 20") {
		t.Fatal("candidate query must not hard-exclude a low-success sibling")
	}
	if !strings.Contains(query, "COALESCE(rsr.rate, mo.success_rate, 0.9) DESC") {
		t.Fatal("candidate query must sort degraded siblings by recent success rate")
	}
	if !strings.Contains(query, "AND v.is_routable = TRUE") {
		t.Fatal("candidate query must retain authoritative hard availability filtering")
	}
}

func TestModelRoutingDiagnosticUsesBindingSafeSchema(t *testing.T) {
	path := filepath.Join("..", "admin", "model_routing_diagnostic.go")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read model routing diagnostic: %v", err)
	}

	query := string(body)
	for _, forbidden := range []string{"FROM request_logs\n"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("diagnostic query contains stale schema reference %q", forbidden)
		}
	}
	for _, required := range []string{
		"cmb.id AS binding_id",
		"SELECT t.binding_id, t.credential_id",
		"FROM request_logs_with_current_month",
		"outbound_model",
		"client_model",
		"canonical_model",
		"AND ts > $2",
		"model_name_mapping",
		"model_aliases",
		"models_canonical",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("diagnostic query must contain %q", required)
		}
	}
}
