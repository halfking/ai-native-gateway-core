package native

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

func TestNativeTenantManager_Name(t *testing.T) {
	m := &NativeTenantManager{}
	if got := m.Name(); got != "native" {
		t.Errorf("Name() = %v, want %v", got, "native")
	}
}

func TestNativeTenantManager_SupportsFeature(t *testing.T) {
	m := &NativeTenantManager{}
	// Native manager provides core CRUD only
	if m.SupportsFeature(authplugin.FeatureTenantHierarchy) {
		t.Error("SupportsFeature should returnfalsefor all features")
	}
}

func TestNativeTenantManager_CreateTenant_Validation(t *testing.T) {
	m := &NativeTenantManager{}
	ctx := context.Background()

	// Testempty ID
	_, err := m.CreateTenant(ctx, &authplugin.CreateTenantRequest{
		ID:  "",
		Name: "test",
	})
	if err == nil {
		t.Error("CreateTenant withempty ID should fail")
	}

	// Test empty Name
	_, err = m.CreateTenant(ctx, &authplugin.CreateTenantRequest{
		ID:   "test",
		Name: "",
	})
	if err == nil {
		t.Error("CreateTenant with empty Name should fail")
	}
}

func TestNativeTenantManager_UpdateTenant_NoChanges(t *testing.T) {
	//This would require database mocking
	t.Skip("Requires database connection")
}
