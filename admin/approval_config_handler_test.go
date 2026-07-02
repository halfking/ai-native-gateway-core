package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
)

// mockConfigManager is a mock implementation of approval.ConfigManager for testing
type mockConfigManager struct {
	config    *approval.ApprovalConfig
	approvers []approval.Approver
	rules     []approval.ApprovalRule
	stats     *approval.ConfigStats
	err       error
}

func (m *mockConfigManager) GetConfig(ctx context.Context, tenantID string) (*approval.ApprovalConfig, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.config == nil {
		return &approval.ApprovalConfig{
			TenantID:            tenantID,
			Enabled:             true,
			Mode:                approval.ModeAutomatic,
			TimeoutSeconds:      3600,
			AutoRejectOnTimeout: true,
			Approvers:           []approval.Approver{},
			Channels:            []approval.NotificationChannel{},
			Rules:               []approval.ApprovalRule{},
			CreatedAt:           time.Now(),
			UpdatedAt:           time.Now(),
		}, nil
	}
	return m.config, nil
}

func (m *mockConfigManager) UpdateConfig(ctx context.Context, tenantID string, config *approval.ApprovalConfig) error {
	if m.err != nil {
		return m.err
	}
	m.config = config
	return nil
}

func (m *mockConfigManager) GetApprovers(ctx context.Context, tenantID string) ([]approval.Approver, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.approvers, nil
}

func (m *mockConfigManager) AddApprover(ctx context.Context, tenantID string, approver *approval.Approver) error {
	if m.err != nil {
		return m.err
	}
	m.approvers = append(m.approvers, *approver)
	return nil
}

func (m *mockConfigManager) UpdateApprover(ctx context.Context, tenantID string, userID string, approver *approval.Approver) error {
	if m.err != nil {
		return m.err
	}
	for i, a := range m.approvers {
		if a.UserID == userID {
			m.approvers[i] = *approver
			return nil
		}
	}
	return nil
}

