package paramguard

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// ─── claude-fable-5-2 强制 adaptive thinking（2026-11-11 事故轮）───
//
// 契约来源：211 生产直连 apiclaude 中继实测（2026-11-11 14:3x +08，
// 凭据 31 / claude-fable-5-2）：
//
//	thinking.type=enabled (+budget_tokens) ⇒ 400
//	  "claude-fable-5-2 requires adaptive thinking; omit thinking or use
//	   thinking.type=adaptive and output_config.effort"
//	thinking.type=disabled                 ⇒ 400（同文案）
//	thinking.type=adaptive                  ⇒ 200
//	无 thinking                             ⇒ 200
//
// 对照组（同方言、同中继）：
//
//	claude-fable-5   五种形态全 200（enabled/disabled 不被拒）
//	claude-fable-5-1 五种形态全 200
//
// ⇒ 门控必须是**精确模型名**；前缀匹配会误伤正在承载流量的两个旧模型。

func thinkingType(t *testing.T, body []byte) string {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	th, ok := obj["thinking"].(map[string]any)
	if !ok {
		return ""
	}
	typ, _ := th["type"].(string)
	return typ
}

func TestAdaptiveOnlyThinking_EnabledBecomesAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[]}`)
	out, reports := ApplyReported(body, paramreg.DialectAnthropic)

	if got := thinkingType(t, out); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive (body=%s)", got, out)
	}
	// budget_tokens 必须一并丢弃：adaptive 是 budgetless 形态。
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	th := obj["thinking"].(map[string]any)
	if _, ok := th["budget_tokens"]; ok {
		t.Fatalf("budget_tokens survived adaptive rewrite: %s", out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want exactly 1", reports)
	}
	if reports[0].Field != "thinking.type" || reports[0].Sent != "adaptive" {
		t.Fatalf("report = %+v, want thinking.type→adaptive", reports[0])
	}
}

func TestAdaptiveOnlyThinking_DisabledBecomesAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,"thinking":{"type":"disabled"},"messages":[]}`)
	out, _ := ApplyReported(body, paramreg.DialectAnthropic)
	if got := thinkingType(t, out); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive (body=%s)", got, out)
	}
}

// 无 thinking 的请求不得凭空造出 thinking 块（上游接受省略）。
func TestAdaptiveOnlyThinking_NoThinkingStaysAbsent(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,"messages":[]}`)
	out, reports := ApplyReported(body, paramreg.DialectAnthropic)
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	if _, ok := obj["thinking"]; ok {
		t.Fatalf("thinking block invented: %s", out)
	}
	if len(reports) != 0 {
		t.Fatalf("reports = %+v, want none", reports)
	}
}

// 已是 adaptive 的不得二次改写（幂等）。
func TestAdaptiveOnlyThinking_AdaptiveIsNoOp(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,"thinking":{"type":"adaptive"},"messages":[]}`)
	out, reports := ApplyReported(body, paramreg.DialectAnthropic)
	if got := thinkingType(t, out); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive", got)
	}
	if len(reports) != 0 {
		t.Fatalf("reports = %+v, want none (already legal)", reports)
	}
}

// ★ 阴性对照：旧模型不得被误伤（211 实测它们对 enabled/disabled 全兼容）。
func TestAdaptiveOnlyThinking_OlderFableModelsUntouched(t *testing.T) {
	for _, model := range []string{"claude-fable-5", "claude-fable-5-1"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[]}`)
			out, reports := ApplyReported(body, paramreg.DialectAnthropic)
			if got := thinkingType(t, out); got != "enabled" {
				t.Fatalf("thinking.type = %q, want enabled (old model must not be rewritten): %s", got, out)
			}
			if len(reports) != 0 {
				t.Fatalf("reports = %+v, want none", reports)
			}
		})
	}
}

// ★ 阴性对照：其它 Anthropic 模型族（sonnet/opus）不得被误伤。
// 2026-11-13 改判：`claude-opus-5-5` 原钉在「不得改写」侧（2026-11-11 同中继
// 实测 enabled 全 200）；当日 apiclaude 把它升级为 adaptive-only（同
// fable-5-2 文案，211 三起活体 400）⇒ 移入 adaptive 清单、移出本对照。
// `claude-sonnet-5` 当日实测 enabled 形态仍 200（211 活体 02:26 +08）⇒ 维持阴性。
func TestAdaptiveOnlyThinking_OtherClaudeFamiliesUntouched(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5", "claude-opus-5", "claude-haiku-4-5"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[]}`)
			out, _ := ApplyReported(body, paramreg.DialectAnthropic)
			if got := thinkingType(t, out); got != "enabled" {
				t.Fatalf("thinking.type = %q, want enabled: %s", got, out)
			}
		})
	}
}

// 宿主前缀形态（anthropic/claude-fable-5-2）同样命中。
func TestAdaptiveOnlyThinking_VendorPrefixedName(t *testing.T) {
	body := []byte(`{"model":"anthropic/claude-fable-5-2","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[]}`)
	out, _ := ApplyReported(body, paramreg.DialectAnthropic)
	if got := thinkingType(t, out); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive", got)
	}
}

