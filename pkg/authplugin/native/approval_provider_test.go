package native

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/approval"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// mockConfigManager implements a mock approval.ConfigManager for testing
type mockConfigManager struct {
	configs   map[string]*approval.ApprovalConfig
	approvers map[string][]approval.Approver
	rules     map[string][]approval.ApprovalRule
}

func newMockConfigManager() *mockConfigManager {
	return &mockConfigManager{
		configs:   make(map[string]*approval.ApprovalConfig),
		approvers: make(map[string][]approval.Approver),
		rules:     make(map[string][]approval.ApprovalRule),
	}
}

func (m *mockConfigManager) GetConfig(ctx context.Context, tenantID string) (*approval.ApprovalConfig, error) {
	if config, ok := m.configs[tenantID]; ok {
		return config, nil
	}
	// Return default config
	return &approval.ApprovalConfig{
		TenantID:       tenantID,
		Enabled:        false,
		Mode:           approval.ModeDisabled,
		TimeoutSeconds: 900,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}, nil
}

func (m *mockConfigManager) UpdateConfig(ctx context.Context, tenantID string, config *approval.ApprovalConfig) error {
	m.configs[tenantID] = config
	return nil
}

func (m *mockConfigManager) GetApprovers(ctx context.Context, tenantID string) ([]approval.Approver, error) {
	if approvers, ok := m.approvers[tenantID]; ok {
		return approvers, nil
	}
	return []approval.Approver{}, nil
}

func (m *mockConfigManager) AddApprover(ctx context.Context, tenantID string, approver *approval.Approver) error {
	m.approvers[tenantID] = append(m.approvers[tenantID], *approver)
	return nil
}

func (m *mockConfigManager) UpdateApprover(ctx context.Context, tenantID, userID string, approver *approval.Approver) error {
	approvers := m.approvers[tenantID]
	for i := range approvers {
		if approvers[i].UserID == userID {
			approvers[i] = *approver
			m.approvers[tenantID] = approvers
			return nil
		}
	}
	return nil
}

