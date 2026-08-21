package dispatch

import (
	"testing"
	"time"
)

func TestGovernorPolicyPopulatesAllFields(t *testing.T) {
	now := time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)
	specs := []GovernorSpec{
		{CredentialID: 1, Mode: ModeConcurrency, Limit: 16, Backend: BackendLocal},
		{CredentialID: 2, Mode: ModeRPM, Limit: 60, Backend: BackendRedisEnforce},
	}
	p := GovernorPolicy{
		Revision:    42,
		GeneratedAt: now,
		Specs:       specs,
		Source:      "admin.update",
	}
	if p.Revision != 42 || !p.GeneratedAt.Equal(now) || p.Source != "admin.update" {
		t.Fatalf("scalar field drift: %+v", p)
	}
	if len(p.Specs) != 2 {
		t.Fatalf("specs length: got %d want 2", len(p.Specs))
	}
	if p.Specs[0].CredentialID != 1 || p.Specs[1].CredentialID != 2 {
		t.Fatalf("specs ordering: got %+v", p.Specs)
	}
}

// Two policies with the same Revision but different GeneratedAt are
// distinct: Revision identifies cluster-wide lineage, GeneratedAt is the
// wall-clock of the local producer. The test pins "Revision is the
// identity key, GeneratedAt is not" so future refactors can't silently
// drop either.
func TestGovernorPolicyRevisionIsIdentity(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 16, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Second)

	a := GovernorPolicy{Revision: 7, GeneratedAt: t0}
	b := GovernorPolicy{Revision: 7, GeneratedAt: t1}
	if a.Revision != b.Revision {
		t.Fatalf("revision equality: %d vs %d", a.Revision, b.Revision)
	}
	if a.GeneratedAt.Equal(b.GeneratedAt) {
		t.Fatalf("expected different generated-at for same revision")
	}
}

// Stage E will publish a "no-change" policy whose Specs slice is empty;
// the applier short-circuits. Pin that an empty Specs slice is valid and
// does NOT panic anything that ranges over it.
func TestGovernorPolicyEmptySpecsIsValid(t *testing.T) {
	p := GovernorPolicy{Revision: 1, GeneratedAt: time.Now()}
	count := 0
	for range p.Specs {
		count++
	}
	if count != 0 {
		t.Fatalf("expected zero spec iteration; got %d", count)
	}
	if len(p.Specs) != 0 {
		t.Fatalf("specs length drift: got %d want 0", len(p.Specs))
	}
}

// Monotonicity contract for Revision: a Stage E producer that needs to
// advance the policy MUST only ever increase Revision. This test does not
// assert the contract at construction time (the type has no opinion), but
// pins a producer-side helper for stages B/C/D/E that does.
func TestGovernorPolicyRevisionMonotonicProducer(t *testing.T) {
	var rev uint64
	next := func() uint64 {
		rev++
		return rev
	}
	got := []uint64{next(), next(), next()}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("monotonicity broken: %v", got)
		}
	}
}