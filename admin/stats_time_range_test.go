package admin

import (
	"net/http/httptest"
	"testing"
)

// 2026-11-23 批判式审计轮：统计端点统一时间范围解析的钉测。
//
// ★ 要钉住的契约：
//  1. days 分支：旧行为逐字保留（相对现在的滚动窗口 + 旧标签形态）。
//  2. start/end 分支：真实区间（[start, end) 半开，end 日全天计入）。
//  3. 半截区间（只给 start 或只给 end）⇒ 报错（不静默回落预设）。
//  4. 越界（>90 天 / days>90 / 非法 days）⇒ 报错（不静默夹紧）。
//  5. SQL 谓词两种形态：days ⇒ 零参数字面量；自定义 ⇒ 参数化时间戳。
func TestParseStatsTimeRange(t *testing.T) {
	// ── days 分支：旧语义 ──────────────────────────────
	cases := []struct {
		query       string
		defaultDays int
		wantDays    int
		wantHours   int
		wantCustom  bool
	}{
		{"", 1, 1, 24, false},
		{"?days=7", 1, 7, 168, false},
		{"?days=30", 1, 30, 720, false},
		{"?days=90", 1, 90, 2160, false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/x"+c.query, nil)
		tr, err := parseStatsTimeRange(r, c.defaultDays)
		if err != nil {
			t.Fatalf("parseStatsTimeRange(%q) unexpected error: %v", c.query, err)
		}
		if tr.Days != c.wantDays || tr.Hours != c.wantHours || tr.Custom != c.wantCustom {
			t.Errorf("parseStatsTimeRange(%q) = days %d/hours %d/custom %v, want %d/%d/%v",
				c.query, tr.Days, tr.Hours, tr.Custom, c.wantDays, c.wantHours, c.wantCustom)
		}
	}

	// ── start/end 分支：真实区间 ───────────────────────
	r := httptest.NewRequest("GET", "/x?start=2026-09-01&end=2026-09-10", nil)
	tr, err := parseStatsTimeRange(r, 7)
	if err != nil {
		t.Fatalf("custom range unexpected error: %v", err)
	}
	if !tr.Custom {
		t.Fatal("custom range must set Custom=true")
	}
	// 含首尾日 ⇒ 09-01~09-10 是 10 天。
	if tr.Days != 10 {
		t.Errorf("custom range days = %d, want 10 (含首尾日)", tr.Days)
	}
	if tr.Hours != 240 {
		t.Errorf("custom range hours = %d, want 240", tr.Hours)
	}
	// [start, end) 半开区间：End = end 日零点 + 1 天。
	if got := tr.End.Sub(tr.Start).Hours(); got != 240 {
		t.Errorf("custom range span = %vh, want 240h", got)
	}

	// ── 错误分支：一律报错，不静默回落 ─────────────────
	bad := []string{
		"?start=2026-09-01",                // 半截区间
		"?end=2026-09-10",                  // 半截区间
		"?start=2026-09-10&end=2026-09-01", // end 早于 start
		"?start=2026-09-01&end=2026-12-31", // 121 天 > 90
		"?days=0",                           // 下界外
		"?days=91",                          // 上界外
		"?days=abc",                         // 非整数
		"?start=not-a-date&end=2026-09-10",  // 非法日期
	}
	for _, q := range bad {
		r := httptest.NewRequest("GET", "/x"+q, nil)
		if _, err := parseStatsTimeRange(r, 7); err == nil {
			t.Errorf("parseStatsTimeRange(%q) must error, got nil", q)
		}
	}
}

// TestStatsTimeRangeSQLPredicate 钉住两种 SQL 谓词形态：
// days ⇒ 零参数（字面量 interval，占位符编号不变）；
// 自定义 ⇒ 参数化时间戳（占 $1..$2）。
func TestStatsTimeRangeSQLPredicate(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?days=7", nil)
	tr, err := parseStatsTimeRange(r, 7)
	if err != nil {
		t.Fatal(err)
	}
	pred, args := tr.sqlPredicate("ts", 1)
	if len(args) != 0 {
		t.Errorf("days branch must have zero args, got %d", len(args))
	}
	if pred != "ts >= NOW() - INTERVAL '168 hours'" {
		t.Errorf("days predicate = %q", pred)
	}

	r2 := httptest.NewRequest("GET", "/x?start=2026-09-01&end=2026-09-10", nil)
	tr2, err := parseStatsTimeRange(r2, 7)
	if err != nil {
		t.Fatal(err)
	}
	pred2, args2 := tr2.sqlPredicate("ts", 1)
	if pred2 != "ts >= $1 AND ts < $2" {
		t.Errorf("custom predicate = %q", pred2)
	}
	if len(args2) != 2 {
		t.Fatalf("custom branch must have 2 args, got %d", len(args2))
	}
	// 占位符顺延：argIdx=3 时谓词必须是 $3/$4（tenant 片段才能接在后面）。
	pred3, args3 := tr2.sqlPredicate("ts", 3)
	if pred3 != "ts >= $3 AND ts < $4" {
		t.Errorf("custom predicate at idx 3 = %q", pred3)
	}
	if len(args3) != 2 {
		t.Fatalf("custom branch args = %d, want 2", len(args3))
	}
}

// TestStatsTimeRangeLabel 钉住窗口标签形态：days 走旧标签，自定义显示日期串。
//
// ★ 时区坑：标签必须用**原始日期串**，不能用 UTC 瞬时 Format
//   （UTC+8 下 2026-09-01 的零点 = 2026-08-31T16:00Z ⇒ 会显示成前一天）。
func TestStatsTimeRangeLabel(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?days=7", nil)
	tr, _ := parseStatsTimeRange(r, 7)
	if tr.label() != "7d" {
		t.Errorf("days label = %q, want 7d", tr.label())
	}
	r2 := httptest.NewRequest("GET", "/x?start=2026-09-01&end=2026-09-10", nil)
	tr2, _ := parseStatsTimeRange(r2, 7)
	if tr2.label() != "2026-09-01~2026-09-10" {
		t.Errorf("custom label = %q", tr2.label())
	}
}
