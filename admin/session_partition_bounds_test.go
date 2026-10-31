package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/db"
)

// session_partition_bounds_test.go —— R181 轮：分区键下界的判据。
//
// # 这道门在守什么
//
// `public.session_turns` / `session_bodies` / `session_turn_details` 都按
// **partition_date** RANGE 分区 ⇒ ⇒ PostgreSQL 的分区裁剪**只认分区键谓词**
// ⇒ ⇒ 只按 **ts** 过滤的读路径**一个分区都裁不掉**。
//
// ★ 真库 A/B（本地 PG17，5 个月分区 + default）：
//   只按 ts              ⇒ Append → 5 个分区全扫
//   ts + partition_date  ⇒ 只剩 session_turns_2026_10
// ★ 生产侧同形态已被 252 的 pg_stat_statements 观测台账记下（/api/logs 的列表
//   语句在 1 秒窗口上 Seq Scan 了 4 个整月分区）。
//
// ⇒ ⇒ 本门钉住：**带 ts 窗口的会话族读点必须同时带 partition_date 谓词**，
// 且该谓词**不得丢行**（必须是 ts 谓词的蕴含条件）。

// TestSessionPartitionDateBounds_UTCCalendarDay pins the **UTC calendar day**
// derivation.
//
// ★★ 为什么是 UTC 日历日（而不是 +08 日历日）：★★
// `partition_date` 的列值 = `calendarDate(ts)` = UTC 日历日
// （domains/session/v2/partition_date.go）⇒ ⇒ 所有写入方写入的列值都是
// 该行的 UTC 日历日 ⇒ ⇒ 折算必须同口径 ⇒ ⇒ 否则会**整月**错位。
func TestSessionPartitionDateBounds_UTCCalendarDay(t *testing.T) {
	cases := []struct {
		name       string
		start, end time.Time
		wantFrom   string
		wantTo     string
	}{
		{
			// ★ +08 的 2026-10-01 00:30 = UTC 2026-09-30 16:30 ⇒ ⇒ UTC 日历日是 09-30。
			// ★ 这正是 FIXME-14368 的形态：行住在**上月**分区里，列值却是 09-30。
			name:     "plus8_month_start_maps_to_previous_utc_day",
			start:    time.Date(2026, 10, 1, 0, 30, 0, 0, time.FixedZone("+08", 8*3600)),
			end:      time.Date(2026, 10, 1, 0, 30, 0, 0, time.FixedZone("+08", 8*3600)),
			wantFrom: "2026-09-30",
			wantTo:   "2026-09-30",
		},
		{
			name:     "plain_utc_day",
			start:    time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC),
			end:      time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC),
			wantFrom: "2026-10-05",
			wantTo:   "2026-10-07",
		},
		{
			// ★ 非法窗口（start > end）⇒ 上下界自然反向 ⇒ 谓词恒假 ⇒
			// ★ 与 ts 谓词同样返回空集 ⇒ 口径一致。
			name:     "inverted_window_stays_inverted",
			start:    time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC),
			end:      time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			wantFrom: "2026-10-07",
			wantTo:   "2026-10-05",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, to := sessionPartitionDateBounds(tc.start, tc.end)
			if from != tc.wantFrom || to != tc.wantTo {
				t.Fatalf("bounds = %q..%q, want %q..%q", from, to, tc.wantFrom, tc.wantTo)
			}
		})
	}
}

// TestSessionPartitionDateBounds_SupersetOfTSWindow pins the **no-row-loss**
// invariant: for any ts inside the window, its partition_date (== calendarDate(ts)
// == UTC calendar day) must lie inside the derived bounds.
//
// ★ 这是「加了谓词会不会漏行」的可证伪等式：★ 若折算口径写错（例如用了
// +08 日历日），这个不变式就会在 +08 凌晨 0~8 点那 8 小时上破掉。
func TestSessionPartitionDateBounds_SupersetOfTSWindow(t *testing.T) {
	// ★ 扫一整天 +08 的全部 24 小时（覆盖 0~8 点那 8 小时的边界区）。
	loc := time.FixedZone("+08", 8*3600)
	for day := 1; day <= 31; day++ {
		start := time.Date(2026, 10, day, 0, 0, 0, 0, loc)
		end := start.Add(24*time.Hour - time.Second)
		from, to := sessionPartitionDateBounds(start, end)
		f, err := time.Parse("2006-01-02", from)
		if err != nil {
			t.Fatalf("bad lower bound %q: %v", from, err)
		}
		u, err := time.Parse("2006-01-02", to)
		if err != nil {
			t.Fatalf("bad upper bound %q: %v", to, err)
		}
		for h := 0; h < 24; h++ {
			ts := start.Add(time.Duration(h) * time.Hour)
			utc := ts.UTC()
			pd := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
			if pd.Before(f) || pd.After(u) {
				t.Fatalf("ts=%s (+08) ⇒ partition_date=%s outside bounds %s..%s —— 谓词会漏行",
					ts.Format(time.RFC3339), pd.Format("2006-01-02"), from, to)
			}
		}
	}
}

