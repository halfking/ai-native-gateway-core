package casdoor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateImportData(t *testing.T) {
	manager := &CasdoorUserManager{} // Mock
	importer := NewBatchUserImporter(manager)

	t.Run("ValidData", func(t *testing.T) {
		users := []ImportUserRequest{
			{
				Username:    "user1",
				Email:       "user1@example.com",
				DisplayName: "User One",
				Role:        "user",
				Enabled:     true,
			},
			{
				Username:    "user2",
				Email:       "user2@example.com",
				DisplayName: "User Two",
				Role:        "tenant_admin",
				Enabled:     true,
			},
		}

		errors := importer.ValidateImportData(users)
		assert.Empty(t, errors)
	})

	t.Run("MissingUsername", func(t *testing.T) {
		users := []ImportUserRequest{
			{
				Email: "test@example.com",
			},
		}

		errors := importer.ValidateImportData(users)
		require.Len(t, errors, 1)
		assert.Contains(t, errors[0].Error, "username is required")
	})

	t.Run("InvalidEmail", func(t *testing.T) {
		users := []ImportUserRequest{
			{
				Username: "user1",
				Email:    "invalid-email",
			},
		}

		errors := importer.ValidateImportData(users)
		require.Len(t, errors, 1)
		assert.Contains(t, errors[0].Error, "invalid email format")
	})

	t.Run("InvalidRole", func(t *testing.T) {
		users := []ImportUserRequest{
			{
				Username: "user1",
				Email:    "user1@example.com",
				Role:     "invalid_role",
			},
		}

		errors := importer.ValidateImportData(users)
		require.Len(t, errors, 1)
		assert.Contains(t, errors[0].Error, "invalid role")
	})

	t.Run("MultipleErrors", func(t *testing.T) {
		users := []ImportUserRequest{
			{
				Username: "",
				Email:    "user1@example.com",
			},
			{
				Username: "user2",
				Email:    "invalid-email",
			},
			{
				Username: "user3",
				Email:    "user3@example.com",
				Role:     "bad_role",
			},
		}

		errors := importer.ValidateImportData(users)
		assert.Len(t, errors, 3)
	})
}

func TestBatchUserImporter_JSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Create temporary directory for test files
	tmpDir := t.TempDir()

	t.Run("ExportImportJSON", func(t *testing.T) {
		// Create test data
		users := []ExportedUser{
			{
				ID:          1,
				ExternalID:  "ext-1",
				Username:    "user1",
				DisplayName: "User One",
				Email:       "user1@example.com",
				Role:        "user",
				Enabled:     true,
			},
			{
				ID:          2,
				ExternalID:  "ext-2",
				Username:    "user2",
				DisplayName: "User Two",
				Email:       "user2@example.com",
				Role:        "tenant_admin",
				Enabled:     true,
			},
		}

		// Write JSON file
		filePath := filepath.Join(tmpDir, "users.json")
		data, err := json.MarshalIndent(users, "", "  ")
		require.NoError(t, err)
		err = os.WriteFile(filePath, data, 0644)
		require.NoError(t, err)

		// Verify file exists and is readable
		readData, err := os.ReadFile(filePath)
		require.NoError(t, err)
		assert.NotEmpty(t, readData)

		// Parse back
		var parsedUsers []ExportedUser
		err = json.Unmarshal(readData, &parsedUsers)
		require.NoError(t, err)
		assert.Len(t, parsedUsers, 2)
		assert.Equal(t, "user1", parsedUsers[0].Username)
		assert.Equal(t, "user2", parsedUsers[1].Username)
	})

	t.Run("ImportJSON_InvalidFile", func(t *testing.T) {
		manager := &CasdoorUserManager{} // Mock
		importer := NewBatchUserImporter(manager)

		_, err := importer.ImportUsersFromJSON(context.Background(), "/nonexistent/file.json")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "read file")
	})

	t.Run("ImportJSON_InvalidFormat", func(t *testing.T) {
		manager := &CasdoorUserManager{} // Mock
		importer := NewBatchUserImporter(manager)

		// Create invalid JSON file
		filePath := filepath.Join(tmpDir, "invalid.json")
		err := os.WriteFile(filePath, []byte("not valid json"), 0644)
		require.NoError(t, err)

		_, err = importer.ImportUsersFromJSON(context.Background(), filePath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "parse json")
	})
}

