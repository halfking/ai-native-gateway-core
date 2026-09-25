package provider

import (
	"os"
	"strings"
	"testing"
)

// TestDumpCandidateSQL writes the exact candidate-build SQL (as assembled by
// candidateQuerySQL, with brokenPairExcludeSQL expanded and the three binding
// params substituted with literals) to /tmp so it can be EXPLAIN ANALYZEd
// against a real database byte-for-byte. Audit round 8 (D10): 纪律⑳ EXPLAIN
// 先于改写. Run with:
//
//	go test ./provider/ -run TestDumpCandidateSQL -count=1
//
// and then substitute the literals for the model under test via sed.
func TestDumpCandidateSQL(t *testing.T) {
	q := candidateQuerySQL()
	for _, want := range []string{
		"recent_success_rate(",
		"v_routable_credential_models v",
		"NOT EXISTS",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("dump missing %q", want)
		}
	}
	if got := strings.Count(q, "$1"); got == 0 {
		t.Fatal("no $1 binding params found")
	}
	if err := os.WriteFile("/tmp/d10-candidate-query.sql", []byte(q), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("written /tmp/d10-candidate-query.sql, bytes:", len(q))
}
