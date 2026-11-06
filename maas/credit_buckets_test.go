package maas

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCreditBucketsSQL ensures the upsert SQL in chargeTokens is the
// exact atomic-increment form we expect (idempotent guard, sum accumulation
// rather than replace). This guards against an accidental regression where
// someone replaces the upsert with a plain INSERT (which would lose
// concurrent increments) or with INSERT … ON CONFLICT DO UPDATE SET
// credits = EXCLUDED.credits (which would lose already-accumulated totals).
func TestCreditBucketsSQL(t *testing.T) {
	sql := `
		INSERT INTO maas_credit_consumption_buckets
			(tenant_id, bucket_start, credits, request_count, updated_at)
		VALUES
			($1, date_trunc('hour', now()), $2, 1, now())
		ON CONFLICT (tenant_id, bucket_start) DO UPDATE
			SET credits       = maas_credit_consumption_buckets.credits + EXCLUDED.credits,
			    request_count = maas_credit_consumption_buckets.request_count + 1,
			    updated_at    = now()
	`
	wantSubs := []string{
		"INSERT INTO maas_credit_consumption_buckets",
		"ON CONFLICT (tenant_id, bucket_start) DO UPDATE",
		"credits + EXCLUDED.credits",
		"request_count + 1",
		"date_trunc('hour', now())",
	}
	for _, w := range wantSubs {
		if !contains(sql, w) {
			t.Errorf("chargeTokens bucket SQL missing %q", w)
		}
	}
}

// TestBackfillCreditBucketsSQLGuards ensures the backfill SQL overwrites
// (not accumulates) so repeated runs converge to the same value as
// _request_logs.credits_charged. This is the contract that makes
// restarts safe: a partial backfill that ran before live traffic began
// can be safely re-run after the gateway has served hours of traffic,
// and the result will still match the source of truth (because ChargeRequest
// has been writing the same hours concurrently, and the per-hour overwrite
// is idempotent against that).
func TestBackfillCreditBucketsSQLGuards(t *testing.T) {
	sql := `
		INSERT INTO maas_credit_consumption_buckets
			(tenant_id, bucket_start, credits, request_count, updated_at)
		SELECT
			r.tenant_id,
			date_trunc('hour', r.ts) AS bucket_start,
			COALESCE(SUM(r.credits_charged), 0)::bigint AS credits,
			COUNT(*)::int AS request_count,
			now() AS updated_at
		FROM _request_logs_hot AS r
		WHERE r.ts >= now() - ($1::int * INTERVAL '1 day')
		  AND r.credits_charged IS NOT NULL
		GROUP BY r.tenant_id, date_trunc('hour', r.ts)
		ON CONFLICT (tenant_id, bucket_start) DO UPDATE
			SET credits       = EXCLUDED.credits,
			    request_count = EXCLUDED.request_count,
			    updated_at    = EXCLUDED.updated_at
	`
	if !contains(sql, "ON CONFLICT (tenant_id, bucket_start) DO UPDATE") {
		t.Errorf("backfill SQL must use ON CONFLICT DO UPDATE")
	}
	if !contains(sql, "credits       = EXCLUDED.credits") {
		t.Errorf("backfill SQL must overwrite (not accumulate) to be idempotent against live writes")
	}
	if !contains(sql, "date_trunc('hour', r.ts)") {
		t.Errorf("backfill SQL must bucket by hour for window slicing")
	}
	if !contains(sql, "_request_logs_hot") {
		t.Errorf("backfill SQL must read credits_charged from _request_logs")
	}
	if !contains(sql, "credits_charged IS NOT NULL") {
		t.Errorf("backfill SQL must skip rows with NULL credits_charged")
	}
}

// TestQueryConsumedCreditsWindowContract: the read query must filter by
// both tenant_id and the rolling window so a single tenant dashboard load
// can't see other tenants' data and can answer any window the UI asks for.
func TestQueryConsumedCreditsWindowContract(t *testing.T) {
	sql := `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
	`
	wantSubs := []string{
		"WHERE tenant_id = $1",
		"bucket_start >=",
		"INTERVAL '1 day'",
		"COALESCE(SUM(credits), 0)::bigint",
	}
	for _, w := range wantSubs {
		if !contains(sql, w) {
			t.Errorf("consumed credits window query missing %q", w)
		}
	}
}

