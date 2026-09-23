package executors

// ir_request_stamp_test.go —— R61 stamp 助手钉桩（S4 审计指 executor 侧
// stamp 无直接回归测试）。

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// TestStampIRRequestID —— R61：stamp 助手行为钉桩（S4 审计指 executor 侧
// stamp 无直接回归测试；nil 载体/空 requestID/正常写入三态）。
func TestStampIRRequestID(t *testing.T) {
	if got := stampIRRequestID(nil, "req-1"); got != nil {
		t.Fatalf("nil carrier must pass through, got %v", got)
	}
	empty := &ir.InternalRequest{}
	if got := stampIRRequestID(empty, ""); got.Metadata != nil {
		t.Fatalf("empty requestID must not allocate Metadata, got %+v", got.Metadata)
	}
	req := &ir.InternalRequest{}
	got := stampIRRequestID(req, "req-abc")
	if got != req || got.Metadata == nil || got.Metadata.RequestID != "req-abc" {
		t.Fatalf("stamp failed: req=%v meta=%+v", got == req, got.Metadata)
	}
	// 已有 Metadata 时复用，不覆盖其他字段。
	req2 := &ir.InternalRequest{Metadata: &ir.Metadata{UserID: "u1"}}
	got2 := stampIRRequestID(req2, "req-2")
	if got2.Metadata.UserID != "u1" || got2.Metadata.RequestID != "req-2" {
		t.Fatalf("stamp must preserve existing metadata: %+v", got2.Metadata)
	}
}
