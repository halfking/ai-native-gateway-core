package recovery

import (
	"context"
	"testing"
)

func TestWarmupFromCoverageRequiresCompleteManifest(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()
	key := "ursm:v2:node:t:123:7:model"
	if err := m.rdb.SAdd(ctx, "ursm:v2:meta:coverage", key).Err(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := m.rdb.Set(ctx, "ursm:v2:meta:ready", "1", 0).Err(); err != nil {
		t.Fatalf("stale ready: %v", err)
	}
	if _, err := m.WarmupFromCoverage(ctx); err == nil {
		t.Fatal("missing coverage node must refuse authoritative gate")
	}
	if m.Ready(ctx) {
		t.Fatal("gate must remain closed after incomplete coverage")
	}
	if err := m.rdb.HSet(ctx, key, "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("node: %v", err)
	}
	count, err := m.WarmupFromCoverage(ctx)
	if err != nil || count != 1 {
		t.Fatalf("complete coverage warmup count=%d err=%v", count, err)
	}
	if !m.Ready(ctx) {
		t.Fatal("gate must open after complete coverage")
	}
}

func TestValidateCoverageRejectsLegacyOnlyKey(t *testing.T) {
	m := New(newMini(t), "ursm:v2:")
	ctx := context.Background()
	if err := m.rdb.SAdd(ctx, "ursm:v2:meta:coverage", "ursm:v2:node:7:model").Err(); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := m.rdb.HSet(ctx, "ursm:v2:node:7:model", "generation", "1", "available", "1").Err(); err != nil {
		t.Fatalf("legacy node: %v", err)
	}
	if _, err := m.ValidateCoverage(ctx); err == nil {
		t.Fatal("legacy-only coverage key must be rejected")
	}
}
