package admin

// R37 (2026-09-17) 钉桩：logs 列表/统计的 from/to 用户可任意指定，
// from=1970 会把 COUNT/SUM 聚合变成分区母表全扫。窗口必须被钳到 366 天
// （与 usage 面口径一致），end 锚定、start 前推。
import (
	"testing"
	"time"
)

func TestClampQueryWindowCapsSpan(t *testing.T) {
	end := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	// 正常窗口不动。
	start := end.Add(-24 * time.Hour)
	gotStart, gotEnd := clampQueryWindow(start, end)
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("normal window mutated: %v..%v", gotStart, gotEnd)
	}

	// from=1970 全表扫窗口 → 钳到 366 天。
	gotStart, gotEnd = clampQueryWindow(time.Unix(0, 0).UTC(), end)
	if gotEnd != end {
		t.Fatalf("end must stay anchored, got %v", gotEnd)
	}
	if d := gotEnd.Sub(gotStart); d > maxLogQueryWindow {
		t.Fatalf("span %v exceeds cap %v", d, maxLogQueryWindow)
	}
	if want := end.Add(-maxLogQueryWindow); !gotStart.Equal(want) {
		t.Fatalf("start = %v, want %v", gotStart, want)
	}
}
