package recovery

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Schema-aware coverage and warmup gates (doc 14 §5.3: in canonical mode
// the coverage manifest only carries canonical node keys, and only
// canonical, complete, pending-free coverage may open the authoritative
// ready gate). miniredis substitute evidence.

func TestValidateCoverageCanonicalModeRejectsLegacyKeys(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	m.SetKeySchemaMode(store.KeySchemaModeCanonical)
	ctx := context.Background()
	key := "ursm:v2:node:tenant-a:7:model"
	if err := m.rdb.SAdd(ctx, "ursm:v2:meta:coverage", key).Err(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := m.rdb.HSet(ctx, key, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("node: %v", err)
	}
	if _, err := m.ValidateCoverage(ctx); err == nil {
		t.Fatal("canonical mode must reject a legacy coverage key")
	}
}

func TestValidateCoverageCanonicalModeAcceptsK2Keys(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	m.SetKeySchemaMode(store.KeySchemaModeCanonical)
	ctx := context.Background()
	k2Key, err := store.K2NodeKeyForTenant("ursm:v2:", "tenant-a", 7, "model")
	if err != nil {
		t.Fatalf("k2 key: %v", err)
	}
	if err := m.rdb.SAdd(ctx, "ursm:v2:meta:coverage", k2Key).Err(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := m.rdb.HSet(ctx, k2Key, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("node: %v", err)
	}
	count, err := m.ValidateCoverage(ctx)
	if err != nil || count != 1 {
		t.Fatalf("canonical coverage count=%d err=%v", count, err)
	}
}

func TestValidateCoverageLegacyModeStillAcceptsLegacyKeys(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()
	key := "ursm:v2:node:tenant-a:7:model"
	if err := m.rdb.SAdd(ctx, "ursm:v2:meta:coverage", key).Err(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := m.rdb.HSet(ctx, key, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("node: %v", err)
	}
	count, err := m.ValidateCoverage(ctx)
	if err != nil || count != 1 {
		t.Fatalf("legacy coverage count=%d err=%v", count, err)
	}
}

func TestWarmupCanonicalModeRequiresCanonicalState(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	m.SetKeySchemaMode(store.KeySchemaModeCanonical)
	ctx := context.Background()
	// Only legacy state survives: canonical warmup must refuse rather
	// than reopen the gate over legacy-only keys.
	if err := m.rdb.HSet(ctx, "ursm:v2:node:tenant-a:7:model", "generation", "1").Err(); err != nil {
		t.Fatalf("legacy node: %v", err)
	}
	if _, err := m.WarmupFromExistingKeys(ctx); err == nil {
		t.Fatal("canonical warmup must refuse when only legacy node state exists")
	}
	k2Key, err := store.K2NodeKeyForTenant("ursm:v2:", "tenant-a", 7, "model")
	if err != nil {
		t.Fatalf("k2 key: %v", err)
	}
	if err := m.rdb.HSet(ctx, k2Key, "generation", "1").Err(); err != nil {
		t.Fatalf("k2 node: %v", err)
	}
	n, err := m.WarmupFromExistingKeys(ctx)
	if err != nil {
		t.Fatalf("warmup with canonical state: %v", err)
	}
	if n != 1 {
		t.Fatalf("canonical warmup count=%d, want 1 (legacy twins must not count)", n)
	}
}

func TestWarmupDualModeCountsCanonicalTuplesOnce(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	m.SetKeySchemaMode(store.KeySchemaModeDual)
	ctx := context.Background()
	if err := m.rdb.HSet(ctx, "ursm:v2:node:tenant-a:7:model", "generation", "1").Err(); err != nil {
		t.Fatalf("legacy node: %v", err)
	}
	k2Key, err := store.K2NodeKeyForTenant("ursm:v2:", "tenant-a", 7, "model")
	if err != nil {
		t.Fatalf("k2 key: %v", err)
	}
	if err := m.rdb.HSet(ctx, k2Key, "generation", "1").Err(); err != nil {
		t.Fatalf("k2 node: %v", err)
	}
	n, err := m.WarmupFromExistingKeys(ctx)
	if err != nil {
		t.Fatalf("warmup: %v", err)
	}
	// One logical tuple mirrored in both grammars warms up as one node.
	if n != 1 {
		t.Fatalf("dual warmup count=%d, want 1 (deduped by logical tuple)", n)
	}
}
