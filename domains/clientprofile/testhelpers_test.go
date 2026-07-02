package clientprofile

import (
	"github.com/kaixuan/llm-gateway-go/domains/session" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/internal/ir"     //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// newTestSessionContext 返回一个最小化的 *session.SessionContext，用于 emitter 测试。
func newTestSessionContext() *session.SessionContext {
	return &session.SessionContext{
		SessionID:     "sess-test",
		RequestID:     "req-test",
		TenantID:      "tenant-test",
		ClientModel:   "gpt-4",
		UpstreamModel: "gpt-4-turbo",
		ClientIR:      nil,
	}
}

// newTestIR 用 userText + assistantText 构造一个 *ir.InternalRequest，用于任务类型推断。
func newTestIR(userText, assistantText string) *ir.InternalRequest {
	return &ir.InternalRequest{
		Messages: []ir.Message{
			{Role: "user", Content: []ir.ContentBlock{{Type: "text", Text: userText}}},
			{Role: "assistant", Content: []ir.ContentBlock{{Type: "text", Text: assistantText}}},
		},
	}
}
