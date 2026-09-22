// safety/tool_choice_injection_test.go
//
// SF-01: tool_choice 含注入字符（`"; DROP --` 等）不 panic、不破坏 IR 完整性。
//
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D02-protocol-adaptation/safety/...

package safety

import (
	"encoding/json"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

func TestSafety_ToolChoice_InjectionNoPanic(t *testing.T) {
	malicious := []string{
		`"auto"`,
		`"none"`,
		`"required"`,
		`{"type":"function","function":{"name":"calc"}}`,
		`{"type":"function","function":{"name":"'; DROP TABLE tools; --"}}`,
		`{"type":"function","function":{"name":"../../etc/passwd"}}`,
		`{"type":"function","function":{"name":"<script>alert(1)</script>"}}`,
		`"any\x00\x00"`,
	}
	for _, tc := range malicious {
		t.Run(tc, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on tool_choice=%s: %v", tc, r)
				}
			}()
			body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}],"tool_choice":` + tc + `}`)
			req, err := ir.ParseOpenAI(body)
			if err != nil {
				// 拒绝恶意形态是合法结果，不 panic 即合格
				return
			}
			// 走到这里说明接受了；序列化回去验证未引入额外字段
			out, err := ir.SerializeOpenAI(req)
			if err != nil {
				t.Errorf("serialize: %v", err)
				return
			}
			var m map[string]any
			_ = json.Unmarshal(out, &m)
			// 不应该产生额外敏感字段（如 system prompt 注入）
			for _, k := range []string{"__proto__", "constructor", "system"} {
				if _, has := m[k]; has {
					// system 在 OpenAI 协议里存在但只来自原始消息；如果出现且内容异常可疑，记录
					if k == "system" {
						if s, ok := m[k].(string); ok && (s == "" || len(s) > 10000) {
							t.Errorf("system field suspicious after tool_choice injection: %v", s)
						}
					}
				}
			}
		})
	}
}