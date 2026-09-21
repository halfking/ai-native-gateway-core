package persist

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func TestWriterCollectsTenantScopedNodeState(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := rdb.HSet(ctx, "ursm:v2:meta:epoch", "counter", "7").Err(); err != nil {
		t.Fatalf("seed epoch: %v", err)
	}
	if err := rdb.HSet(ctx, "ursm:v2:node:tenant-a:42:model:with:colon", map[string]string{
		"available": "1", "tenant_id": "tenant-a", "cool_until_ms": "1234567890000", "generation": "3",
	}).Err(); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	w := New(rdb, "ursm:v2:", nil)
	rows, err := w.Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("row count=%d, want 1", len(rows))
	}
	row := rows[0]
	if row.TenantID != "tenant-a" || row.CredentialID != 42 || row.RawModel != "model:with:colon" {
		t.Fatalf("unexpected identity: %+v", row)
	}
	if row.RecoveryEpoch != 7 || row.CoolUntil == nil {
		t.Fatalf("epoch/cool state not captured: %+v", row)
	}
}

func TestWriterCollectsEmptyRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	w := New(rdb, "ursm:v2:", nil /* db 留 nil 时只做 Redis 拉取 */)
	rows, err := w.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	_ = rows
	_ = sql.ErrNoRows
	_ = (*pgxpool.Pool)(nil)
}

func TestWriterRejectsWrongTypeNodeHash(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	if err := rdb.Set(context.Background(), "ursm:v2:node:tenant-a:42:model", "not-a-hash", 0).Err(); err != nil {
		t.Fatalf("seed wrong-type node: %v", err)
	}

	_, err := New(rdb, "ursm:v2:", nil).Collect(context.Background())
	if err == nil {
		t.Fatal("Collect should surface a wrong-type node hash error")
	}
	if !strings.Contains(err.Error(), "read node hash") {
		t.Fatalf("Collect error = %v, want read node hash context", err)
	}
}

// TestWriterSkipsRequestDedupMarkerKeys pins the P4 fix
// (docs/audit/2026-09-12-r14-observation-p5p4-readonly.md §C): Collect
// scans node:* and requestDedupKey (record_request.go) writes STRING
// flags under that namespace; the legacy grammar's trailing rejoin used
// to "decode" them into node tuples, so one live dedup marker made
// SafeHGetAll abort the whole snapshot batch every tick and
// ursm_node_snapshot_min stalled from 2026-09-11 21:16. Marker keys must
// be skipped before parse in every grammar's shape, while genuine node
// hashes in the same scan still collect and unknown wrong-type keys
// still abort (TestWriterRejectsWrongTypeNodeHash).
func TestWriterSkipsRequestDedupMarkerKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := rdb.HSet(ctx, "ursm:v2:node:12:gpt-5.6-sol", map[string]string{
		"available": "1", "health": "ok", "generation": "2",
	}).Err(); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	dedupMarkers := []string{
		// legacy node key + marker suffix (the leak: rejoin swallows the
		// suffix into RawModel)
		"ursm:v2:node:12:gpt-5.6-sol:request_dedup:ab12cd34",
		// tenant-scoped legacy node key + marker suffix
		"ursm:v2:node:tenant-a:42:model:with:colon:request_dedup:ef56ab78",
		// k2-shaped node key + marker suffix (strict grammar rejects the
		// suffix; the skip must happen before parse either way)
		"ursm:v2:node:k2:dGVuYW50LWE:42:bW9kZWw:request_dedup:0192abcd",
	}
	for _, k := range dedupMarkers {
		if err := rdb.Set(ctx, k, "1", 0).Err(); err != nil {
			t.Fatalf("seed dedup marker %s: %v", k, err)
		}
	}

	rows, err := New(rdb, "ursm:v2:", nil).Collect(ctx)
	if err != nil {
		t.Fatalf("Collect aborted on dedup markers: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("row count=%d, want 1 — dedup markers must not become rows", len(rows))
	}
	if rows[0].CredentialID != 12 || rows[0].RawModel != "gpt-5.6-sol" {
		t.Fatalf("unexpected identity: %+v", rows[0])
	}
}

// TestWriterCollectsHealthStatusBridge pins the persist half of UT-UR-12
// (会话优化 v4 T5 / P1-5): the "health" hash field written by
// record_request.lua lands in Row.HealthStatus, which Flush already
// persists into ursm_node_snapshot_min.health_status — the admin snapshot
// therefore renders the bridged rich health state with no schema change.
func TestWriterCollectsHealthStatusBridge(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	if err := rdb.HSet(ctx, "ursm:v2:node:t:55:bridge-model", map[string]string{
		"available": "0", "health": "degraded", "generation": "9", "source_priority": "10",
	}).Err(); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	w := New(rdb, "ursm:v2:", nil)
	rows, err := w.Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("row count=%d, want 1", len(rows))
	}
	if rows[0].HealthStatus != "degraded" {
		t.Fatalf("HealthStatus=%q, want degraded (bridge must reach the persist snapshot)", rows[0].HealthStatus)
	}
	if rows[0].Available {
		t.Fatalf("Available=true, want false — health is display-only; availability stays independently captured")
	}
}
