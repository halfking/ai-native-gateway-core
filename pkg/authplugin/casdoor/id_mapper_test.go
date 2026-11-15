package casdoor

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIDMapper_MapOrCreate_NewMapping(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: no existing mapping
	mock.ExpectQuery(`SELECT internal_id FROM auth_id_mapping`).
		WithArgs("casdoor", "uuid-123", "default").
		WillReturnError(pgx.ErrNoRows)

	// Mock: create new mapping
	mock.ExpectQuery(`INSERT INTO auth_id_mapping`).
		WithArgs("uuid-123", "casdoor", "default", "testuser", "Test User", "test@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"internal_id"}).AddRow(42))

	// Execute
	internalID, err := mapper.MapOrCreate(ctx, "casdoor", "uuid-123", "default", "testuser", "Test User", "test@example.com")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, 42, internalID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_MapOrCreate_ExistingMapping(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: existing mapping found
	mock.ExpectQuery(`SELECT internal_id FROM auth_id_mapping`).
		WithArgs("casdoor", "uuid-123", "default").
		WillReturnRows(pgxmock.NewRows([]string{"internal_id"}).AddRow(42))

	// Mock: update cached fields
	mock.ExpectExec(`UPDATE auth_id_mapping`).
		WithArgs(42, "casdoor", "uuid-123", "Test User Updated", "newemail@example.com", "testuser").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Execute
	internalID, err := mapper.MapOrCreate(ctx, "casdoor", "uuid-123", "default", "testuser", "Test User Updated", "newemail@example.com")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, 42, internalID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_MapOrCreate_RequiredFields(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		externalID string
		tenantID   string
		wantErr    bool
	}{
		{"missing provider", "", "uuid-123", "default", true},
		{"missing externalID", "casdoor", "", "default", true},
		{"missing tenantID", "casdoor", "uuid-123", "", true},
		{"all present", "casdoor", "uuid-123", "default", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			mapper := NewIDMapper(mock)
			ctx := context.Background()

			if !tt.wantErr {
				// Setup mocks for valid case
				mock.ExpectQuery(`SELECT internal_id FROM auth_id_mapping`).
					WithArgs(tt.provider, tt.externalID, tt.tenantID).
					WillReturnError(pgx.ErrNoRows)
				mock.ExpectQuery(`INSERT INTO auth_id_mapping`).
					WithArgs(tt.externalID, tt.provider, tt.tenantID, "user", "User", "email@test.com").
					WillReturnRows(pgxmock.NewRows([]string{"internal_id"}).AddRow(1))
			}

			_, err = mapper.MapOrCreate(ctx, tt.provider, tt.externalID, tt.tenantID, "user", "User", "email@test.com")

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NoError(t, mock.ExpectationsWereMet())
			}
		})
	}
}

func TestIDMapper_GetByInternalID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: found
	mock.ExpectQuery(`SELECT provider, external_id, tenant_id`).
		WithArgs(42).
		WillReturnRows(pgxmock.NewRows([]string{"provider", "external_id", "tenant_id"}).
			AddRow("casdoor", "uuid-123", "default"))

	// Execute
	provider, externalID, tenantID, err := mapper.GetByInternalID(ctx, 42)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "casdoor", provider)
	assert.Equal(t, "uuid-123", externalID)
	assert.Equal(t, "default", tenantID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_GetByInternalID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: not found
	mock.ExpectQuery(`SELECT provider, external_id, tenant_id`).
		WithArgs(999).
		WillReturnError(pgx.ErrNoRows)

	// Execute
	_, _, _, err = mapper.GetByInternalID(ctx, 999)

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mapping not found")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_GetByExternalID(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: found
	mock.ExpectQuery(`SELECT internal_id FROM auth_id_mapping`).
		WithArgs("casdoor", "uuid-123", "default").
		WillReturnRows(pgxmock.NewRows([]string{"internal_id"}).AddRow(42))

	// Execute
	internalID, err := mapper.GetByExternalID(ctx, "casdoor", "uuid-123", "default")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, 42, internalID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_GetByExternalID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: not found
	mock.ExpectQuery(`SELECT internal_id FROM auth_id_mapping`).
		WithArgs("casdoor", "nonexistent", "default").
		WillReturnError(pgx.ErrNoRows)

	// Execute
	_, err = mapper.GetByExternalID(ctx, "casdoor", "nonexistent", "default")

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mapping not found")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_GetByUsername(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: found
	mock.ExpectQuery(`SELECT internal_id, external_id FROM auth_id_mapping`).
		WithArgs("casdoor", "testuser", "default").
		WillReturnRows(pgxmock.NewRows([]string{"internal_id", "external_id"}).
			AddRow(42, "uuid-123"))

	// Execute
	internalID, externalID, err := mapper.GetByUsername(ctx, "casdoor", "testuser", "default")

	// Assert
	require.NoError(t, err)
	assert.Equal(t, 42, internalID)
	assert.Equal(t, "uuid-123", externalID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_Delete(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: successful delete
	mock.ExpectExec(`DELETE FROM auth_id_mapping WHERE internal_id`).
		WithArgs(42).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	// Execute
	err = mapper.Delete(ctx, 42)

	// Assert
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_Delete_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: no rows affected
	mock.ExpectExec(`DELETE FROM auth_id_mapping WHERE internal_id`).
		WithArgs(999).
		WillReturnResult(pgxmock.NewResult("DELETE", 0))

	// Execute
	err = mapper.Delete(ctx, 999)

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mapping not found")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_DeleteByExternal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock: successful delete
	mock.ExpectExec(`DELETE FROM auth_id_mapping`).
		WithArgs("casdoor", "uuid-123", "default").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	// Execute
	err = mapper.DeleteByExternal(ctx, "casdoor", "uuid-123", "default")

	// Assert
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_ListByProvider(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	now := time.Now()

	// Mock: list mappings
	mock.ExpectQuery(`SELECT internal_id, external_id, username, display_name, email, created_at, last_sync_at`).
		WithArgs("casdoor", "default", 100).
		WillReturnRows(pgxmock.NewRows([]string{
			"internal_id", "external_id", "username", "display_name", "email", "created_at", "last_sync_at",
		}).
			AddRow(1, "uuid-1", "user1", "User One", "user1@test.com", now, &now).
			AddRow(2, "uuid-2", "user2", "User Two", "user2@test.com", now, &now))

	// Execute
	mappings, err := mapper.ListByProvider(ctx, "casdoor", "default", 100)

	// Assert
	require.NoError(t, err)
	assert.Len(t, mappings, 2)
	assert.Equal(t, 1, mappings[0].InternalID)
	assert.Equal(t, "uuid-1", mappings[0].ExternalID)
	assert.Equal(t, "casdoor", mappings[0].Provider)
	assert.Equal(t, "default", mappings[0].TenantID)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIDMapper_ListByProvider_DefaultLimit(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mapper := NewIDMapper(mock)
	ctx := context.Background()

	// Mock with default limit
	mock.ExpectQuery(`SELECT internal_id, external_id, username, display_name, email, created_at, last_sync_at`).
		WithArgs("casdoor", "default", 100).
		WillReturnRows(pgxmock.NewRows([]string{
			"internal_id", "external_id", "username", "display_name", "email", "created_at", "last_sync_at",
		}))

	// Execute with 0 limit (should use default 100)
	_, err = mapper.ListByProvider(ctx, "casdoor", "default", 0)

	// Assert
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
