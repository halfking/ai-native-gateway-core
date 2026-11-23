package dashboardapi

// dashboard_window_test.go —— 仪表盘族「共享窗口」的行为契约
// （2026-11-23 会话统计/看板日期范围对齐轮）。
//
// # 这道门守什么
//
// 用户报的缺陷是「会话统计页的日期范围与看板不一致」。根因有两层：
//
//	① **前端**：会话统计的控件只有 4 个滚动档位（近24小时/7/30/90 天）、
//	   没有自定义区间；看板的控件是 7 个日历日预设 + 自定义起止。
//	② **后端**：会话总览虽然**接受** start_date/end_date，但此前只做校验、
//	   窗口谓词仍逐字是 `first_request_at >= NOW() - INTERVAL 'N days'`
//	   ⇒ **显式区间被静默忽略**（界面显示的区间 ≠ 数据窗口）。
//
// ⇒ 后端这一层必须钉死三条不变量：
//
//	① **days 路径逐字不变**（现役调用方零影响）：没有显式区间时，SQL 仍是
//	   `col >= NOW() - INTERVAL 'N days'` 的字面量形态、**不占参数位**。
//	② **显式区间真的生效**：calendar 模式必须用参数化的 [start, end)
//	   （end 独占 ⇒ 末日整天被包含），且边界 = 系统时区日历日。
//	③ **上一周期与日桶跟着窗口走**：prevClause = [start-窗口长, start)；
//	   日桶按系统时区日历日分桶（否则趋势横轴比所选区间整体前移一天）。

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// withFixedTZ 把系统时区钉成 Asia/Shanghai（默认口径），返回还原函数。
func withFixedTZ(t *testing.T) {
	t.Helper()
	settings.SystemTimezoneLocationForTest("Asia/Shanghai")
	t.Cleanup(func() { settings.SystemTimezoneLocationForTest("") })
}

// TestWindowDaysPathIsByteIdentical：days 路径**逐字不变**。
//
// ★ 这是「现役调用方零影响」的唯一保证：任何时候没传 start_date/end_date，
//
//	生成的 SQL 片段必须与改动前**逐字相同**，且**不追加任何参数**
//	（追加参数会让既有占位符编号全部错位 ⇒ 运行时参数个数不匹配）。
func TestWindowDaysPathIsByteIdentical(t *testing.T) {
	withFixedTZ(t)

	r := httptest.NewRequest("GET", "/api/admin/dashboard/session-overview?days=7", nil)
	win, err := parseSessionWindow(r, QueryParams{Days: 7})
	if err != nil {
		t.Fatalf("parseSessionWindow 报错: %v", err)
	}
	if win.calendar {
		t.Fatal("没传 start_date/end_date 却落进 calendar 模式 ⇒ 现役口径被改")
	}
	if win.days != 7 {
		t.Errorf("win.days = %d，期望 7（跟随请求参数）", win.days)
	}

	args := []interface{}{}
	argIdx := 1
	got := win.clause("first_request_at", &args, &argIdx)
	const want = "first_request_at >= NOW() - INTERVAL '7 days'"
	if got != want {
		t.Errorf("rolling 窗口片段 = %q，期望逐字 %q", got, want)
	}
	if len(args) != 0 {
		t.Errorf("rolling 路径追加了 %d 个参数（%v）⇒ 既有占位符编号会错位", len(args), args)
	}
	if argIdx != 1 {
		t.Errorf("rolling 路径把 argIdx 推到 %d（期望仍为 1）", argIdx)
	}
}

// TestWindowCalendarPathUsesRealBounds：显式区间必须真的进到 SQL 谓词里。
func TestWindowCalendarPathUsesRealBounds(t *testing.T) {
	withFixedTZ(t)

	r := httptest.NewRequest("GET",
		"/api/admin/dashboard/session-overview?days=7&start_date=2026-11-17&end_date=2026-11-23", nil)
	win, err := parseSessionWindow(r, QueryParams{Days: 7})
	if err != nil {
		t.Fatalf("parseSessionWindow 报错: %v", err)
	}
	if !win.calendar {
		t.Fatal("传了 start_date/end_date 却没落进 calendar 模式")
	}
	if win.startDate != "2026-11-17" || win.endDate != "2026-11-23" {
		t.Errorf("窗口日期串 = [%s, %s]，期望 [2026-11-17, 2026-11-23]",
			win.startDate, win.endDate)
	}

	// 系统时区 Asia/Shanghai ⇒ 2026-11-17 00:00 = 2026-11-16 16:00Z；
	// end 独占 ⇒ 2026-11-24 00:00 = 2026-11-23 16:00Z。
	wantStart := time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 11, 23, 16, 0, 0, 0, time.UTC)
	if !win.startUTC.Equal(wantStart) {
		t.Errorf("startUTC = %s，期望 %s（系统时区日历日折 UTC）", win.startUTC, wantStart)
	}
	if !win.endUTC.Equal(wantEnd) {
		t.Errorf("endUTC = %s，期望 %s（end 独占 = 次日零点）", win.endUTC, wantEnd)
	}

	args := []interface{}{}
	argIdx := 1
	got := win.clause("first_request_at", &args, &argIdx)
	const wantClause = "first_request_at >= $1 AND first_request_at < $2"
	if got != wantClause {
		t.Errorf("calendar 窗口片段 = %q，期望 %q", got, wantClause)
	}
	if len(args) != 2 {
		t.Fatalf("calendar 路径追加了 %d 个参数（%v），期望 2 个边界", len(args), args)
	}
	if !args[0].(time.Time).Equal(wantStart) || !args[1].(time.Time).Equal(wantEnd) {
		t.Errorf("边界参数 = %v，期望 [%s, %s]", args, wantStart, wantEnd)
	}
	if argIdx != 3 {
		t.Errorf("argIdx = %d，期望 3（两个边界参数各占一位）", argIdx)
	}
}

