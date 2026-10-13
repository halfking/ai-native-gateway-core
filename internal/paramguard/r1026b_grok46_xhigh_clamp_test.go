package paramguard

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/internal/reasoncap"
)

// r1026b（2026-10-13 审计轮）：grok-4.6 的 xhigh 档位事故判据。
//
// 事故形态（raw-logs 2026-10-13T19:36 三连，客户端 ZCode chat 腿显式发
// reasoning_effort="xhigh"）：reasoncap 表曾把 grok-4.6 登记为支持 xhigh
// （注释无实证）→ fixReasoningEffort 的能力钳制空转 → vapeur 中转的
// grok-4.6-1 后端 400 "Supported values are: 'none', 'low', 'medium',
// and 'high'"，单候选整请求失败。
//
// 修复：按上游明示能力集订正表项（xhigh 出列、none 入列）。本判据锁
// 两条腿（chat 顶层 / native responses 嵌套）都把 xhigh 钳到 high，
// 并钉住表项不再回潮 xhigh。
func TestFixReasoningEffort_ClampsXHighToHigh_Grok46(t *testing.T) {
	// 前提在场：能力表必须认识 grok-4.6（否则钳制根本不触发，判据假绿）。
	caps := reasoncap.Resolve(nil, "grok-4.6", nil)
	if !caps.Supported {
		t.Fatalf("premise missing: reasoncap has no capability row for grok-4.6; clamp would never fire")
	}
	for _, bad := range caps.Efforts {
		if bad == "xhigh" {
			t.Fatalf("premise violated: grok-4.6 capability row still lists xhigh — the upstream rejects it (vapeur grok-4.6-1, 2026-10-13)")
		}
	}

	cases := []struct {
		name    string
		body    string
		dialect paramreg.Dialect
	}{
		{
			// 生产失败组合：vapeur（catalog 空 → Resolve 回落协议）在
			// openai-responses 线格式上承载嵌套 reasoning.effort。
			name:    "native responses 腿嵌套 reasoning.effort（vapeur 方言回落，生产实证形态）",
			body:    `{"model":"grok-4.6","reasoning":{"effort":"xhigh"},"input":[{"role":"user","content":"hi"}]}`,
			dialect: paramreg.DialectResponses,
		},
		{
			// chat 载体的现实对应：直连 xAI（DialectGrok）。注意 Grok 能力
			// 有意不匹配 DialectOpenAIChat（reasonDialectMatchesParamDialect
			// 的跨供应商误改防护），泛 chat 中转（DialectUnknown）按教义
			// fail-open 不钳。
			name:    "chat 腿顶层 reasoning_effort（直连 xAI 方言）",
			body:    `{"model":"grok-4.6","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`,
			dialect: paramreg.DialectGrok,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, reports := ApplyReported([]byte(tc.body), tc.dialect)
			var obj map[string]any
			if err := json.Unmarshal(out, &obj); err != nil {
				t.Fatalf("unmarshal output: %v", err)
			}
			foundClamp := false
			for _, r := range reports {
				if r.Action == "clamp" && (r.Field == "reasoning_effort" || r.Field == "reasoning.effort") {
					foundClamp = true
					if r.Sent != "high" || r.Original != "xhigh" {
						t.Errorf("clamp report = %q → %q, want xhigh → high", r.Original, r.Sent)
					}
				}
			}
			if !foundClamp {
				t.Fatalf("no clamp report — xhigh passed through untouched (capability row regression?): %s", out)
			}
			// 出站体终值断言（按载体分派）。
			if tc.dialect == paramreg.DialectGrok {
				if obj["reasoning_effort"] != "high" {
					t.Fatalf("reasoning_effort = %v, want high (clamped from xhigh)", obj["reasoning_effort"])
				}
			} else {
				reasoning, _ := obj["reasoning"].(map[string]any)
				if reasoning == nil || reasoning["effort"] != "high" {
					t.Fatalf("reasoning.effort = %v, want high (clamped from xhigh)", obj["reasoning"])
				}
			}
		})
	}
}
