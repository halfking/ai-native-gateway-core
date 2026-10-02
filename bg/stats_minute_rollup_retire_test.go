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

// 12h 审计钉测：迟到冲刷宽限重扫。累加器 flush（30s tick）晚于 rollup
// （60s tick）时会在 M+1:00~M+1:30 重建刚退役的 M 分钟悬空键；若 retire
// 下界仍钉在游标 since=M+1:00，该键永不再被扫到。retireScanFloor 必须把
// 下界拉回 until-grace，保证最近 grace 窗内的已闭分钟每 tick 重扫。
func TestRetireScanFloorKeepsGraceLookback(t *testing.T) {
	until := time.Date(2026, 10, 1, 4, 0, 5, 0, time.UTC)

	// 常态：游标已追平当前分钟 —— 下界 = until-grace，覆盖最近两个已闭分钟。
	since := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	want := until.Add(-retireGraceWindow)
	got := retireScanFloor(since, until)
	if !got.Equal(want) {
		t.Fatalf("cursor caught up: floor = %v, want %v", got, want)
	}
	if !got.Before(since) {
		t.Fatalf("cursor caught up: floor %v must look back before since %v", got, since)
	}

	// 回填/落后：游标早于 grace 窗 —— 下界取 since，行为与历史一致。
	oldSince := time.Date(2026, 9, 30, 22, 0, 0, 0, time.UTC)
	if got := retireScanFloor(oldSince, until); !got.Equal(oldSince) {
		t.Fatalf("cursor behind: floor = %v, want since %v", got, oldSince)
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

// P2-2 钉测：维度/钻取表的闭分钟退役必须与 rollupDims 同口径——上界开
// 区间保当前分钟、存在性检查走同一视图、error_kind 只认 failure、扫描
// 下界与主表共用 retireScanFloor（迟到冲刷对维度表同样成立）。
func TestRetireClosedDimsMatchesRollupDimShape(t *testing.T) {
	sql := retireClosedDimStatement(`COALESCE(NULLIF(r.client_profile, ''), '__unknown__')`)
	if !strings.Contains(sql, "m.bucket < date_trunc('minute', $2::timestamptz)") {
		t.Fatal("dim retire must stop before the open minute")
	}
	if strings.Contains(sql, "m.bucket <= date_trunc('minute', $2") {
		t.Fatal("dim retire includes the open minute")
	}
	if !strings.Contains(sql, "request_logs_with_current_month") {
		t.Fatal("dim retire existence check drifted off the rollup view")
	}
	if !strings.Contains(sql, "($3 <> 'error_kind' OR r.request_status = 'failure')") {
		t.Fatal("dim retire must keep the error_kind failure-only filter in sync with rollupDims")
	}
	if !strings.Contains(sql, `= m.dim_key`) {
		t.Fatal("dim retire must compare the rollup dim_key expression against m.dim_key")
	}
	rollupSrc, err := os.ReadFile("stats_minute_rollup.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rollupSrc), "return w.retireClosedDims(ctx, since, until)") {
		t.Fatal("rollupDims no longer retires dim/drill keys the view stopped emitting")
	}
}

func TestRetireClosedErrorDrillMatchesRollupDrillShape(t *testing.T) {
	sql := retireClosedErrorDrillMinuteSQL
	if !strings.Contains(sql, "m.bucket < date_trunc('minute', $2::timestamptz)") {
		t.Fatal("drill retire must stop before the open minute")
	}
	if !strings.Contains(sql, "r.request_status = 'failure'") {
		t.Fatal("drill retire must stay failure-only like rollupDims")
	}
	// 哨兵口径必须与 rollupDims 的 drill SELECT 一致：model_name 空串、
	// error_kind '__unknown__'。口径漂移会让 NOT EXISTS 误删视图仍在
	// 产出的键（把活数据当悬空键清掉）。
	if !strings.Contains(sql, "COALESCE(NULLIF(r.outbound_model, ''), NULLIF(r.client_model, ''), '') = m.model_name") {
		t.Fatal("drill retire model_name sentinel drifted from rollupDims")
	}
	if !strings.Contains(sql, "COALESCE(NULLIF(r.error_kind, ''), '__unknown__') = m.error_kind") {
		t.Fatal("drill retire error_kind sentinel drifted from rollupDims")
	}
	retireSrc, err := os.ReadFile("stats_minute_rollup_retire.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(retireSrc), "floor := retireScanFloor(since, until)") {
		t.Fatal("dim/drill retire must share the late-flush grace rescan floor")
	}
}