// TestWindowRejectsBadRanges：非法区间必须报错（由调用方回 400）。
func TestWindowRejectsBadRanges(t *testing.T) {
	withFixedTZ(t)

	cases := []struct {
		name  string
		query string
	}{
		{"只给 start", "start_date=2026-11-17"},
		{"只给 end", "end_date=2026-11-23"},
		{"end 早于 start", "start_date=2026-11-23&end_date=2026-11-17"},
		{"格式错", "start_date=2026-13-40&end_date=2026-11-23"},
		{"跨度超限", "start_date=2020-01-01&end_date=2026-11-23"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/admin/dashboard/session-overview?"+c.query, nil)
		if _, err := parseSessionWindow(r, QueryParams{Days: 7}); err == nil {
			t.Errorf("%s：期望报错，实际 nil（非法区间会被当成合法窗口静默取数）", c.name)
		}
	}
}

// TestPrevClauseFollowsCalendarWindow：上一周期 = [start-窗口长, start)。
func TestPrevClauseFollowsCalendarWindow(t *testing.T) {
	withFixedTZ(t)

	r := httptest.NewRequest("GET",
		"/api/admin/dashboard/session-overview?start_date=2026-11-17&end_date=2026-11-23", nil)
	win, err := parseSessionWindow(r, QueryParams{Days: 7})
	if err != nil {
		t.Fatalf("parseSessionWindow 报错: %v", err)
	}
	args := []interface{}{}
	argIdx := 1
	got := win.prevClause("first_request_at", &args, &argIdx)
	const wantClause = "first_request_at >= $1 AND first_request_at < $2"
	if got != wantClause {
		t.Errorf("prev 窗口片段 = %q，期望 %q", got, wantClause)
	}
	if len(args) != 2 {
		t.Fatalf("prev 追加了 %d 个参数，期望 2", len(args))
	}
	span := win.endUTC.Sub(win.startUTC)
	wantPrevStart := win.startUTC.Add(-span)
	if !args[0].(time.Time).Equal(wantPrevStart) {
		t.Errorf("上一周期起点 = %s，期望 %s（= start - 窗口长）", args[0], wantPrevStart)
	}
	if !args[1].(time.Time).Equal(win.startUTC) {
		t.Errorf("上一周期终点 = %s，期望 %s（= start）", args[1], win.startUTC)
	}
}

// TestDayExprFollowsSystemTimezone：日桶按系统时区分桶（趋势横轴与所选区间同尺）。
func TestDayExprFollowsSystemTimezone(t *testing.T) {
	withFixedTZ(t)

	// rolling 形态：逐字沿用 DATE(col)。
	rolling := sessionWindow{days: 7}
	args := []interface{}{}
	argIdx := 1
	if got := rolling.dayExpr("first_request_at", &args, &argIdx); got != "DATE(first_request_at)" {
		t.Errorf("rolling 日桶表达式 = %q，期望逐字 %q", got, "DATE(first_request_at)")
	}
	if len(args) != 0 {
		t.Errorf("rolling 日桶追加了参数（%v）⇒ 既有占位符编号会错位", args)
	}

	// calendar 形态：按系统时区日历日。
	r := httptest.NewRequest("GET",
		"/api/admin/dashboard/session-overview?start_date=2026-11-17&end_date=2026-11-23", nil)
	win, err := parseSessionWindow(r, QueryParams{Days: 7})
	if err != nil {
		t.Fatalf("parseSessionWindow 报错: %v", err)
	}
	args = []interface{}{}
	argIdx = 1
	got := win.dayExpr("first_request_at", &args, &argIdx)
	const wantExpr = "(first_request_at AT TIME ZONE $1)::date"
	if got != wantExpr {
		t.Errorf("calendar 日桶表达式 = %q，期望 %q", got, wantExpr)
	}
	if len(args) != 1 || args[0] != "Asia/Shanghai" {
		t.Errorf("日桶时区参数 = %v，期望 [Asia/Shanghai]", args)
	}
}

// TestClauseAtReusesSamePlaceholders：同一窗口在一条 SQL 里出现两次时
// 必须复用**同一组占位符**（否则参数会被追加两遍 / 占位符对不上）。
func TestClauseAtReusesSamePlaceholders(t *testing.T) {
	withFixedTZ(t)

	r := httptest.NewRequest("GET",
		"/api/admin/dashboard/session-overview?start_date=2026-11-17&end_date=2026-11-23", nil)
	win, err := parseSessionWindow(r, QueryParams{Days: 7})
	if err != nil {
		t.Fatalf("parseSessionWindow 报错: %v", err)
	}
	args := []interface{}{}
	argIdx := 1
	where := win.clause("first_request_at", &args, &argIdx)
	filter := win.clauseAt("ss.first_request_at", 1) // 复用同一组占位符
	if filter != "ss.first_request_at >= $1 AND ss.first_request_at < $2" {
		t.Errorf("FILTER 片段 = %q，期望复用 $1/$2", filter)
	}
	if where == filter {
		t.Error("WHERE 与 FILTER 的列名写法应不同（一个裸列、一个带 ss. 前缀）")
	}
	if len(args) != 2 {
		t.Errorf("参数数 = %d，期望仍是 2（FILTER 不得再追加参数）", len(args))
	}
}
