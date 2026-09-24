package mockprobe

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// realdbDSN 沿用仓库真库测试惯例（bg/sql_audit_realdb_test.go）：
// TEST_DATABASE_URL / TEST_DB_URL 二选一，未设置即跳过。
func realdbDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	return dsn
}

// historyRow 是 mock_probe_history 的查询投影。
type historyRow struct {
	channel       string
	supplier      string
	stream        bool
	latencyMs     int
	statusCode    int
	errorCode     *string
	requestID     *string
	failureStreak int
}

// TestHistoryStoreRealDB：HistoryStore 异步写入 mock_probe_history 后行可
// 查回（channel/supplier/stream/latency/status/failure_streak 全列回环；
// error_code/request_id 的 NULLIF 语义）。前置：目标库已应用
// migrations/036_mock_probe_history.sql。
func TestHistoryStoreRealDB(t *testing.T) {
	dsn := realdbDSN(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var tableExists bool
	err = pool.QueryRow(ctx,
		`SELECT to_regclass('public.mock_probe_history') IS NOT NULL`).Scan(&tableExists)
	if err != nil || !tableExists {
		t.Skipf("mock_probe_history 不存在（未应用 migrations/036？exists=%v err=%v），跳过", tableExists, err)
	}

	store := NewHistoryStore(ctx, pool)
	store.Insert(HistoryRecord{
		Channel:       "mock-fast:stream",
		Supplier:      "mock-fast",
		Stream:        true,
		Protocol:      "openai",
		LatencyMs:     12,
		StatusCode:    200,
		RequestID:     "chatcmpl-mock-test",
		FailureStreak: 0,
	})
	store.Insert(HistoryRecord{
		Channel:       "mock-slow:nonstream",
		Supplier:      "mock-slow",
		Stream:        false,
		Protocol:      "openai",
		LatencyMs:     999,
		StatusCode:    500,
		ErrorCode:     "http_500",
		FailureStreak: 2,
	})
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	store.Close(closeCtx)
	if store.Dropped() != 0 {
		t.Fatalf("records dropped: %d", store.Dropped())
	}

	qctx, qcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer qcancel()
	rows, err := pool.Query(qctx, `
		SELECT channel, supplier, stream, latency_ms, status_code, error_code, request_id, failure_streak
		FROM mock_probe_history
		WHERE request_id = 'chatcmpl-mock-test' OR (channel='mock-slow:nonstream' AND error_code='http_500')
		ORDER BY channel`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()
	got := []historyRow{}
	for rows.Next() {
		var r historyRow
		if err := rows.Scan(&r.channel, &r.supplier, &r.stream, &r.latencyMs, &r.statusCode, &r.errorCode, &r.requestID, &r.failureStreak); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	ok, bad := got[0], got[1]
	if ok.channel != "mock-fast:stream" || ok.supplier != "mock-fast" || !ok.stream ||
		ok.latencyMs != 12 || ok.statusCode != 200 ||
		ok.requestID == nil || *ok.requestID != "chatcmpl-mock-test" ||
		ok.errorCode != nil || ok.failureStreak != 0 {
		t.Fatalf("ok row mismatch: %+v", ok)
	}
	if bad.channel != "mock-slow:nonstream" || bad.supplier != "mock-slow" || bad.stream ||
		bad.statusCode != 500 || bad.errorCode == nil || *bad.errorCode != "http_500" ||
		bad.failureStreak != 2 {
		t.Fatalf("error row mismatch: %+v", bad)
	}
}
