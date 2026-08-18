package migration

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

func TestMetadataInitializeAndAdvanceCheckpoint(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	m := NewMetadata(rdb, "p:")
	initial := Metadata{
		Owner:             "halfking",
		LedgerID:          "ursm-v2-k2-20260818-001",
		Mode:              store.KeySchemaModeLegacy,
		CutoverEpoch:      0,
		PreflightChecksum: "abc123",
		RollbackDeadline:  "2026-09-01T00:00:00Z",
		Checkpoint:        CheckpointPreflight,
	}
	if err := m.Initialize(ctx, initial); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	got, err := m.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Owner != initial.Owner || got.LedgerID != initial.LedgerID || got.Mode != store.KeySchemaModeLegacy || got.Checkpoint != CheckpointPreflight {
		t.Fatalf("metadata = %+v", got)
	}
	if err := m.Advance(ctx, 0, CheckpointCopy, store.KeySchemaModeDual); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, err = m.Read(ctx)
	if err != nil || got.CutoverEpoch != 1 || got.Checkpoint != CheckpointCopy || got.Mode != store.KeySchemaModeDual {
		t.Fatalf("advanced metadata = %+v err=%v", got, err)
	}
}

func TestMetadataInitializeCannotReplaceLedgerIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	m := NewMetadata(rdb, "p:")
	first := Metadata{Owner: "halfking", LedgerID: "one", Mode: store.KeySchemaModeLegacy, Checkpoint: CheckpointPreflight}
	if err := m.Initialize(ctx, first); err != nil {
		t.Fatalf("first initialize: %v", err)
	}
	second := first
	second.LedgerID = "two"
	if err := m.Initialize(ctx, second); err == nil {
		t.Fatal("metadata initialize must reject a different, non-reusable ledger ID")
	}
}

func TestMetadataAdvanceFencesStaleEpoch(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	m := NewMetadata(rdb, "p:")
	if err := m.Initialize(ctx, Metadata{Owner: "halfking", LedgerID: "one", Mode: store.KeySchemaModeLegacy, Checkpoint: CheckpointPreflight}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := m.Advance(ctx, 0, CheckpointCopy, store.KeySchemaModeDual); err != nil {
		t.Fatalf("first advance: %v", err)
	}
	if err := m.Advance(ctx, 0, CheckpointRollback, store.KeySchemaModeLegacy); err == nil {
		t.Fatal("stale epoch must be fenced, not overwrite a newer checkpoint")
	}
}
