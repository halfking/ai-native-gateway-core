package remotecontrol

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/sessionstate"
)

// MockSessionManager 模拟会话管理器
type MockSessionManager struct {
	sessions map[string]*domain.PipelineRequest
	states   map[string]*sessionstate.SessionStateMachine
}

func NewMockSessionManager() *MockSessionManager {
	return &MockSessionManager{
		sessions: make(map[string]*domain.PipelineRequest),
		states:   make(map[string]*sessionstate.SessionStateMachine),
	}
}

func (m *MockSessionManager) GetSession(ctx context.Context, sessionID string) (*domain.PipelineRequest, error) {
	if session, ok := m.sessions[sessionID]; ok {
		return session, nil
	}
	return nil, nil
}

func (m *MockSessionManager) PauseSession(ctx context.Context, sessionID string) error {
	return nil
}

func (m *MockSessionManager) ResumeSession(ctx context.Context, sessionID string) error {
	return nil
}

func (m *MockSessionManager) TerminateSession(ctx context.Context, sessionID, reason string) error {
	return nil
}

func (m *MockSessionManager) GetSessionState(ctx context.Context, sessionID string) (*sessionstate.SessionStateMachine, error) {
	if state, ok := m.states[sessionID]; ok {
		return state, nil
	}
	state := sessionstate.NewSessionStateMachine(sessionID, "tenant_001")
	m.states[sessionID] = state
	return state, nil
}

func TestLarkCommandParser_ParseCommand(t *testing.T) {
	parser := NewLarkCommandParser()

	tests := []struct {
		name           string
		content        string
		expectedType   CommandType
		expectedSessID string
		shouldError    bool
	}{
		{
			name:           "pause command",
			content:        "/pause session_123",
			expectedType:   CommandTypePause,
			expectedSessID: "session_123",
			shouldError:    false,
		},
		{
			name:           "resume command",
			content:        "/resume session_456",
			expectedType:   CommandTypeResume,
			expectedSessID: "session_456",
			shouldError:    false,
		},
		{
			name:           "terminate command with reason",
			content:        "/terminate session_789 reason=\"manual stop\"",
			expectedType:   CommandTypeTerminate,
			expectedSessID: "session_789",
			shouldError:    false,
		},
		{
			name:           "status command",
			content:        "/status session_abc",
			expectedType:   CommandTypeStatus,
			expectedSessID: "session_abc",
			shouldError:    false,
		},
		{
			name:           "chinese pause command",
			content:        "/暂停 session_123",
			expectedType:   CommandTypePause,
			expectedSessID: "session_123",
			shouldError:    false,
		},
		{
			name:        "not a command",
			content:     "hello world",
			shouldError: true,
		},
		{
			name:        "unknown command",
			content:     "/unknown session_123",
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := parser.ParseCommand(tt.content, "user_1", "张三")

			if tt.shouldError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if cmd.Type != tt.expectedType {
				t.Errorf("expected type %s, got %s", tt.expectedType, cmd.Type)
			}

			if cmd.SessionID != tt.expectedSessID {
				t.Errorf("expected session_id %s, got %s", tt.expectedSessID, cmd.SessionID)
			}

			if cmd.IssuerID != "user_1" {
				t.Errorf("expected issuer_id user_1, got %s", cmd.IssuerID)
			}
		})
	}
}

func TestLarkCommandParser_ParseParameters(t *testing.T) {
	parser := NewLarkCommandParser()

	parts := []string{
		"reason=\"manual termination\"",
		"priority=10",
		"force=true",
	}

	params := parser.parseParameters(parts)

	if params["reason"] != "manual termination" {
		t.Errorf("expected reason 'manual termination', got %v", params["reason"])
	}

	if params["priority"] != "10" {
		t.Errorf("expected priority '10', got %v", params["priority"])
	}

	if params["force"] != "true" {
		t.Errorf("expected force 'true', got %v", params["force"])
	}
}

func TestSimpleAuthorizationChecker(t *testing.T) {
	checker := NewSimpleAuthorizationChecker()

	// 设置角色
	checker.SetUserRole("user_admin", "tenant_001", RoleAdmin)
	checker.SetUserRole("user_viewer", "tenant_001", RoleViewer)
	checker.SetUserRole("user_super", "tenant_001", RoleSuperAdmin)

	// 测试管理员权限
	if !checker.CanExecute("user_admin", CommandTypePause, "tenant_001") {
		t.Error("admin should be able to pause")
	}

	if checker.CanExecute("user_admin", CommandTypeTerminate, "tenant_001") {
		t.Error("admin should not be able to terminate")
	}

	// 测试查看者权限
	if !checker.CanExecute("user_viewer", CommandTypeInspect, "tenant_001") {
		t.Error("viewer should be able to inspect")
	}

	if checker.CanExecute("user_viewer", CommandTypePause, "tenant_001") {
		t.Error("viewer should not be able to pause")
	}

	// 测试超级管理员权限
	if !checker.CanExecute("user_super", CommandTypeTerminate, "tenant_001") {
		t.Error("super admin should be able to terminate")
	}
}