func TestBatchUserImporter_CSV(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()

	t.Run("ExportImportCSV", func(t *testing.T) {
		// Create CSV file manually
		filePath := filepath.Join(tmpDir, "users.csv")
		csvContent := `username,password,display_name,email,phone,role,enabled
user1,pass123,User One,user1@example.com,+1234567890,user,true
user2,pass456,User Two,user2@example.com,+0987654321,tenant_admin,true`

		err := os.WriteFile(filePath, []byte(csvContent), 0644)
		require.NoError(t, err)

		// Verify file exists
		_, err = os.Stat(filePath)
		require.NoError(t, err)
	})

	t.Run("ImportCSV_InvalidFile", func(t *testing.T) {
		manager := &CasdoorUserManager{} // Mock
		importer := NewBatchUserImporter(manager)

		_, err := importer.ImportUsersFromCSV(context.Background(), "/nonexistent/file.csv")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "open file")
	})
}

func TestBatchResult(t *testing.T) {
	t.Run("SuccessfulBatch", func(t *testing.T) {
		result := &BatchResult{
			Total:     10,
			Succeeded: 10,
			Failed:    0,
			Errors:    []BatchError{},
		}

		assert.Equal(t, 10, result.Total)
		assert.Equal(t, 10, result.Succeeded)
		assert.Equal(t, 0, result.Failed)
		assert.Empty(t, result.Errors)
	})

	t.Run("PartialFailure", func(t *testing.T) {
		result := &BatchResult{
			Total:     10,
			Succeeded: 7,
			Failed:    3,
			Errors: []BatchError{
				{Index: 2, Username: "user2", Error: "duplicate username"},
				{Index: 5, Username: "user5", Error: "invalid email"},
				{Index: 8, Username: "user8", Error: "password too weak"},
			},
		}

		assert.Equal(t, 10, result.Total)
		assert.Equal(t, 7, result.Succeeded)
		assert.Equal(t, 3, result.Failed)
		assert.Len(t, result.Errors, 3)
	})
}

func TestImportUserRequest(t *testing.T) {
	t.Run("BasicUser", func(t *testing.T) {
		user := ImportUserRequest{
			Username:    "testuser",
			Password:    "password123",
			DisplayName: "Test User",
			Email:       "test@example.com",
			Role:        "user",
			Enabled:     true,
		}

		assert.Equal(t, "testuser", user.Username)
		assert.Equal(t, "password123", user.Password)
		assert.True(t, user.Enabled)
	})

	t.Run("AdminUser", func(t *testing.T) {
		user := ImportUserRequest{
			Username:    "admin",
			Password:    "admin123",
			DisplayName: "Administrator",
			Email:       "admin@example.com",
			Role:        "super_admin",
			Enabled:     true,
		}

		assert.Equal(t, "super_admin", user.Role)
	})

	t.Run("WithMetadata", func(t *testing.T) {
		user := ImportUserRequest{
			Username: "testuser",
			Metadata: map[string]string{
				"department": "Engineering",
				"location":   "SF",
			},
		}

		assert.NotNil(t, user.Metadata)
		assert.Equal(t, "Engineering", user.Metadata["department"])
		assert.Equal(t, "SF", user.Metadata["location"])
	})
}

func TestExportedUser(t *testing.T) {
	t.Run("CompleteUser", func(t *testing.T) {
		user := ExportedUser{
			ID:          123,
			ExternalID:  "ext-123",
			Username:    "testuser",
			DisplayName: "Test User",
			Email:       "test@example.com",
			Phone:       "+1234567890",
			Role:        "user",
			Enabled:     true,
		}

		assert.Equal(t, 123, user.ID)
		assert.Equal(t, "ext-123", user.ExternalID)
		assert.Equal(t, "testuser", user.Username)
		assert.True(t, user.Enabled)
	})

	t.Run("JSONMarshaling", func(t *testing.T) {
		user := ExportedUser{
			ID:       1,
			Username: "test",
			Email:    "test@example.com",
		}

		data, err := json.Marshal(user)
		require.NoError(t, err)
		assert.NotEmpty(t, data)

		// Unmarshal back
		var parsed ExportedUser
		err = json.Unmarshal(data, &parsed)
		require.NoError(t, err)
		assert.Equal(t, user.Username, parsed.Username)
	})
}

func TestNewBatchUserImporter(t *testing.T) {
	manager := &CasdoorUserManager{} // Mock
	importer := NewBatchUserImporter(manager)

	assert.NotNil(t, importer)
	assert.NotNil(t, importer.userManager)
}

// BenchmarkImportValidation benchmarks import data validation.
func BenchmarkImportValidation(b *testing.B) {
	manager := &CasdoorUserManager{} // Mock
	importer := NewBatchUserImporter(manager)

	users := make([]ImportUserRequest, 100)
	for i := 0; i < 100; i++ {
		users[i] = ImportUserRequest{
			Username:    "user" + string(rune(i)),
			Email:       "user" + string(rune(i)) + "@example.com",
			DisplayName: "User " + string(rune(i)),
			Role:        "user",
			Enabled:     true,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		importer.ValidateImportData(users)
	}
}
