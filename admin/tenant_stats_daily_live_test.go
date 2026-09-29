package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tenant_stats_daily_live_test.go — live-DB regression for the tenant stats
// daily time series (2026-09-30 统计 UI 优化轮).
//
// getTenantStats 新增的 daily 序列依赖 generate_series 左连接补零 +
// FILTER 聚合（成功/失败分桶），这类 SQL 形状（42803 generate_series /
// FILTER 语法 / 类型转换 $2::int）无法被 sqlmock 捕获，必须对真实
// PostgreSQL 验证。本测试种子 3 行请求（今日 2 行含 1 失败、昨日 1 行、
// 前天故意留空验证零填充），调用真实 handler，校验序列长度/分桶/求和，
// 结束后清理种子（零残留）。
//
// Run with:
//
//	TEST_DATABASE_URL=postgres://... go test -count=1 -run TestTenantStatsDaily_Live ./admin
func TestTenantStatsDaily_Live(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live-DB regression")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const code = "zz-stats-daily"

	// ── 清场 + 种子 ──────────────────────────────────────
	cleanup := func() {
		//nolint:errcheck // 清场 best-effort
		pool.Exec(ctx, `DELETE FROM request_logs_hot WHERE tenant_id = $1`, code)
		//nolint:errcheck // 清场 best-effort
		pool.Exec(ctx, `DELETE FROM tenants WHERE code = $1`, code)
	}
	cleanup()
	defer cleanup()

	if _, err := pool.Exec(ctx, `
		INSERT INTO tenants (code, name, status) VALUES ($1, 'ZZ Stats Daily', 'active')
	`, code); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	// 今日 2 行（1 成功 1 失败）、昨日 1 行；前日留空验证零填充。
	seed := `
		INSERT INTO request_logs_hot (request_id, ts, tenant_id, success, total_tokens, credits_charged, cost_usd)
		VALUES
		 ('zz-stats-daily-r1', now(),                        $1, true,  100, 10, 0.5),
		 ('zz-stats-daily-r2', now(),                        $1, false,  50,  0, 0.0),
		 ('zz-stats-daily-r3', now() - INTERVAL '1 day',     $1, true,   70,  7, 0.3)`
	if _, err := pool.Exec(ctx, seed, code); err != nil {
		t.Fatalf("seed request_logs_hot: %v", err)
	}

	// ── 调用真实 handler ────────────────────────────────
	h := &Handler{db: pool}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/"+code+"/stats?days=7", nil)
	h.getTenantStats(rec, req, code)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got struct {
		Days  int `json:"days"`
		Daily []struct {
			Date     string  `json:"date"`
			Requests int64   `json:"requests"`
			Success  int64   `json:"success"`
			Errors   int64   `json:"errors"`
			Tokens   int64   `json:"tokens"`
			Credits  int64   `json:"credits"`
			Cost     float64 `json:"cost_usd"`
		} `json:"daily"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// ── 断言：7 条连续日期 + 分桶正确 + 零填充 ──────────
	if got.Days != 7 {
		t.Fatalf("days = %d, want 7", got.Days)
	}
	if len(got.Daily) != 7 {
		t.Fatalf("daily len = %d, want 7 (generate_series zero-fill)", len(got.Daily))
	}
	today := got.Daily[6]
	yesterday := got.Daily[5]
	dayBefore := got.Daily[4]
	if today.Requests != 2 || today.Success != 1 || today.Errors != 1 {
		t.Fatalf("today bucket = %+v, want requests=2 success=1 errors=1", today)
	}
	if today.Tokens != 150 || today.Credits != 10 || today.Cost < 0.49 || today.Cost > 0.51 {
		t.Fatalf("today aggregate = %+v, want tokens=150 credits=10 cost≈0.5", today)
	}
	if yesterday.Requests != 1 || yesterday.Success != 1 || yesterday.Errors != 0 {
		t.Fatalf("yesterday bucket = %+v, want requests=1 success=1", yesterday)
	}
	if dayBefore.Requests != 0 || dayBefore.Tokens != 0 {
		t.Fatalf("day-before bucket = %+v, want zero-filled", dayBefore)
	}
	// 日期连续且升序。
	for i := 1; i < len(got.Daily); i++ {
		prev, _ := time.Parse("2006-01-02", got.Daily[i-1].Date)
		cur, _ := time.Parse("2006-01-02", got.Daily[i].Date)
		if cur.Sub(prev) != 24*time.Hour {
			t.Fatalf("daily not consecutive at %d: %s -> %s", i, got.Daily[i-1].Date, got.Daily[i].Date)
		}
	}
}
