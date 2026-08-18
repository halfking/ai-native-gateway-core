package migration

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMetadataInitializeAndAdvanceCheckpoint(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	m := NewMetadataLua(rdb, "p:")
	initial := Metadata{
		Owner:             "halfking",
		LedgerID:          "ursm-v2-k2-20260818-001",
		Mode:              ModeLegacy,
		CutoverEpoch:      0,
		PreflightChecksum: "abc123",
		RollbackDeadline: time.Date(2026,9,1,0,0,0,0, time.UTC),
		Checkpoint:        CheckpointPreflight,
	}
	if err := m.Initialize(ctx, initial); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	got, err := m.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Owner != initial.Owner || got.LedgerID != initial.LedgerID || got.Mode != ModeLegacy || got.Checkpoint != CheckpointPreflight {
		t.Fatalf("metadata = %+v", got)
	}
	if err := m.Advance(ctx, 0, CheckpointCopy, storeKeySchemaModeFromMode(ModeDual)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, err = m.Read(ctx)
	if err != nil || got.CutoverEpoch != 1 || got.Checkpoint != CheckpointCopy || got.Mode != ModeDual {
		t.Fatalf("advanced metadata = %+v err=%v", got, err)
	}
}

func TestMetadataInitializeCannotReplaceLedgerIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	m := NewMetadataLua(rdb, "p:")
	first := Metadata{Owner: "halfking", LedgerID: "one", Mode: ModeLegacy, Checkpoint: CheckpointPreflight}
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
	m := NewMetadataLua(rdb, "p:")
	if err := m.Initialize(ctx, Metadata{Owner: "halfking", LedgerID: "one", Mode: ModeLegacy, Checkpoint: CheckpointPreflight}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := m.Advance(ctx, 0, CheckpointCopy, storeKeySchemaModeFromMode(ModeDual)); err != nil {
		t.Fatalf("first advance: %v", err)
	}
	if err := m.Advance(ctx, 0, CheckpointRollback, storeKeySchemaModeFromMode(ModeLegacy)); err == nil {
		t.Fatal("stale epoch must be fenced, not overwrite a newer checkpoint")
	}
}
