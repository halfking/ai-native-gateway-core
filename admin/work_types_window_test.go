package admin

import "testing"

// 2026-11-19 时间范围统一轮：?days= 参数化窗口的纯函数钉测。
// 契约：缺省/非法值回落 24h（旧行为），1..90 夹紧，标签随窗口变化。
func TestParseStatsWindow(t *testing.T) {
	cases := []struct {
		raw       string
		wantLabel string
		wantHours int
	}{
		{"", "24h", 24},        // 缺省：旧行为不变
		{"garbage", "24h", 24}, // 非法：回落而非 500
		{"0", "24h", 24},       // 下界外回落
		{"-5", "24h", 24},      // 负数回落
		{"91", "24h", 24},      // 上界外回落
		{"1", "1d", 24},        // 今天
		{"7", "7d", 168},       // 近 7 天
		{"30", "30d", 720},     // 近 30 天
		{"90", "90d", 2160},    // 上界
	}
	for _, c := range cases {
		label, dur := parseStatsWindow(c.raw)
		if label != c.wantLabel || int(dur.Hours()) != c.wantHours {
			t.Errorf("parseStatsWindow(%q) = %q/%vh, want %q/%dh", c.raw, label, int(dur.Hours()), c.wantLabel, c.wantHours)
		}
	}
}
