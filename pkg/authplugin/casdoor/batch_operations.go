package casdoor

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

// BatchUserImporter provides batch import/export operations for users.
type BatchUserImporter struct {
	userManager *CasdoorUserManager
}

// NewBatchUserImporter creates a new batch user importer.
func NewBatchUserImporter(userManager *CasdoorUserManager) *BatchUserImporter {
	return &BatchUserImporter{
		userManager: userManager,
	}
}

// ImportUserRequest represents a user to import.
type ImportUserRequest struct {
	Username    string            `json:"username"`
	Password    string            `json:"password,omitempty"`
	DisplayName string            `json:"display_name,omitempty"`
	Email       string            `json:"email,omitempty"`
	Phone       string            `json:"phone,omitempty"`
	Role        string            `json:"role,omitempty"`
	Enabled     bool              `json:"enabled"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// ExportedUser represents an exported user.
type ExportedUser struct {
	ID          int               `json:"id"`
	ExternalID  string            `json:"external_id"`
	Username    string            `json:"username"`
	DisplayName string            `json:"display_name,omitempty"`
	Email       string            `json:"email,omitempty"`
	Phone       string            `json:"phone,omitempty"`
	Role        string            `json:"role"`
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// BatchResult represents the result of a batch operation.
type BatchResult struct {
	Total     int          `json:"total"`
	Succeeded int          `json:"succeeded"`
	Failed    int          `json:"failed"`
	Errors    []BatchError `json:"errors,omitempty"`
	Duration  time.Duration `json:"duration"`
}

// BatchError represents an error that occurred during batch processing.
type BatchError struct {
	Index    int    `json:"index"`
	Username string `json:"username,omitempty"`
	Error    string `json:"error"`
}

// ImportUsers imports multiple users from a slice.
// Returns detailed results including success/failure counts and errors.
func (b *BatchUserImporter) ImportUsers(ctx context.Context, users []ImportUserRequest) (*BatchResult, error) {
	startTime := time.Now()
	result := &BatchResult{
		Total:  len(users),
		Errors: make([]BatchError, 0),
	}

	for i, userReq := range users {
		// Create user via user manager
		createReq := &authplugin.CreateUserRequest{
			Username:    userReq.Username,
			Password:    userReq.Password,
			DisplayName: userReq.DisplayName,
			Email:       userReq.Email,
			Role:        userReq.Role,
		}

		// Set default role if not specified
		if createReq.Role == "" {
			createReq.Role = "user"
		}

		_, err := b.userManager.CreateUser(ctx, createReq)
		if err != nil {
			result.Failed++
			result.Errors = append(result.Errors, BatchError{
				Index:    i,
				Username: userReq.Username,
				Error:    err.Error(),
			})
			continue
		}

		result.Succeeded++
	}

	result.Duration = time.Since(startTime)
	return result, nil
}

// ImportUsersFromJSON imports users from a JSON file.
func (b *BatchUserImporter) ImportUsersFromJSON(ctx context.Context, filePath string) (*BatchResult, error) {
	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	// Parse JSON
	var users []ImportUserRequest
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	// Import users
	return b.ImportUsers(ctx, users)
}

// ImportUsersFromCSV imports users from a CSV file.
// CSV format: username,password,display_name,email,phone,role,enabled
func (b *BatchUserImporter) ImportUsersFromCSV(ctx context.Context, filePath string) (*BatchResult, error) {
	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	// Read CSV
	reader := csv.NewReader(file)
	
	// Skip header
	if _, err := reader.Read(); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	// Parse records
	users := make([]ImportUserRequest, 0)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read record: %w", err)
		}

		if len(record) < 7 {
			continue // Skip invalid records
		}

		user := ImportUserRequest{
			Username:    record[0],
			Password:    record[1],
			DisplayName: record[2],
			Email:       record[3],
			Phone:       record[4],
			Role:        record[5],
			Enabled:     record[6] == "true",
		}

		users = append(users, user)
	}

	// Import users
	return b.ImportUsers(ctx, users)
}

// ExportUsers exports users to a slice.
func (b *BatchUserImporter) ExportUsers(ctx context.Context, filter *authplugin.UserFilter) ([]ExportedUser, error) {
	// List users from user manager
	users, err := b.userManager.ListUsers(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	// Convert to exported format
	exported := make([]ExportedUser, 0, len(users))
	for _, user := range users {
		// Convert metadata from map[string]interface{} to map[string]string
		metadata := make(map[string]string)
		for k, v := range user.Metadata {
			if str, ok := v.(string); ok {
				metadata[k] = str
			}
		}

		exported = append(exported, ExportedUser{
			ID:          user.ID,
			ExternalID:  user.ExternalID,
			Username:    user.Username,
			DisplayName: user.DisplayName,
			Email:       user.Email,
			Phone:       "", // Phone not available in User struct
			Role:        user.Role,
			Enabled:     user.Enabled,
			CreatedAt:   user.CreatedAt,
			UpdatedAt:   user.UpdatedAt,
			Metadata:    metadata,
		})
	}

	return exported, nil
}

// ExportUsersToJSON exports users to a JSON file.
func (b *BatchUserImporter) ExportUsersToJSON(ctx context.Context, filter *authplugin.UserFilter, filePath string) error {
	// Export users
	users, err := b.ExportUsers(ctx, filter)
	if err != nil {
		return err
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// ExportUsersToCSV exports users to a CSV file.
func (b *BatchUserImporter) ExportUsersToCSV(ctx context.Context, filter *authplugin.UserFilter, filePath string) error {
	// Export users
	users, err := b.ExportUsers(ctx, filter)
	if err != nil {
		return err
	}

	// Create file
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer file.Close()

	// Write CSV
	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	if err := writer.Write([]string{
		"id", "external_id", "username", "display_name", "email", "phone", "role", "enabled", "created_at", "updated_at",
	}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	// Write records
	for _, user := range users {
		record := []string{
			fmt.Sprintf("%d", user.ID),
			user.ExternalID,
			user.Username,
			user.DisplayName,
			user.Email,
			user.Phone,
			user.Role,
			fmt.Sprintf("%t", user.Enabled),
			user.CreatedAt.Format(time.RFC3339),
			user.UpdatedAt.Format(time.RFC3339),
		}

		if err := writer.Write(record); err != nil {
			return fmt.Errorf("write record: %w", err)
		}
	}

	return nil
}

// ValidateImportData validates import data before actual import.
func (b *BatchUserImporter) ValidateImportData(users []ImportUserRequest) []BatchError {
	errors := make([]BatchError, 0)

	for i, user := range users {
		// Validate username
		if user.Username == "" {
			errors = append(errors, BatchError{
				Index:    i,
				Username: user.Username,
				Error:    "username is required",
			})
			continue
		}

		// Validate email format (basic) - only if email is provided
		if user.Email != "" && !strings.Contains(user.Email, "@") {
			errors = append(errors, BatchError{
				Index:    i,
				Username: user.Username,
				Error:    "invalid email format",
			})
			continue
		}

		// Validate role
		validRoles := map[string]bool{
			"super_admin":  true,
			"tenant_admin": true,
			"user":         true,
		}
		if user.Role != "" && !validRoles[user.Role] {
			errors = append(errors, BatchError{
				Index:    i,
				Username: user.Username,
				Error:    fmt.Sprintf("invalid role: %s", user.Role),
			})
		}
	}

	return errors
}
