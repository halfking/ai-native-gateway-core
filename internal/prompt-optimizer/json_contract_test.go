package promptoptimizer

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestJSONContractUsesSnakeCase 钉住 API 的 JSON 字段名是 snake_case。
//
// 回归背景（2026-11-11 端到端实测）：所有导出结构体都缺 json tag，
// 于是 encoding/json 退回到 Go 字段名（"Original" / "Optimized" /
// "AppliedStrategies"），与文档承诺的 "original" / "optimized" /
// "applied_strategies" 不一致 ⇒ 消费方按文档写代码直接取不到字段。
//
// 这类契约只有机器能守住：文档写对了不代表代码对，测试也不看文档。
func TestJSONContractUsesSnakeCase(t *testing.T) {
	o := NewOptimizer()
	result, err := o.Optimize(t.Context(), "帮我翻译这段文字", OptimizeOptions{
		TaskType:      "translation",
		InjectHarness: true,
	})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	payload := string(raw)

	for _, want := range []string{
		`"original"`,
		`"optimized"`,
		`"applied_strategies"`,
		`"harness_elements"`,
		`"context_sources"`,
		`"metadata"`,
		`"start_time"`,
		`"duration"`,
		`"iterations"`,
		`"confidence_score"`,
		`"history"`,
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("JSON 缺少字段 %s:\n%s", want, payload)
		}
	}

	// Go 字段名不应出现在 JSON 里（除非恰好与 snake_case 相同）。
	for _, unwanted := range []string{
		`"Original"`,
		`"Optimized"`,
		`"AppliedStrategies"`,
		`"HarnessElements"`,
		`"ConfidenceScore"`,
	} {
		if strings.Contains(payload, unwanted) {
			t.Errorf("JSON 不应出现 Go 字段名 %s:\n%s", unwanted, payload)
		}
	}
}

// TestJSONDurationIsReadable 钉住耗时是可读字符串而非纳秒整数。
//
// 断言必须**解析出 duration 字段本身**再判断，不能用 "payload 里有没有某个
// 字符" 这种全文扫描 —— 实测踩过：扫 `s"` 会意外命中 `"iterations"`
// （iterations 的 s 紧跟引号），于是裸 time.Duration 的纳秒整数也能通过。
func TestJSONDurationIsReadable(t *testing.T) {
	o := NewOptimizer()
	result, err := o.Optimize(t.Context(), "测试", OptimizeOptions{})
	if err != nil {
		t.Fatalf("Optimize: %v", err)
	}

	raw, err := json.Marshal(result.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}

	dur, ok := decoded["duration"]
	if !ok {
		t.Fatalf("metadata 缺少 duration 字段: %s", raw)
	}

	str, isStr := dur.(string)
	if !isStr {
		t.Fatalf("duration 应为字符串，实际类型 %T（值 %v）—— 退回纳秒整数了", dur, dur)
	}
	if !strings.Contains(str, "s") {
		t.Errorf("duration 字符串应带时间单位，实际 %q", str)
	}
}

// TestJSONResultContract 钉住带评估结果的 JSON 字段名。
func TestJSONResultContract(t *testing.T) {
	o := &optimizer{
		config:    defaultConfig(),
		evaluator: &fixedEvaluator{score: 0.75},
	}

	result, err := o.OptimizeWithEvaluation(t.Context(), "测试", EvaluationOptions{
		TestCases: []TestCase{{ID: "t1", Input: "i", Expected: "e"}},
	})
	if err != nil {
		t.Fatalf("OptimizeWithEvaluation: %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	payload := string(raw)

	for _, want := range []string{
		`"prompt"`,
		`"baseline_score"`,
		`"optimized_score"`,
		`"improvement"`,
		`"absolute_delta"`,
		`"metric_scores"`,
	} {
		if !strings.Contains(payload, want) {
			t.Errorf("JSON 缺少字段 %s:\n%s", want, payload)
		}
	}
}

// TestJSONNeverEmitsNaNOrInf 钉住 JSON 不含 NaN/Infinity（非法 JSON）。
//
// 回归背景：基线为 0 时 (opt-base)/base 会产出 NaN 或 +Inf，
// encoding/json 对这两个值会直接报错（整个响应 500）。
func TestJSONNeverEmitsNaNOrInf(t *testing.T) {
	o := &optimizer{
		config:    defaultConfig(),
		evaluator: &fixedEvaluator{score: 0},
	}

	result, err := o.OptimizeWithEvaluation(t.Context(), "测试", EvaluationOptions{
		TestCases: []TestCase{{ID: "t1", Input: "i", Expected: "e"}},
	})
	if err != nil {
		t.Fatalf("OptimizeWithEvaluation: %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("基线为 0 时 JSON 序列化失败（说明含 NaN/Inf）: %v", err)
	}
	payload := string(raw)

	for _, bad := range []string{"NaN", "Infinity", "-Infinity"} {
		if strings.Contains(payload, bad) {
			t.Errorf("JSON 不应含 %s: %s", bad, payload)
		}
	}
}
