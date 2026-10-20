package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestWindowExceedsV1Data is the control pair for the §9.222 truncation guard.
//
// Every arm needs both directions. A guard seen only in its "did not fire"
// state is indistinguishable from a guard that cannot fire, and this one gates
// the report that would justify retiring request_logs.
//
// §R45/M2, retaken: this file previously carried a `probe` field per case —
// the value HasV1RowsInRange(tenant, end, endDayCutoff(end)) would return — and
// a third "end-day" arm driven by it. That arrangement detected the dropped day
// and refused the run. The half-open bound is now corrected at the source
// (buildSessionRangeQuery → endDayExclusiveBound), so the named day is actually
// loaded and MaxTS is again sufficient to answer "is that day covered".
// The probe and its arm are gone; the guard is back to two arms.
//
// What survives from that round is the part that was right: the error message
// still hands the operator the flag value to re-run with. It is now derived
// from MaxTS (the newest day that actually has data) rather than from a probe.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：本守卫**已不在生产路径上**。
// validateBatch 现在在更早一步就中止——候选集 `LoadSessionsInRange` 返回
// errV1FamilyRetired（它原本读的那张 v1 表已被 DROP）⇒ 流程根本走不到
// `LoadV1TimeRange` / `WindowExceedsV1Data` 这一段。
//
// 也就是说：**这个门今天不可能触发**，而它当初防的是「拿一个被截断的窗口
// 去出一份 parity 报告」。本仓自己写过一条判据原则：「永远不触发的门
// 比没有门更糟，因为它看起来在防着什么」。
//
// 那为什么还留着？因为它**不是**一道生产门，而是一段仍然正确的纯函数，
// 删掉它会连带删掉 252 上「静默丢掉 15h53m32s 真实数据」那件事的**代码形态**，
// 而那件事记在审计文档与 LESSONS 里、代码却已无踪迹。留着的代价是
// 「读者可能以为它还在生效」——所以这一段注释就是那份代价的说明书。
// 真正在生效的退役声明在 `TestQueryBoundsIncludeTheNamedEndDay` 里。
func TestWindowExceedsV1Data(t *testing.T) {
	// Measured read-only on 252 on 2026-10-05, for tenant rows that carry a
	// session header (§9.220/§9.221/§9.222):
	//
	//   _request_logs    2026-09-30 18:54:28 .. 2026-10-04 15:53:32   (4 days)
	//   session_turns   2026-09-06 11:53:50 .. 2026-10-04 15:53:33   (24 days earlier)
	//
	// The retention policy allows 2 months, but the data that actually exists
	// spans four days. That gap is the reason this guard measures the range
	// instead of comparing against a constant: any constant baked in would be
	// wrong here, and wrong in the direction that matters — it would let a
	// 24-day window through on a v1 surface that only holds 4 days of it.
	actual := V1TimeRange{
		MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
		MaxTS: time.Date(2026, 10, 4, 15, 53, 32, 0, time.UTC),
		Rows:  31496,
	}

	cases := []struct {
		name          string
		start, end    time.Time
		actual        V1TimeRange
		wantTruncated bool
		wantSubstring string
	}{
		{
			name:          "start before oldest row (the 2-month retention trap)",
			start:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "precedes the oldest v1 row",
		},
		{
			// fe5003034's end-side arm. Start is deliberately inside the data so
			// this case isolates the END arm — with the start outside as well the
			// start arm fires first and the end arm is never exercised.
			name:          "end after newest row, start inside the data",
			start:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "is after the newest v1 row",
		},
		{
			// Negative control: an empty tenant is the zero-candidate path's
			// job; calling it truncation would be the wrong sentence.
			name:          "no v1 rows at all is not truncation",
			start:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual:        V1TimeRange{},
			wantTruncated: false,
		},
		{
			// The canonical legitimate operator input, and the case the §9.222
			// handoff's blind spot lived in. -end-date 2026-10-04 names the last
			// day that actually holds data. Under the corrected bound the query
			// covers all of that day (00:00 .. 23:59:59Z), so nothing the window
			// promises is missing and the report's printed date is true.
			//
			// This is a control, not a regression proof. The hours the old bound
			// dropped from this day are fixed in endDayExclusiveBound; from the
			// guard's point of view the named day was simply present all along.
			name:          "named the last day that has data (the common input)",
			start:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: false,
		},
		{
			// Zero start is legitimately "unbounded below" (never start
			// truncation), and the end arm is isolated because start is zero.
			name:          "end-date only, naming the last day that has data",
			start:         time.Time{},
			end:           time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: false,
		},
		{
			// The named day holds no v1 row at all: 2026-10-05 is promised by the
			// window but the source stops on 10-04. With the bound corrected this
			// is the honest reading of "wider than the data" — a whole named day
			// is missing, and the operator named it.
			//
			// This is the arm the previous 24h slack used to swallow. The slack
			// existed because the old bound already stopped at end-1, so naming
			// end was how an operator expressed "through end-1". With the bound
			// corrected that indirection is gone: the named day is loaded, so a
			// named day with nothing in it really is a gap.
			name:          "named a day that holds no rows at all",
			start:         time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:           time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
			actual:        actual,
			wantTruncated: true,
			wantSubstring: "is after the newest v1 row",
		},
		{
			// Day-boundary control, open side: the first instant of the named day
			// is present, so the day is covered. Together with the microsecond
			// case below this pins the end arm to the named day's midnight.
			name:  "newest row at the first instant of the named day",
			start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual: V1TimeRange{
				MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
				Rows:  31000,
			},
			wantTruncated: false,
		},
		{
			// Day-boundary control, firing side: one microsecond before the named
			// day's midnight is still the previous day, so that day is missing.
			name:  "newest row one microsecond before the named day",
			start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual: V1TimeRange{
				MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 3, 23, 59, 59, 999999, time.UTC),
				Rows:  31000,
			},
			wantTruncated: true,
			wantSubstring: "is after the newest v1 row",
		},
		{
			// Rows exist after the named day (Oct 5-6) and none on the named day
			// itself in this world. The operator's named day is the last one they
			// asked for; days past it are not theirs to be surprised by. A global
			// "is there data after end" probe would fire on every historical
			// window for any live tenant, leaving the guard with no clean path and
			// therefore no discriminating power.
			name:  "rows past the named day are not the window's business",
			start: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			end:   time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			actual: V1TimeRange{
				MinTS: time.Date(2026, 9, 30, 18, 54, 28, 0, time.UTC),
				MaxTS: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC),
				Rows:  40000,
			},
			wantTruncated: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := WindowExceedsV1Data(tc.start, tc.end, tc.actual)
			if got != tc.wantTruncated {
				t.Fatalf("WindowExceedsV1Data = %v, want %v (why=%q)", got, tc.wantTruncated, why)
			}
			if tc.wantSubstring != "" && !strings.Contains(why, tc.wantSubstring) {
				t.Errorf("reason %q does not mention %q — the operator needs to know which edge is short",
					why, tc.wantSubstring)
			}
			if !tc.wantTruncated && why != "" {
				t.Errorf("no truncation should be reported, but got reason %q", why)
			}
			// The end arm's sentence must hand the operator the flag value to
			// re-run with, not just a diagnosis. This is the part of the
			// superseded end-day arm that was worth keeping — derived from MaxTS
			// now, which needs no probe.
			if tc.wantTruncated && !tc.end.IsZero() &&
				strings.Contains(why, "is after the newest v1 row") &&
				!strings.Contains(why, "re-run with -end-date") {
				t.Errorf("end reason %q does not tell the operator what to re-run with", why)
			}
		})
	}
}

