package native

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// Comprehensive mock-based tests for NativeTenantManager using pgxmock
// These tests target >90% code coverage without requiring a real database

func TestNewNativeTenantManager_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, err := NewNativeTenantManager(mock)
	require.NoError(t, err)
	assert.NotNil(t, mgr)
	assert.Equal(t, "native", mgr.Name())
}

func TestNewNativeTenantManager_NilDB(t *testing.T) {
	_, err := NewNativeTenantManager(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db must not be nil")
}

func TestTenantManager_GetTenant_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"tenant1", "Tenant One", "Tenant 1 Display", "active", time.Now(), time.Now(),
	)

	mock.ExpectQuery(`SELECT (.+) FROM tenants WHERE code`).
		WithArgs("tenant1").
		WillReturnRows(rows)

	tenant, err := mgr.GetTenant(ctx, "tenant1")
	require.NoError(t, err)
	assert.Equal(t, "tenant1", tenant.ID)
	assert.Equal(t, "Tenant One", tenant.Name)
	assert.Equal(t, "active", tenant.Status)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_GetTenant_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectQuery(`SELECT (.+) FROM tenants WHERE code`).
		WithArgs("nonexistent").
		WillReturnError(errors.New("no rows"))

	_, err = mgr.GetTenant(ctx, "nonexistent")
	assert.Equal(t, authplugin.ErrNotFound, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_ListTenants_NoFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"t1", "Tenant 1", "Display 1", "active", time.Now(), time.Now(),
	).AddRow(
		"t2", "Tenant 2", "Display 2", "disabled", time.Now(), time.Now(),
	)

	mock.ExpectQuery(`SELECT (.+) FROM tenants ORDER BY code`).
		WillReturnRows(rows)

	tenants, err := mgr.ListTenants(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, tenants, 2)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_ListTenants_WithStatusFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"t1", "Tenant 1", "Display 1", "active", time.Now(), time.Now(),
	)

	filter := &authplugin.TenantFilter{
		Status: "active",
	}

	mock.ExpectQuery(`SELECT (.+) FROM tenants WHERE`).
		WithArgs("active").
		WillReturnRows(rows)

	tenants, err := mgr.ListTenants(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, tenants, 1)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_ListTenants_WithSearchTerm(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"t1", "Acme Corp", "Acme", "active", time.Now(), time.Now(),
	)

	filter := &authplugin.TenantFilter{
		SearchTerm: "Acme",
	}

	mock.ExpectQuery(`SELECT (.+) FROM tenants WHERE`).
		WithArgs("%Acme%").
		WillReturnRows(rows)

	tenants, err := mgr.ListTenants(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, tenants, 1)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_ListTenants_WithLimitAndOffset(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"t1", "Test Tenant", "Test", "active", time.Now(), time.Now(),
	)

	filter := &authplugin.TenantFilter{
		Limit:  5,
		Offset: 10,
	}

	mock.ExpectQuery(`SELECT (.+) FROM tenants ORDER BY code LIMIT`).
		WithArgs(5, 10).
		WillReturnRows(rows)

	tenants, err := mgr.ListTenants(ctx, filter)
	require.NoError(t, err)
	assert.Len(t, tenants, 1)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_ListTenants_EmptyResult(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	})

	mock.ExpectQuery(`SELECT (.+) FROM tenants ORDER BY code`).
		WillReturnRows(rows)

	tenants, err := mgr.ListTenants(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, tenants, 0)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_CreateTenant_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"new-tenant", "New Tenant", "New Display", "active", time.Now(), time.Now(),
	)

	mock.ExpectQuery(`INSERT INTO tenants`).
		WithArgs("new-tenant", "New Tenant", "New Display").
		WillReturnRows(rows)

	req := &authplugin.CreateTenantRequest{
		ID:          "new-tenant",
		Name:        "New Tenant",
		DisplayName: "New Display",
	}

	tenant, err := mgr.CreateTenant(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "new-tenant", tenant.ID)
	assert.Equal(t, "active", tenant.Status)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_CreateTenant_WithDefaultDisplayName(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"tenant", "Internal Name", "Internal Name", "active", time.Now(), time.Now(),
	)

	mock.ExpectQuery(`INSERT INTO tenants`).
		WithArgs("tenant", "Internal Name", "Internal Name").
		WillReturnRows(rows)

	req := &authplugin.CreateTenantRequest{
		ID:   "tenant",
		Name: "Internal Name",
		// DisplayName omitted - should default to Name
	}

	tenant, err := mgr.CreateTenant(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "Internal Name", tenant.DisplayName)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_CreateTenant_DuplicateID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectQuery(`INSERT INTO tenants`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("duplicate key value violates unique constraint"))

	req := &authplugin.CreateTenantRequest{
		ID:   "duplicate",
		Name: "Duplicate",
	}

	_, err = mgr.CreateTenant(ctx, req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_UpdateTenant_Name(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"t1", "Updated Name", "Display", "active", time.Now(), time.Now(),
	)

	newName := "Updated Name"
	mock.ExpectQuery(`UPDATE tenants SET`).
		WithArgs(newName, "t1").
		WillReturnRows(rows)

	req := &authplugin.UpdateTenantRequest{
		Name: &newName,
	}

	tenant, err := mgr.UpdateTenant(ctx, "t1", req)
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", tenant.Name)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_UpdateTenant_DisplayName(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"t1", "Name", "Updated Display", "active", time.Now(), time.Now(),
	)

	newDisplay := "Updated Display"
	mock.ExpectQuery(`UPDATE tenants SET`).
		WithArgs(newDisplay, "t1").
		WillReturnRows(rows)

	req := &authplugin.UpdateTenantRequest{
		DisplayName: &newDisplay,
	}

	tenant, err := mgr.UpdateTenant(ctx, "t1", req)
	require.NoError(t, err)
	assert.Equal(t, "Updated Display", tenant.DisplayName)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_UpdateTenant_BothFields(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"t1", "New Name", "New Display", "active", time.Now(), time.Now(),
	)

	newName := "New Name"
	newDisplay := "New Display"
	mock.ExpectQuery(`UPDATE tenants SET`).
		WithArgs(newName, newDisplay, "t1").
		WillReturnRows(rows)

	req := &authplugin.UpdateTenantRequest{
		Name:        &newName,
		DisplayName: &newDisplay,
	}

	tenant, err := mgr.UpdateTenant(ctx, "t1", req)
	require.NoError(t, err)
	assert.Equal(t, "New Name", tenant.Name)
	assert.Equal(t, "New Display", tenant.DisplayName)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_UpdateTenant_NoChanges(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	rows := pgxmock.NewRows([]string{
		"code", "name", "display_name", "status", "created_at", "updated_at",
	}).AddRow(
		"t1", "Name", "Display", "active", time.Now(), time.Now(),
	)

	mock.ExpectQuery(`SELECT (.+) FROM tenants WHERE code`).
		WithArgs("t1").
		WillReturnRows(rows)

	req := &authplugin.UpdateTenantRequest{}
	tenant, err := mgr.UpdateTenant(ctx, "t1", req)
	require.NoError(t, err)
	assert.Equal(t, "t1", tenant.ID)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_UpdateTenant_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	newName := "Updated"
	mock.ExpectQuery(`UPDATE tenants SET`).
		WithArgs(newName, "nonexistent").
		WillReturnError(errors.New("no rows"))

	req := &authplugin.UpdateTenantRequest{
		Name: &newName,
	}

	_, err = mgr.UpdateTenant(ctx, "nonexistent", req)
	assert.Error(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_DeleteTenant_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`DELETE FROM tenants WHERE code`).
		WithArgs("t1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = mgr.DeleteTenant(ctx, "t1")
	assert.NoError(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_DeleteTenant_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`DELETE FROM tenants WHERE code`).
		WithArgs("nonexistent").
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	err = mgr.DeleteTenant(ctx, "nonexistent")
	assert.Equal(t, authplugin.ErrNotFound, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_EnableTenant_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE tenants SET status`).
		WithArgs("active", "t1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.EnableTenant(ctx, "t1")
	assert.NoError(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_EnableTenant_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE tenants SET status`).
		WithArgs("active", "nonexistent").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = mgr.EnableTenant(ctx, "nonexistent")
	assert.Equal(t, authplugin.ErrNotFound, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_DisableTenant_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE tenants SET status`).
		WithArgs("disabled", "t1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.DisableTenant(ctx, "t1")
	assert.NoError(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_DisableTenant_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE tenants SET status`).
		WithArgs("disabled", "nonexistent").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = mgr.DisableTenant(ctx, "nonexistent")
	assert.Equal(t, authplugin.ErrNotFound, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTenantManager_SetTenantStatus_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr, _ := NewNativeTenantManager(mock)
	ctx := context.Background()

	mock.ExpectExec(`UPDATE tenants SET status`).
		WithArgs("suspended", "t1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.setTenantStatus(ctx, "t1", "suspended")
	assert.NoError(t, err)

	assert.NoError(t, mock.ExpectationsWereMet())
}
