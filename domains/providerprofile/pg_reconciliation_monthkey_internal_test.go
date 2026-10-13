package providerprofile

import (
	"testing"
	"time"
)

// ⚠️ 这条判据钉的是 2026-10-13 线上实测的时区缺陷：
//
//	同一个月份，两条读路径给出不同答案，且**都不报错**：
//	  GET /provider-cost-reconciliation                → count=19 ✅
//	  GET /provider-cost-reconciliation?month=2026-10  → count=0  ❌
//
//	根因：把 time.Time 交给 date 列时，PG 按**会话 TimeZone** 折算。
//	默认路径用 now.Location()（+08），显式路径用 time.Parse（UTC），
//	于是月初零点被折成了 2026-09-30，两边键不一致。
//
//	⇒ monthKey 必须只由「年+月」决定，与传入时刻的 Location/时分秒无关。
//
//	变异方式：把 monthKey 改成 month.Format("2006-01-02")（用调用方的日）
//　或改成 month.UTC().Format(...) 之类依赖 Location 的写法。
func TestMonthKeyIsCalendarMonthOnly(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	utc := time.UTC
	tokyo := time.FixedZone("JST", 9*3600)

	// 同一个日历月，四种构造方式必须给出同一个键。
	want := "2026-10-01"
	for _, loc := range []*time.Location{shanghai, utc, tokyo} {
		for _, day := range []int{1, 15, 28} {
			got := monthKey(time.Date(2026, time.October, day, 13, 45, 7, 0, loc))
			if got != want {
				t.Fatalf("monthKey(2026-10-%02d @ %s) = %q, want %q", day, loc, got, want)
			}
		}
	}

	// 月初零点 +08：旧写法会折成 2026-09-30，这正是线上症状。
	if got := monthKey(time.Date(2026, time.October, 1, 0, 0, 0, 0, shanghai)); got != want {
		t.Fatalf("月初零点 +08 的键 = %q, want %q（折成上月最后一天就是线上那个 bug）", got, want)
	}

	// 相邻月不能混淆。
	if got := monthKey(time.Date(2026, time.September, 30, 23, 59, 59, 0, shanghai)); got != "2026-09-01" {
		t.Fatalf("9 月末的键 = %q, want 2026-09-01", got)
	}
	if got := monthKey(time.Date(2026, time.December, 31, 0, 0, 0, 0, shanghai)); got != "2026-12-01" {
		t.Fatalf("12 月末的键 = %q, want 2026-12-01", got)
	}
}
