package bg

// report_rollup_worker_realdb_test.go —— 对账报表 worker 追赶编排的真库
// 回归（2026-09-25 审计轮）。门控与 db 包 realdb 测试相同：
// TEST_DATABASE_URL / TEST_DB_URL 未设置时跳过；目标库应为一次性 scratch 库。
//
// 验证三件事：
//  1. MissingDatesOverride 注入被 catchUpMissed 消费（编排接线正确）；
//  2. 昨日（runRecovered 已跑）被跳过，不重复聚合；
//  3. 缺失日期经 RollupDate 真正补上 daily_total 行（含零流量日），
//     追赶后 MissingRollupDates 不再报该日缺失——闭环收敛。
import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/reportrollup"
)

func TestReportRollupWorker_CatchUp_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect: %v", err)
	}
	defer pool.Close()

	w := NewReportRollupWorker(pool)
	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1).Truncate(24 * time.Hour)
	threeDaysAgo := now.AddDate(0, 0, -3).Truncate(24 * time.Hour)

	// 前置：两张目标表清空（scratch 库纪律）。
	for _, tbl := range []string{"report_snapshots", "usage_facts"} {
		if _, err := pool.Exec(ctx, "DELETE FROM "+tbl); err != nil {
			t.Fatalf("clean %s: %v", tbl, err)
		}
	}

	// 注入缺失探测：昨日 + 前日（昨日由 runRecovered 先跑，catchUp 应跳过）。
	var probed int
	w.MissingDatesOverride = func(ctx context.Context, lookbackDays int, now time.Time) ([]time.Time, error) {
		probed++
		return []time.Time{yesterday, threeDaysAgo}, nil
	}

	// runRecovered（startup-backfill 触发路径）：先聚合昨日，再追赶缺失日。
	w.runRecovered(ctx, "test-startup")

	if probed != 1 {
		t.Errorf("missing-dates probe called %d times, want 1", probed)
	}
	// 三天前（追赶日）应有 daily_total 行（零流量日也是一行）。
	var cnt int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM report_snapshots WHERE scope = 'daily_total' AND report_date = $1`,
		threeDaysAgo).Scan(&cnt); err != nil {
		t.Fatalf("count catchup day: %v", err)
	}
	if cnt != 1 {
		t.Errorf("catchup day rows = %d, want 1", cnt)
	}
	// 追赶收敛：真实探测不再报这两日缺失。
	missing, err := reportrollup.MissingRollupDates(ctx, pool, 7, now)
	if err != nil {
		t.Fatalf("MissingRollupDates after catchup: %v", err)
	}
	for _, d := range missing {
		if d.Equal(yesterday) || d.Equal(threeDaysAgo) {
			t.Errorf("day %s still reported missing after catch-up", d.Format("2006-01-02"))
		}
	}

	// 跳过语义：skip（昨日，午夜截断刻度）必须与探测输出相等——再跑一次
	// 追赶，只应补跑 threeDaysAgo 一天（昨日被跳过；幂等使重复无害，
	// 但跳过是设计契约，此处是真断言而非注释声明）。
	if got := w.catchUpMissed(ctx, "test-skip-assert", yesterday); got != 1 {
		t.Errorf("catchUpMissed attempted %d days, want 1 (yesterday must be skipped)", got)
	}

	// 清理。
	if _, err := pool.Exec(ctx, "DELETE FROM report_snapshots"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
