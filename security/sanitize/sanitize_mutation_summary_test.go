// sanitize_mutation_summary_test.go — 脱敏命中计数载体（/live 阶段树 Phase 1）
// （2026-11-16）。
//
// 阶段树要显示「脱敏命中几处」，但**不能**用 SanitizeMap 来数：映射表里是
// **原始敏感值**（placeholder → 原文），把它带出安全包就是带密钥。
//
// ⇒ 这里另开一个**只承载计数与耗时**的载体（SanitizeMutationSummary）。
// 本测试钉三件事：
//  1. 载体可读，且只带计数/耗时；
//  2. **未测量**（ctx 里没有）⇒ 第二个返回值 false（调用方禁止用 0 冒充）；
//  3. count < 0 视为无测量，不写入 ctx（不把「没测到」写成「0 处命中」）。
package sanitize

import (
	"context"
	"testing"
)

func TestSanitizeMutationSummary_RoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := SanitizeMutationSummaryFromContext(ctx); ok {
		t.Fatal("summary must be absent (not measured) when the middleware never ran")
	}

	ctx = WithSanitizeMutationSummary(ctx, SanitizeMutationSummary{Count: 3, DurMs: 12})
	sum, ok := SanitizeMutationSummaryFromContext(ctx)
	if !ok {
		t.Fatal("summary must be readable after WithSanitizeMutationSummary")
	}
	if sum.Count != 3 {
		t.Errorf("Count = %d, want 3", sum.Count)
	}
	if sum.DurMs != 12 {
		t.Errorf("DurMs = %d, want 12", sum.DurMs)
	}
}

func TestSanitizeMutationSummary_ZeroIsMeasured(t *testing.T) {
	// ★ 「脱敏跑了、命中 0 处」与「脱敏没跑/没测到」是**两件事**：
	// 前者必须能表达（Count=0 且 ok=true），后者必须表达不了（ok=false）。
	ctx := WithSanitizeMutationSummary(context.Background(), SanitizeMutationSummary{Count: 0, DurMs: 1})
	sum, ok := SanitizeMutationSummaryFromContext(ctx)
	if !ok {
		t.Fatal("Count=0 with a measured duration must be reported as measured")
	}
	if sum.Count != 0 {
		t.Errorf("Count = %d, want 0", sum.Count)
	}
}

func TestSanitizeMutationSummary_NegativeIsNotMeasured(t *testing.T) {
	ctx := WithSanitizeMutationSummary(context.Background(), SanitizeMutationSummary{Count: -1, DurMs: -1})
	if _, ok := SanitizeMutationSummaryFromContext(ctx); ok {
		t.Fatal("negative count must not be stored (it would read as a measured zero)")
	}
}
