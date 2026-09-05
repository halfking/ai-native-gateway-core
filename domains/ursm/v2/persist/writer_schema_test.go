package persist

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

// Schema-aware Collect (doc 14 §2: persist must consume the schema origin
// explicitly). miniredis substitute — SCAN behavior only.

func TestCollectRecognizesBothSchemas(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	// legacy node for one tuple
	if err := rdb.HSet(ctx, "ursm:v2:node:tenant-a:42:model-a", map[string]string{
		"available": "1", "tenant_id": "tenant-a", "generation": "2",
	}).Err(); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	// canonical node for a different tuple
	k2Key, err := store.K2NodeKeyForTenant("ursm:v2:", "tenant-b", 7, "m:x")
	if err != nil {
		t.Fatalf("k2 key: %v", err)
	}
	if err := rdb.HSet(ctx, k2Key, map[string]string{
		"available": "1", "tenant_id": "tenant-b", "generation": "5",
	}).Err(); err != nil {
		t.Fatalf("seed k2: %v", err)
	}
	w := New(rdb, "ursm:v2:", nil)
	rows, err := w.Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("row count=%d, want 2", len(rows))
	}
	byTenant := map[string]Row{}
	for _, r := range rows {
		byTenant[r.TenantID] = r
	}
	if r := byTenant["tenant-a"]; r.Schema != store.KeySchemaLegacy || r.CredentialID != 42 || r.RawModel != "model-a" {
		t.Fatalf("legacy row = %+v", r)
	}
	if r := byTenant["tenant-b"]; r.Schema != store.KeySchemaK2 || r.CredentialID != 7 || r.RawModel != "m:x" {
		t.Fatalf("k2 row = %+v", r)
	}
}

func TestCollectPrefersCanonicalForSameTuple(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := rdb.HSet(ctx, "ursm:v2:node:tenant-a:42:model-a", map[string]string{
		"available": "0", "tenant_id": "tenant-a", "generation": "1", "last_err": "legacy-stale",
	}).Err(); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	k2Key, err := store.K2NodeKeyForTenant("ursm:v2:", "tenant-a", 42, "model-a")
	if err != nil {
		t.Fatalf("k2 key: %v", err)
	}
	if err := rdb.HSet(ctx, k2Key, map[string]string{
		"available": "1", "tenant_id": "tenant-a", "generation": "9",
	}).Err(); err != nil {
		t.Fatalf("seed k2: %v", err)
	}
	w := New(rdb, "ursm:v2:", nil)
	rows, err := w.Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	// One logical tuple ⇒ one audit row, sourced from the canonical grammar.
	if len(rows) != 1 {
		t.Fatalf("row count=%d, want 1 (canonical row must replace the legacy twin)", len(rows))
	}
	if rows[0].Schema != store.KeySchemaK2 || rows[0].Generation != 9 || !rows[0].Available {
		t.Fatalf("row = %+v, want canonical origin generation 9 available", rows[0])
	}
}

func TestCollectStillExcludesUnparseableKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	// Ambiguous legacy key: parse fails (non-decimal credential segment)
	// and the k2 marker is reserved — the key must stay excluded rather
	// than guessed into a tuple (doc 14 §4).
	if err := rdb.HSet(ctx, "ursm:v2:node:k2:not-a-number:model", map[string]string{
		"available": "1", "generation": "1",
	}).Err(); err != nil {
		t.Fatalf("seed ambiguous: %v", err)
	}
	w := New(rdb, "ursm:v2:", nil)
	rows, err := w.Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("row count=%d, want 0: ambiguous keys must not enter the snapshot", len(rows))
	}
}
