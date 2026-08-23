package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Unit tests — no PG required.

func TestParseScopeRevision_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		version int64
		hash    string
	}{
		{name: "version 1 with full hash", token: "1:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", version: 1, hash: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
		{name: "version 42 short hash", token: "42:abcdef", version: 42, hash: "abcdef"},
		{name: "trailing colons preserved", token: "5:abc:def", version: 5, hash: "abc:def"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sr, err := parseScopeRevision(tc.token)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if sr.Version != tc.version {
				t.Errorf("Version = %d, want %d", sr.Version, tc.version)
			}
			if sr.Hash != tc.hash {
				t.Errorf("Hash = %q, want %q", sr.Hash, tc.hash)
			}
			if sr.Raw != tc.token {
				t.Errorf("Raw = %q, want %q", sr.Raw, tc.token)
			}
		})
	}
}

func TestParseScopeRevision_Malformed(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "whitespace only", token: "   "},
		{name: "no colon", token: "12abcdef"},
		{name: "missing version", token: ":abc"},
		{name: "missing hash", token: "12:"},
		{name: "non-numeric version", token: "abc:def"},
		{name: "zero version", token: "0:abc"},
		{name: "negative version", token: "-1:abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseScopeRevision(tc.token); err == nil {
				t.Errorf("parseScopeRevision(%q) returned no error; expected malformed", tc.token)
			}
		})
	}
}

func TestScopeRevisionsEqual(t *testing.T) {
	a := scopeRevision{Version: 7, Hash: "abc"}
	b := scopeRevision{Version: 7, Hash: "abc"}
	c := scopeRevision{Version: 7, Hash: "xyz"}
	d := scopeRevision{Version: 8, Hash: "abc"}
	zero1 := scopeRevision{}
	zero2 := scopeRevision{}

	if !scopeRevisionsEqual(a, b) {
		t.Error("equal revisions should compare true")
	}
	if scopeRevisionsEqual(a, c) {
		t.Error("different hash should compare false")
	}
	if scopeRevisionsEqual(a, d) {
		t.Error("different version should compare false")
	}
	if !scopeRevisionsEqual(zero1, zero2) {
		t.Error("zero revisions (no row yet) should compare true against each other")
	}
	if scopeRevisionsEqual(zero1, a) {
		t.Error("zero vs populated should compare false")
	}
}

func TestFormatScopeRevision(t *testing.T) {
	if got := formatScopeRevision(0, "abc"); got != "" {
		t.Errorf("formatScopeRevision(0, _) = %q, want empty", got)
	}
	if got := formatScopeRevision(3, "abc"); got != "3:abc" {
		t.Errorf("formatScopeRevision(3, abc) = %q, want %q", got, "3:abc")
	}
}