// TestLogsSourceFromSQLWindowed_CarriesPartitionKeyBound pins that the list
// path's FROM source carries the partition-key bound on **both** arms and on
// the details join.
//
// ★ 三处都要有：★
//
//	① turns 热臂 · ② turns 父表臂 —— 少了任一处，那一侧就裁不掉；
//	③ details 侧（ON 子句）—— 少了它，details 的月分区**仍然全扫**
//	（实测：只给 turns 侧加下界时，details 侧仍是 3 个分区 Seq Scan）。
func TestLogsSourceFromSQLWindowed_CarriesPartitionKeyBound(t *testing.T) {
	sql := logsSourceFromSQLWindowed("$1", "$2")
	if !strings.Contains(sql, "t.partition_date >= $1") || !strings.Contains(sql, "t.partition_date <= $2") {
		t.Fatalf("windowed source must carry the partition-key bound on the turns arms: %s", sql)
	}
	if strings.Count(sql, "t.partition_date >= $1") != 2 {
		t.Fatalf("both arms (hot + parent) must carry the bound, got %d occurrences", strings.Count(sql, "t.partition_date >= $1"))
	}
	if !strings.Contains(sql, "d.partition_date >= $1") || !strings.Contains(sql, "d.partition_date <= $2") {
		t.Fatalf("windowed source must carry the partition-key bound on the details join: %s", sql)
	}
	// ★ 视图形态**不能**承担这件事：★ 它的 118 列投影被冻结（R37）⇒ ⇒ 视图
	// 不暴露 partition_date ⇒ ⇒ 裁剪不可达 ⇒ ⇒ 列表路径必须走原生形态。
	if strings.Contains(sql, "request_logs_with_current_month") {
		t.Fatalf("windowed source must not be view-backed (the 118-column projection is frozen): %s", sql)
	}
}

// TestSessionFamilyTurnsSourceWindowedSQL_MatchesNativeShape pins that the
// windowed source keeps the **two-leg** shape (hot ∪ parent) and the same
// details join as the plain native source — i.e. adding the bound must not
// change **which** relations are read.
//
// ★ 为什么重要：★ 视图（canonical）与原生源在 890 之后**行集相同**
//
//	（实测：view 2,810 行 / native 2,810 行 / native-only = 0 / view-only = 0）
//	⇒ ⇒ 列表路径换形态**不丢行** ⇒ ★ 但这条等价性依赖「两臂 + 同一个 details
//	JOIN」不被改坏 ⇒ ⇒ 本门把它钉住。
func TestSessionFamilyTurnsSourceWindowedSQL_MatchesNativeShape(t *testing.T) {
	windowed := db.SessionFamilyTurnsSourceWindowedSQL("$1", "$2")
	plain := db.SessionFamilyTurnsSourceSQL()

	for _, rel := range []string{
		"public.session_turns_hot",
		"public.session_turns",
		"public.session_turn_details_hot",
		"public.session_turn_details",
	} {
		if !strings.Contains(windowed, rel) {
			t.Errorf("windowed source lost relation %q", rel)
		}
		if !strings.Contains(plain, rel) {
			t.Errorf("plain native source lost relation %q (fixture drift?)", rel)
		}
	}
	if !strings.Contains(windowed, "UNION ALL") {
		t.Errorf("windowed source must keep the two-leg UNION ALL shape")
	}
	// ★ 两臂的 JOIN 条件必须逐字保留（去掉了 ::text 冗余转型的那版）。
	for _, cond := range []string{
		"d.tenant_id = t.tenant_id",
		"d.request_id = t.request_id",
		"d.partition_date = t.partition_date",
	} {
		if !strings.Contains(windowed, cond) {
			t.Errorf("windowed source lost join condition %q", cond)
		}
	}
}