func TestHasPermission(t *testing.T) {
	tests := []struct {
		role       Role
		permission Permission
		expected   bool
	}{
		{RoleSuperAdmin, PermissionPauseSession, true},
		{RoleSuperAdmin, PermissionTerminateSession, true},
		{RoleAdmin, PermissionPauseSession, true},
		{RoleAdmin, PermissionTerminateSession, false},
		{RoleViewer, PermissionInspectSession, true},
		{RoleViewer, PermissionPauseSession, false},
		{RoleOperator, PermissionPauseSession, true},
		{RoleOperator, PermissionTerminateSession, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.role)+"_"+string(tt.permission), func(t *testing.T) {
			result := HasPermission(tt.role, tt.permission)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestDefaultCommandExecutor_Execute(t *testing.T) {
	sessionMgr := NewMockSessionManager()
	authChecker := NewSimpleAuthorizationChecker()
	auditLogger := NewInMemoryAuditLogger()

	// 设置用户角色
	authChecker.SetUserRole("user_admin", "tenant_001", RoleAdmin)

	executor := NewDefaultCommandExecutor(sessionMgr, authChecker, auditLogger)

	// 创建测试会话
	sessionMgr.states["session_123"] = sessionstate.NewSessionStateMachine("session_123", "tenant_001")

	// 测试pause指令
	cmd := &RemoteCommand{
		Type:       CommandTypePause,
		SessionID:  "session_123",
		TenantID:   "tenant_001",
		IssuerID:   "user_admin",
		IssuerName: "管理员",
	}

	err := executor.Execute(context.Background(), cmd)
	if err != nil {
		t.Fatalf("failed to execute pause command: %v", err)
	}

	if cmd.Status != CommandStatusCompleted {
		t.Errorf("expected status completed, got %s", cmd.Status)
	}

	// 检查审计日志
	logs := auditLogger.GetCommands()
	if len(logs) == 0 {
		t.Error("expected audit logs")
	}
}

func TestDefaultCommandExecutor_PermissionDenied(t *testing.T) {
	sessionMgr := NewMockSessionManager()
	authChecker := NewSimpleAuthorizationChecker()
	auditLogger := NewInMemoryAuditLogger()

	// 设置用户为viewer（没有pause权限）
	authChecker.SetUserRole("user_viewer", "tenant_001", RoleViewer)

	executor := NewDefaultCommandExecutor(sessionMgr, authChecker, auditLogger)

	cmd := &RemoteCommand{
		Type:       CommandTypePause,
		SessionID:  "session_123",
		TenantID:   "tenant_001",
		IssuerID:   "user_viewer",
		IssuerName: "查看者",
	}

	err := executor.Execute(context.Background(), cmd)
	if err == nil {
		t.Error("expected permission denied error")
	}

	if cmd.Status != CommandStatusFailed {
		t.Errorf("expected status failed, got %s", cmd.Status)
	}
}

func TestLarkCommandAPI_HandleCommand(t *testing.T) {
	sessionMgr := NewMockSessionManager()
	authChecker := NewSimpleAuthorizationChecker()
	auditLogger := NewInMemoryAuditLogger()

	authChecker.SetUserRole("user_1", "tenant_001", RoleAdmin)

	executor := NewDefaultCommandExecutor(sessionMgr, authChecker, auditLogger)
	api := NewLarkCommandAPI(executor)

	// 创建测试会话
	sessionMgr.states["session_123"] = sessionstate.NewSessionStateMachine("session_123", "tenant_001")

	result, err := api.HandleCommand(
		context.Background(),
		"/pause session_123",
		"user_1",
		"张三",
		"tenant_001",
	)

	if err != nil {
		t.Fatalf("failed to handle command: %v", err)
	}

	if result == "" {
		t.Error("expected non-empty result")
	}

	// 验证结果包含成功信息
	if !contains(result, "✅") && !contains(result, "成功") {
		t.Errorf("result should indicate success, got: %s", result)
	}
}

func TestValidateSessionID(t *testing.T) {
	tests := []struct {
		sessionID string
		valid     bool
	}{
		{"session_123", true},
		{"sess_abc", true},
		{"ab", false},
		{"", false},
		{"valid_session_id_12345", true},
	}

	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			result := ValidateSessionID(tt.sessionID)
			if result != tt.valid {
				t.Errorf("expected %v, got %v", tt.valid, result)
			}
		})
	}
}

func TestGetCommandTemplates(t *testing.T) {
	templates := GetCommandTemplates()

	if len(templates) == 0 {
		t.Error("expected command templates")
	}

	// 验证包含基本指令
	found := make(map[CommandType]bool)
	for _, tmpl := range templates {
		found[tmpl.Type] = true
	}

	expectedTypes := []CommandType{
		CommandTypePause,
		CommandTypeResume,
		CommandTypeTerminate,
		CommandTypeInspect,
		CommandTypeStatus,
	}

	for _, expectedType := range expectedTypes {
		if !found[expectedType] {
			t.Errorf("missing template for command type: %s", expectedType)
		}
	}
}

func TestFormatCommandResult(t *testing.T) {
	parser := NewLarkCommandParser()

	cmd := &RemoteCommand{
		ID:         "cmd_123",
		Type:       CommandTypePause,
		SessionID:  "session_123",
		IssuerName: "张三",
		Status:     CommandStatusCompleted,
		Result: map[string]any{
			"message": "session paused successfully",
		},
	}

	result := parser.FormatCommandResult(cmd)

	if result == "" {
		t.Error("expected non-empty result")
	}

	// 验证包含关键信息
	if !contains(result, "cmd_123") {
		t.Error("result should contain command ID")
	}

	if !contains(result, "session_123") {
		t.Error("result should contain session ID")
	}

	if !contains(result, "张三") {
		t.Error("result should contain issuer name")
	}
}

// 辅助函数
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
