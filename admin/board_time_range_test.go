package admin

import (
	"fmt"
	"testing"
	"time"
)

func TestBoardPresetTimeRange(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 30, 0, 0, time.UTC)
	todayStart := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		days      int
		wantStart time.Time
		wantDays  int
	}{
		{1, todayStart, 1},
		{7, todayStart.Add(-6 * 24 * time.Hour), 7},
		{30, todayStart.Add(-29 * 24 * time.Hour), 30},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dd", tc.days), func(t *testing.T) {
			tr := boardPresetTimeRange(tc.days, now)
			if !tr.Start.Equal(tc.wantStart) {
				t.Fatalf("Start = %v, want %v", tr.Start, tc.wantStart)
			}
			if !tr.End.Equal(now) {
				t.Fatalf("End = %v, want %v", tr.End, now)
			}
			if tr.Days != tc.wantDays {
				t.Fatalf("Days = %d, want %d", tr.Days, tc.wantDays)
			}
			if tr.Custom {
				t.Fatal("preset range must not be custom")
			}
		})
	}
}

func TestBoardRequestLogsFromClause(t *testing.T) {
	// 2026-10-15（r1033）：看板 fallback 换到会话族原生源
	// （session_turns hot ∪ parent，115 列 v1 形状投影）。旧断言钉的是
	// 纯 v1 包装视图 request_logs_with_current_month_without_customer_id
	// ——S4 停写后该腿冻结，已弃用。
	from, alias := boardRequestLogsFromClause()
	if alias != "r" {
		t.Fatalf("alias = %q", alias)
	}
	for _, needle := range []string{
		"FROM public.session_turns_hot t",
		"FROM public.session_turns t",
		"session_turn_details_hot d",
		"LEFT JOIN public.session_turn_details d",
		"AS request_status", // 投影按 v1 列名供给（board SQL 按名字引用）
		") AS r",
	} {
		if !containsFold(from, needle) {
			t.Fatalf("from 缺 %q（会话族两腿投影不完整）", needle)
		}
	}
	if containsFold(from, "without_customer_id") {
		t.Fatalf("from 仍是纯 v1 包装视图：%q", from)
	}
}

func TestBoardLogsWhereUsesAbsoluteWindow(t *testing.T) {
	tr := boardTimeRange{
		Start: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	}
	where, args := boardLogsWhere(tr, "r", "tenant-a")
	if !containsAll(where, "r.ts >= $1", "r.ts < $2", "tenant_id = $3") {
		t.Fatalf("unexpected where: %s", where)
	}
	if len(args) != 3 {
		t.Fatalf("args len = %d", len(args))
	}
}

func TestTrendPointsCoverRange(t *testing.T) {
	tr := boardPresetTimeRange(7, time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC))
	points := []boardTrendPoint{{Bucket: tr.Start.Add(15 * time.Minute).Format(time.RFC3339)}}
	if !trendPointsCoverRange(tr, points) {
		t.Fatal("expected coverage when first bucket near range start")
	}
	late := []boardTrendPoint{{Bucket: tr.Start.Add(48 * time.Hour).Format(time.RFC3339)}}
	if trendPointsCoverRange(tr, late) {
		t.Fatal("expected no coverage when first bucket too late")
	}
}

func TestBoardTimeRangeTrendBucketMinutes(t *testing.T) {
	cases := []struct {
		name string
		tr   boardTimeRange
		want int
	}{
		{"today", boardTimeRange{Days: 1}, 5},
		{"7d", boardTimeRange{Days: 7}, 15},
		{"30d", boardTimeRange{Days: 30}, 60},
		{"custom short", boardTimeRange{
			Custom: true,
			Start:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			End:    time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		}, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tr.trendBucketMinutes(); got != tc.want {
				t.Fatalf("trendBucketMinutes() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSqlTrendBucketUsesFloorInterval(t *testing.T) {
	sql := sqlTrendBucket("bucket", 15)
	if sql == "" {
		t.Fatal("expected non-empty SQL")
	}
	if !containsAll(sql, "FLOOR", "15", "interval") {
		t.Fatalf("unexpected bucket SQL: %s", sql)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !containsFold(s, p) {
			return false
		}
	}
	return true
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && stringIndexFold(s, sub) >= 0))
}

func stringIndexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