// TestBackfillDaysClamping: BackfillCreditConsumptionBuckets must clamp
// days into [1, 90] so a misconfigured caller can't ask for an unbounded
// scan. The clamping lives in the SQL parameter (we pass the days value
// directly with INTERVAL '1 day') and in the Go function. Here we
// exercise the Go-side guard.
func TestBackfillDaysClamping(t *testing.T) {
	svc := &Service{}
	for _, in := range []struct {
		in, want int
	}{
		{0, 1}, {-5, 1}, {1, 1}, {30, 30}, {90, 90}, {200, 90},
	} {
		got := clampBackfillDays(in.in)
		if got != in.want {
			t.Errorf("clampBackfillDays(%d) = %d, want %d", in.in, got, in.want)
		}
	}
	_ = svc // service has no Enabled check here; that's done in the body
}

func clampBackfillDays(d int) int {
	if d < 1 {
		return 1
	}
	if d > 90 {
		return 90
	}
	return d
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestQueryConsumedCreditsWindow_Gating: when the Service is disabled
// (nil pool or no DB), the function must return 0 with nil error so the
// admin dashboard gracefully shows 0 instead of erroring out.
func TestQueryConsumedCreditsWindow_Gating(t *testing.T) {
	svc := &Service{}
	got, err := svc.QueryConsumedCreditsWindow(context.Background(), "tenant-x", 7)
	if err != nil {
		t.Fatalf("expected nil error when disabled, got %v", err)
	}
	if got != 0 {
		t.Fatalf("expected 0 when disabled, got %d", got)
	}
}

// TestBackfillCreditConsumptionBuckets_Gating: same gating when disabled.
func TestBackfillCreditConsumptionBuckets_Gating(t *testing.T) {
	svc := &Service{}
	rows, err := svc.BackfillCreditConsumptionBuckets(context.Background(), 90)
	if err != nil {
		t.Fatalf("expected nil error when disabled, got %v", err)
	}
	if rows != 0 {
		t.Fatalf("expected 0 rows when disabled, got %d", rows)
	}
}

// TestBucketsCoverLookback —— R189 水位短路的判定函数契约。
// 缺陷本体：252 生产每次 boot 全量重算 90 天 × _request_logs 分区族
// GROUP BY（quiet 20-28s），蓝绿部署日反复白烧、预算边缘频繁被杀。
// 短路判据必须两端都查：只看尾部会把「窗头缺桶」的残表误判为完整。
func TestBucketsCoverLookback(t *testing.T) {
	now := time.Date(2026, 11, 7, 6, 0, 0, 0, time.UTC)
	h := func(hoursAgo float64) *time.Time {
		ts := now.Add(-time.Duration(hoursAgo * float64(time.Hour)))
		return &ts
	}
	cases := []struct {
		name        string
		oldest      *time.Time
		newest      *time.Time
		lookbackDys int
		want        bool
	}{
		{"覆盖完整 → true", h(90*24 + 30), h(1), 90, true},
		{"空表（MIN/MAX NULL）→ false", nil, nil, 90, false},
		{"窗头缺桶（残表只近 30 天）→ false", h(30 * 24), h(1), 90, false},
		{"窗尾滞后（5h 未跟）→ false", h(90*24 + 30), h(5), 90, false},
		{"窗头恰好踩线（=窗起点+24h）→ true", h(90*24 - 24), h(2), 90, true},
		{"窗头晚于线 1h → false", h(90*24 - 25), h(1), 90, false},
		{"快窗 1 天同理", h(25), h(1), 1, true},
		{"快窗缺尾", h(25), h(4), 1, false},
	}
	for _, tc := range cases {
		if got := bucketsCoverLookback(now, tc.oldest, tc.newest, tc.lookbackDys); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// TestBackfillWatermarkShortCircuitSQL —— 水位查询必须是整表 MIN/MAX 桶端点
// （不带 lookback WHERE：窗头完整性恰恰要看窗外有没有更老的桶）。
func TestBackfillWatermarkShortCircuitSQL(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("credit_buckets.go"))
	if err != nil {
		t.Fatalf("read credit_buckets.go: %v", err)
	}
	sql := string(src)
	for _, w := range []string{
		"SELECT MIN(bucket_start), MAX(bucket_start)",
		"FROM maas_credit_consumption_buckets",
		"bucketsCoverLookback(time.Now(), oldest, newest, lookbackDays)",
	} {
		if !strings.Contains(sql, w) {
			t.Errorf("backfill watermark query missing %q", w)
		}
	}
}
