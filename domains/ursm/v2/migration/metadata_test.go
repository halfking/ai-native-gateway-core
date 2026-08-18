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
		RollbackDeadline:  time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
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

func TestMetadataWriteRejectsDifferentIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	store := &MetadataHash{Prefix: "p:", RDB: rdb}
	metadata := Metadata{
		Owner: "halfking", LedgerID: "ursm-v2-k2-test", Mode: ModeDual,
		CutoverEpoch: 1, StartedAt: time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 8, 18, 0, 1, 0, 0, time.UTC),
		Checkpoint: CheckpointPreflight, RollbackDeadline: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
	}
	if err := store.Write(ctx, metadata); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	metadata.LedgerID = "ursm-v2-k2-other"
	if err := store.Write(ctx, metadata); err == nil {
		t.Fatal("metadata write must reject a different ledger identity")
	}
}

func TestMetadataWriteRejectsRollbackDeadlineMutation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	store := &MetadataHash{Prefix: "p:", RDB: rdb}
	metadata := Metadata{
		Owner: "halfking", LedgerID: "ursm-v2-k2-test", Mode: ModeDual,
		CutoverEpoch: 1, StartedAt: time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 8, 18, 0, 1, 0, 0, time.UTC),
		Checkpoint: CheckpointPreflight, RollbackDeadline: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
	}
	if err := store.Write(ctx, metadata); err != nil {
		t.Fatalf("write deadline: %v", err)
	}
	metadata.RollbackDeadline = time.Time{}
	if err := store.Write(ctx, metadata); err == nil {
		t.Fatal("metadata write must reject rollback deadline mutation")
	}
	if value, err := rdb.HGet(ctx, MetadataKey("p:"), "rollback_deadline").Result(); err != nil || value == "" {
		t.Fatalf("rollback deadline field = %q err=%v, want original value retained", value, err)
	}
}

func TestMetadataHashCASCheckpointAdvancesEpoch(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	store := &MetadataHash{Prefix: "p:", RDB: rdb}
	metadata := Metadata{
		Owner: "halfking", LedgerID: "ursm-v2-k2-test", Mode: ModeDual,
		CutoverEpoch: 1, StartedAt: time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 8, 18, 0, 1, 0, 0, time.UTC),
		Checkpoint: CheckpointPreflight, RollbackDeadline: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
	}
	if err := store.Write(ctx, metadata); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
	if err := store.CASCheckpoint(ctx, 1, CheckpointCopy, metadata.UpdatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("advance checkpoint: %v", err)
	}
	if err := store.CASCheckpoint(ctx, 1, CheckpointCoverage, metadata.UpdatedAt.Add(2*time.Minute)); err == nil {
		t.Fatal("stale checkpoint CAS must fail")
	}
	if got, err := store.Read(ctx); err != nil || got.CutoverEpoch != 2 || got.Checkpoint != CheckpointCopy {
		t.Fatalf("metadata after CAS = %+v err=%v", got, err)
	}
}

func TestMetadataReadRejectsInvalidRollbackDeadline(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	key := "p:meta:migration"
	if err := rdb.HSet(ctx, key, map[string]string{
		"owner":             "halfking",
		"ledger_id":         "ursm-v2-k2-test",
		"mode":              "legacy",
		"cutover_epoch":     "0",
		"started_at":        "2026-08-18T00:00:00Z",
		"updated_at":        "2026-08-18T00:00:00Z",
		"checkpoint":        "preflight",
		"rollback_deadline": "not-a-time",
	}).Err(); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	if _, err := NewMetadataLua(rdb, "p:").Read(ctx); err == nil {
		t.Fatal("metadata read must reject malformed rollback deadline")
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
