//go:build integration

package bg

// stats_minute_rollup_rebuild_integration_test.go
//
// 真库门：历史回填所依赖的两条语句——整键替换（rollupMainStatement）与闭分钟
// 退役（retireClosedMainMinuteSQL）——必须在真实 PostgreSQL 上可规划。
//
// 这两条是修好存量脏数据的唯一手段：游标只前进，已聚合的分钟不会被重算，
// 只有 RebuildRangeStats 能覆盖过去。而它们各自的探测排除谓词是**运行时**
// fmt.Sprintf 拼进去的，静态断言看不见——单测里那句 Contains(constName) 在正确
// 代码上反而恒红（成品 SQL 里根本没有常量名）。
//
// 这里只 EXPLAIN，不落数据：回填会重写全租户聚合表，测试不做写入。
//
// 跑法：
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./bg/ -run TestStatsRebuildSQLParses

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/maas"
)

func TestStatsRebuildSQLParses(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset — skipping does NOT constitute evidence the rebuild SQL parses")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	from := time.Now().UTC().Truncate(24 * time.Hour)
	to := from.Add(24 * time.Hour)
	credits := maas.RequestLogCreditsSQL("r", true)

	cases := []struct {
		name string
		sql  string
	}{
		{"rollup-main", rollupMainStatement(credits)},
		{"retire-main", retireClosedMainMinuteSQL},
		{"retire-dim", retireClosedDimStatement(`COALESCE(NULLIF(r.client_profile, ''), '__unknown__')`)},
		{"retire-error-drill", retireClosedErrorDrillMinuteSQL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// EXPLAIN 只规划不执行：回填是重写全租户聚合的写操作。
			if _, err := pool.Exec(ctx, "EXPLAIN "+tc.sql, from, to); err != nil {
				t.Fatalf("%s failed to plan against the real schema: %v", tc.name, err)
			}
			if !strings.Contains(tc.sql, "node-probe-worker") {
				t.Errorf("%s lost its probe exclusion in the rendered SQL", tc.name)
			}
		})
	}
}

// TestStatsRebuildCorrectsPollutedRow is the behavioural half a plan check
// cannot give: it writes a polluted rollup row, runs the rebuild statements, and
// requires the probe-inflated key to be gone and the business count to be exact.
//
// Runs inside a transaction that is rolled back, so it leaves no data behind.
func TestStatsRebuildCorrectsPollutedRow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset — skipping does NOT constitute evidence the rebuild corrects data")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	// Pick a minute that actually has traffic so there is something to correct.
	var bucket time.Time
	var tenant string
	err = tx.QueryRow(ctx, `
		SELECT date_trunc('minute', ts), COALESCE(NULLIF(tenant_id, ''), 'default')
		FROM request_logs_with_current_month
		WHERE ts > now() - interval '3 days'
		  AND request_status IN ('success', 'failure', 'rate_limited')
		LIMIT 1
	`).Scan(&bucket, &tenant)
	if err != nil {
		t.Skipf("no recent traffic to rebuild against: %v", err)
	}
	until := bucket.Add(time.Minute)

	// Seed the shape the pre-fix code produced: a key that exists only because
	// probe traffic produced it (tenant that never appears in real traffic).
	const ghostTenant = "__probe_only_tenant__"
	if _, err := tx.Exec(ctx, `
		INSERT INTO request_stats_minute
			(bucket, tenant_id, provider_id, canonical_id, requests, success_count, failure_count)
		VALUES ($1, $2, 0, 0, 999, 0, 999)
	`, bucket, ghostTenant); err != nil {
		t.Fatalf("seed polluted row: %v", err)
	}

	credits := maas.RequestLogCreditsSQL("r", true)
	if _, err := tx.Exec(ctx, rollupMainStatement(credits), bucket, until); err != nil {
		t.Fatalf("rollup main: %v", err)
	}
	if _, err := tx.Exec(ctx, retireClosedMainMinuteSQL, bucket, until); err != nil {
		t.Fatalf("retire main: %v", err)
	}

	var ghost int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM request_stats_minute
		WHERE bucket = $1 AND tenant_id = $2
	`, bucket, ghostTenant).Scan(&ghost); err != nil {
		t.Fatal(err)
	}
	if ghost != 0 {
		t.Errorf("probe-only key survived the rebuild (%d rows): the retire pass must delete "+
			"keys the probe-filtered view no longer produces", ghost)
	}

	// The real tenant's minute must now equal the probe-filtered view exactly.
	var minuteReq, viewReq int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(requests), 0) FROM request_stats_minute
		WHERE bucket = $1 AND tenant_id = $2
	`, bucket, tenant).Scan(&minuteReq); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM request_logs_with_current_month
		WHERE request_status IN ('success', 'failure', 'rate_limited')
		  AND ts >= $1 AND ts < $2
		  AND COALESCE(NULLIF(tenant_id, ''), 'default') = $3
		  AND `+bgProbePredicate+`
	`, bucket, until, tenant).Scan(&viewReq); err != nil {
		t.Fatal(err)
	}
	if minuteReq != viewReq {
		t.Errorf("rebuilt minute = %d requests, probe-filtered view = %d", minuteReq, viewReq)
	}
	t.Logf("rebuilt %s for tenant %s: %d requests (view agrees)", bucket, tenant, minuteReq)
}

// bgProbePredicate is the rendered view-safe probe predicate, duplicated here as
// a literal only because the test asserts a property of the DATA (the rebuilt
// row equals the filtered view) rather than of the production statement. The
// production statements themselves are checked by TestStatsRebuildSQLParses.
const bgProbePredicate = `(NOT COALESCE('probe' = ANY(r.quality_flags), FALSE)` +
	` AND COALESCE(r.task_type, '') <> 'probe_triggered'` +
	` AND COALESCE(r.origin_actor, '') NOT IN ('node-probe-worker', 'active-probe-worker', 'probe-service', 'credential-selfcheck-worker'))`
