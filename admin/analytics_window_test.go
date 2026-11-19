package admin

import (
	"testing"
	"time"
)

// 2026-11-19 时间范围统一轮：?window= Nd 扩展的纯函数钉测。
// 契约：24h/7d 字面量语义不变；Nd 接受 1..90 天；非法值回落错误（handler
// 层转为 400，与旧 "must be 24h or 7d" 行为一致——错误文案更新为三形态）。
func TestParseAnalyticsWindow(t *testing.T) {
	cases := []struct {
		raw       string
		wantLabel string
		wantDur   time.Duration
		wantErr   bool
	}{
		{"", "7d", 7 * 24 * time.Hour, false},     // 缺省：旧行为
		{"7d", "7d", 7 * 24 * time.Hour, false},   // 字面量保留（MV 可命中）
		{"24h", "24h", 24 * time.Hour, false},     // 字面量保留
		{" 7D ", "7d", 7 * 24 * time.Hour, false}, // 大小写+空白容忍
		{"1d", "1d", 24 * time.Hour, false},       // Nd 下界
		{"3d", "3d", 72 * time.Hour, false},
		{"30d", "30d", 720 * time.Hour, false},
		{"90d", "90d", 2160 * time.Hour, false}, // Nd 上界
		{"0d", "", 0, true},                     // 越界回落错误
		{"91d", "", 0, true},
		{"7w", "", 0, true}, // 非天单位
		{"abc", "", 0, true},
	}
	for _, c := range cases {
		label, dur, err := parseAnalyticsWindow(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseAnalyticsWindow(%q) expected error, got %q/%v", c.raw, label, dur)
			}
			continue
		}
		if err != nil || label != c.wantLabel || dur != c.wantDur {
			t.Errorf("parseAnalyticsWindow(%q) = %q/%v (err=%v), want %q/%v", c.raw, label, dur, err, c.wantLabel, c.wantDur)
		}
	}
}
