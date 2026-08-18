package store

import (
	"context"
	"testing"
)

// Schema-aware read precedence for PipelineNodeViews (doc 14 §5.2.1/§5.3).
// miniredis substitute: proves precedence routing only, not real Redis
// pipeline semantics.

func seedNode(t *testing.T, s *Store, key, available string) {
	t.Helper()
	if err := s.rdb.HSet(context.Background(), key,
		"available", available, "source_priority", "10", "generation", "1").Err(); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

func TestPipelineNodeViewsDualReadsCanonicalFirst(t *testing.T) {
	s, _ := newTestStore(t)
	s.SetKeySchemaMode(KeySchemaModeDual)
	ctx := context.Background()

	legacyOnly := NodeQuery{TenantID: "legacy-only", CredentialID: 1, RawModel: "m"}
	bothLegacy := NodeQuery{TenantID: "both", CredentialID: 2, RawModel: "m"}
	bothK2 := NodeQuery{TenantID: "both", CredentialID: 2, RawModel: "m"}

	legacySetOnly := NodeKeySetForTenant("ursm:v2:", legacyOnly.TenantID, legacyOnly.CredentialID, legacyOnly.RawModel)
	seedNode(t, s, legacySetOnly.Node, "1")

	both := NodeKeySetForTenant("ursm:v2:", bothLegacy.TenantID, bothLegacy.CredentialID, bothLegacy.RawModel)
	k2Both, err := K2KeySetForTenant("ursm:v2:", bothLegacy.TenantID, bothLegacy.CredentialID, bothLegacy.RawModel)
	if err != nil {
		t.Fatalf("k2 set: %v", err)
	}
	seedNode(t, s, both.Node, "0")
	seedNode(t, s, k2Both.Node, "1")

	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{legacyOnly, bothK2})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	// Canonical missing → exact-tuple legacy fallback (dual mode).
	if !views[0].Available {
		t.Fatalf("legacy-only node must be readable via fallback, got %+v", views[0])
	}
	// Both present → canonical wins.
	if !views[1].Available {
		t.Fatalf("canonical value must win when both grammars exist, got %+v", views[1])
	}
}

func TestPipelineNodeViewsCanonicalModeDoesNotFallBack(t *testing.T) {
	s, _ := newTestStore(t)
	s.SetKeySchemaMode(KeySchemaModeCanonical)
	ctx := context.Background()

	legacyOnly := NodeQuery{TenantID: "legacy-only", CredentialID: 1, RawModel: "m"}
	set := NodeKeySetForTenant("ursm:v2:", legacyOnly.TenantID, legacyOnly.CredentialID, legacyOnly.RawModel)
	seedNode(t, s, set.Node, "1")

	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{legacyOnly})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if views[0].Available {
		t.Fatalf("canonical mode must not fall back to the legacy key, got %+v", views[0])
	}
}

func TestPipelineNodeViewsLegacyModeReadsLegacyKeys(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	q := NodeQuery{TenantID: "t", CredentialID: 1, RawModel: "m"}
	set := NodeKeySetForTenant("ursm:v2:", q.TenantID, q.CredentialID, q.RawModel)
	k2, err := K2KeySetForTenant("ursm:v2:", q.TenantID, q.CredentialID, q.RawModel)
	if err != nil {
		t.Fatalf("k2 set: %v", err)
	}
	seedNode(t, s, set.Node, "0")
	seedNode(t, s, k2.Node, "1")

	views, err := s.PipelineNodeViews(ctx, "ursm:v2:", []NodeQuery{q})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if views[0].Available {
		t.Fatalf("legacy mode must read the legacy key only, got %+v", views[0])
	}
}
