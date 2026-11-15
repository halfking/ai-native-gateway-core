package casdoor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

func TestCasdoorApprovalProvider_Name(t *testing.T) {
	provider := &CasdoorApprovalProvider{}
	if provider.Name() != "casdoor" {
		t.Errorf("expected name 'casdoor', got %s", provider.Name())
	}
}

func TestCasdoorApprovalProvider_GetConfig(t *testing.T) {
	// Create mock Casdoor server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get-organization" {
			org := casdoorOrganization{
				Owner:       "test-org",
				Name:        "tenant1",
				DisplayName: "Tenant 1",
				Tags: []string{
					"approval_config={\"enabled\":true,\"require_approval\":true,\"timeout_minutes\":30}",
				},
			}
			json.NewEncoder(w).Encode(org)
		}
	}))
	defer server.Close()

	client := server.Client()

	provider := NewCasdoorApprovalProvider(client, server.URL, "test", "secret", nil, "test-org", "test-app")
	ctx := context.Background()

	config, err := provider.GetConfig(ctx, "tenant1")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if !config.Enabled {
		t.Error("expected enabled config")
	}
	if !config.RequireApproval {
		t.Error("expected require_approval=true")
	}
	if config.TimeoutMinutes != 30 {
		t.Errorf("expected 30 minute timeout, got %d", config.TimeoutMinutes)
	}
}

func TestCasdoorApprovalProvider_UpdateConfig(t *testing.T) {
	updateCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get-organization" {
			org := casdoorOrganization{
				Owner: "test-org",
				Name:  "tenant1",
				Tags:  []string{},
			}
			json.NewEncoder(w).Encode(org)
		} else if r.URL.Path == "/api/update-organization" {
			updateCalled = true
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
		}
	}))
	defer server.Close()

	client := server.Client()

	provider := NewCasdoorApprovalProvider(client, server.URL, "test", "secret", nil, "test-org", "test-app")
	ctx := context.Background()

	config := &authplugin.ApprovalConfig{
		Enabled:         true,
		RequireApproval: false,
		TimeoutMinutes:  45,
		TimeoutAction:   "auto_approve",
	}

	err := provider.UpdateConfig(ctx, "tenant1", config)
	if err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	if !updateCalled {
		t.Error("expected update-organization endpoint to be called")
	}
}

func TestCasdoorApprovalProvider_GetApprovers(t *testing.T) {
	mockMapper := &mockIDMapper{
		casdoorToLocalMap: map[string]int{
			"alice": 101,
			"bob":   102,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get-users" {
			users := []casdoorUser{
				{
					Name:        "alice",
					DisplayName: "Alice Admin",
					Email:       "alice@example.com",
					Type:        "approver",
					IsForbidden: false,
					Properties: map[string]string{
						"approver_priority": "1",
						"approver_enabled":  "true",
					},
				},
				{
					Name:        "bob",
					DisplayName: "Bob Reviewer",
					Email:       "bob@example.com",
					Type:        "approver",
					IsForbidden: false,
					Properties: map[string]string{
						"approver_priority": "2",
					},
				},
				{
					Name:        "charlie",
					DisplayName: "Charlie User",
					Type:        "normal-user",
				},
			}
			json.NewEncoder(w).Encode(users)
		}
	}))
	defer server.Close()

	client := server.Client()

	provider := NewCasdoorApprovalProvider(client, server.URL, "test", "secret", mockMapper, "test-org", "test-app")
	ctx := context.Background()

	approvers, err := provider.GetApprovers(ctx, "tenant1")
	if err != nil {
		t.Fatalf("GetApprovers failed: %v", err)
	}

	if len(approvers) != 2 {
		t.Fatalf("expected 2 approvers, got %d", len(approvers))
	}

	// Verify alice
	if approvers[0].Username != "alice" {
		t.Errorf("expected username 'alice', got %s", approvers[0].Username)
	}
	if approvers[0].Priority != 1 {
		t.Errorf("expected priority 1, got %d", approvers[0].Priority)
	}
	if approvers[0].UserID != "101" {
		t.Errorf("expected mapped user ID '101', got %s", approvers[0].UserID)
	}
}

