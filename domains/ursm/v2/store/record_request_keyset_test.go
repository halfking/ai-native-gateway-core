package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// L2 schema-aware store entry points. miniredis is the dependency for all
// tests in this file: it proves key routing, precedence and fail-closed
// logic only, never real Redis Lua dialect or atomicity (doc 14 §1.5).

func TestRecordRequestKeySetLegacyModeMatchesRecordRequest(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	set := NodeKeySetForTenant("ursm:v2:", "tenant-a", 7, "model-a")

	// Default mode is legacy: the key-set entry point must drive exactly the
	// legacy keys, byte for byte, like the explicit-key API.
	res, err := s.RecordRequestKeySet(ctx, set, RecordOutcome{
		Success: true, NowMs: 1_000_000, LatencyMs: 100, RequestID: "r1",
		NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	})
	if err != nil || res.Status != "applied" {
		t.Fatalf("keyset record: %+v err=%v", res, err)
	}
	if !mr.Exists(set.Node) {
		t.Fatal("legacy node key must be written")
	}
	if got := mr.HGet(set.Node, "samples_1m"); got != "1" {
		t.Fatalf("samples_1m=%q, want 1", got)
	}
	for _, k := range []string{set.Win1m, set.Win5m, set.Win30m} {
		if !mr.Exists(k) {
			t.Fatalf("legacy window key %q must be written", k)
		}
	}
	for _, k := range mr.Keys() {
		if strings.Contains(k, ":k2:") {
			t.Fatalf("legacy mode must not write canonical key %q", k)
		}
	}
}

func TestRecordRequestDualWritesBothSchemasAsOneSet(t *testing.T) {
	s, mr := newTestStore(t)
	s.SetKeySchemaMode(KeySchemaModeDual)
	ctx := context.Background()
	set := NodeKeySetForTenant("ursm:v2:", "tenant-a", 7, "model-a")
	res, err := s.RecordRequestKeySet(ctx, set, RecordOutcome{
		Success: true, NowMs: 1_000_000, LatencyMs: 100, RequestID: "r1",
		NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	})
	if err != nil || res.Status != "applied" {
		t.Fatalf("dual record: %+v err=%v", res, err)
	}
	k2, err := K2KeySetForTenant("ursm:v2:", "tenant-a", 7, "model-a")
	if err != nil {
		t.Fatalf("k2 set: %v", err)
	}
	// Both grammars carry the same state for the same logical tuple.
	for _, node := range []string{set.Node, k2.Node} {
		if !mr.Exists(node) {
			t.Fatalf("node key %q must exist in dual mode", node)
		}
		for field, want := range map[string]string{"samples_1m": "1", "samples_5m": "1", "samples_30m": "1", "generation": "1", "available": "1"} {
			if got := mr.HGet(node, field); got != want {
				t.Fatalf("%s %s=%q, want %q", node, field, got, want)
			}
		}
	}
	for _, w := range []string{set.Win1m, set.Win5m, set.Win30m, k2.Win1m, k2.Win5m, k2.Win30m} {
		if !mr.Exists(w) {
			t.Fatalf("window key %q must exist in dual mode", w)
		}
	}
}

func TestRecordRequestDualDedupCrossesSchemas(t *testing.T) {
	s, mr := newTestStore(t)
	s.SetKeySchemaMode(KeySchemaModeDual)
	ctx := context.Background()
	set := NodeKeySetForTenant("ursm:v2:", "tenant-a", 7, "model-a")
	o := RecordOutcome{
		Success: true, NowMs: 1_000_000, LatencyMs: 100, DedupKey: "req-42", RequestID: "r1",
		NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	}
	// Simulate a legacy-only-era write: the legacy dedup key exists but the
	// canonical side has never seen the request.
	if err := mr.Set(requestDedupKey(set.Node, "req-42"), "1"); err != nil {
		t.Fatalf("seed legacy dedup: %v", err)
	}
	res, err := s.RecordRequestKeySet(ctx, set, o)
	if err != nil {
		t.Fatalf("dual record: %v", err)
	}
	if res.Status != "duplicate" {
		t.Fatalf("status=%q, want duplicate: a legacy-recorded request must stay duplicate in dual mode", res.Status)
	}
	// No partial write: neither grammar recorded the event.
	if mr.Exists(set.Node) || mr.Exists(set.Win1m) {
		t.Fatal("duplicate must not write the legacy set")
	}
	if k2, _ := K2KeySetForTenant("ursm:v2:", "tenant-a", 7, "model-a"); mr.Exists(k2.Node) || mr.Exists(k2.Win1m) {
		t.Fatal("duplicate must not write the canonical set")
	}
}

func TestRecordRequestDualManualHoldCrossesSchemas(t *testing.T) {
	s, mr := newTestStore(t)
	s.SetKeySchemaMode(KeySchemaModeDual)
	ctx := context.Background()
	set := NodeKeySetForTenant("ursm:v2:", "tenant-a", 7, "model-a")
	k2, err := K2KeySetForTenant("ursm:v2:", "tenant-a", 7, "model-a")
	if err != nil {
		t.Fatalf("k2 set: %v", err)
	}
	// An admin hold recorded on one grammar only must dominate both.
	mr.HSet(k2.Node, "manual_hold", "1")
	res, err := s.RecordRequestKeySet(ctx, set, RecordOutcome{
		Success: true, NowMs: 1_000_000, LatencyMs: 100, RequestID: "r1",
		NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	})
	if err != nil {
		t.Fatalf("dual record: %v", err)
	}
	if res.Status != "ignored_manual_hold" {
		t.Fatalf("status=%q, want ignored_manual_hold", res.Status)
	}
	if mr.Exists(set.Node) {
		t.Fatal("hold on the canonical set must block the legacy write too")
	}
}

func TestRecordRequestDualEmptyTenantMaintainsLegacyOnly(t *testing.T) {
	s, mr := newTestStore(t)
	s.SetKeySchemaMode(KeySchemaModeDual)
	ctx := context.Background()
	// Empty tenant exists only in legacy compatibility/shadow rules
	// (doc 14 §2): dual mode must keep serving it on legacy bytes instead
	// of dropping the write.
	set := NodeKeySetForTenant("ursm:v2:", "", 12, "gpt-4")
	res, err := s.RecordRequestKeySet(ctx, set, RecordOutcome{
		Success: true, NowMs: 1_000_000, LatencyMs: 100, RequestID: "r1",
		NodeTTL: time.Hour, Window5mTTL: 6 * time.Minute, Window30mTTL: 35 * time.Minute,
	})
	if err != nil || res.Status != "applied" {
		t.Fatalf("dual record empty tenant: %+v err=%v", res, err)
	}
	if !mr.Exists(set.Node) {
		t.Fatal("legacy node key must still be written for the empty tenant")
	}
	for _, k := range mr.Keys() {
		if strings.Contains(k, ":k2:") {
			t.Fatalf("canonical key %q must not exist for the empty tenant", k)
		}
	}
}
