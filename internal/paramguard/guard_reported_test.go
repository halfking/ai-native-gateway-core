package paramguard

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

func findReport(reports []Report, field, action string) *Report {
	for i := range reports {
		if reports[i].Field == field && reports[i].Action == action {
			return &reports[i]
		}
	}
	return nil
}

// TestApplyReportedGrokXHighNormalize：grok-4.6 能力表含 xhigh，客户端
// 发 "x-high"（连字符）应被归一为 "xhigh" 出站，并报告 normalize
// （响应回显还原用）。语义不变，不产生 clamp。
func TestApplyReportedGrokXHighNormalize(t *testing.T) {
	body := []byte(`{"model":"grok-4.6","messages":[],"reasoning_effort":"x-high"}`)
	out, reports := ApplyReported(body, paramreg.DialectGrok)
	if !strings.Contains(string(out), `"reasoning_effort":"xhigh"`) {
		t.Fatalf("body = %s, want xhigh", out)
	}
	rep := findReport(reports, "reasoning_effort", "normalize")
	if rep == nil || rep.Original != "x-high" || rep.Sent != "xhigh" {
		t.Fatalf("normalize report = %+v", rep)
	}
	if findReport(reports, "reasoning_effort", "clamp") != nil {
		t.Fatalf("grok-4.6 supports xhigh; clamp must not fire: %+v", reports)
	}
}

// TestApplyReportedGrok4XHighClamp：grok-4（无 xhigh 档）发 "x-high" →
// 归一 xhigh 后就近降档 high，两条报告都产生（还原链取 clamp 的
// Original=x-high）。
func TestApplyReportedGrok4XHighClamp(t *testing.T) {
	body := []byte(`{"model":"grok-4","messages":[],"reasoning_effort":"x-high"}`)
	out, reports := ApplyReported(body, paramreg.DialectGrok)
	if !strings.Contains(string(out), `"reasoning_effort":"high"`) {
		t.Fatalf("body = %s, want high", out)
	}
	if findReport(reports, "reasoning_effort", "normalize") == nil {
		t.Fatalf("normalize report missing: %+v", reports)
	}
	rep := findReport(reports, "reasoning_effort", "clamp")
	if rep == nil || rep.Original != "x-high" || rep.Sent != "high" {
		t.Fatalf("clamp report = %+v", rep)
	}
}

// TestApplyReportedTemperatureCap：temperature 超方言上限被截断并报告。
func TestApplyReportedTemperatureCap(t *testing.T) {
	body := []byte(`{"model":"claude-x","messages":[],"temperature":2}`)
	out, reports := ApplyReported(body, paramreg.DialectAnthropic)
	if !strings.Contains(string(out), `"temperature":1`) {
		t.Fatalf("body = %s", out)
	}
	rep := findReport(reports, "temperature", "clamp")
	if rep == nil || rep.Sent != "1" {
		t.Fatalf("report = %+v", rep)
	}
}

// TestApplyReportedOSeriesRenameNotReported：max_tokens→max_completion_tokens
// 是纯格式转换（值不变），不产生报告。
func TestApplyReportedOSeriesRenameNotReported(t *testing.T) {
	body := []byte(`{"model":"o3","messages":[],"max_tokens":512}`)
	out, reports := ApplyReported(body, paramreg.DialectOpenAIChat)
	if !strings.Contains(string(out), `"max_completion_tokens":512`) {
		t.Fatalf("body = %s", out)
	}
	if len(reports) != 0 {
		t.Fatalf("format conversion must not report: %+v", reports)
	}
}

// TestApplyReportedGrokStrip：Grok reasoning 模型的 stop 等被剔除并报告。
func TestApplyReportedGrokStrip(t *testing.T) {
	body := []byte(`{"model":"grok-4","messages":[],"stop":["END"],"presence_penalty":0.5}`)
	out, reports := ApplyReported(body, paramreg.DialectGrok)
	if strings.Contains(string(out), "presence_penalty") || strings.Contains(string(out), `"stop"`) {
		t.Fatalf("body = %s", out)
	}
	if findReport(reports, "stop", "strip") == nil || findReport(reports, "presence_penalty", "strip") == nil {
		t.Fatalf("reports = %+v", reports)
	}
}

// TestApplyBackwardCompatible：旧入口行为不变（返回 body 不带报告）。
func TestApplyBackwardCompatible(t *testing.T) {
	body := []byte(`{"model":"claude-x","messages":[],"thinking":{"type":"enabled","budget_tokens":10000},"max_tokens":5000,"temperature":1.5}`)
	out := Apply(body, paramreg.DialectAnthropic)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	var budget struct {
		BudgetTokens int `json:"budget_tokens"`
	}
	_ = json.Unmarshal(obj["thinking"], &budget)
	if budget.BudgetTokens != 4999 {
		t.Fatalf("budget = %d, want 4999 (max_tokens-1)", budget.BudgetTokens)
	}
	if _, still := obj["temperature"]; still {
		t.Fatal("thinking-enabled anthropic body must drop temperature")
	}
}

// TestApplyReportedNestedResponsesEffort：native responses 形态出站的
// 嵌套 reasoning.effort 同样被归一/降档（grok-4 无 xhigh 档）。
func TestApplyReportedNestedResponsesEffort(t *testing.T) {
	body := []byte(`{"model":"grok-4","input":[],"reasoning":{"effort":"x-high","summary":"auto"}}`)
	out, reports := ApplyReported(body, paramreg.DialectResponses)
	if !strings.Contains(string(out), `"effort":"high"`) {
		t.Fatalf("body = %s, want effort high", out)
	}
	rep := findReport(reports, "reasoning.effort", "clamp")
	if rep == nil || rep.Original != "x-high" || rep.Sent != "high" {
		t.Fatalf("clamp report = %+v", rep)
	}
	if findReport(reports, "reasoning.effort", "normalize") == nil {
		t.Fatalf("normalize report missing: %+v", reports)
	}
}

// TestApplyReportedNestedResponsesNormalizeOnly：模型支持 xhigh 时只归一。
func TestApplyReportedNestedResponsesNormalizeOnly(t *testing.T) {
	body := []byte(`{"model":"grok-4.6","input":[],"reasoning":{"effort":"x-high"}}`)
	out, reports := ApplyReported(body, paramreg.DialectResponses)
	if !strings.Contains(string(out), `"effort":"xhigh"`) {
		t.Fatalf("body = %s, want xhigh", out)
	}
	if findReport(reports, "reasoning.effort", "clamp") != nil {
		t.Fatalf("grok-4.6 supports xhigh: %+v", reports)
	}
}