// 非 Anthropic 方言不受此规则约束（thinking 语义不同）。
func TestAdaptiveOnlyThinking_OtherDialectsUntouched(t *testing.T) {
	for _, d := range []paramreg.Dialect{paramreg.DialectOpenAIChat, paramreg.DialectResponses, paramreg.DialectMiniMax} {
		body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[]}`)
		out, _ := ApplyReported(body, d)
		if got := thinkingType(t, out); got != "enabled" {
			t.Fatalf("dialect %s: thinking.type = %q, want enabled", d, got)
		}
	}
}

// ★ disabled → adaptive 会把 thinking 从「关」变成「开」⇒ 必须连带剥离
// Anthropic 在 thinking 开启时拒收的采样参数（temperature / top_p），
// 否则改写后会引上游二次 400。
func TestAdaptiveOnlyThinking_StripsSamplingWhenDisabledBecomesEnabled(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,"temperature":1,"top_p":0.9,"thinking":{"type":"disabled"},"messages":[]}`)
	out, _ := ApplyReported(body, paramreg.DialectAnthropic)
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	if got := thinkingType(t, out); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive", got)
	}
	for _, k := range []string{"temperature", "top_p"} {
		if _, ok := obj[k]; ok {
			t.Fatalf("%s survived: Anthropic rejects sampling params when thinking is active: %s", k, out)
		}
	}
}

// 未知 thinking 形态不得被网关猜改（猜错会把上游自己会拒的形态变成
// 网关改出来的形态 —— 错误归属从上游转移到网关）。
func TestAdaptiveOnlyThinking_UnknownTypeNotGuessed(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-2","max_tokens":8192,"thinking":{"type":"weird"},"messages":[]}`)
	out, _ := ApplyReported(body, paramreg.DialectAnthropic)
	if got := thinkingType(t, out); got != "weird" {
		t.Fatalf("thinking.type = %q, want weird (gateway must not guess)", got)
	}
}

// ─── claude-opus-5-5 强制 adaptive thinking（2026-11-13 事故轮）───
//
// 契约来源：211 生产网关日志（2026-11-13 02:23~02:34 +08，apiclaude 中继，
// ZCode chat 形态请求）：
//
//	thinking.type=enabled (+budget_tokens) ⇒ 400
//	  "claude-opus-5-5 requires adaptive thinking; omit thinking or use
//	   thinking.type=adaptive and output_config.effort"
//	⇒ 三候选（凭据 31/59/99036）全败 ⇒ 客户端收到
//	  "No available provider for model 'claude-opus-5-5'"。
//
//	无 thinking ⇒ 200（同窗活体实测）
//	claude-sonnet-5 + enabled(+budget) ⇒ 200（对照组，同中继同窗实测）

func TestAdaptiveOnlyThinking_Opus55EnabledBecomesAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[]}`)
	out, reports := ApplyReported(body, paramreg.DialectAnthropic)

	if got := thinkingType(t, out); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive (body=%s)", got, out)
	}
	// budget_tokens 必须一并丢弃：adaptive 是 budgetless 形态。
	var obj map[string]any
	_ = json.Unmarshal(out, &obj)
	th := obj["thinking"].(map[string]any)
	if _, ok := th["budget_tokens"]; ok {
		t.Fatalf("budget_tokens survived adaptive rewrite: %s", out)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want exactly 1", reports)
	}
	if reports[0].Field != "thinking.type" || reports[0].Sent != "adaptive" {
		t.Fatalf("report = %+v, want thinking.type→adaptive", reports[0])
	}
}

func TestAdaptiveOnlyThinking_Opus55DisabledBecomesAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","max_tokens":8192,"thinking":{"type":"disabled"},"messages":[]}`)
	out, _ := ApplyReported(body, paramreg.DialectAnthropic)
	if got := thinkingType(t, out); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive (body=%s)", got, out)
	}
}

// 旧 opus（不带 -5-5）不得被前缀误伤。
func TestAdaptiveOnlyThinking_OlderOpusUntouched(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "claude-opus-4-8"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[]}`)
			out, reports := ApplyReported(body, paramreg.DialectAnthropic)
			if got := thinkingType(t, out); got != "enabled" {
				t.Fatalf("thinking.type = %q, want enabled (old opus must not be rewritten): %s", got, out)
			}
			if len(reports) != 0 {
				t.Fatalf("reports = %+v, want none", reports)
			}
		})
	}
}

// requiresAdaptiveThinking 的判据表自身要钉住（防止有人把它改成前缀匹配）。
func TestRequiresAdaptiveThinking_ExactNameTable(t *testing.T) {
	yes := []string{"claude-fable-5-2", "anthropic/claude-fable-5-2", "CLAUDE-FABLE-5-2", "claude-opus-5-5", "anthropic/claude-opus-5-5"}
	for _, m := range yes {
		if !requiresAdaptiveThinking(m) {
			t.Fatalf("requiresAdaptiveThinking(%q) = false, want true", m)
		}
	}
	no := []string{"claude-fable-5", "claude-fable-5-1", "claude-fable-5-3", "claude-sonnet-5", "claude-opus-5", "claude-opus-5-6", "", "claude-fable"}
	for _, m := range no {
		if requiresAdaptiveThinking(m) {
			t.Fatalf("requiresAdaptiveThinking(%q) = true, want false", m)
		}
	}
}
