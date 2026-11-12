package admin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// TestHotLegGateWindowClampLogic 钉 F-58（2026-11-12 53 增量）的钳位语义：
// hot-only 腿的闸门 = min(7d 硬帽, 保留小时数)，与 resolvePromoteConfig 的
// floor(1h)/默认(8h) 语义对齐。纯函数部分在此测试；settings 读取路径由
// hotLegGateWindow() 在线行为覆盖（默认 fallback=8 与 spec_lifecycle.go
// Default:8 逐字一致，再漂移即本测试与 spec 双红）。
func TestHotLegGateWindowClampLogic(t *testing.T) {
	tests := []struct {
		name     string
		hours    int
		expected time.Duration
	}{
		{"spec 默认 8h", 8, 8 * time.Hour},
		{"floor：0 回落到 1h（对齐 resolvePromoteConfig）", 0, time.Hour},
		{"floor：负数回落到 1h", -5, time.Hour},
		{"floor：不足 1h 回落到 1h", 1, time.Hour},
		{"720h（spec Max）被 7d 硬帽压住", 720, 7 * 24 * time.Hour},
		{"7d 恰好等于硬帽", 168, 7 * 24 * time.Hour},
		{"2d 介于留存与硬帽之间", 48, 48 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, clampHotLegGateHours(tt.hours))
		})
	}
}

// TestHotLegGateWindowDefaultBelowHardCap 是 F-58 的**行为守卫**（防「改回常量」型回归）：
// 空 settings（Global=nil ⇒ fallback 8h）下闸门必须 < 7d 硬帽——若有人把
// logs.go 的闸门改回 `hotLegMaxWindow` 常量（即 F-58 缺陷形态），8h~7d 窗会
// 重新静默少算，本测试当场红。
func TestHotLegGateWindowDefaultBelowHardCap(t *testing.T) {
	prev := settings.Global
	settings.Global = nil
	t.Cleanup(func() { settings.Global = prev })

	gate := hotLegGateWindow()
	assert.Equal(t, 8*time.Hour, gate, "空 settings 下闸门必须等于 spec 默认 8h")
	assert.Less(t, gate, hotLegMaxWindow, "闸门必须低于 7d 硬帽（F-58 缺陷形态=7d）")
}
