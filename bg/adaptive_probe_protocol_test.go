// adaptive_probe_protocol_test.go — 探针侧自适应消解的判据。
//
// 三条要钉住的东西：
//
//  1. 非选择器输入逐字节不变（绝大多数供应商走这条，任何改动都是回归）。
//  2. 选择器在探针侧解析出的值与数据面**同一个纯函数**给出的一致。
//  3. 五个有模型上下文的探针入口**真的**调了接线函数（源码锚点判据）——
//     本仓已多次登记「写了判定体但没有生产调用点」这一型缺陷
//     （team_budget_gate 写好后零调用点至今）。判定体绿不算接线成功。
package bg

import (
	"os"
	"strings"
	"testing"

	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

func TestResolveAdaptiveProbeProtocol(t *testing.T) {
	declaredBoth := []string{"openai-completions", "openai-responses"}
	declaredAnthropicBase := []string{"anthropic-messages", "openai-completions"}

	cases := []struct {
		name     string
		protocol string
		rawModel string
		allowed  []string
		want     string
	}{
		// ① 非选择器 ⇒ 原样返回（零行为变化）
		{"canonical completions untouched", "openai-completions", "claude-sonnet-5", nil, "openai-completions"},
		{"canonical responses untouched", "openai-responses", "claude-sonnet-5", nil, "openai-responses"},
		{"canonical anthropic untouched", "anthropic-messages", "gpt-5", nil, "anthropic-messages"},
		{"canonical gemini untouched", "gemini-generate", "gpt-5", nil, "gemini-generate"},
		{"legacy alias untouched by this layer", "openai-response", "gpt-5", nil, "openai-response"},
		{"empty protocol untouched", "", "gpt-5", nil, ""},

		// ② 选择器 + 未声明 ⇒ 惰性退回 completions（与启用前等价）
		{"adaptive undeclared is inert", "adaptive", "claude-sonnet-5", nil, "openai-completions"},
		{"adaptive with empty declaration is inert", "adaptive", "gpt-5", []string{}, "openai-completions"},
		{"adaptive with only dirty declaration is inert", "adaptive", "gpt-5", []string{"", "nope"}, "openai-completions"},
		{"adaptive selector normalized case/space", "  Adaptive  ", "claude-sonnet-5", nil, "openai-completions"},

		// ③ 选择器 + 已声明 ⇒ 与数据面同一判定
		{"claude uses anthropic when declared", "adaptive", "claude-sonnet-5", declaredAnthropicBase, "anthropic-messages"},
		{"gpt-5 uses responses when declared", "adaptive", "gpt-5.3-codex", declaredBoth, "openai-responses"},
		{"gemini falls back to base when not declared", "adaptive", "gemini-2.5-pro", declaredBoth, "openai-completions"},
		{"unknown family uses base", "adaptive", "my-proxy-v2", declaredBoth, "openai-completions"},
		{"declared order decides the base", "adaptive", "deepseek-v4", []string{"openai-responses", "openai-completions"}, "openai-responses"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAdaptiveProbeProtocol(tc.protocol, tc.rawModel, tc.allowed)
			if got != tc.want {
				t.Fatalf("resolveAdaptiveProbeProtocol(%q, %q, %v) = %q, want %q",
					tc.protocol, tc.rawModel, tc.allowed, got, tc.want)
			}
		})
	}
}

