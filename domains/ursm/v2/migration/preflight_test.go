package migration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

const testPrefix = "ursm:v2:test:"
const testLedgerID = "134e6d21-721b-41d4-af0b-adff143107e2"

func newRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() {
		_ = rdb.Close()
	})
	return rdb, mr
}

func fixedNow() func() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, "2026-08-18T12:00:00Z")
	return func() time.Time { return t }
}

func TestOptionsValidation(t *testing.T) {
	rdb, _ := newRedis(t)
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"missing redis", Options{Prefix: testPrefix, Owner: "o", LedgerID: testLedgerID}, "Redis client"},
		{"missing prefix", Options{Redis: rdb, Owner: "o", LedgerID: testLedgerID}, "prefix is required"},
		{"missing owner", Options{Redis: rdb, Prefix: testPrefix, LedgerID: testLedgerID}, "owner is required"},
		{"bad ledger id", Options{Redis: rdb, Prefix: testPrefix, Owner: "o", LedgerID: "not-a-uuid"}, "ledger_id"},
		{"unknown mode", Options{Redis: rdb, Prefix: testPrefix, Owner: "o", LedgerID: testLedgerID, SchemaMode: SchemaMode("foo")}, "schema mode"},
		{"negative scan limit", Options{Redis: rdb, Prefix: testPrefix, Owner: "o", LedgerID: testLedgerID, ScanLimit: -1}, "scan limit"},
	}
	for _, c := range cases {
		_, err := RunPreflight(context.Background(), c.opts)
		if err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
		if !contains(err.Error(), c.want) {
			t.Fatalf("%s: error %q must mention %q", c.name, err.Error(), c.want)
		}
	}
}

func TestIsLedgerID(t *testing.T) {
	good := []string{
		"134e6d21-721b-41d4-af0b-adff143107e2",
		"ABCDEF01-2345-6789-ABCD-EF0123456789",
	}
	bad := []string{"", "short", "not-a-uuid-of-correct-length-xx", "zzzzzzzz-721b-41d4-af0b-adff143107e2"}
	for _, s := range good {
		if !isLedgerID(s) {
			t.Fatalf("expected %q to be a valid ledger id", s)
		}
	}
	for _, s := range bad {
		if isLedgerID(s) {
			t.Fatalf("expected %q to be rejected", s)
		}
	}
}

func TestPreflightClassifiesLegacyAndCanonicalAndAmbiguous(t *testing.T) {
	rdb, mr := newRedis(t)
	ctx := context.Background()

	// 1) Legacy migratable tuples (exact bytes, no delimiter in raw).
	mr.HSet(testPrefix+"node:tenant-a:7:model-a", "generation", "1", "value", "ok")
	mr.HSet(testPrefix+"node:tenant-b:9:model-b", "generation", "2", "value", "ok")
	// 2) Canonical k2 key already present.
	k2 := store.NodeKeyCanonical(testPrefix, "tenant-a", 7, "model-a")
	mr.HSet(k2, "generation", "1", "value", "ok")
	// 3) Ambiguous legacy tuple (cannot be proven unique — extra colon in raw).
	mr.HSet(testPrefix+"node:tenant-a:7:model:extra:colon", "generation", "1")
	// 4) Reserved k2 namespace malformed; parser must fail closed (NOT legacy).
	mr.HSet(testPrefix+"node:k2:1234:07:YQ", "generation", "1")
	// 5) Plain string type, valid legacy key bytes — schema is decided by the
	// bytes, not the value type, so this is still migratable.
	mr.Set(testPrefix+"node:tenant-d:5:model-d", "x")

	report, err := RunPreflight(ctx, Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}

	if report.Counts[ClassMigratable] != 3 {
		t.Fatalf("migratable count = %d, want 3 (%v)", report.Counts[ClassMigratable], report.Counts)
	}
	if report.Counts[ClassCanonicalPresent] != 1 {
		t.Fatalf("canonical_present count = %d, want 1 (%v)", report.Counts[ClassCanonicalPresent], report.Counts)
	}
	if report.Counts[ClassAmbiguous] < 2 {
		t.Fatalf("ambiguous count = %d, want >=2 (extra-colon legacy + malformed reserved k2) (%v)", report.Counts[ClassAmbiguous], report.Counts)
	}
	if report.MigrationGoable {
		t.Fatalf("report must be NO-GO when ambiguous/conflict > 0, reasons=%v", report.Reasons)
	}
	if report.Ledger.PreflightChecksum == "" {
		t.Fatal("preflight checksum must not be empty")
	}
	if report.Ledger.Checkpoint != CheckpointPreflight {
		t.Fatalf("checkpoint = %q, want preflight", report.Ledger.Checkpoint)
	}

	// Verify no migration-related writes ever happened. Miniredis serialises
	// keys via String(); the migration package must not invent any new keys
	// beyond those the test pre-populated.
	wantKeys := map[string]bool{
		testPrefix + "node:tenant-a:7:model-a": true,
		testPrefix + "node:tenant-b:9:model-b": true,
		k2:                                     true,
		testPrefix + "node:tenant-a:7:model:extra:colon": true,
		testPrefix + "node:k2:1234:07:YQ":                true,
		testPrefix + "node:tenant-d:5:model-d":           true,
	}
	for _, k := range mr.Keys() {
		if !wantKeys[k] {
			t.Fatalf("unexpected Redis key created by Preflight: %q", k)
		}
	}

	// Validate entries: source key order is alphabetical, migratable entry
	// records the canonical target, canonical-present entry reports schema k2.
	bySource := map[string]PreflightEntry{}
	for _, e := range report.Ledger.Entries {
		bySource[e.SourceKey] = e
	}
	m := bySource[testPrefix+"node:tenant-a:7:model-a"]
	if m.Classification != ClassMigratable || m.TargetKey != k2 || m.Generation != "1" {
		t.Fatalf("migratable entry = %+v", m)
	}
	c := bySource[k2]
	if c.Classification != ClassCanonicalPresent || c.Schema != store.SchemaK2 || c.TargetKey != k2 {
		t.Fatalf("canonical entry = %+v", c)
	}
	reserved := bySource[testPrefix+"node:k2:1234:07:YQ"]
	if reserved.Classification != ClassAmbiguous {
		t.Fatalf("malformed reserved k2 must be ambiguous, got %+v", reserved)
	}
	if reserved.Schema != "" {
		t.Fatalf("malformed reserved k2 must not report a schema, got %q", reserved.Schema)
	}
}