func (m *mockConfigManager) RemoveApprover(ctx context.Context, tenantID, userID string) error {
	approvers := m.approvers[tenantID]
	for i := range approvers {
		if approvers[i].UserID == userID {
			m.approvers[tenantID] = append(approvers[:i], approvers[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockConfigManager) GetRules(ctx context.Context, tenantID string) ([]approval.ApprovalRule, error) {
	if rules, ok := m.rules[tenantID]; ok {
		return rules, nil
	}
	return []approval.ApprovalRule{}, nil
}

func (m *mockConfigManager) AddRule(ctx context.Context, tenantID string, rule *approval.ApprovalRule) error {
	m.rules[tenantID] = append(m.rules[tenantID], *rule)
	return nil
}

func (m *mockConfigManager) RemoveRule(ctx context.Context, tenantID, ruleName string) error {
	rules := m.rules[tenantID]
	for i := range rules {
		if rules[i].Name == ruleName {
			m.rules[tenantID] = append(rules[:i], rules[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockConfigManager) GetConfigStats(ctx context.Context, tenantID string) (*approval.ConfigStats, error) {
	return &approval.ConfigStats{
		TenantID: tenantID,
		Enabled:  false,
	}, nil
}

func TestNativeApprovalProvider_Name(t *testing.T) {
	provider := NewNativeApprovalProvider(newMockConfigManager())
	if provider.Name() != "native" {
		t.Errorf("expected name 'native', got %s", provider.Name())
	}
}

func TestNativeApprovalProvider_GetConfig(t *testing.T) {
	mock := newMockConfigManager()
	provider := NewNativeApprovalProvider(mock)
	ctx := context.Background()

	// Test default config
	config, err := provider.GetConfig(ctx, "tenant1")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	if config.Enabled {
		t.Error("expected disabled config by default")
	}

	// Test with existing config
	mock.configs["tenant2"] = &approval.ApprovalConfig{
		TenantID:       "tenant2",
		Enabled:        true,
		Mode:           approval.ModeManual,
		TimeoutSeconds: 1800,
	}

	config, err = provider.GetConfig(ctx, "tenant2")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}
	if !config.Enabled {
		t.Error("expected enabled config")
	}
	if !config.RequireApproval {
		t.Error("expected manual mode to map to RequireApproval=true")
	}
	if config.TimeoutMinutes != 30 {
		t.Errorf("expected 30 minute timeout, got %d", config.TimeoutMinutes)
	}
}

func TestNativeApprovalProvider_UpdateConfig(t *testing.T) {
	mock := newMockConfigManager()
	provider := NewNativeApprovalProvider(mock)
	ctx := context.Background()

	pluginConfig := &authplugin.ApprovalConfig{
		Enabled:         true,
		RequireApproval: true,
		TimeoutMinutes:  20,
		TimeoutAction:   "deny",
	}

	err := provider.UpdateConfig(ctx, "tenant1", pluginConfig)
	if err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	// Verify it was saved
	domainConfig := mock.configs["tenant1"]
	if domainConfig == nil {
		t.Fatal("config was not saved")
	}
	if !domainConfig.Enabled {
		t.Error("expected enabled config")
	}
	if domainConfig.Mode != approval.ModeManual {
		t.Errorf("expected manual mode, got %s", domainConfig.Mode)
	}
	if domainConfig.TimeoutSeconds != 1200 {
		t.Errorf("expected 1200 seconds, got %d", domainConfig.TimeoutSeconds)
	}
	if !domainConfig.AutoRejectOnTimeout {
		t.Error("expected auto reject on timeout")
	}
}

func TestNativeApprovalProvider_Approvers(t *testing.T) {
	mock := newMockConfigManager()
	provider := NewNativeApprovalProvider(mock)
	ctx := context.Background()

	// Add approver
	approver := &authplugin.Approver{
		UserID:      "user1",
		Username:    "alice",
		DisplayName: "Alice Admin",
		Email:       "alice@example.com",
		Priority:    1,
		Enabled:     true,
	}

	err := provider.AddApprover(ctx, "tenant1", approver)
	if err != nil {
		t.Fatalf("AddApprover failed: %v", err)
	}

	// Get approvers
	approvers, err := provider.GetApprovers(ctx, "tenant1")
	if err != nil {
		t.Fatalf("GetApprovers failed: %v", err)
	}
	if len(approvers) != 1 {
		t.Fatalf("expected 1 approver, got %d", len(approvers))
	}
	if approvers[0].UserID != "user1" {
		t.Errorf("expected user1, got %s", approvers[0].UserID)
	}

	// Update approver
	approver.Priority = 2
	err = provider.UpdateApprover(ctx, "tenant1", "user1", approver)
	if err != nil {
		t.Fatalf("UpdateApprover failed: %v", err)
	}

	// Verify update
	approvers, _ = provider.GetApprovers(ctx, "tenant1")
	if approvers[0].Priority != 2 {
		t.Errorf("expected priority 2, got %d", approvers[0].Priority)
	}

	// Remove approver
	err = provider.RemoveApprover(ctx, "tenant1", "user1")
	if err != nil {
		t.Fatalf("RemoveApprover failed: %v", err)
	}

	// Verify removal
	approvers, _ = provider.GetApprovers(ctx, "tenant1")
	if len(approvers) != 0 {
		t.Errorf("expected 0 approvers after removal, got %d", len(approvers))
	}
}

func TestNativeApprovalProvider_Rules(t *testing.T) {
	mock := newMockConfigManager()
	provider := NewNativeApprovalProvider(mock)
	ctx := context.Background()

	// Add rule
	rule := &authplugin.ApprovalRule{
		Name:        "high_cost",
		Description: "Require approval for high cost requests",
		Conditions: map[string]interface{}{
			"condition_0": map[string]string{
				"field":    "cost",
				"operator": "gt",
				"value":    "100",
			},
		},
		Priority: 10,
		Enabled:  true,
	}

	err := provider.AddRule(ctx, "tenant1", rule)
	if err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	// Get rules
	rules, err := provider.GetRules(ctx, "tenant1")
	if err != nil {
		t.Fatalf("GetRules failed: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Name != "high_cost" {
		t.Errorf("expected high_cost rule, got %s", rules[0].Name)
	}

	// Remove rule
	err = provider.RemoveRule(ctx, "tenant1", "high_cost")
	if err != nil {
		t.Fatalf("RemoveRule failed: %v", err)
	}

	// Verify removal
	rules, _ = provider.GetRules(ctx, "tenant1")
	if len(rules) != 0 {
		t.Errorf("expected 0 rules after removal, got %d", len(rules))
	}
}

func TestNativeApprovalProvider_SupportsFeature(t *testing.T) {
	provider := NewNativeApprovalProvider(newMockConfigManager())

	tests := []struct {
		feature  authplugin.ApprovalFeature
		expected bool
	}{
		{authplugin.FeatureMultiLevelApproval, true},
		{authplugin.FeatureApprovalDelegation, false},
		{authplugin.FeatureApprovalEscalation, true},
	}

	for _, tt := range tests {
		result := provider.SupportsFeature(tt.feature)
		if result != tt.expected {
			t.Errorf("SupportsFeature(%v) = %v, expected %v", tt.feature, result, tt.expected)
		}
	}
}

func TestNativeApprovalProvider_NilConfigManager(t *testing.T) {
	provider := NewNativeApprovalProvider(nil)
	ctx := context.Background()

	// All methods should return ErrProviderUnavailable
	_, err := provider.GetConfig(ctx, "tenant1")
	if err != authplugin.ErrProviderUnavailable {
		t.Errorf("expected ErrProviderUnavailable, got %v", err)
	}

	err = provider.UpdateConfig(ctx, "tenant1", &authplugin.ApprovalConfig{})
	if err != authplugin.ErrProviderUnavailable {
		t.Errorf("expected ErrProviderUnavailable, got %v", err)
	}

	_, err = provider.GetApprovers(ctx, "tenant1")
	if err != authplugin.ErrProviderUnavailable {
		t.Errorf("expected ErrProviderUnavailable, got %v", err)
	}
}