func TestSingleRawModelForRevision(t *testing.T) {
	cases := []struct {
		name   string
		models []string
		want   string
		ok     bool
	}{
		{name: "empty", models: nil, ok: false},
		{name: "single", models: []string{"gpt-5.6-terra"}, want: "gpt-5.6-terra", ok: true},
		{name: "same raw", models: []string{"gpt-5.6-terra", "gpt-5.6-terra"}, want: "gpt-5.6-terra", ok: true},
		{name: "mixed", models: []string{"gpt-5.6-terra", "gpt-5.6"}, want: "", ok: false},
		{name: "blank first", models: []string{"", "gpt-5.6-terra"}, want: "", ok: false},
		{name: "trim-equal", models: []string{" gpt-5.6-terra ", "gpt-5.6-terra"}, want: "gpt-5.6-terra", ok: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := singleRawModelForRevision(tc.models)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("singleRawModelForRevision(%v) = (%q, %v), want (%q, %v)", tc.models, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestEnsureScopeRevision_SeedsMissingRow deletes the revision row then
// confirms ensureScopeRevision recreates version=1 without bumping an
// existing revision on a second call.
func TestSingleCanonicalForRevision(t *testing.T) {
	idPtr := func(v int64) *int64 { return &v }
	cases := []struct {
		name string
		ids  []*int64
		want int64
		ok   bool
	}{
		{name: "empty", ids: nil, ok: false},
		{name: "single", ids: []*int64{idPtr(173264)}, want: 173264, ok: true},
		{name: "same canonical", ids: []*int64{idPtr(173264), idPtr(173264)}, want: 173264, ok: true},
		{name: "mixed canonical", ids: []*int64{idPtr(173264), idPtr(192049)}, ok: false},
		{name: "zero canonical", ids: []*int64{idPtr(0)}, ok: false},
		{name: "nil canonical", ids: []*int64{nil}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidates := make([]resolveCandidate, len(tc.ids))
			for i, id := range tc.ids {
				candidates[i].CanonicalID = id
			}
			got, ok := singleCanonicalForRevision(candidates)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("singleCanonicalForRevision(%v) = (%d, %v), want (%d, %v)", tc.ids, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestEnsureCanonicalScopeRevision_SeedsMissingRow(t *testing.T) {
	pool := reorderTestPool(t)
	f := newReorderTestFixture(t, pool, 2)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `DELETE FROM public.candidate_binding_scope_revision_canonical WHERE canonical_id = $1`, f.canonicalID)
	if err != nil {
		t.Fatalf("delete canonical revision: %v", err)
	}
	before, err := loadCanonicalScopeRevision(ctx, pool, f.canonicalID)
	if err != nil {
		t.Fatalf("load canonical after delete: %v", err)
	}
	if before.Raw != "" {
		t.Fatalf("expected empty canonical revision after delete, got %q", before.Raw)
	}

	seeded, err := ensureCanonicalScopeRevision(ctx, pool, f.canonicalID)
	if err != nil {
		t.Fatalf("ensure canonical: %v", err)
	}
	if !strings.HasPrefix(seeded.Raw, "1:") {
		t.Fatalf("seeded canonical revision = %q, want prefix 1:", seeded.Raw)
	}

	again, err := ensureCanonicalScopeRevision(ctx, pool, f.canonicalID)
	if err != nil {
		t.Fatalf("ensure canonical again: %v", err)
	}
	if again.Raw != seeded.Raw {
		t.Fatalf("second canonical ensure changed revision: %q -> %q", seeded.Raw, again.Raw)
	}
}

// Integration tests — require LLM_GATEWAY_PG_URL. They use the same pool
// and fixture helpers as routing_candidate_binding_test.go so a missing PG
// cleanly SKIPs via reorderTestPool's t.Skip path.

// TestRoutingCandidateBindingReorder_IntegrationBumpMonotonic drives two
// consecutive reorders and asserts scope_version advances from 1 to 2 to 3.
// This proves the migration 566 canonical trigger fires per write and the
// wire token advances monotonically.
func TestRoutingCandidateBindingReorder_IntegrationBumpMonotonic(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderTestFixture(t, pool, 2)

	revV1 := f.reorderRevision(t, h)
	if !strings.HasPrefix(revV1, "1:") {
		t.Fatalf("initial revision = %q, want prefix %q", revV1, "1:")
	}

	first := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: revV1,
		Items: []routingCandidateReorderItem{
			{CredentialID: int(f.credIDs[1]), ManualPriority: 1},
			{CredentialID: int(f.credIDs[0]), ManualPriority: 2},
		},
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first reorder status = %d, body = %s", first.Code, first.Body.String())
	}
	revV2 := f.reorderRevision(t, h)
	if !strings.HasPrefix(revV2, "2:") {
		t.Fatalf("after first reorder, revision = %q, want prefix %q", revV2, "2:")
	}

	second := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: revV2,
		Items: []routingCandidateReorderItem{
			{CredentialID: int(f.credIDs[0]), ManualPriority: 1},
			{CredentialID: int(f.credIDs[1]), ManualPriority: 2},
		},
	})
	if second.Code != http.StatusOK {
		t.Fatalf("second reorder status = %d, body = %s", second.Code, second.Body.String())
	}
	revV3 := f.reorderRevision(t, h)
	if !strings.HasPrefix(revV3, "3:") {
		t.Fatalf("after second reorder, revision = %q, want prefix %q", revV3, "3:")
	}
}

// TestRoutingCandidateBindingReorder_IntegrationResolveEcho resolves the
// raw_model twice with a reorder in between and confirms the resolve's
// reorder_revision advances by exactly one each time. This is the contract
// the dashboard's drag-and-drop relies on for 409 detection.
func TestRoutingCandidateBindingReorder_IntegrationResolveEcho(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderTestFixture(t, pool, 2)

	// First resolve: should see version 1.
	revV1 := f.reorderRevision(t, h)
	if !strings.HasPrefix(revV1, "1:") {
		t.Fatalf("initial resolve revision = %q, want prefix %q", revV1, "1:")
	}

	// Drive one reorder through the handler.
	rec := doReorder(t, h, routingCandidateReorderRequest{
		CanonicalID:      f.canonicalID,
		ExpectedRevision: revV1,
		Items: []routingCandidateReorderItem{
			{CredentialID: int(f.credIDs[1]), ManualPriority: 1},
			{CredentialID: int(f.credIDs[0]), ManualPriority: 2},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Second resolve should now echo version 2 (the trigger bumped the
	// singleton revision row inside the same statement as our UPDATE).
	revV2 := f.reorderRevision(t, h)
	if !strings.HasPrefix(revV2, "2:") {
		t.Fatalf("post-reorder resolve revision = %q, want prefix %q", revV2, "2:")
	}
	if revV2 == revV1 {
		t.Fatalf("reorder_revision did not advance after a successful reorder: %q == %q", revV1, revV2)
	}
}

// TestRoutingCandidateBindingReorder_IntegrationNoOpBump updates a binding
// row WITHOUT changing manual_priority / provider_model_id / credential_id
// (only updated_at changes) and confirms scope_version does NOT advance.
// This is the trigger's short-circuit clause and is critical: if it were
// missing, every cache refresh would force a 409 refetch on the dashboard.
func TestRoutingCandidateBindingReorder_IntegrationNoOpBump(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderTestFixture(t, pool, 2)

	revBefore := f.reorderRevision(t, h)
	if !strings.HasPrefix(revBefore, "1:") {
		t.Fatalf("initial revision = %q, want prefix %q", revBefore, "1:")
	}

	// Touch ONLY updated_at. The trigger must short-circuit.
	_, err := pool.Exec(context.Background(),
		`UPDATE credential_model_bindings SET updated_at = NOW() WHERE id = $1`,
		f.bindingIDs[0])
	if err != nil {
		t.Fatalf("no-op update: %v", err)
	}

	// Give the trigger's clock a beat so we never race a same-millisecond bump.
	time.Sleep(2 * time.Millisecond)

	revAfter := f.reorderRevision(t, h)
	if revAfter != revBefore {
		t.Fatalf("no-op update bumped scope_revision: before=%q after=%q", revBefore, revAfter)
	}

	// Sanity: a real manual_priority change DOES still bump the counter.
	_, err = pool.Exec(context.Background(),
		`UPDATE credential_model_bindings SET manual_priority = 9, updated_at = NOW() WHERE id = $1`,
		f.bindingIDs[0])
	if err != nil {
		t.Fatalf("real update: %v", err)
	}
	revBumped := f.reorderRevision(t, h)
	if !strings.HasPrefix(revBumped, "2:") {
		t.Fatalf("real update did not bump scope_revision: got %q, want prefix %q", revBumped, "2:")
	}

	// Sanity: flipping the boolean priority flag also bumps the counter —
	// the 568 predicate treats priority as a scope-affecting routing input.
	_, err = pool.Exec(context.Background(),
		`UPDATE credential_model_bindings SET priority = NOT priority, updated_at = NOW() WHERE id = $1`,
		f.bindingIDs[0])
	if err != nil {
		t.Fatalf("priority flip update: %v", err)
	}
	revPriority := f.reorderRevision(t, h)
	if !strings.HasPrefix(revPriority, "3:") {
		t.Fatalf("priority flip did not bump scope_revision: got %q, want prefix %q", revPriority, "3:")
	}
}

// TestRoutingCandidateBindingReorder_IntegrationCanonicalPriorityBump
// locks in the canonical-scope-revision contract introduced by migration
// 571: a credential_model_bindings UPDATE that flips ONLY cmb.priority
// (with manual_priority / provider_model_id / credential_id untouched)
// must advance candidate_binding_scope_revision_canonical.scope_version
// and change scope_hash. Prior to 571, the canonical update function's
// predicate excluded priority, so a priority-only flip silently bypassed
// the trigger and drag-reorder OCC could not detect priority drift.
func TestRoutingCandidateBindingReorder_IntegrationCanonicalPriorityBump(t *testing.T) {
	pool := reorderTestPool(t)
	h := &Handler{db: pool}
	f := newReorderTestFixture(t, pool, 2)
	ctx := context.Background()

	revBefore := f.reorderRevision(t, h)
	if !strings.HasPrefix(revBefore, "1:") {
		t.Fatalf("initial canonical revision = %q, want prefix %q", revBefore, "1:")
	}
	hashBefore := strings.TrimPrefix(revBefore, "1:")

	// Flip only priority on a single binding — no manual_priority /
	// provider_model_id / credential_id change, so the 569 predicate
	// (without the priority term) would have short-circuited the
	// canonical trigger. Migration 571 adds the priority clause to the
	// predicate and b.priority::text to the hash, so this UPDATE must
	// bump scope_version AND change scope_hash.
	_, err := pool.Exec(ctx,
		`UPDATE credential_model_bindings SET priority = NOT priority, updated_at = NOW() WHERE id = $1`,
		f.bindingIDs[0])
	if err != nil {
		t.Fatalf("priority-only flip: %v", err)
	}

	revAfter := f.reorderRevision(t, h)
	if !strings.HasPrefix(revAfter, "2:") {
		t.Fatalf("canonical revision did not advance after priority flip: got %q, want prefix %q", revAfter, "2:")
	}
	hashAfter := strings.TrimPrefix(revAfter, "2:")
	if hashAfter == hashBefore {
		t.Fatalf("canonical scope_hash unchanged after priority flip: %q", hashAfter)
	}

	// Sanity: a no-op (priority unchanged, only updated_at moves) must
	// still NOT bump the canonical revision. This guards the priority
	// predicate against accidentally widening to cover any row write.
	var storedPriority bool
	if err := pool.QueryRow(ctx,
		`SELECT priority FROM credential_model_bindings WHERE id = $1`, f.bindingIDs[0],
	).Scan(&storedPriority); err != nil {
		t.Fatalf("read priority: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE credential_model_bindings SET priority = $1, updated_at = NOW() WHERE id = $2`,
		storedPriority, f.bindingIDs[0],
	); err != nil {
		t.Fatalf("no-op priority write: %v", err)
	}

	revNoOp := f.reorderRevision(t, h)
	if !strings.HasPrefix(revNoOp, "2:") {
		t.Fatalf("no-op priority write bumped canonical revision: got %q, want prefix %q", revNoOp, "2:")
	}

	// Another priority flip should advance to 3:, confirming the trigger
	// keeps firing on every priority transition, not just the first one.
	if _, err := pool.Exec(ctx,
		`UPDATE credential_model_bindings SET priority = NOT priority, updated_at = NOW() WHERE id = $1`,
		f.bindingIDs[0],
	); err != nil {
		t.Fatalf("second priority flip: %v", err)
	}
	revAgain := f.reorderRevision(t, h)
	if !strings.HasPrefix(revAgain, "3:") {
		t.Fatalf("second priority flip did not bump canonical revision: got %q, want prefix %q", revAgain, "3:")
	}
}
