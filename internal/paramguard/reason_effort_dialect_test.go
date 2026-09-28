// 2026-09-28 审计：reasonDialectMatchesParamDialect 的白名单漏掉
// GLM / DeepSeek / Ark，导致 fixReasoningEffort 对这三个家族整体空转。
//
// paramreg 注册表对 reasoning_effort 的注记明确要求"须按目标能力收窄"
// （internal/paramreg/registry.go:206），但该规则此前只对
// OpenAI / Grok / Mistral / KimiEffort 生效。这三个被漏掉的家族都
// 在 OpenAI 线上暴露 reasoning_effort 字段，且能力表里 Efforts 非空，
// 于是客户端发来的值原样出站，不收窄也不报错。
package paramguard

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/internal/reasoncap"
)

func TestReasonDialectMatchesParamDialect_CoversEffortCapableFamilies(t *testing.T) {
	cases := []struct {
		name    string
		reason  reasoncap.Dialect
		dialect paramreg.Dialect
		want    bool
	}{
		// 2026-09-28 新增：这三族在 OpenAI 线上有 reasoning_effort 字段。
		{"glm -> glm", reasoncap.DialectGLM, paramreg.DialectGLM, true},
		{"deepseek -> deepseek", reasoncap.DialectDeepSeek, paramreg.DialectDeepSeek, true},
		{"ark -> ark", reasoncap.DialectArk, paramreg.DialectArk, true},
		// 既有行为不得回退。
		{"grok -> grok", reasoncap.DialectGrok, paramreg.DialectGrok, true},
		{"grok -> responses", reasoncap.DialectGrok, paramreg.DialectResponses, true},
		{"mistral -> mistral", reasoncap.DialectMistral, paramreg.DialectMistral, true},
		{"kimi_effort -> kimi", reasoncap.DialectKimiEffort, paramreg.DialectKimi, true},
		{"openai -> chat", reasoncap.DialectOpenAI, paramreg.DialectOpenAIChat, true},
		{"openai -> responses", reasoncap.DialectOpenAI, paramreg.DialectResponses, true},
		// 跨方言仍必须拒绝：宁可不动，也不能往不认识该字段的上游写。
		{"glm -> openai chat", reasoncap.DialectGLM, paramreg.DialectOpenAIChat, false},
		{"deepseek -> grok", reasoncap.DialectDeepSeek, paramreg.DialectGrok, false},
		{"grok -> glm", reasoncap.DialectGrok, paramreg.DialectGLM, false},
		// 无线上 effort 字段的家族继续不参与 effort 收窄。
		{"anthropic -> anthropic", reasoncap.DialectAnthropic, paramreg.DialectAnthropic, false},
		{"gemini25 -> gemini", reasoncap.DialectGemini25, paramreg.DialectGemini, false},
		{"qwen -> qwen", reasoncap.DialectQwen, paramreg.DialectQwen, false},
		{"minimax -> minimax", reasoncap.DialectMiniMax, paramreg.DialectMiniMax, false},
		{"ollama -> ollama", reasoncap.DialectOllama, paramreg.DialectOllama, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reasonDialectMatchesParamDialect(c.reason, c.dialect); got != c.want {
				t.Errorf("reasonDialectMatchesParamDialect(%q, %q) = %v, want %v",
					c.reason, c.dialect, got, c.want)
			}
		})
	}
}

// 端到端钉死：GLM 上游收到"关闭思考"意图时，出站体必须被收窄成
// 该模型支持的最低档，而不是原样透传。
func TestApplyReported_GLMNarrowesDisableIntent(t *testing.T) {
	out, reports := ApplyReported(
		[]byte(`{"model":"glm-5.2","reasoning_effort":"disabled","max_tokens":512}`),
		paramreg.DialectGLM,
	)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}
	if got := obj["reasoning_effort"]; got != "none" {
		t.Errorf("reasoning_effort = %v, want \"none\" (glm-5.2 支持的最低档)", got)
	}
	if len(reports) == 0 {
		t.Error("expected a clamp report so the paramledger records the rewrite")
	}
}

