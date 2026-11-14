package native

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// Integration tests for NativeTenantManager that require a real database connection.
// Set TEST_DATABASE_URL or TEST_DB_URL to run these tests.

func setupTenantTestDB(t *testing.T) (*pgxpool.Pool, *NativeTenantManager) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set, skipping integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)

	mgr, err := NewNativeTenantManager(pool)
	if err != nil {
		t.Fatalf("NewNativeTenantManager: %v", err)
	}

	return pool, mgr
}

func cleanupTestTenant(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM tenants WHERE code = $1`, tenantID)
}

func TestNativeTenantManager_Integration_CreateAndGet(t *testing.T) {
	pool, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	tenantID := "test-tenant-" + time.Now().Format("20060102-150405")
	defer cleanupTestTenant(t, pool, tenantID)

	// Create tenant
	req := &authplugin.CreateTenantRequest{
		ID:          tenantID,
		Name:        "Test Tenant Internal",
		DisplayName: "Test Tenant Display",
		IsInternal:  false,
	}

	tenant, err := mgr.CreateTenant(ctx, req)
	if err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}

	if tenant.ID != tenantID {
		t.Errorf("Tenant ID = %v, want %v", tenant.ID, tenantID)
	}
	if tenant.Name != "Test Tenant Internal" {
		t.Errorf("Tenant Name = %v, want Test Tenant Internal", tenant.Name)
	}
	if tenant.DisplayName != "Test Tenant Display" {
		t.Errorf("DisplayName = %v, want Test Tenant Display", tenant.DisplayName)
	}
	if tenant.Status != "active" {
		t.Errorf("Status = %v, want active", tenant.Status)
	}

	// GetTenant
	retrieved, err := mgr.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant failed: %v", err)
	}
	if retrieved.ID != tenantID {
		t.Errorf("Retrieved tenant ID = %v, want %v", retrieved.ID, tenantID)
	}
	if retrieved.Name != tenant.Name {
		t.Errorf("Retrieved name = %v, want %v", retrieved.Name, tenant.Name)
	}
}

func TestNativeTenantManager_Integration_CreateDuplicate(t *testing.T) {
	pool, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	tenantID := "test-dup-tenant-" + time.Now().Format("20060102-150405")
	defer cleanupTestTenant(t, pool, tenantID)

	req := &authplugin.CreateTenantRequest{
		ID:   tenantID,
		Name: "Duplicate Test",
	}

	// Create first tenant
	_, err := mgr.CreateTenant(ctx, req)
	if err != nil {
		t.Fatalf("First CreateTenant failed: %v", err)
	}

	// Try to create duplicate
	_, err = mgr.CreateTenant(ctx, req)
	if err == nil {
		t.Error("CreateTenant with duplicate ID should fail")
	}
	// Should return ErrConflict or wrap it
	if err != nil {
		t.Logf("Duplicate error: %v", err)
	}
}

func TestNativeTenantManager_Integration_GetNonExistent(t *testing.T) {
	_, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	_, err := mgr.GetTenant(ctx, "non-existent-tenant-xyz")
	if err != authplugin.ErrNotFound {
		t.Errorf("GetTenant non-existent should return ErrNotFound, got %v", err)
	}
}

func TestNativeTenantManager_Integration_ListTenants(t *testing.T) {
	pool, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	// Create test tenants
	baseID := "test-list-tenant-" + time.Now().Format("20060102-150405")
	tenantIDs := make([]string, 0, 3)

	for i := 0; i < 3; i++ {
		tenantID := baseID + "-" + string(rune('a'+i))
		req := &authplugin.CreateTenantRequest{
			ID:          tenantID,
			Name:        "List Test " + string(rune('A'+i)),
			DisplayName: "Display " + string(rune('A'+i)),
		}
		_, err := mgr.CreateTenant(ctx, req)
		if err != nil {
			t.Fatalf("CreateTenant %d failed: %v", i, err)
		}
		tenantIDs = append(tenantIDs, tenantID)
		defer cleanupTestTenant(t, pool, tenantID)
	}

	// List all tenants (no filter)
	allTenants, err := mgr.ListTenants(ctx, nil)
	if err != nil {
		t.Fatalf("ListTenants failed: %v", err)
	}
	if len(allTenants) < 3 {
		t.Logf("ListTenants returned %d tenants (expected at least 3 from this test)", len(allTenants))
	}

	// List with search term
	searchFilter := &authplugin.TenantFilter{
		SearchTerm: baseID,
	}
	searchResults, err := mgr.ListTenants(ctx, searchFilter)
	if err != nil {
		t.Fatalf("ListTenants with search failed: %v", err)
	}
	if len(searchResults) < 3 {
		t.Errorf("Search returned %d tenants, want at least 3", len(searchResults))
	}

	// List with status filter
	statusFilter := &authplugin.TenantFilter{
		Status: "active",
	}
	activeResults, err := mgr.ListTenants(ctx, statusFilter)
	if err != nil {
		t.Fatalf("ListTenants with status filter failed: %v", err)
	}
	if len(activeResults) == 0 {
		t.Error("Should have at least some active tenants")
	}

	// List with limit
	limitFilter := &authplugin.TenantFilter{
		Limit: 2,
	}
	limitedResults, err := mgr.ListTenants(ctx, limitFilter)
	if err != nil {
		t.Fatalf("ListTenants with limit failed: %v", err)
	}
	if len(limitedResults) > 2 {
		t.Errorf("Limited search returned %d tenants, want max 2", len(limitedResults))
	}

	// List with offset
	offsetFilter := &authplugin.TenantFilter{
		Limit:  1,
		Offset: 1,
	}
	offsetResults, err := mgr.ListTenants(ctx, offsetFilter)
	if err != nil {
		t.Fatalf("ListTenants with offset failed: %v", err)
	}
	if len(offsetResults) > 1 {
		t.Errorf("Offset search returned %d tenants, want max 1", len(offsetResults))
	}
}

func TestNativeTenantManager_Integration_UpdateTenant(t *testing.T) {
	pool, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	tenantID := "test-update-tenant-" + time.Now().Format("20060102-150405")
	defer cleanupTestTenant(t, pool, tenantID)

	// Create tenant
	req := &authplugin.CreateTenantRequest{
		ID:          tenantID,
		Name:        "Original Name",
		DisplayName: "Original Display",
	}
	tenant, err := mgr.CreateTenant(ctx, req)
	if err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}

	// Update name
	newName := "Updated Name"
	updateReq := &authplugin.UpdateTenantRequest{
		Name: &newName,
	}
	updated, err := mgr.UpdateTenant(ctx, tenantID, updateReq)
	if err != nil {
		t.Fatalf("UpdateTenant failed: %v", err)
	}
	if updated.Name != newName {
		t.Errorf("Updated Name = %v, want %v", updated.Name, newName)
	}

	// Update display name
	newDisplayName := "Updated Display Name"
	displayReq := &authplugin.UpdateTenantRequest{
		DisplayName: &newDisplayName,
	}
	updated, err = mgr.UpdateTenant(ctx, tenantID, displayReq)
	if err != nil {
		t.Fatalf("UpdateTenant display name failed: %v", err)
	}
	if updated.DisplayName != newDisplayName {
		t.Errorf("Updated DisplayName = %v, want %v", updated.DisplayName, newDisplayName)
	}

	// Verify UpdatedAt changed
	if !updated.UpdatedAt.After(tenant.CreatedAt) {
		t.Error("UpdatedAt should be after CreatedAt")
	}
}

func TestNativeTenantManager_Integration_UpdateNonExistent(t *testing.T) {
	_, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	newName := "Some Name"
	updateReq := &authplugin.UpdateTenantRequest{
		Name: &newName,
	}
	_, err := mgr.UpdateTenant(ctx, "non-existent-tenant", updateReq)
	if err == nil {
		t.Error("UpdateTenant on non-existent tenant should fail")
	}
}

func TestNativeTenantManager_Integration_DeleteTenant(t *testing.T) {
	pool, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	tenantID := "test-delete-tenant-" + time.Now().Format("20060102-150405")
	defer cleanupTestTenant(t, pool, tenantID)

	// Create tenant
	req := &authplugin.CreateTenantRequest{
		ID:   tenantID,
		Name: "To Delete",
	}
	_, err := mgr.CreateTenant(ctx, req)
	if err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}

	// Delete tenant
	err = mgr.DeleteTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("DeleteTenant failed: %v", err)
	}

	// Verify tenant is deleted
	_, err = mgr.GetTenant(ctx, tenantID)
	if err != authplugin.ErrNotFound {
		t.Error("GetTenant on deleted tenant should return ErrNotFound")
	}
}

func TestNativeTenantManager_Integration_DeleteNonExistent(t *testing.T) {
	_, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	err := mgr.DeleteTenant(ctx, "non-existent-tenant-xyz")
	if err != authplugin.ErrNotFound {
		t.Errorf("DeleteTenant non-existent should return ErrNotFound, got %v", err)
	}
}

func TestNativeTenantManager_Integration_EnableDisableTenant(t *testing.T) {
	pool, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	tenantID := "test-enable-disable-" + time.Now().Format("20060102-150405")
	defer cleanupTestTenant(t, pool, tenantID)

	// Create tenant (starts as active)
	req := &authplugin.CreateTenantRequest{
		ID:   tenantID,
		Name: "Enable Disable Test",
	}
	tenant, err := mgr.CreateTenant(ctx, req)
	if err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}
	if tenant.Status != "active" {
		t.Errorf("New tenant status = %v, want active", tenant.Status)
	}

	// Disable tenant
	err = mgr.DisableTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("DisableTenant failed: %v", err)
	}

	// Verify status is disabled
	disabled, err := mgr.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant after disable failed: %v", err)
	}
	if disabled.Status != "disabled" {
		t.Errorf("Disabled tenant status = %v, want disabled", disabled.Status)
	}

	// Re-enable tenant
	err = mgr.EnableTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("EnableTenant failed: %v", err)
	}

	// Verify status is active
	enabled, err := mgr.GetTenant(ctx, tenantID)
	if err != nil {
		t.Fatalf("GetTenant after enable failed: %v", err)
	}
	if enabled.Status != "active" {
		t.Errorf("Enabled tenant status = %v, want active", enabled.Status)
	}
}

func TestNativeTenantManager_Integration_EnableDisableNonExistent(t *testing.T) {
	_, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	err := mgr.DisableTenant(ctx, "non-existent-tenant")
	if err != authplugin.ErrNotFound {
		t.Errorf("DisableTenant non-existent should return ErrNotFound, got %v", err)
	}

	err = mgr.EnableTenant(ctx, "non-existent-tenant")
	if err != authplugin.ErrNotFound {
		t.Errorf("EnableTenant non-existent should return ErrNotFound, got %v", err)
	}
}

func TestNativeTenantManager_Integration_CreateWithDefaultDisplayName(t *testing.T) {
	pool, mgr := setupTenantTestDB(t)
	ctx := context.Background()

	tenantID := "test-default-display-" + time.Now().Format("20060102-150405")
	defer cleanupTestTenant(t, pool, tenantID)

	// Create tenant without DisplayName
	req := &authplugin.CreateTenantRequest{
		ID:   tenantID,
		Name: "Internal Name",
		// DisplayName omitted
	}
	tenant, err := mgr.CreateTenant(ctx, req)
	if err != nil {
		t.Fatalf("CreateTenant failed: %v", err)
	}

	// DisplayName should default to Name
	if tenant.DisplayName != "Internal Name" {
		t.Errorf("DisplayName = %v, want Internal Name (defaulted from Name)", tenant.DisplayName)
	}
}
