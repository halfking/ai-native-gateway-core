package reasoncap

import (
	"context"
	"testing"
)

// 2026-10-13 gpt-5.6-sol 无响应事故判据（reasoncap 层）。
//
// 上游 api.vapeur.ai 对 reasoning_effort="max" 的 400 报错明示该家族
// 支持集为 none/low/medium/high/xhigh。静态表曾把 "max" 也声明进去，
// paramguard.ClampEffort 因此空转（max 在集合内原样放行）→ 三连 400。
//
// 本文件锁「表不再声明 max」。锁全集等值（而非只断言 not-contains）
// 是为了让任何往该条目加档位的改动先撞上这里。max→xhigh 的就近收窄
// 由 paramguard 层判据覆盖（effort_clamp_gpt56_test.go；reasonnorm 反向
// 依赖本包，此处不能引入断言）。
func TestGPT5FamilyEffortsExcludeMax(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-5", "gpt-5.1-turbo", "gpt-5.6"} {
		caps := Resolve(context.Background(), model, nil)
		if !caps.Supported {
			t.Fatalf("%s: expected supported caps", model)
		}
		want := []string{"none", "minimal", "low", "medium", "high", "xhigh"}
		if len(caps.Efforts) != len(want) {
			t.Fatalf("%s: efforts = %v, want %v", model, caps.Efforts, want)
		}
		for i := range want {
			if caps.Efforts[i] != want[i] {
				t.Fatalf("%s: efforts = %v, want %v", model, caps.Efforts, want)
			}
		}
	}
}