// deepseek-v4 没有真正的零档，关闭意图应收窄到 low（最便宜），
// 而不是修复前的 high（并列取高所致）。
func TestApplyReported_DeepSeekNarrowesDisableIntentToLow(t *testing.T) {
	out, _ := ApplyReported(
		[]byte(`{"model":"deepseek-v4-flash","reasoning_effort":"disabled","max_tokens":512}`),
		paramreg.DialectDeepSeek,
	)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}
	if got := obj["reasoning_effort"]; got != "low" {
		t.Errorf("reasoning_effort = %v, want \"low\"", got)
	}
}

// 合法档位在 GLM 上必须原样通过，不得被规则改动。
func TestApplyReported_GLMKeepsSupportedEffortUntouched(t *testing.T) {
	for _, effort := range []string{"low", "high", "max", "none"} {
		out, reports := ApplyReported(
			[]byte(`{"model":"glm-5.2","reasoning_effort":"`+effort+`","max_tokens":512}`),
			paramreg.DialectGLM,
		)
		var obj map[string]any
		if err := json.Unmarshal(out, &obj); err != nil {
			t.Fatalf("unmarshal out: %v", err)
		}
		if got := obj["reasoning_effort"]; got != effort {
			t.Errorf("reasoning_effort = %v, want %q unchanged", got, effort)
		}
		if len(reports) != 0 {
			t.Errorf("effort %q is already supported, expected no report, got %+v", effort, reports)
		}
	}
}

// x-high 别名归一在 GLM 上也必须生效（此前因方言门而空转）。
func TestApplyReported_GLMNormalizesEffortAlias(t *testing.T) {
	out, _ := ApplyReported(
		[]byte(`{"model":"glm-5.2","reasoning_effort":"x-high","max_tokens":512}`),
		paramreg.DialectGLM,
	)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}
	if got := obj["reasoning_effort"]; got != "xhigh" {
		t.Errorf("reasoning_effort = %v, want \"xhigh\" (alias normalized)", got)
	}
}

// 2026-09-29 守门：方舟 doubao-pro-thinking 的能力表为
// {minimal, low, medium, high}（无 none 零档）。本轮修复让
// `reasoning_effort:"disabled"` 在方舟方言上被收窄成 `minimal`——
// 这是档位调节器调到最便宜档，**不是**关闭思考。方舟的真正关闭开关
// 是 `thinking:{"type":"disabled"}`（实测见 §5）。
//
// 本测试钉死两件事：
//  1. 方舟收窄到最便宜档 = `minimal`（不是 GLM 的 `none`、不是
//     deepseek 的 `low`，按各自能力表各自语义）。
//  2. **没有任何方舟路径把 `disabled` 直接删掉**——它是个有效档位
//     请求，不会被网关"贴心地"变成"没有 reasoning_effort 字段"。
//
// 若有人后续改 ClampEffort 兜底（譬如"如果是 disabled 就把字段删掉"），
// 这条会立刻红；同理若有人让方舟走 Anthropic/Qwen 那样的"零档=删字段"
// 路径，这条也会红。
func TestArk_EffortIsTierNotKillSwitch(t *testing.T) {
	out, reports := ApplyReported(
		[]byte(`{"model":"doubao-pro-thinking","reasoning_effort":"disabled","max_tokens":512}`),
		paramreg.DialectArk,
	)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}
	// 方舟最便宜档是 `minimal`（reasoning_defaults.go:249）。
	// 这是档位，不是"关闭"——`thinking:{"type":"disabled"}` 才是关闭。
	if got, present := obj["reasoning_effort"]; !present || got != "minimal" {
		t.Errorf("reasoning_effort = %v (present=%v), want \"minimal\" "+
			"(方舟能力表最便宜档，不是关闭)", got, present)
	}
	// 必须有一次 clamp 报告留痕：方舟上这是档位下移，不是删除。
	if len(reports) == 0 {
		t.Error("expected a clamp report; 关闭意图在方舟上必须被收窄并留痕")
	}
}
