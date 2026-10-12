package paramguard

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// 2026-10-13 gpt-5.6-sol 无响应事故判据（paramguard 服务路径层）。
//
// 出站两条腿都经 ApplyReported → fixReasoningEffort clamp：
//   - chat 腿（convertResponsesToChatBody 产出 / chat 客户端原生）：
//     顶层 reasoning_effort，方言 DialectOpenAIChat；
//   - native responses 腿（vapeur catalog_code 为空 → paramreg.Resolve
//     回落 DialectResponses）：嵌套 reasoning.effort。
//
// 事故形态：客户端 effort="max"，gpt-5 家族表曾声明支持 max → clamp 空转
// → 上游 400。判据锁两种载体都把 max 降到 xhigh。
func TestFixReasoningEffort_ClampsMaxToXHigh_GPT56(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		dialect paramreg.Dialect
	}{
		{
			name:    "chat 腿顶层 reasoning_effort",
			body:    `{"model":"gpt-5.6-sol","reasoning_effort":"max","messages":[]}`,
			dialect: paramreg.DialectOpenAIChat,
		},
		{
			name:    "native responses 腿嵌套 reasoning.effort（vapeur 方言回落）",
			body:    `{"model":"gpt-5.6-sol","reasoning":{"effort":"max"},"input":[]}`,
			dialect: paramreg.DialectResponses,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, reports := ApplyReported([]byte(tc.body), tc.dialect)
			var obj map[string]any
			if err := json.Unmarshal(out, &obj); err != nil {
				t.Fatalf("unmarshal output: %v", err)
			}
			// 前提在场：必须产生 clamp 报告（否则是「没看见」的假绿）。
			foundClamp := false
			for _, r := range reports {
				if r.Action == "clamp" && (r.Field == "reasoning_effort" || r.Field == "reasoning.effort") {
					foundClamp = true
				}
			}
			if !foundClamp {
				t.Fatalf("expected clamp report, got %+v", reports)
			}
			switch tc.dialect {
			case paramreg.DialectOpenAIChat:
				if obj["reasoning_effort"] != "xhigh" {
					t.Fatalf("reasoning_effort = %v, want xhigh", obj["reasoning_effort"])
				}
			case paramreg.DialectResponses:
				r, ok := obj["reasoning"].(map[string]any)
				if !ok {
					t.Fatalf("reasoning object missing: %v", obj["reasoning"])
				}
				if r["effort"] != "xhigh" {
					t.Fatalf("reasoning.effort = %v, want xhigh", r["effort"])
				}
			}
		})
	}
}