// TestResolveAdaptiveProbeProtocol_MatchesDataPlane 是「两侧同口径」的正面判据：
// 探针侧与数据面必须对同一 (model, allowed) 给出同一个值。
//
// 数据面的一致性判据在 provider 包（resolveAdaptiveProtocols），这里直接对
// catalog 的 SSOT 逐例对拍——两侧都不许自己实现一遍判定语义。
func TestResolveAdaptiveProbeProtocol_MatchesDataPlane(t *testing.T) {
	allowed := []string{"openai-completions", "openai-responses", "anthropic-messages"}
	models := []string{
		"gpt-5.3-codex", "gpt-5", "o3", "o4-mini",
		"claude-sonnet-5", "claude-opus-4-1", "claude-haiku-4-5",
		"gemini-2.5-pro", "gemini-3-flash",
		"deepseek-v4", "qwen3.5-397b", "kimi-k2.5", "MiniMax-M2.5",
		"doubao-seed-2.0", "grok-4.2", "my-proxy-v2",
	}
	for _, m := range models {
		t.Run(m, func(t *testing.T) {
			probeSide := resolveAdaptiveProbeProtocol(providercatalog.ProtocolAdaptive, m, allowed)
			dataSide := providercatalog.ResolveAdaptiveProtocol(m, allowed)
			if probeSide != dataSide {
				t.Fatalf("model %q: probe side %q != data plane %q", m, probeSide, dataSide)
			}
		})
	}
}

// TestResolveProbeProtocol_NilPoolDeclinesSafely 钉住失败面：拿不到数据库句柄
// 时，选择器退化为 completions（而不是空串或选择器本身漏给描述符层）。
func TestResolveProbeProtocol_NilPoolDeclinesSafely(t *testing.T) {
	resetAdaptiveProbeCacheForTest()
	t.Cleanup(resetAdaptiveProbeCacheForTest)

	if got := resolveProbeProtocol(t.Context(), nil, 36, "adaptive", "claude-sonnet-5"); got != providercatalog.ProtocolOpenAICompletions {
		t.Fatalf("nil pool: got %q, want %q", got, providercatalog.ProtocolOpenAICompletions)
	}
	// 非选择器即使没有数据库也必须原样返回，且不得触发任何查询入口。
	for _, p := range providercatalog.Protocols {
		if got := resolveProbeProtocol(t.Context(), nil, 36, p, "claude-sonnet-5"); got != p {
			t.Fatalf("nil pool, non-selector %q: got %q, want unchanged", p, got)
		}
	}
	// provider id 缺失（0）同样不得查库。
	if got := resolveProbeProtocol(t.Context(), nil, 0, "adaptive", "gpt-5"); got != providercatalog.ProtocolOpenAICompletions {
		t.Fatalf("provider id 0: got %q", got)
	}
}

// TestProbeSitesUseAdaptiveResolver 是接线判据（源码锚点）。
//
// 为什么必须钉源码而不是只测纯函数：纯函数全绿而调用点漏接，正是本仓
// 「写了判定体 ≠ 门在承重」的同型缺陷。这里逐个锚住「有模型上下文」的探针
// 入口，任一入口退回裸 probeDescriptorFor(t.Protocol) 就当场红。
func TestProbeSitesUseAdaptiveResolver(t *testing.T) {
	sites := []struct {
		file  string
		label string
		// want 是该文件里必须出现的接线片段数量。
		want int
	}{
		{"model_probe.go", "featured cycle Layer 4 / probeModel / verifyTargetModality", 3},
		{"active_probe_executor.go", "Run / runModelsList", 2},
	}
	for _, site := range sites {
		t.Run(site.file, func(t *testing.T) {
			raw, err := os.ReadFile(site.file)
			if err != nil {
				t.Fatalf("read %s: %v", site.file, err)
			}
			src := string(raw)
			if got := strings.Count(src, "resolveProbeProtocol("); got < site.want {
				t.Fatalf("%s (%s): resolveProbeProtocol 出现 %d 次，应至少 %d 次——有模型上下文的探针入口必须走自适应消解",
					site.file, site.label, got, site.want)
			}
			// 反证：这些文件里不应再有裸的 probeDescriptorFor(t.Protocol) /
			// probeDescriptorFor(target.Protocol)。留着它就是漏接的入口。
			for _, banned := range []string{
				"probeDescriptorFor(t.Protocol)",
				"probeDescriptorFor(target.Protocol)",
			} {
				if strings.Contains(src, banned) {
					t.Fatalf("%s: 仍有漏接的 %s（应改为 probeDescriptorFor(resolveProbeProtocol(...)))", site.file, banned)
				}
			}
		})
	}
}
