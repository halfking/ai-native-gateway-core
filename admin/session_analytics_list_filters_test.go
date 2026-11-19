package admin

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

func TestAppendSessionListFilters_TaskAndRFC3339(t *testing.T) {
	where, args, next, err := appendSessionAnalyticsListFilters(
		" WHERE 1=1", nil, 2,
		"task-9",
		"2026-10-01T00:00:00Z",
		"2026-10-02T12:00:00Z",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "sd.task_id = $2 OR ss.gw_task_id = $2") {
		t.Fatalf("task filter missing: %s", where)
	}
	if !strings.Contains(where, "ss.last_request_at >= $3") || !strings.Contains(where, "ss.first_request_at <= $4") {
		t.Fatalf("date filter missing: %s", where)
	}
	if next != 5 {
		t.Fatalf("next arg = %d, want 5", next)
	}
	if len(args) != 3 || args[0] != "task-9" {
		t.Fatalf("args = %#v", args)
	}
	from := args[1].(time.Time)
	to := args[2].(time.Time)
	if !from.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("from = %s", from)
	}
	if !to.Equal(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("to = %s", to)
	}
}

// TestAppendSessionListFilters_CalendarDayIsInclusive —— 钉测「纯日历日」在系统时区下的语义。
//
// ★ 为什么本门必须显式钉时区（2026-11-20 审计轮修复）：
//
//	时区轮（e3fc236ee）把纯日期串（"2026-10-01"）的解释口径从「UTC 零点」
//	改成「**系统时区**零点」（settings.ParseSystemDate）⇒ 同一串日期在
//	不同系统时区下对应**不同的 UTC 瞬间**。
//	⇒ 本门若不钉时区，判据就随「跑测机器/进程的默认时区」漂移
//	⇒ 表现形态是「本机绿、CI 红」或「CI 绿、本机红」的薛定谔红。
//	⇒ 纪律：**凡依赖系统时区的判据，一律用 settings.SystemTimezoneLocationForTest
//	把时区钉死**（与 admin/board_time_range_test.go、admin/usage_timerange_test.go 同款）。
//
// ★ 语义断言（Asia/Shanghai = UTC+8）：
//
//	from = 2026-10-01 00:00 **+08:00** = 2026-09-30 16:00:00 UTC
//	to   = 2026-10-02 00:00 **+08:00** − 1µs = 2026-10-01 15:59:59.999999 UTC
//	（endOfDay 的 −1µs 保证「当日最后一刻」被包含 ⇒ 日历日闭区间语义不变）
func TestAppendSessionListFilters_CalendarDayIsInclusive(t *testing.T) {
	settings.SystemTimezoneLocationForTest("Asia/Shanghai")
	t.Cleanup(func() { settings.SystemTimezoneLocationForTest("") })

	_, args, _, err := appendSessionAnalyticsListFilters(" WHERE 1=1", nil, 1, "", "2026-10-01", "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	from := args[0].(time.Time)
	to := args[1].(time.Time)

	sh := time.FixedZone("CST", 8*3600)
	wantFrom := time.Date(2026, 10, 1, 0, 0, 0, 0, sh)
	wantTo := time.Date(2026, 10, 2, 0, 0, 0, 0, sh).Add(-time.Microsecond)

	if !from.Equal(wantFrom) {
		t.Fatalf("from = %s（期望 %s）", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Fatalf("to = %s（期望 %s）", to, wantTo)
	}
	// ★ 日历日闭区间语义本身的钉测（不依赖具体时区偏移）：
	//   from 与 to 必须落在**同一个**日历日内，且 to 距该日 00:00 不到 24h。
	if !to.After(from) {
		t.Fatalf("to 必须晚于 from（闭区间语义丢失）：from=%s to=%s", from, to)
	}
	if to.Sub(from) >= 24*time.Hour {
		t.Fatalf("to-from = %s，跨度必须 < 24h（否则含次日整天 ⇒ 闭区间语义错）", to.Sub(from))
	}
}

func TestAppendSessionListFilters_AbsentParamsDoNotChangeWhere(t *testing.T) {
	where, args, next, err := appendSessionAnalyticsListFilters(" WHERE 1=1", []any{"tenant"}, 2, "  ", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if where != " WHERE 1=1" || len(args) != 1 || next != 2 {
		t.Fatalf("where=%q args=%#v next=%d", where, args, next)
	}
}

func TestListTaskFilterColumnsExistInStartupMigrations(t *testing.T) {
	where, _, _, err := appendSessionAnalyticsListFilters(" WHERE 1=1", nil, 1, "task-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where, "sd.task_id") || !strings.Contains(where, "ss.gw_task_id") {
		t.Fatalf("where = %s", where)
	}
	summaries, err := os.ReadFile("../sql/migrations/startup/655_session_summaries_schema_reconcile.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summaries), "ADD COLUMN IF NOT EXISTS gw_task_id text") {
		t.Fatal("ss.gw_task_id is not a session_summaries column in migration 655")
	}
	dim, err := os.ReadFile("../sql/migrations/startup/350_session_analytics_fix.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dim), "task_id       VARCHAR(128)") {
		t.Fatal("sd.task_id is not declared on session_dim in migration 350")
	}
}

func TestAppendSessionListFilters_RejectsIgnoredShapes(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
		want string
	}{
		{"bad from", "10/01/2026", "", "invalid date_from"},
		{"bad to", "", "yesterday", "invalid date_to"},
		{"inverted", "2026-10-02T00:00:00Z", "2026-10-01T00:00:00Z", "date_from must be <= date_to"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := appendSessionAnalyticsListFilters(" WHERE 1=1", nil, 1, "", tc.from, tc.to)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
