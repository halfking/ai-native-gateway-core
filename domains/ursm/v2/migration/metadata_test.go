package migration

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMetadataDirectAdvanceIsDisabled(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	if err := NewMetadataLua(rdb, "p:").Advance(context.Background(), 0, CheckpointCopy, storeKeySchemaModeFromMode(ModeDual)); err == nil {
		t.Fatal("direct metadata advance must be disabled")
	}
}

func TestMetadataHashDirectCheckpointCASIsDisabled(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	if err := (&MetadataHash{Prefix: "p:", RDB: rdb}).CASCheckpoint(context.Background(), 0, CheckpointCopy, time.Now().UTC()); err == nil {
		t.Fatal("direct checkpoint CAS must be disabled")
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

func TestMetadataReadRejectsInvalidRollbackDeadline(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	key := "p:meta:migration"
	if err := rdb.HSet(ctx, key, map[string]string{
		"owner": "halfking", "ledger_id": "ursm-v2-k2-test", "mode": "legacy", "cutover_epoch": "0",
		"started_at": "2026-08-18T00:00:00Z", "updated_at": "2026-08-18T00:00:00Z",
		"checkpoint": "preflight", "rollback_deadline": "not-a-time",
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
	metadata := NewMetadataLua(rdb, "p:")
	first := Metadata{Owner: "halfking", LedgerID: "one", Mode: ModeLegacy, Checkpoint: CheckpointPreflight}
	if err := metadata.Initialize(ctx, first); err != nil {
		t.Fatalf("first initialize: %v", err)
	}
	first.LedgerID = "two"
	if err := metadata.Initialize(ctx, first); err == nil {
		t.Fatal("metadata initialize must reject a different, non-reusable ledger ID")
	}
}