func (m *mockConfigManager) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	if m.err != nil {
		return m.err
	}
	for i, a := range m.approvers {
		if a.UserID == userID {
			m.approvers = append(m.approvers[:i], m.approvers[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockConfigManager) GetRules(ctx context.Context, tenantID string) ([]approval.ApprovalRule, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.rules, nil
}

func (m *mockConfigManager) AddRule(ctx context.Context, tenantID string, rule *approval.ApprovalRule) error {
	if m.err != nil {
		return m.err
	}
	m.rules = append(m.rules, *rule)
	return nil
}

func (m *mockConfigManager) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	if m.err != nil {
		return m.err
	}
	for i, r := range m.rules {
		if r.Name == ruleName {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockConfigManager) GetConfigStats(ctx context.Context, tenantID string) (*approval.ConfigStats, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.stats == nil {
		return &approval.ConfigStats{
			TenantID:         tenantID,
			Enabled:          true,
			Mode:             string(approval.ModeAutomatic),
			ApproverCount:    len(m.approvers),
			RuleCount:        len(m.rules),
			EnabledApprovers: len(m.approvers),
			EnabledRules:     len(m.rules),
			TimeoutSeconds:   3600,
			LastUpdated:      time.Now(),
		}, nil
	}
	return m.stats, nil
}

func TestApprovalConfigHandler_GetConfig(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		authContext    *AuthContext
		expectedStatus int
	}{
		{
			name: "successful get config as super admin",
			path: "/api/admin/tenants/tenant1/approval-config",
			authContext: &AuthContext{
				UserID:   1,
				TenantID: "default",
				Role:     "super_admin",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "successful get config as tenant admin",
			path: "/api/admin/tenants/tenant1/approval-config",
			authContext: &AuthContext{
				UserID:   2,
				TenantID: "tenant1",
				Role:     "tenant_admin",
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "forbidden - tenant admin accessing other tenant",
			path: "/api/admin/tenants/tenant2/approval-config",
			authContext: &AuthContext{
				UserID:   2,
				TenantID: "tenant1",
				Role:     "tenant_admin",
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:           "missing tenant_id",
			path:           "/api/admin/tenants//approval-config",
			authContext:    &AuthContext{Role: "super_admin"},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockConfigManager{}
			handler := NewApprovalConfigHandler(mock)

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.authContext != nil {
				req = SetAuthContext(req, tt.authContext)
			}

			rr := httptest.NewRecorder()
			handler.GetConfig(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}
		})
	}
}

func TestApprovalConfigHandler_UpdateConfig(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		authContext    *AuthContext
		body           interface{}
		expectedStatus int
	}{
		{
			name: "successful update as super admin",
			path: "/api/admin/tenants/tenant1/approval-config",
			authContext: &AuthContext{
				UserID:   1,
				TenantID: "default",
				Role:     "super_admin",
			},
			body: approval.ApprovalConfig{
				Enabled:        true,
				Mode:           approval.ModeAutomatic,
				TimeoutSeconds: 7200,
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "successful update as tenant admin",
			path: "/api/admin/tenants/tenant1/approval-config",
			authContext: &AuthContext{
				UserID:   2,
				TenantID: "tenant1",
				Role:     "tenant_admin",
			},
			body: approval.ApprovalConfig{
				Enabled:        true,
				Mode:           approval.ModeManual,
				TimeoutSeconds: 3600,
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "forbidden - tenant admin updating other tenant",
			path: "/api/admin/tenants/tenant2/approval-config",
			authContext: &AuthContext{
				UserID:   2,
				TenantID: "tenant1",
				Role:     "tenant_admin",
			},
			body:           approval.ApprovalConfig{},
			expectedStatus: http.StatusForbidden,
		},
		{
			name: "invalid request body",
			path: "/api/admin/tenants/tenant1/approval-config",
			authContext: &AuthContext{
				Role: "super_admin",
			},
			body:           "invalid json",
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockConfigManager{}
			handler := NewApprovalConfigHandler(mock)

			var body []byte
			if str, ok := tt.body.(string); ok {
				body = []byte(str)
			} else {
				body, _ = json.Marshal(tt.body)
			}

			req := httptest.NewRequest(http.MethodPut, tt.path, bytes.NewReader(body))
			if tt.authContext != nil {
				req = SetAuthContext(req, tt.authContext)
			}

			rr := httptest.NewRecorder()
			handler.UpdateConfig(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}
		})
	}
}

func TestApprovalConfigHandler_GetApprovers(t *testing.T) {
	mock := &mockConfigManager{
		approvers: []approval.Approver{
			{
				UserID:   "user1",
				Name:     "User One",
				Email:    "user1@example.com",
				Role:     "admin",
				Priority: 1,
				Enabled:  true,
			},
		},
	}
	handler := NewApprovalConfigHandler(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/tenant1/approvers", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "tenant1",
		Role:     "tenant_admin",
	})

	rr := httptest.NewRecorder()
	handler.GetApprovers(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if count, ok := response["count"].(float64); !ok || int(count) != 1 {
		t.Errorf("expected count 1, got %v", response["count"])
	}
}

func TestApprovalConfigHandler_AddApprover(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		authContext    *AuthContext
		body           approval.Approver
		expectedStatus int
	}{
		{
			name: "successful add approver",
			path: "/api/admin/tenants/tenant1/approvers",
			authContext: &AuthContext{
				UserID:   1,
				TenantID: "tenant1",
				Role:     "tenant_admin",
			},
			body: approval.Approver{
				UserID:   "user1",
				Name:     "User One",
				Email:    "user1@example.com",
				Role:     "admin",
				Priority: 1,
				Enabled:  true,
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name: "forbidden - wrong tenant",
			path: "/api/admin/tenants/tenant2/approvers",
			authContext: &AuthContext{
				UserID:   1,
				TenantID: "tenant1",
				Role:     "tenant_admin",
			},
			body:           approval.Approver{UserID: "user1"},
			expectedStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockConfigManager{}
			handler := NewApprovalConfigHandler(mock)

			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(body))
			if tt.authContext != nil {
				req = SetAuthContext(req, tt.authContext)
			}

			rr := httptest.NewRecorder()
			handler.AddApprover(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}
		})
	}
}

func TestApprovalConfigHandler_UpdateApprover(t *testing.T) {
	mock := &mockConfigManager{
		approvers: []approval.Approver{
			{UserID: "user1", Name: "Old Name"},
		},
	}
	handler := NewApprovalConfigHandler(mock)

	updatedApprover := approval.Approver{
		UserID:   "user1",
		Name:     "New Name",
		Email:    "updated@example.com",
		Role:     "admin",
		Priority: 2,
		Enabled:  true,
	}

	body, _ := json.Marshal(updatedApprover)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/tenants/tenant1/approvers/user1", bytes.NewReader(body))
	req = SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "tenant1",
		Role:     "tenant_admin",
	})

	rr := httptest.NewRecorder()
	handler.UpdateApprover(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}
}

func TestApprovalConfigHandler_DeleteApprover(t *testing.T) {
	mock := &mockConfigManager{
		approvers: []approval.Approver{
			{UserID: "user1", Name: "User One"},
		},
	}
	handler := NewApprovalConfigHandler(mock)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/tenants/tenant1/approvers/user1", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "tenant1",
		Role:     "tenant_admin",
	})

	rr := httptest.NewRecorder()
	handler.DeleteApprover(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	if len(mock.approvers) != 0 {
		t.Errorf("expected approvers to be empty, got %d items", len(mock.approvers))
	}
}

func TestApprovalConfigHandler_GetRules(t *testing.T) {
	mock := &mockConfigManager{
		rules: []approval.ApprovalRule{
			{
				Name:     "high_cost_rule",
				Enabled:  true,
				Priority: 10,
				Conditions: []approval.RuleCondition{
					{Field: "cost", Operator: "gt", Value: "100"},
				},
				Action: approval.RuleAction{
					Type:      "require_approval",
					RiskLevel: approval.RiskHigh,
					Reason:    "High cost detected",
				},
			},
		},
	}
	handler := NewApprovalConfigHandler(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/tenant1/approval-rules", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "tenant1",
		Role:     "tenant_admin",
	})

	rr := httptest.NewRecorder()
	handler.GetRules(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var response map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if count, ok := response["count"].(float64); !ok || int(count) != 1 {
		t.Errorf("expected count 1, got %v", response["count"])
	}
}

