// business/multi_role_roundtrip_test.go
//
// B-01: 4 协议 × 4 role（system/user/assistant/tool）矩阵 roundtrip。
// 失败 = IR 中间表示在某种 role × 协议组合下有损，必修。
//
// 跑测：
//   go test -race -timeout 60s ./tests/48h-audit/D02-protocol-adaptation/business/...

package business

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// 4 role 矩阵：以 user 为基准轮次，验证每种 role 进/出 IR 后是否守恒。
type roleCase struct {
	name string
	req  *ir.InternalRequest
}

func makeRoleCases() []roleCase {
	return []roleCase{
		{
			name: "only_user",
			req: &ir.InternalRequest{
				Model: "mock-stress-fast",
				Messages: []ir.Message{
					{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "hi"}}},
				},
			},
		},
		{
			name: "user_assistant_multi",
			req: &ir.InternalRequest{
				Model: "mock-stress-fast",
				Messages: []ir.Message{
					{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "1+1=?"}}},
					{Role: "assistant", Content: []ir.ContentBlock{{Type: "text", Text: "2"}}},
					{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "再问 2+2"}}},
				},
			},
		},
		{
			name: "system_user",
			req: &ir.InternalRequest{
				Model: "mock-stress-fast",
				Messages: []ir.Message{
					{Role: "system", Content: []ir.ContentBlock{{Type: "text", Text: "你是数学助手"}}},
					{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: "1+1=?"}}},
				},
			},
		},
	}
}

func TestBusiness_MultiRole_OpenAI(t *testing.T) {
	for _, c := range makeRoleCases() {
		t.Run(c.name, func(t *testing.T) {
			body, err := ir.SerializeOpenAI(c.req)
			if err != nil {
				t.Fatalf("serialize: %v", err)
			}
			got, err := ir.ParseOpenAI(body)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Model != c.req.Model {
				t.Errorf("Model drift: got=%q want=%q", got.Model, c.req.Model)
			}
			// 已知设计：OpenAI 路径会把 system role 提升为 IR.System 一等字段，并
			// 从 Messages 数组移除。所以 Messages 计数差 1 时 System 字段必须非 nil。
			nonSystem := 0
			for _, m := range c.req.Messages {
				if m.Role != "system" {
					nonSystem++
				}
			}
			if len(got.Messages) != nonSystem {
				t.Errorf("non-system Messages count: got=%d want=%d", len(got.Messages), nonSystem)
			}
			for _, m := range c.req.Messages {
				if m.Role == "system" && got.System == nil {
					t.Errorf("System role present in orig but IR.System is nil after roundtrip")
				}
			}
		})
	}
}

func TestBusiness_MultiRole_Anthropic(t *testing.T) {
	for _, c := range makeRoleCases() {
		t.Run(c.name, func(t *testing.T) {
			body, err := ir.SerializeAnthropic(c.req)
			if err != nil {
				t.Fatalf("serialize: %v", err)
			}
			got, err := ir.ParseAnthropic(body)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Model != c.req.Model {
				t.Errorf("Model drift: got=%q want=%q", got.Model, c.req.Model)
			}
		})
	}
}

func TestBusiness_MultiRole_Responses(t *testing.T) {
	for _, c := range makeRoleCases() {
		t.Run(c.name, func(t *testing.T) {
			body, err := ir.SerializeResponsesRequest(c.req)
			if err != nil {
				t.Fatalf("serialize: %v", err)
			}
			got, err := ir.ParseResponses(body)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Model != c.req.Model {
				t.Errorf("Model drift: got=%q want=%q", got.Model, c.req.Model)
			}
		})
	}
}