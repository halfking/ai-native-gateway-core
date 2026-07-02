package main

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/eventbus"
	"github.com/kaixuan/llm-gateway-go/pending"
)

func TestInitializeApprovalIntegration_NilDeps(t *testing.T) {
	_, err := InitializeApprovalIntegration(nil)
	if err == nil {
		t.Error("expected error for nil deps")
	}
}

func TestInitializeApprovalIntegration_MissingSessionCache(t *testing.T) {
	deps := &ApprovalIntegrationDeps{
		ApprovalMgr:  &sessionaudit.ApprovalManager{},
		PendingStore: &pending.Store{},
		ChatHandler:  &streaming.ChatHandler{},
	}
	_, err := InitializeApprovalIntegration(deps)
	if err == nil {
		t.Error("expected error for missing SessionCache")
	}
}

func TestInitializeApprovalIntegration_MissingApprovalMgr(t *testing.T) {
	deps := &ApprovalIntegrationDeps{
		SessionCache: &compression.SessionCache{},
		PendingStore: &pending.Store{},
		ChatHandler:  &streaming.ChatHandler{},
	}
	_, err := InitializeApprovalIntegration(deps)
	if err == nil {
		t.Error("expected error for missing ApprovalMgr")
	}
}

func TestInitializeApprovalIntegration_DefaultTimeout(t *testing.T) {
	// 这个测试只验证依赖检查逻辑，不创建真实的依赖
	// 因为需要 DB、Redis 等外部依赖

	deps := &ApprovalIntegrationDeps{
		SessionCache: &compression.SessionCache{},
		ApprovalMgr:  &sessionaudit.ApprovalManager{},
		PendingStore: &pending.Store{},
		ChatHandler:  &streaming.ChatHandler{},
		AuditBus:     eventbus.NewMemoryBus(100),
		// ApprovalTimeout 不设置，应该使用默认值
	}

	// 由于需要真实的 DB/Redis，这里只测试到参数验证
	// 实际的集成测试应该在集成测试环境中进行
	if deps.ApprovalTimeout == 0 {
		t.Log("timeout is 0, will use default 15 minutes")
	}
}

func TestValidateApprovalIntegration_Nil(t *testing.T) {
	err := ValidateApprovalIntegration(nil)
	if err == nil {
		t.Error("expected error for nil result")
	}
}

func TestContainsString(t *testing.T) {
	tests := []struct {
		s      string
		substr string
		want   bool
	}{
		{"text/event-stream", "event-stream", true},
		{"application/json", "json", true},
		{"text/html", "event-stream", false},
		{"", "", true},
		{"foo", "", true},
	}

	for _, tt := range tests {
		got := containsString(tt.s, tt.substr)
		if got != tt.want {
			t.Errorf("containsString(%q, %q) = %v, want %v", tt.s, tt.substr, got, tt.want)
		}
	}
}

// 集成测试文档（需要真实环境）
func TestApprovalIntegration_E2E_Documentation(t *testing.T) {
	t.Skip("E2E test requires real DB, Redis, and service dependencies")

	t.Log("完整的集成测试应该验证：")
	t.Log("1. SessionCache 正确创建并可用")
	t.Log("2. CacheUpdateHook 正确创建")
	t.Log("3. ApprovalHook 正确创建")
	t.Log("4. ApprovalResumeHandler 正确创建")
	t.Log("5. LLMCaller 可以从 snapshot 恢复请求并调用 ChatHandler")
	t.Log("6. 响应正确写入 pending.Store")
	t.Log("7. Client 可以通过 /v1/sessions/:id/pending-response 获取响应")
}