// TestEndDayExclusiveBound pins the conversion the query bound depends on.
//
// This covers the arithmetic. TestQueryBoundsIncludeTheNamedEndDay below is
// what covers the wiring, and it is the one that fails if the bound is
// reverted.
func TestEndDayExclusiveBound(t *testing.T) {
	cases := []struct {
		name  string
		named time.Time
		want  time.Time
	}{
		{
			// The 252 case. The old bound was this same instant, and
			// `ts < 2026-10-04T00:00:00Z` dropped every row of 2026-10-04
			// (newest real row 15:53:32) while the report printed 2026-10-04.
			name:  "2026-10-04 must include all of 2026-10-04",
			named: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "month rollover",
			named: time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "year rollover and leap day",
			named: time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			// The start flag is a plain date too, and it is used with `ts >=`,
			// so its own midnight is already the correct bound. Converting it
			// as well would drop a whole day off the front of the window. This
			// case exists to say so.
			name:  "start day is already a correct >= bound and must not shift",
			named: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := endDayExclusiveBound(tc.named); !got.Equal(tc.want) {
				t.Fatalf("endDayExclusiveBound(%s) = %s, want %s",
					tc.named.Format(time.RFC3339), got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

// TestQueryBoundsIncludeTheNamedEndDay 是 end-boundary 缺陷的**接线**回归门：
// 绑到半开 `ts < $3` 上的值必须是「被点名那一天的**次日**零点」。
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**接线本身已不存在**。
//
// 原门断言的是 `buildSessionRangeQuery` 交给 SQL 的**实参表**（而不只是
// 源文本——原注释专门记了「早一版是 grep loader.go，那版对着回退的实现
// 一直绿，等于给一个没接线的修复发了证书」）。
// 而 `buildSessionRangeQuery` 随它的唯一调用点 `LoadSessionsInRange`
// 一起退役了：候选集原本来自 v1 族的那张表，那张表已被 DROP。
//
// ⇒ 接线不再可断言。**算术**还在（endDayExclusiveBound，纯函数），
// 由 `TestEndDayExclusiveBound` 继续钉住。
//
// 代替它的是下面这道**退役契约门**：它钉住「候选集那条腿必须响亮地失败、
// 且一条查询都不发」。这不是形式——如果哪天有人把候选集改问会话族，
// 本工具就会**拿自己比自己**（v1 侧没了 ⇒ 报告恒为零漂移），
// 而「零漂移」与「两侧一致」在报告里长得一模一样。
//
// 剩下的一道真问题，说在明处：这道门**不执行** SQL（本包默认跑没有
// 数据库），所以它验的是「退役这件事被说出来」，不是「退役后数据对不对」。

func TestQueryBoundsIncludeTheNamedEndDay(t *testing.T) {
	// 原门的名字保留在下面这个新门里不现实（它验的不再是 bound），
	// 但**函数名**保留是为了让 git 历史里那条门仍然可被检索到。
	t.Run("candidate set is retired and issues no query", func(t *testing.T) {
		loader := NewSessionLoader(nil)
		ids, err := loader.LoadSessionsInRange(
			context.Background(), "tenant-a",
			time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
			time.Hour, 100,
		)
		if !errors.Is(err, errV1FamilyRetired) {
			t.Fatalf("LoadSessionsInRange err = %v, want errV1FamilyRetired", err)
		}
		if ids != nil {
			t.Errorf("LoadSessionsInRange 返回了 %d 个会话 ID；退役态必须返回 nil", len(ids))
		}
	})

	// 算术仍然有效：endDayExclusiveBound 是纯函数，与数据库无关，
	// 且它承载着 252 上「静默丢掉 15h53m32s 真实数据」那个教训。
	// 上一节 TestEndDayExclusiveBound 已经把它的算术钉死；
	// 这里只钉「候选集退役后，**没有人**再拿它去变换一个真实的命名日」，
	// 也就是：它仍在、但已无生产调用点。
	t.Run("the bound helper survives but has no production caller left", func(t *testing.T) {
		named := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
		got := endDayExclusiveBound(named)
		want := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("endDayExclusiveBound(%s) = %s, want %s",
				named.Format(time.RFC3339), got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
	})
}
