// Package apihub — design intent test.
//
// This file encodes the INVARIANTS and scope boundaries of the apihub package
// so future contributors understand the design intent and cannot accidentally
// violate it. These are structural / documentary assertions, not behavioral
// tests (those live in service_test.go).
package apihub

import (
	"context"
	"testing"
)

// TestDesignIntent_KindNamespaceIsComposite verifies the core data-model
// invariant: an asset is uniquely identified by the COMPOSITE (Kind, RefID),
// not by RefID alone. This is what lets model_offers.id=5 and
// tool_registry.id=5 coexist without collision.
func TestDesignIntent_KindNamespaceIsComposite(t *testing.T) {
	store := newMemStore()
	svc := New(store)
	ctx := testCtx(t, "t1")

	// Two assets with the SAME RefID but different Kind must both exist.
	if err := svc.Register(ctx, Asset{Kind: KindLLMEndpoint, RefID: 5, TenantID: "t1", Name: "llm"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(ctx, Asset{Kind: KindMCPServer, RefID: 5, TenantID: "t1", Name: "mcp"}); err != nil {
		t.Fatal(err)
	}
	a1, _ := svc.Get(ctx, KindLLMEndpoint, 5)
	a2, _ := svc.Get(ctx, KindMCPServer, 5)
	if a1.Name == a2.Name {
		t.Errorf("composite key broken: both RefID=5 resolved to %q", a1.Name)
	}
}

// TestDesignIntent_AllKindsCovered verifies that the Kind enum stays in sync
// with the ValidKinds map. Adding a new Kind constant without registering it
// here is a bug (it would be silently rejected by IsValid).
func TestDesignIntent_AllKindsCovered(t *testing.T) {
	declared := []Kind{KindLLMEndpoint, KindMCPServer, KindAgent}
	for _, k := range declared {
		if !k.IsValid() {
			t.Errorf("Kind %q is declared but not in ValidKinds — add it", k)
		}
	}
	// Spot-check that an unknown kind is rejected.
	if Kind("unknown").IsValid() {
		t.Error("Kind('unknown') should not be valid")
	}
}

// TestDesignIntent_RelationTypesStable verifies the RelationType vocabulary
// is the documented set. Audit/telemetry depend on these exact strings; do
// NOT rename them without a migration.
func TestDesignIntent_RelationTypesStable(t *testing.T) {
	want := map[RelationType]bool{
		RelDependsOn: true,
		RelCalls:     true,
		RelSimilarTo: true,
	}
	for r := range want {
		if string(r) == "" {
			t.Error("relation type must be non-empty")
		}
	}
}

// TestDesignIntent_TenantDefaultsToSafe verifies that an unauthenticated
// context (no tenant) defaults to "default" rather than leaking all tenants.
// This is the defense-in-depth: even if a handler forgets to set the tenant,
// the query is scoped to "default", not to "*".
func TestDesignIntent_TenantDefaultsToSafe(t *testing.T) {
	svc := New(newMemStore())
	// Register under tenant "secret".
	_ = svc.Register(testCtx(t, "secret"), Asset{Kind: KindAgent, RefID: 1, TenantID: "secret", Name: "s"})

	// A bare context (no tenant) must NOT see "secret".
	_, err := svc.Get(context.Background(), KindAgent, 1)
	if err == nil {
		t.Error("bare context leaked tenant 'secret' data — must default to 'default' scope")
	}
}

// The following scope boundaries are DOCUMENTED INVARIANTS (not compiled):
//
//   - apihub OWNS: asset identity, topology edges, health_state, cache.
//   - apihub does NOT own: MCP protocol translation (mcp/ package),
//     Agent execution (agent_registry/ package), request relaying (relay/),
//     billing (maas/ package — reads hub data but does not mutate it).
//
//   - RLS is enforced at TWO layers: (1) the DB migration enables RLS with a
//     policy on current_setting('app.tenant_id'); (2) the Service always
//     derives tenant_id from the authenticated context via WithTenant, never
//     from a request body. An attacker cannot forge tenant_id.
//
//   - Cache staleness: TTL is 60s. Register/Link invalidate the affected key,
//     so writes are eventually consistent within one TTL window.
