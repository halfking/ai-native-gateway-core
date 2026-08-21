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
