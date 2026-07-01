package autoroute

// FeatureFlags 默认值与 opt-out 测试（2026-06-28 起"全量启动，没有灰度"）。
//
// 历史：
//   - UseChannelQualityRouting 最初默认 false（灰度）
//   - 2026-06-28 经二次审计后，operator 决策"全量启动，没有灰度"
//   - 默认值翻转为 true，环境变量 AUTO_USE_CHANNEL_QUALITY_ROUTING=false
//     用于 opt-out
//
// 这些测试保护"默认值"不会在未来的修改中意外回到 false，导致意外的
// 全量回退（旧 2 维公式）。

import (
	"os"
	"testing"
)

// TestDefaultFeatureFlags_ChannelQualityOn 是默认值锁定测试：
// 防止有人在未来的修改中意外将 UseChannelQualityRouting 翻回 false。
func TestDefaultFeatureFlags_ChannelQualityOn(t *testing.T) {
	d := DefaultFeatureFlags()
	if !d.UseChannelQualityRouting {
		t.Fatalf("DefaultFeatureFlags().UseChannelQualityRouting must be true " +
			"(全量启动，没有灰度). Opt-out via AUTO_USE_CHANNEL_QUALITY_ROUTING=false.")
	}
	// 其它 V2 flag 仍保持默认关闭（各自独立 opt-in）
	if d.UseSimplifiedScoring || d.UseHotTop3Pool ||
		d.UseCacheRevalidation || d.Use48hFallback || d.EnableV2Logic {
		t.Errorf("其它 V2 flag 应保持默认关闭: %+v", d)
	}
}

// TestLoadFeatureFlagsFromEnv_DefaultOn 验证未设环境变量时，默认开启。
func TestLoadFeatureFlagsFromEnv_DefaultOn(t *testing.T) {
	// 确保未设置 AUTO_USE_CHANNEL_QUALITY_ROUTING
	orig, hadOrig := os.LookupEnv("AUTO_USE_CHANNEL_QUALITY_ROUTING")
	_ = os.Unsetenv("AUTO_USE_CHANNEL_QUALITY_ROUTING")
	defer func() {
		if hadOrig {
			_ = os.Setenv("AUTO_USE_CHANNEL_QUALITY_ROUTING", orig)
		}
	}()

	flags := LoadFeatureFlagsFromEnv()
	if !flags.UseChannelQualityRouting {
		t.Fatalf("未设 env 时 UseChannelQualityRouting 必须为 true（全量启动）")
	}
}

// TestLoadFeatureFlagsFromEnv_OptOutFalse 验证显式 env=false 能 opt-out。
func TestLoadFeatureFlagsFromEnv_OptOutFalse(t *testing.T) {
	orig, hadOrig := os.LookupEnv("AUTO_USE_CHANNEL_QUALITY_ROUTING")
	_ = os.Setenv("AUTO_USE_CHANNEL_QUALITY_ROUTING", "false")
	defer func() {
		if hadOrig {
			_ = os.Setenv("AUTO_USE_CHANNEL_QUALITY_ROUTING", orig)
		} else {
			_ = os.Unsetenv("AUTO_USE_CHANNEL_QUALITY_ROUTING")
		}
	}()

	flags := LoadFeatureFlagsFromEnv()
	if flags.UseChannelQualityRouting {
		t.Fatalf("env=false 应能 opt-out，但 UseChannelQualityRouting 仍为 true")
	}
}

// TestLoadFeatureFlagsFromEnv_ExplicitOn 验证显式 env=true 仍为 true。
func TestLoadFeatureFlagsFromEnv_ExplicitOn(t *testing.T) {
	orig, hadOrig := os.LookupEnv("AUTO_USE_CHANNEL_QUALITY_ROUTING")
	_ = os.Setenv("AUTO_USE_CHANNEL_QUALITY_ROUTING", "true")
	defer func() {
		if hadOrig {
			_ = os.Setenv("AUTO_USE_CHANNEL_QUALITY_ROUTING", orig)
		} else {
			_ = os.Unsetenv("AUTO_USE_CHANNEL_QUALITY_ROUTING")
		}
	}()

	flags := LoadFeatureFlagsFromEnv()
	if !flags.UseChannelQualityRouting {
		t.Fatalf("env=true 时 UseChannelQualityRouting 必须为 true")
	}
}
