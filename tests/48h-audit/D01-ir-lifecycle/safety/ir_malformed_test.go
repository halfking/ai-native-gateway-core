// safety/ir_malformed_test.go
//
// SF-03: JSON 解析畸形不 panic、不泄漏内部栈。
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D01-ir-lifecycle/safety/...

package safety

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestSafety_ParseOpenAI_MalformedInput_NoPanic
func TestSafety_ParseOpenAI_MalformedInput_NoPanic(t *testing.T) {
	malformed := []string{
		"",
		"not json at all",
		`{"model":`,                              // 半截
		`{"model":"x","messages":null}`,          // 字段 null
		`{"model":"x","messages":[{}]}`,          // 缺 role
		`{"model":"x","messages":[{"role":42}]}`, // role 类型错
	}
	for _, body := range malformed {
		t.Run(body, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("parser panicked on %q: %v", body, r)
				}
			}()
			_, err := ir.ParseOpenAI([]byte(body))
			// 期望：要么正常解析，要么返回非 nil 错误。不 panic 即合格。
			_ = err
		})
	}
}

func TestSafety_ParseAnthropic_MalformedInput_NoPanic(t *testing.T) {
	for _, body := range []string{"", "{", `{"messages":null}`} {
		t.Run(body, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("parser panicked on %q: %v", body, r)
				}
			}()
			_, _ = ir.ParseAnthropic([]byte(body))
		})
	}
}

func TestSafety_ParseGemini_MalformedInput_NoPanic(t *testing.T) {
	for _, body := range []string{"", "{", `{"contents":null}`} {
		t.Run(body, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("parser panicked on %q: %v", body, r)
				}
			}()
			_, _ = ir.ParseGemini([]byte(body))
		})
	}
}

func TestSafety_ParseResponses_MalformedInput_NoPanic(t *testing.T) {
	for _, body := range []string{"", "{", `{"input":null}`} {
		t.Run(body, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("parser panicked on %q: %v", body, r)
				}
			}()
			_, _ = ir.ParseResponses([]byte(body))
		})
	}
}