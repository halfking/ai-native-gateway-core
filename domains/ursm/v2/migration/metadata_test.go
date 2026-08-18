package migration

import (
	"context"
	"strings"
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

func TestMetadataReadRejectsIncompleteIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := rdb.HSet(ctx, MetadataKey("p:"), map[string]string{
		"owner": "halfking", "ledger_id": "ursm-v2-k2-test", "mode": "dual", "cutover_epoch": "0",
	}).Err(); err != nil {
		t.Fatalf("seed incomplete metadata: %v", err)
	}
	if _, err := NewMetadataLua(rdb, "p:").Read(ctx); err == nil {
		t.Fatal("incomplete metadata must be rejected")
	}
}

func TestMetadataInitializeRejectsInvalidModeWithoutPartialWrite(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	metadata := NewMetadataLua(rdb, "p:")
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	if err := metadata.Initialize(ctx, Metadata{
		Owner: "halfking", LedgerID: "ursm-v2-k2-test", Mode: Mode("invalid"),
		CutoverEpoch: 0, StartedAt: now, UpdatedAt: now, Checkpoint: CheckpointPreflight,
	}); err == nil {
		t.Fatal("invalid mode must be rejected")
	}
	if values, err := rdb.HGetAll(ctx, metadata.key()).Result(); err != nil {
		t.Fatalf("read metadata after rejected initialize: %v", err)
	} else if len(values) != 0 {
		t.Fatalf("rejected initialize left partial metadata: %v", values)
	}
}
func TestMetadataInitializeCannotReplaceLedgerIdentity(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	metadata := NewMetadataLua(rdb, "p:")
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	first := Metadata{
		Owner: "halfking", LedgerID: "one", Mode: ModeLegacy,
		CutoverEpoch: 0, StartedAt: now, UpdatedAt: now, Checkpoint: CheckpointPreflight,
	}
	if err := metadata.Initialize(ctx, first); err != nil {
		t.Fatalf("first initialize: %v", err)
	}
	first.LedgerID = "two"
	if err := metadata.Initialize(ctx, first); err == nil {
		t.Fatal("metadata initialize must reject a different, non-reusable ledger ID")
	}
}

// TestMirrorPromotionFencedByIdentityAndEpoch covers the Lua CAS in
// MetadataHash.mirrorPromotion: an owner-approved PG transition only
// advances Redis when (owner, ledger_id, cutover_epoch, predecessor
// checkpoint) all agree. Any disagreement leaves the durable HASH intact
// so the next ReconcilePromotion can retry without losing authority.
func TestMirrorPromotionFencedByIdentityAndEpoch(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	store := &MetadataHash{Prefix: "p:", RDB: rdb}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if err := store.Write(ctx, Metadata{
		Owner: "halfking", LedgerID: "ursm-v2-k2-test", Mode: ModeDual,
		CutoverEpoch: 3, StartedAt: now, UpdatedAt: now,
		Checkpoint: CheckpointCopy,
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	evidence := strings.Repeat("a", 64)
	good := PromotionRequest{
		LedgerID: "ursm-v2-k2-test", Owner: "halfking",
		ExpectedCheckpoint: CheckpointCopy, ExpectedEpoch: 3,
		Checkpoint: CheckpointCoverage, Mode: ModeDual,
		Actor: "halfking", ApprovedBy: "reviewer",
		EvidenceSHA256: evidence, EvidenceRef: "evidence://coverage",
	}
	if err := store.mirrorPromotion(ctx, good, now); err != nil {
		t.Fatalf("mirror good promotion: %v", err)
	}
	md, err := store.Read(ctx)
	if err != nil {
		t.Fatalf("read after mirror: %v", err)
	}
	if md.Checkpoint != CheckpointCoverage || md.CutoverEpoch != 4 {
		t.Fatalf("post-mirror = checkpoint:%q epoch:%d, want coverage/4", md.Checkpoint, md.CutoverEpoch)
	}
	// Stale epoch must fail closed.
	stale := good
	stale.ExpectedEpoch = 3 // already advanced to 4
	if err := store.mirrorPromotion(ctx, stale, now); err == nil {
		t.Fatal("mirror with stale epoch must fail closed")
	}
	// Different owner must fail closed.
	wrong := good
	wrong.Owner = "not-the-owner"
	wrong.ExpectedEpoch = 4
	if err := store.mirrorPromotion(ctx, wrong, now); err == nil {
		t.Fatal("mirror with mismatched owner must fail closed")
	}
	// Different ledger_id must fail closed.
	wrong = good
	wrong.LedgerID = "ursm-v2-k2-other"
	wrong.ExpectedEpoch = 4
	if err := store.mirrorPromotion(ctx, wrong, now); err == nil {
		t.Fatal("mirror with mismatched ledger_id must fail closed")
	}
	// Mismatched predecessor checkpoint must fail closed.
	wrong = good
	wrong.ExpectedCheckpoint = CheckpointPreflight
	wrong.ExpectedEpoch = 4
	if err := store.mirrorPromotion(ctx, wrong, now); err == nil {
		t.Fatal("mirror with mismatched predecessor must fail closed")
	}
	// Authoritative next promotion still succeeds.
	next := good
	next.ExpectedCheckpoint = CheckpointCoverage
	next.ExpectedEpoch = 4
	next.Checkpoint = CheckpointDual
	next.Mode = ModeDual
	next.EvidenceRef = "evidence://dual"
	if err := store.mirrorPromotion(ctx, next, now); err != nil {
		t.Fatalf("mirror authoritative next promotion: %v", err)
	}
	md, err = store.Read(ctx)
	if err != nil {
		t.Fatalf("read after second mirror: %v", err)
	}
	if md.Checkpoint != CheckpointDual || md.CutoverEpoch != 5 {
		t.Fatalf("post-second-mirror = checkpoint:%q epoch:%d, want dual/5", md.Checkpoint, md.CutoverEpoch)
	}
}