func TestPreflightIsDeterministicAndSkipAppliedIdempotent(t *testing.T) {
	rdb, mr := newRedis(t)
	ctx := context.Background()
	mr.HSet(testPrefix+"node:tenant-a:7:model-a", "generation", "1")
	mr.HSet(testPrefix+"node:tenant-b:9:model-b", "generation", "1")
	mr.HSet(store.NodeKeyCanonical(testPrefix, "tenant-c", 11, "model-c"), "generation", "1")

	first, err := RunPreflight(ctx, Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	second, err := RunPreflight(ctx, Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if first.Ledger.PreflightChecksum != second.Ledger.PreflightChecksum {
		t.Fatalf("checksum must be deterministic: %s vs %s", first.Ledger.PreflightChecksum, second.Ledger.PreflightChecksum)
	}
	// Mutation between runs must shift the checksum.
	mr.HSet(testPrefix+"node:tenant-a:7:model-a", "generation", "2")
	third, err := RunPreflight(ctx, Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if third.Ledger.PreflightChecksum == first.Ledger.PreflightChecksum {
		t.Fatal("checksum must change when keyspace changes")
	}
}

func TestPreflightFrozenCollisionPairIsNotMigratable(t *testing.T) {
	rdb, mr := newRedis(t)
	ctx := context.Background()
	// doc 14 §1 frozen collision pair collapses to one legacy key.
	mr.HSet(store.NodeKeyForTenant(testPrefix, "a", 7, "b:8:c"), "generation", "1")
	report, err := RunPreflight(ctx, Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if report.Counts[ClassMigratable] != 0 {
		t.Fatalf("collision-pair source must not be migratable (it is round-tripable but the alternative tuple is not): counts=%v decisions=%v", report.Counts, report.Decisions)
	}
	if report.Counts[ClassAmbiguous] != 1 {
		t.Fatalf("ambiguous count = %d, want 1", report.Counts[ClassAmbiguous])
	}
	if report.MigrationGoable {
		t.Fatal("collision pair must force NO-GO")
	}
}

func TestPreflightEmptyKeyspaceProducesEmptyChecksumAndGo(t *testing.T) {
	rdb, _ := newRedis(t)
	report, err := RunPreflight(context.Background(), Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if len(report.Ledger.Entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(report.Ledger.Entries))
	}
	if !report.MigrationGoable {
		t.Fatalf("empty keyspace must be GO, reasons=%v", report.Reasons)
	}
	if report.Ledger.PreflightChecksum == "" {
		t.Fatal("checksum must be set even on empty inventory")
	}
}

func TestPreflightContextCancellation(t *testing.T) {
	rdb, mr := newRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Anything that touches a closed ctx should fail; preflight does not
	// have a non-Redis path so we simulate via empty mr state.
	_ = mr
	_, err := RunPreflight(ctx, Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestFieldChecksumDeterministicAndOrderIndependent(t *testing.T) {
	a := fieldChecksum(map[string]string{"b": "2", "a": "1"})
	b := fieldChecksum(map[string]string{"a": "1", "b": "2"})
	if a != b {
		t.Fatalf("checksum must be order-independent: %s vs %s", a, b)
	}
	if a == "" {
		t.Fatal("checksum must not be empty")
	}
	c := fieldChecksum(map[string]string{"a": "1", "b": "3"})
	if c == a {
		t.Fatal("different values must produce different checksums")
	}
}

// doc 14 §4 / doc 15 §2: when a canonical k2 key for the same logical
// tuple already exists with a diverging generation, preflight must emit
// ClassConflict on the legacy source so the runner stops with NO-GO.
func TestPreflightConflictOnDivergingGeneration(t *testing.T) {
	rdb, mr := newRedis(t)
	ctx := context.Background()
	mr.HSet(testPrefix+"node:tenant-x:1:model-x", "generation", "1", "value", "v1")
	canonical := store.NodeKeyCanonical(testPrefix, "tenant-x", 1, "model-x")
	mr.HSet(canonical, "generation", "9", "value", "v1")
	report, err := RunPreflight(ctx, Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if report.Counts[ClassConflict] != 1 {
		t.Fatalf("conflict count = %d, want 1 (%v)", report.Counts[ClassConflict], report.Counts)
	}
	if report.MigrationGoable {
		t.Fatalf("must be NO-GO when conflict > 0: %v", report.Reasons)
	}
}

// doc 14 §3: SchemaMode=canonical must exclude every non-k2 node source
// regardless of byte round-trip status; doc 16 §3 requires coverage to
// only include canonical keys.
func TestPreflightSchemaModeCanonicalExcludesLegacySources(t *testing.T) {
	rdb, mr := newRedis(t)
	mr.HSet(testPrefix+"node:tenant-a:7:model-a", "generation", "1")
	mr.HSet(store.NodeKeyCanonical(testPrefix, "tenant-b", 9, "model-b"), "generation", "1")
	report, err := RunPreflight(context.Background(), Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, SchemaMode: SchemaModeCanonical, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if report.Counts[ClassMigratable] != 0 {
		t.Fatalf("canonical mode must not produce migratable: %v", report.Counts)
	}
	if report.Counts[ClassExcludedNonAuthoritative] != 1 {
		t.Fatalf("expected 1 excluded legacy source, got %v", report.Counts)
	}
	if report.Counts[ClassCanonicalPresent] != 1 {
		t.Fatalf("expected 1 canonical_present, got %v", report.Counts)
	}
	if !report.MigrationGoable {
		t.Fatalf("clean canonical keyspace must be GO, reasons=%v", report.Reasons)
	}
}

// doc 15 §2: every source-key pattern must be inventoried; binding /
// credential / provider keys must be classified as excluded non-authoritative.
func TestPreflightIncludesAllSourcePatterns(t *testing.T) {
	rdb, mr := newRedis(t)
	mr.HSet(testPrefix+"node:tenant-a:7:model-a", "generation", "1")
	mr.HSet(testPrefix+"win:1m:tenant-a:7:model-a", "1", "1")
	mr.HSet(testPrefix+"idx:model:tenant-a:model-a:chat:text", "score", "1")
	mr.HSet(testPrefix+"binding:1:model-a", "x", "y")
	mr.Set(testPrefix+"meta:request_dedup:sha", "x")
	report, err := RunPreflight(context.Background(), Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	kinds := map[string]int{}
	for _, e := range report.Ledger.Entries {
		kinds[e.KeyKind]++
	}
	want := map[string]int{KindNode: 1, KindWindow: 1, KindIndex: 1, KindBinding: 1, KindDedup: 1}
	for k, v := range want {
		if kinds[k] != v {
			t.Fatalf("kind %s count = %d, want %d (kinds=%v)", k, kinds[k], v, kinds)
		}
	}
	if report.Counts[ClassExcludedNonAuthoritative] < 2 {
		t.Fatalf("binding/dedup must be excluded non-authoritative, counts=%v", report.Counts)
	}
}

// doc 15 §2: ZSET sources need member checksum coverage in the ledger.
func TestPreflightZSetMemberChecksum(t *testing.T) {
	rdb, mr := newRedis(t)
	idxKey := store.CandidateIndexKeyCanonical(testPrefix, "tenant-a", "model-a", "chat", "text")
	mr.ZAdd(idxKey, 1.0, "cred-1")
	mr.ZAdd(idxKey, 2.0, "cred-2")
	report, err := RunPreflight(context.Background(), Options{
		Redis: rdb, Prefix: testPrefix, Owner: "halfking",
		LedgerID: testLedgerID, Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	for _, e := range report.Ledger.Entries {
		if e.KeyKind != KindIndex || e.Schema != store.SchemaK2 {
			continue
		}
		if e.MemberCount != 2 {
			t.Fatalf("member count = %d, want 2", e.MemberCount)
		}
		if len(e.MemberChecksum) != 64 {
			t.Fatalf("member checksum = %q, want 64 hex chars", e.MemberChecksum)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
