package examples

import (
	"context"
	"strings"
	"testing"
)

// TestRunBasic 冒烟：基础优化链路能跑通并产出结构化提示词。
func TestRunBasic(t *testing.T) {
	result, err := RunBasic(context.Background())
	if err != nil {
		t.Fatalf("RunBasic: %v", err)
	}
	if result.Optimized == "" {
		t.Fatal("优化结果不应为空")
	}
	if result.Optimized == result.Original {
		t.Fatal("优化结果不应与原文相同")
	}
	if !strings.Contains(result.Optimized, "# 角色定义") {
		t.Errorf("输出应含结构化角色定义:\n%s", result.Optimized)
	}
	if len(result.AppliedStrategies) == 0 {
		t.Error("应记录已应用的策略")
	}
	if len(result.HarnessElements) == 0 {
		t.Error("应记录已注入的 harness 要素")
	}
}

// TestRunBatch 冒烟：批量优化链路能跑通且保持顺序。
func TestRunBatch(t *testing.T) {
	results, err := RunBatch(context.Background())
	if err != nil {
		t.Fatalf("RunBatch: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("应返回 3 条结果，实际 %d", len(results))
	}
	want := []string{"总结这篇文章", "分析这组数据", "生成一个报告"}
	for i, w := range want {
		if results[i].Original != w {
			t.Errorf("第 %d 条错位: got %q want %q", i, results[i].Original, w)
		}
	}
}

// TestRunEvaluationFailsWithoutLLM 钉住未接 LLM 时明确报错而不是静默给 0 分。
func TestRunEvaluationFailsWithoutLLM(t *testing.T) {
	_, err := RunEvaluation(context.Background())
	if err == nil {
		t.Fatal("未接 LLM 客户端时必须报错")
	}
	if !strings.Contains(err.Error(), "llm client") {
		t.Errorf("错误信息应指向 llm client，实际: %v", err)
	}
}

// TestFormatBasicReport 冒烟：报告格式化能渲染出关键信息。
func TestFormatBasicReport(t *testing.T) {
	result, err := RunBasic(context.Background())
	if err != nil {
		t.Fatalf("RunBasic: %v", err)
	}
	report := FormatBasicReport(result)

	for _, want := range []string{"优化前", "优化后", "应用的策略", "Harness"} {
		if !strings.Contains(report, want) {
			t.Errorf("报告缺少 %q:\n%s", want, report)
		}
	}
}
