//go:build integration

package routeincident

// 2026-10-18：事件留存 worker 的真库行为判据。
//
// 运行（**只指向本地容器，不要指向生产**——本用例会 DELETE 真行）：
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test -tags=integration ./domains/routeincident/ -run TestEventRetention -v -count=1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// retentionTestPool 连接 TEST_PG_URL 并确保 fixture 表存在。
func retentionTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; retention integration test requires a live PostgreSQL")
	}
	if !strings.Contains(dsn, "127.0.0.1") && !strings.Contains(dsn, "localhost") {
		t.Fatalf("TEST_PG_URL 不指向本机（%s）。本用例会真实 DELETE 行，"+
			"拒绝对生产库运行。", dsn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("TEST_PG_URL unreachable: %v", err)
	}
	if _, err := pool.Exec(ctx, fixtureDDLRouteIncident); err != nil {
		pool.Close()
		t.Fatalf("apply fixture DDL: %v", err)
	}
	// ★ 必须 TRUNCATE：本 worker 是**全表 TTL**，它删的不会只限于本用例种的行。
	// fixture 用的是 CREATE TABLE IF NOT EXISTS，而这张表可能已经带着
	// 库里原有的数据 —— 不清空就会「顺带删掉别人的行」，
	// 且 EventsDeleted 的断言会被无关行污染（实测首跑 127 vs 期望 25）。
	// 本包既有的集成测试同样以 TRUNCATE 起手，沿用该约定。
	if _, err := pool.Exec(ctx,
		`TRUNCATE route_incident_events, route_incidents RESTART IDENTITY CASCADE`); err != nil {
		pool.Close()
		t.Fatalf("truncate fixture tables: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// retentionTimeIndex 建一个 created_at 首列索引，并在测试结束时摘掉。
//
// ★ 索引是本 worker 的前置条件，必须由测试自己建、自己拆：
// 共用 fixture 表的其它用例不该被本用例的状态影响，反之亦然。
func retentionTimeIndex(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	const idx = "retention_probe_idx_created_at"
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS %s ON route_incident_events (created_at)`, idx)); err != nil {
		t.Fatalf("create created_at index: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP INDEX IF EXISTS %s`, idx))
	})
}

// dropRetentionTimeIndex 确保探测处于「无 created_at 首列索引」状态。
func dropRetentionTimeIndex(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `
		SELECT ic.relname
		FROM pg_index i
		JOIN pg_class ic ON ic.oid = i.indexrelid
		WHERE i.indrelid = 'route_incident_events'::regclass
		  AND i.indisvalid
		  AND (SELECT array_agg(a.attname ORDER BY k.ord)
		       FROM unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
		       JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum)[1]
		      = 'created_at'`)
	if err != nil {
		t.Fatalf("list created_at-leading indexes: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		names = append(names, n)
	}
	for _, n := range names {
		if _, err := pool.Exec(ctx, fmt.Sprintf(`DROP INDEX IF EXISTS %s`, n)); err != nil {
			t.Fatalf("drop index %s: %v", n, err)
		}
		t.Logf("已摘除 %s（保持「无时间索引」前提）", n)
	}
}

// seedEvents 造 oldCount 条超龄行 + freshCount 条未超龄行。
func seedEvents(t *testing.T, pool *pgxpool.Pool, oldCount, freshCount int) {
	t.Helper()
	ctx := context.Background()
	var incID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO route_incidents
		   (tenant_id, endpoint_protocol, model, state, first_failure_at)
		 VALUES ('retention-t','chat','m','active', now() - interval '90 days')
		 RETURNING id`).Scan(&incID); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM route_incidents WHERE id = $1`, incID)
	})

	for i := 0; i < oldCount; i++ {
		// request_id 留 NULL：避开 fixture 的部分唯一索引，
		// 让本用例只考察「按 created_at 删」，不掺入幂等语义。
		if _, err := pool.Exec(ctx, `
			INSERT INTO route_incident_events
			  (incident_id, event_type, created_at)
			VALUES ($1, 'failure_observed', now() - interval '60 days')`, incID); err != nil {
			t.Fatalf("seed old event %d: %v", i, err)
		}
	}
	for i := 0; i < freshCount; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO route_incident_events
			  (incident_id, event_type, created_at)
			VALUES ($1, 'failure_observed', now())`, incID); err != nil {
			t.Fatalf("seed fresh event %d: %v", i, err)
		}
	}
}

func countEventsOf(t *testing.T, pool *pgxpool.Pool, incidentID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM route_incident_events WHERE incident_id = $1`,
		incidentID).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// TestEventRetentionSkipsWithoutTimeIndex：缺前置索引时**明确跳过**而不是硬删。