func TestApprovalConfigHandler_AddRule(t *testing.T) {
	mock := &mockConfigManager{}
	handler := NewApprovalConfigHandler(mock)

	rule := approval.ApprovalRule{
		Name:     "sensitive_content_rule",
		Enabled:  true,
		Priority: 5,
		Conditions: []approval.RuleCondition{
			{Field: "message_content", Operator: "contains", Value: "confidential"},
		},
		Action: approval.RuleAction{
			Type:      "require_approval",
			RiskLevel: approval.RiskMedium,
			Reason:    "Sensitive content detected",
		},
	}

	body, _ := json.Marshal(rule)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/tenants/tenant1/approval-rules", bytes.NewReader(body))
	req = SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "tenant1",
		Role:     "tenant_admin",
	})

	rr := httptest.NewRecorder()
	handler.AddRule(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rr.Code)
	}

	if len(mock.rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(mock.rules))
	}
}

func TestApprovalConfigHandler_DeleteRule(t *testing.T) {
	mock := &mockConfigManager{
		rules: []approval.ApprovalRule{
			{Name: "test_rule", Enabled: true},
		},
	}
	handler := NewApprovalConfigHandler(mock)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/tenants/tenant1/approval-rules/test_rule", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "tenant1",
		Role:     "tenant_admin",
	})

	rr := httptest.NewRecorder()
	handler.DeleteRule(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	if len(mock.rules) != 0 {
		t.Errorf("expected rules to be empty, got %d items", len(mock.rules))
	}
}

func TestApprovalConfigHandler_GetConfigStats(t *testing.T) {
	mock := &mockConfigManager{
		approvers: []approval.Approver{
			{UserID: "user1", Enabled: true},
			{UserID: "user2", Enabled: true},
		},
		rules: []approval.ApprovalRule{
			{Name: "rule1", Enabled: true},
		},
	}
	handler := NewApprovalConfigHandler(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/tenants/tenant1/approval-config/stats", nil)
	req = SetAuthContext(req, &AuthContext{
		UserID:   1,
		TenantID: "tenant1",
		Role:     "tenant_admin",
	})

	rr := httptest.NewRecorder()
	handler.GetConfigStats(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var stats approval.ConfigStats
	if err := json.NewDecoder(rr.Body).Decode(&stats); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if stats.ApproverCount != 2 {
		t.Errorf("expected approver count 2, got %d", stats.ApproverCount)
	}

	if stats.RuleCount != 1 {
		t.Errorf("expected rule count 1, got %d", stats.RuleCount)
	}
}

func TestApprovalConfigHandler_ExtractTenantID(t *testing.T) {
	handler := &ApprovalConfigHandler{}

	tests := []struct {
		path     string
		expected string
	}{
		{"/api/admin/tenants/tenant1/approval-config", "tenant1"},
		{"/api/admin/tenants/my-tenant/approvers", "my-tenant"},
		{"/api/admin/tenants/abc123/approval-rules", "abc123"},
		{"/invalid/path", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := handler.extractTenantID(tt.path)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestApprovalConfigHandler_ExtractUserID(t *testing.T) {
	handler := &ApprovalConfigHandler{}

	tests := []struct {
		path     string
		expected string
	}{
		{"/api/admin/tenants/tenant1/approvers/user1", "user1"},
		{"/api/admin/tenants/tenant1/approvers/user-abc", "user-abc"},
		{"/invalid/path", ""},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := handler.extractUserID(tt.path)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestApprovalConfigHandler_CanAccessTenant(t *testing.T) {
	handler := &ApprovalConfigHandler{}

	tests := []struct {
		name        string
		authContext *AuthContext
		tenantID    string
		expected    bool
	}{
		{
			name: "super admin can access any tenant",
			authContext: &AuthContext{
				Role:     "super_admin",
				TenantID: "default",
			},
			tenantID: "tenant1",
			expected: true,
		},
		{
			name: "tenant admin can access own tenant",
			authContext: &AuthContext{
				Role:     "tenant_admin",
				TenantID: "tenant1",
			},
			tenantID: "tenant1",
			expected: true,
		},
		{
			name: "tenant admin cannot access other tenant",
			authContext: &AuthContext{
				Role:     "tenant_admin",
				TenantID: "tenant1",
			},
			tenantID: "tenant2",
			expected: false,
		},
		{
			name:        "no auth context",
			authContext: nil,
			tenantID:    "tenant1",
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authContext != nil {
				req = SetAuthContext(req, tt.authContext)
			}

			result := handler.canAccessTenant(req, tt.tenantID)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}