func TestCasdoorApprovalProvider_AddApprover(t *testing.T) {
	getUserCalled := false
	updateUserCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get-user" {
			getUserCalled = true
			user := casdoorUser{
				Name:        "alice",
				DisplayName: "Alice Admin",
				Email:       "alice@example.com",
				Type:        "normal-user",
				Properties:  make(map[string]string),
			}
			json.NewEncoder(w).Encode(user)
		} else if r.URL.Path == "/api/update-user" {
			updateUserCalled = true
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
		}
	}))
	defer server.Close()

	client := server.Client()

	provider := NewCasdoorApprovalProvider(client, server.URL, "test", "secret", nil, "test-org", "test-app")
	ctx := context.Background()

	approver := &authplugin.Approver{
		UserID:      "101",
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

	if !getUserCalled {
		t.Error("expected get-user endpoint to be called")
	}
	if !updateUserCalled {
		t.Error("expected update-user endpoint to be called")
	}
}

func TestCasdoorApprovalProvider_GetRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/get-organization" {
			ruleJSON := `{"name":"high_cost","description":"High cost rule","priority":10,"enabled":true}`
			org := casdoorOrganization{
				Owner: "test-org",
				Name:  "tenant1",
				Tags: []string{
					fmt.Sprintf("approval_rule=%s", ruleJSON),
				},
			}
			json.NewEncoder(w).Encode(org)
		}
	}))
	defer server.Close()

	client := server.Client()

	provider := NewCasdoorApprovalProvider(client, server.URL, "test", "secret", nil, "test-org", "test-app")
	ctx := context.Background()

	rules, err := provider.GetRules(ctx, "tenant1")
	if err != nil {
		t.Fatalf("GetRules failed: %v", err)
	}

	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}

	if rules[0].Name != "high_cost" {
		t.Errorf("expected rule name 'high_cost', got %s", rules[0].Name)
	}
	if rules[0].Priority != 10 {
		t.Errorf("expected priority 10, got %d", rules[0].Priority)
	}
}

func TestCasdoorApprovalProvider_SupportsFeature(t *testing.T) {
	provider := &CasdoorApprovalProvider{}

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

func TestCasdoorApprovalProvider_NilClient(t *testing.T) {
	provider := &CasdoorApprovalProvider{
		client: nil,
	}
	ctx := context.Background()

	_, err := provider.GetConfig(ctx, "tenant1")
	if err != authplugin.ErrProviderUnavailable {
		t.Errorf("expected ErrProviderUnavailable, got %v", err)
	}

	err = provider.UpdateConfig(ctx, "tenant1", &authplugin.ApprovalConfig{})
	if err != authplugin.ErrProviderUnavailable {
		t.Errorf("expected ErrProviderUnavailable, got %v", err)
	}
}

// mockIDMapper for testing
type mockIDMapper struct {
	casdoorToLocalMap map[string]int
	localToCasdoorMap map[int]string
}

// MapOrCreate 实现生产用到的映射能力：命中则复用，未命中则登记一条新映射。
func (m *mockIDMapper) MapOrCreate(ctx context.Context, provider, externalID, tenantID, username, displayName, email string) (int, error) {
	if id, ok := m.casdoorToLocalMap[externalID]; ok {
		return id, nil
	}
	id := 100 + len(m.casdoorToLocalMap)
	m.casdoorToLocalMap[externalID] = id
	m.localToCasdoorMap[id] = externalID
	return id, nil
}

// GetByInternalID 反查：本地 ID → (provider, externalID, tenantID)。
func (m *mockIDMapper) GetByInternalID(ctx context.Context, internalID int) (provider, externalID, tenantID string, err error) {
	for name, id := range m.casdoorToLocalMap {
		if id == internalID {
			return "casdoor", name, "tenant1", nil
		}
	}
	return "", "", "", fmt.Errorf("mapping not found for internal id %d", internalID)
}
