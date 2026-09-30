package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/maas"
)

func TestRetireClosedMainSQLSkipsOpenMinute(t *testing.T) {
	sql := retireClosedMainMinuteSQL
	if !strings.Contains(sql, "m.bucket < date_trunc('minute', $2::timestamptz)") {
		t.Fatal("retire must stop before the minute that contains until")
	}
	if strings.Contains(sql, "m.bucket <= date_trunc('minute', $2") {
		t.Fatal("retire includes the open minute, where minute_flush is still adding rows")
	}
	if !strings.Contains(sql, "request_logs_with_current_month") {
		t.Fatal("retire existence check drifted off the rollup view")
	}
	if !strings.Contains(sql, "COALESCE(r.canonical_id, 0) = m.canonical_id") {
		t.Fatal("retire key must match the rollup canonical expression")
	}
	src, err := os.ReadFile("stats_minute_rollup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "return w.retireClosedMain(ctx, since, until)") {
		t.Fatal("rollupMain no longer retires keys the view stopped emitting")
	}
}

// 本地 8782 库上的 2026-09-26 10:37+08 / provider 314。
// request_logs.canonical_id 仍是 887407，视图因 session_turns.canonical_id
// 为空而把同一分钟记在 canonical 0。事务内重算并退役闭分钟多余键，然后回滚。
func TestRetireClosedMinuteDropsFlushCanonicalNotInView(t *testing.T) {
	dsn := os.Getenv("LLM_GATEWAY_ROLLUP_TEST_DSN")
	if dsn == "" {
		t.Skip("LLM_GATEWAY_ROLLUP_TEST_DSN unset")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("rollup test database unreachable: %v", err)
	}

	since := time.Date(2026, 9, 26, 2, 37, 0, 0, time.UTC)
	until := since.Add(time.Minute)
	const (
		tenant   = "default"
		provider = int64(314)
		flushKey = int64(887407)
	)

	var beforeReq int64
	var beforeCost float64
	err = pool.QueryRow(ctx, `
		SELECT requests, cost_usd
		FROM request_stats_minute
		WHERE bucket = $1 AND tenant_id = $2 AND provider_id = $3 AND canonical_id = $4
	`, since, tenant, provider, flushKey).Scan(&beforeReq, &beforeCost)
	if err != nil {
		t.Skipf("incident minute row not in this database: %v", err)
	}
	if beforeReq != 1 || absFloat(beforeCost-0.284420) > 1e-6 {
		t.Fatalf("incident row changed before the test: requests=%d cost=%v", beforeReq, beforeCost)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	// 走事务，避免把这一分钟写进 8782 正在读的库。语句与 rollupMain 相同。
	credits := maas.RequestLogCreditsSQL("r", true)
	if _, err := tx.Exec(ctx, rollupMainStatement(credits), since, until); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, retireClosedMainMinuteSQL, since, until); err != nil {
		t.Fatal(err)
	}

	var flushLeft int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM request_stats_minute
		WHERE bucket = $1 AND tenant_id = $2 AND provider_id = $3 AND canonical_id = $4
	`, since, tenant, provider, flushKey).Scan(&flushLeft); err != nil {
		t.Fatal(err)
	}
	if flushLeft != 0 {
		t.Fatalf("flush canonical %d still in the closed minute after retire", flushKey)
	}

	var minuteReq, viewReq int64
	var minuteCost, viewCost float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(requests), 0), COALESCE(SUM(cost_usd), 0)
		FROM request_stats_minute
		WHERE bucket = $1 AND tenant_id = $2 AND provider_id = $3
	`, since, tenant, provider).Scan(&minuteReq, &minuteCost); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(cost_usd), 0)
		FROM request_logs_with_current_month
		WHERE request_status IN ('success', 'failure', 'rate_limited')
		  AND ts >= $1 AND ts < $2
		  AND COALESCE(NULLIF(tenant_id, ''), 'default') = $3
		  AND COALESCE(provider_id, 0) = $4
	`, since, until, tenant, provider).Scan(&viewReq, &viewCost); err != nil {
		t.Fatal(err)
	}
	if minuteReq != viewReq || absFloat(minuteCost-viewCost) > 1e-6 {
		t.Fatalf("closed minute = %d req / %v, view = %d req / %v", minuteReq, minuteCost, viewReq, viewCost)
	}

	openBucket := until
	tag, err := tx.Exec(ctx, `
		INSERT INTO request_stats_minute (
			bucket, tenant_id, provider_id, canonical_id, requests, cost_usd
		) VALUES ($1, '__retire_probe__', 0, 0, 1, 0)
	`, openBucket)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("probe insert affected %d", tag.RowsAffected())
	}
	if _, err := tx.Exec(ctx, retireClosedMainMinuteSQL, since, until); err != nil {
		t.Fatal(err)
	}
	var probe int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM request_stats_minute
		WHERE bucket = $1 AND tenant_id = '__retire_probe__'
	`, openBucket).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if probe != 1 {
		t.Fatal("retire deleted the open minute")
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var afterReq int64
	if err := pool.QueryRow(ctx, `
		SELECT requests FROM request_stats_minute
		WHERE bucket = $1 AND tenant_id = $2 AND provider_id = $3 AND canonical_id = $4
	`, since, tenant, provider, flushKey).Scan(&afterReq); err != nil {
		t.Fatalf("rollback lost the incident row: %v", err)
	}
	if afterReq != 1 {
		t.Fatalf("rollback changed incident requests to %d", afterReq)
	}
}