//
// ★ 这条是 §43 实测教训的固化：生产上「按 created_at 删」在无首列索引时
// 退化成全索引扫描（170 万行冷缓存 25.7 s vs statement_timeout 30 s），
// 单批就可能超时并留下死元组。宁可跳过出声，也不要「删不掉且更膨胀」。
func TestEventRetentionSkipsWithoutTimeIndex(t *testing.T) {
	pool := retentionTestPool(t)
	dropRetentionTimeIndex(t, pool)

	ctx := context.Background()
	var incID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO route_incidents
		   (tenant_id, endpoint_protocol, model, state, first_failure_at)
		 VALUES ('retention-t','chat','m','active', now() - interval '90 days')
		 RETURNING id`).Scan(&incID); err != nil {
		t.Fatalf("seed incident: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM route_incidents WHERE id = $1`, incID)
	})
	for i := 0; i < 5; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO route_incident_events (incident_id, event_type, created_at)
			VALUES ($1,'failure_observed', now() - interval '60 days')`, incID); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	w := NewEventRetentionWorker(pool, 24*time.Hour).WithInterval(time.Hour)
	res, err := w.CleanupOnce(ctx)
	if err != nil {
		t.Fatalf("CleanupOnce: %v", err)
	}
	if !res.SkippedNoIndex {
		t.Error("SkippedNoIndex = false，want true —— 缺 created_at 首列索引时必须跳过")
	}
	if res.EventsDeleted != 0 {
		t.Errorf("EventsDeleted = %d, want 0 when skipping", res.EventsDeleted)
	}
	// 前提仍在场：行确实还在（否则「什么都没删」可能只是「压根没种」）。
	if n := countEventsOf(t, pool, incID); n != 5 {
		t.Errorf("跳过之后仍有 %d 行，want 5（前提：确实种进去了 5 行）", n)
	}
}

// TestEventRetentionDeletesOnlyExpiredInBatches：有索引时按批删除，且只删超龄行。
func TestEventRetentionDeletesOnlyExpiredInBatches(t *testing.T) {
	pool := retentionTestPool(t)
	retentionTimeIndex(t, pool)
	seedEvents(t, pool, 25, 7)

	w := NewEventRetentionWorker(pool, 24*time.Hour).WithInterval(time.Hour).WithBatchSize(10)
	res, err := w.CleanupOnce(context.Background())
	if err != nil {
		t.Fatalf("CleanupOnce: %v", err)
	}
	if res.EventsDeleted != 25 {
		t.Errorf("EventsDeleted = %d, want 25", res.EventsDeleted)
	}
	// 25 行 / 批 10 ⇒ 批次应为 3（10+10+5）。
	if res.Batches != 3 {
		t.Errorf("Batches = %d, want 3", res.Batches)
	}

	// 只删超龄，未超龄的 7 行必须还在。
	var remaining int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM route_incident_events WHERE created_at > now() - interval '24 hours'`).
		Scan(&remaining); err != nil {
		t.Fatalf("count fresh: %v", err)
	}
	if remaining < 7 {
		t.Errorf("未超龄行只剩 %d 行，want ≥7 —— 留存误删了近期证据", remaining)
	}
}

// TestEventRetentionYieldsToPeer：同网另一台实例持锁时本轮让出。
//
// 这条守护 §38 的教训：本仓有过「245 与 154 各自 60s 独立跑」的双写事故，
// 留存也必须跨实例互斥，否则两台会同时对同一批行下手。
func TestEventRetentionYieldsToPeer(t *testing.T) {
	pool := retentionTestPool(t)
	retentionTimeIndex(t, pool)

	ctx := context.Background()
	holder, err := pool.Begin(ctx) // 独立事务，独占同一 advisory key
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}
	var got bool
	if err := holder.QueryRow(ctx,
		`SELECT pg_try_advisory_xact_lock($1)`, advisoryLockKeyBase).Scan(&got); err != nil {
		t.Fatalf("holder acquire lock: %v", err)
	}
	if !got {
		t.Fatal("holder 未能取得 advisory lock，测试前提不成立")
	}
	defer func() { _ = holder.Rollback(context.Background()) }()

	w := NewEventRetentionWorker(pool, 24*time.Hour).WithInterval(time.Hour)
	_, err = w.CleanupOnce(ctx)
	if !errors.Is(err, errHeldByPeer) {
		t.Fatalf("err = %v, want errHeldByPeer", err)
	}
}

// TestEventRetentionAlwaysLogsIncludingNoop：零回收也必须出声。
//
// ★ 与 bg/lite_retention_worker 的 §42 同源：周期 6h、保留期按天，
// 稳态下绝大多数轮次删除数必然为 0。只在删到东西时打日志，
// 会让「活着无事可做」与「根本没在跑」渲染成同一种沉默。
// 断言打在**运行期日志记录**上，否则文件头的说明文字会把判据喂饱。
func TestEventRetentionAlwaysLogsIncludingNoop(t *testing.T) {
	pool := retentionTestPool(t)
	retentionTimeIndex(t, pool)
	seedEvents(t, pool, 3, 0) // 全是超龄行

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	w := NewEventRetentionWorker(pool, 24*time.Hour).WithInterval(time.Hour)
	w.runOnceLogged(context.Background()) // 第一轮：真删
	w.runOnceLogged(context.Background()) // 第二轮：零回收 ← 本用例的重点

	var modes []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode slog line: %v", err)
		}
		if m["msg"] == "route incident event retention sweep" {
			modes = append(modes, fmt.Sprint(m["mode"]))
		}
	}
	if len(modes) != 2 {
		t.Fatalf("want 2 sweep records (一轮回收 + 一轮零回收), got %d: %v", len(modes), modes)
	}
	if modes[0] != "reclaimed" {
		t.Errorf("第一轮 mode = %q, want \"reclaimed\"", modes[0])
	}
	// ★ 阴性对照的第二条腿：没有这一条，一个「无论删没删都打 reclaimed」的
	// 退化实现也能全绿，而 mode 语义已死。
	if modes[1] != "noop" {
		t.Errorf("零回收那轮 mode = %q, want \"noop\" —— "+
			"沉默必须被区分出来，否则无法判断 worker 是否在跑", modes[1])
	}
}
