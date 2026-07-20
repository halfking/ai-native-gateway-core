package credentialstate

import (
	"testing"
	"time"
)

func TestDedupByCredentialModel_NoDuplicates(t *testing.T) {
	ts := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	in := []StateUpdate{
		{CredentialID: 1, Model: "a", UpdatedAt: ts},
		{CredentialID: 2, Model: "b", UpdatedAt: ts},
		{CredentialID: 3, Model: "c", UpdatedAt: ts},
	}
	out := dedupByCredentialModel(in)
	if len(out) != 3 {
		t.Fatalf("len=%d want 3 (no dedup needed)", len(out))
	}
	for i, u := range out {
		if u.CredentialID != in[i].CredentialID || u.Model != in[i].Model {
			t.Errorf("out[%d] != in[%d]", i, i)
		}
	}
}

func TestDedupByCredentialModel_DedupKeepsLatestUpdatedAt(t *testing.T) {
	t0 := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Second)
	t2 := t0.Add(2 * time.Second)
	in := []StateUpdate{
		{CredentialID: 1, Model: "a", UpdatedAt: t0, HealthStatus: strPtr("first")},
		{CredentialID: 2, Model: "b", UpdatedAt: t2, HealthStatus: strPtr("newest-b")},
		{CredentialID: 1, Model: "a", UpdatedAt: t2, HealthStatus: strPtr("newest-a")}, // collides with idx 0
		{CredentialID: 2, Model: "b", UpdatedAt: t1, HealthStatus: strPtr("middle-b")}, // collides with idx 1
		{CredentialID: 3, Model: "c", UpdatedAt: t0, HealthStatus: strPtr("lonely-c")},
	}
	out := dedupByCredentialModel(in)
	if len(out) != 3 {
		t.Fatalf("len=%d want 3 (deduped 5→3)", len(out))
	}
	// Output must remain in input order of the *winning* occurrences, sorted
	// by the index of the chosen row. The winning indices in this fixture
	// are 2 (newest 1/a, idx=2), 1 (newest 2/b, idx=1), 4 (lonely 3/c, idx=4).
	// Sorting ascending by idx → [1, 2, 4], so out order is:
	//   idx 1 (newest 2/b) wins  → first
	//   idx 2 (newest 1/a) wins  → second
	//   idx 4 (lonely 3/c) wins  → third
	want := []struct {
		cred int
		mdl  string
		hp   string
	}{
		{2, "b", "newest-b"},
		{1, "a", "newest-a"},
		{3, "c", "lonely-c"},
	}
	for i, w := range want {
		if out[i].CredentialID != w.cred || out[i].Model != w.mdl || out[i].HealthStatus == nil || *out[i].HealthStatus != w.hp {
			t.Errorf("out[%d] = (%d,%s,%v); want (%d,%s,%s)",
				i, out[i].CredentialID, out[i].Model, derefStr(out[i].HealthStatus),
				w.cred, w.mdl, w.hp)
		}
	}
}

func TestDedupByCredentialModel_TiePrefersLaterOccurrence(t *testing.T) {
	ts := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	in := []StateUpdate{
		{CredentialID: 1, Model: "a", UpdatedAt: ts, HealthStatus: strPtr("first")},
		{CredentialID: 1, Model: "a", UpdatedAt: ts, HealthStatus: strPtr("second-tied")},
	}
	out := dedupByCredentialModel(in)
	if len(out) != 1 {
		t.Fatalf("len=%d want 1", len(out))
	}
	if out[0].HealthStatus == nil || *out[0].HealthStatus != "second-tied" {
		t.Errorf("tie should prefer later occurrence, got %v", derefStr(out[0].HealthStatus))
	}
}

// TestDedupByCredentialModel_ExhaustivelyShapedFor21000 reproduces the exact
// shape that triggered PG SQLSTATE 21000 in the 2026-07-20 S07/S12 load test:
// a single batch whose input contained duplicate (credential_id, model)
// pairs. After dedup, the resulting slice can be safely executed as a
// multi-row INSERT ... ON CONFLICT DO UPDATE.
func TestDedupByCredentialModel_ExhaustivelyShapedFor21000(t *testing.T) {
	ts := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	// Build a 100-row batch with 50 distinct (credential_id, model) pairs,
	// each appearing twice (to mimic concurrent probe writes for the same
	// credential+model).
	in := make([]StateUpdate, 0, 100)
	for i := 0; i < 50; i++ {
		in = append(in,
			StateUpdate{CredentialID: 9010 + i, Model: "gpt-4", UpdatedAt: ts},
			StateUpdate{CredentialID: 9010 + i, Model: "gpt-4", UpdatedAt: ts.Add(time.Duration(i+1) * time.Second)},
		)
	}
	if got := len(dedupByCredentialModel(in)); got != 50 {
		t.Fatalf("dedup result len=%d, want 50 (each pair collapsed to 1)", got)
	}
}

func strPtr(s string) *string { return &s }
func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}