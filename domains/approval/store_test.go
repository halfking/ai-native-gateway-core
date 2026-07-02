package approval

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDB holds the test database connection.
// In a real implementation, you would use testcontainers to spin up PostgreSQL.
// For now, we'll implement the structure and you can integrate testcontainers later.
var testDB *pgxpool.Pool

func setupTestDB(t *testing.T) *pgxpool.Pool {
	// TODO: Use testcontainers to spin up PostgreSQL
	// For now, skip if no test DB is available
	if testDB == nil {
		t.Skip("Test database not available. Set up testcontainers for integration tests.")
	}
	return testDB
}

func cleanupApprovalTables(t *testing.T, pool *pgxpool.Pool) {
	ctx := context.Background()
	_, err := pool.Exec(ctx, "DELETE FROM approval_requests")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM approval_rules")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM approval_approvers")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM approval_configs")
	require.NoError(t, err)
}

func TestPGApprovalStore_CreateRequest(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	req := &ApprovalRequest{
		RequestID:     uuid.New().String(),
		SessionID:     uuid.New().String(),
		TenantID:      "test-tenant",
		TriggerType:   TriggerSensitiveContent,
		TriggerReason: "Detected PII in message",
		RiskLevel:     RiskHigh,
		SessionSummary: SessionSummary{
			MessageCount: 5,
			TotalTokens:  1000,
			Duration:     "5m",
		},
		SensitiveInfo: []SensitiveItemSummary{
			{
				Type:       "PII",
				Content:    "***-**-1234",
				Location:   "message[0].content",
				Confidence: 0.95,
			},
		},
		UserMessage:     "My SSN is 123-45-6789",
		EstimatedCost:   0.05,
		EstimatedTokens: 500,
		Status:          StatusPending,
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(1 * time.Hour),
		Metadata: map[string]interface{}{
			"source": "api",
		},
	}

	err := store.CreateRequest(ctx, req)
	require.NoError(t, err)

	// Verify it was created
	retrieved, err := store.GetRequest(ctx, req.RequestID)
	require.NoError(t, err)
	assert.Equal(t, req.RequestID, retrieved.RequestID)
	assert.Equal(t, req.SessionID, retrieved.SessionID)
	assert.Equal(t, req.TenantID, retrieved.TenantID)
	assert.Equal(t, req.TriggerType, retrieved.TriggerType)
	assert.Equal(t, req.RiskLevel, retrieved.RiskLevel)
	assert.Equal(t, req.Status, retrieved.Status)
	assert.Equal(t, req.SessionSummary.MessageCount, retrieved.SessionSummary.MessageCount)
	assert.Len(t, retrieved.SensitiveInfo, 1)
}

func TestPGApprovalStore_CreateRequest_DuplicateID(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	requestID := uuid.New().String()
	req := &ApprovalRequest{
		RequestID:     requestID,
		SessionID:     uuid.New().String(),
		TenantID:      "test-tenant",
		TriggerType:   TriggerHighCost,
		TriggerReason: "Cost exceeds threshold",
		RiskLevel:     RiskMedium,
		Status:        StatusPending,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
	}

	err := store.CreateRequest(ctx, req)
	require.NoError(t, err)

	// Try to create again with same ID
	req2 := *req
	req2.SessionID = uuid.New().String()
	err = store.CreateRequest(ctx, &req2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "request_id already exists")
}

func TestPGApprovalStore_GetRequest_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	_, err := store.GetRequest(ctx, "non-existent-id")
	assert.ErrorIs(t, err, ErrRequestNotFound)
}

func TestPGApprovalStore_UpdateRequest(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	// Create initial request
	req := &ApprovalRequest{
		RequestID:     uuid.New().String(),
		SessionID:     uuid.New().String(),
		TenantID:      "test-tenant",
		TriggerType:   TriggerToolCall,
		TriggerReason: "Tool call requires approval",
		RiskLevel:     RiskMedium,
		Status:        StatusPending,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
	}

	err := store.CreateRequest(ctx, req)
	require.NoError(t, err)

	// Update to approved
	req.Status = StatusApproved
	req.ApprovedBy = "admin@example.com"
	req.ApprovedAt = time.Now()
	req.ApprovalNote = "Looks good"

	err = store.UpdateRequest(ctx, req)
	require.NoError(t, err)

	// Verify update
	retrieved, err := store.GetRequest(ctx, req.RequestID)
	require.NoError(t, err)
	assert.Equal(t, StatusApproved, retrieved.Status)
	assert.Equal(t, "admin@example.com", retrieved.ApprovedBy)
	assert.Equal(t, "Looks good", retrieved.ApprovalNote)
	assert.False(t, retrieved.ApprovedAt.IsZero())
}

func TestPGApprovalStore_UpdateRequest_NotFound(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	req := &ApprovalRequest{
		RequestID: "non-existent-id",
		Status:    StatusApproved,
	}

	err := store.UpdateRequest(ctx, req)
	assert.ErrorIs(t, err, ErrRequestNotFound)
}

func TestPGApprovalStore_ListRequests(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	tenantID := "test-tenant"

	// Create multiple requests
	for i := 0; i < 5; i++ {
		status := StatusPending
		riskLevel := RiskMedium
		if i%2 == 0 {
			status = StatusApproved
			riskLevel = RiskHigh
		}

		req := &ApprovalRequest{
			RequestID:     uuid.New().String(),
			SessionID:     uuid.New().String(),
			TenantID:      tenantID,
			TriggerType:   TriggerSensitiveContent,
			TriggerReason: "Test request",
			RiskLevel:     riskLevel,
			Status:        status,
			CreatedAt:     time.Now().Add(time.Duration(-i) * time.Minute),
			ExpiresAt:     time.Now().Add(1 * time.Hour),
		}

		err := store.CreateRequest(ctx, req)
		require.NoError(t, err)
	}

	// List all for tenant
	requests, err := store.ListRequests(ctx, ApprovalFilter{
		TenantID: tenantID,
		Limit:    10,
	})
	require.NoError(t, err)
	assert.Len(t, requests, 5)

	// List only pending
	requests, err = store.ListRequests(ctx, ApprovalFilter{
		TenantID: tenantID,
		Status:   StatusPending,
		Limit:    10,
	})
	require.NoError(t, err)
	assert.Len(t, requests, 2)
	for _, req := range requests {
		assert.Equal(t, StatusPending, req.Status)
	}

	// List only high risk
	requests, err = store.ListRequests(ctx, ApprovalFilter{
		TenantID:  tenantID,
		RiskLevel: RiskHigh,
		Limit:     10,
	})
	require.NoError(t, err)
	assert.Len(t, requests, 3)
	for _, req := range requests {
		assert.Equal(t, RiskHigh, req.RiskLevel)
	}

	// Test pagination
	requests, err = store.ListRequests(ctx, ApprovalFilter{
		TenantID: tenantID,
		Limit:    2,
	})
	require.NoError(t, err)
	assert.Len(t, requests, 2)

	requests, err = store.ListRequests(ctx, ApprovalFilter{
		TenantID: tenantID,
		Limit:    2,
		Offset:   2,
	})
	require.NoError(t, err)
	assert.Len(t, requests, 2)
}

func TestPGApprovalStore_SaveAndGetConfig(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	config := &ApprovalConfig{
		TenantID:            "test-tenant",
		Enabled:             true,
		Mode:                ModeAutomatic,
		TimeoutSeconds:      3600,
		AutoRejectOnTimeout: true,
		Approvers: []Approver{
			{
				UserID:   "user1",
				Name:     "Alice",
				Email:    "alice@example.com",
				Role:     "admin",
				Priority: 1,
				Enabled:  true,
			},
		},
		Channels: []NotificationChannel{
			{
				Type: ChannelEmail,
				Config: map[string]string{
					"smtp_host": "smtp.example.com",
				},
				Enabled: true,
			},
		},
		Rules: []ApprovalRule{
			{
				Name:     "High Cost",
				Enabled:  true,
				Priority: 10,
				Conditions: []RuleCondition{
					{
						Field:    "cost",
						Operator: "gt",
						Value:    "10.0",
					},
				},
				Action: RuleAction{
					Type:      "require_approval",
					RiskLevel: RiskHigh,
					Reason:    "Cost exceeds $10",
				},
			},
		},
	}

	err := store.SaveConfig(ctx, config)
	require.NoError(t, err)

	// Retrieve and verify
	retrieved, err := store.GetConfig(ctx, "test-tenant")
	require.NoError(t, err)
	assert.Equal(t, config.TenantID, retrieved.TenantID)
	assert.Equal(t, config.Enabled, retrieved.Enabled)
	assert.Equal(t, config.Mode, retrieved.Mode)
	assert.Len(t, retrieved.Approvers, 1)
	assert.Equal(t, "Alice", retrieved.Approvers[0].Name)
	assert.Len(t, retrieved.Channels, 1)
	assert.Equal(t, ChannelEmail, retrieved.Channels[0].Type)
	assert.Len(t, retrieved.Rules, 1)
	assert.Equal(t, "High Cost", retrieved.Rules[0].Name)
}

func TestPGApprovalStore_GetConfig_DefaultWhenNotExists(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	// Get config for non-existent tenant should return default
	config, err := store.GetConfig(ctx, "non-existent-tenant")
	require.NoError(t, err)
	assert.Equal(t, "non-existent-tenant", config.TenantID)
	assert.False(t, config.Enabled)
	assert.Equal(t, ModeDisabled, config.Mode)
}

func TestPGApprovalStore_SaveConfig_Update(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	// Create initial config
	config := &ApprovalConfig{
		TenantID:            "test-tenant",
		Enabled:             false,
		Mode:                ModeDisabled,
		TimeoutSeconds:      3600,
		AutoRejectOnTimeout: true,
	}

	err := store.SaveConfig(ctx, config)
	require.NoError(t, err)

	// Update config
	config.Enabled = true
	config.Mode = ModeAutomatic
	config.TimeoutSeconds = 7200

	err = store.SaveConfig(ctx, config)
	require.NoError(t, err)

	// Verify update
	retrieved, err := store.GetConfig(ctx, "test-tenant")
	require.NoError(t, err)
	assert.True(t, retrieved.Enabled)
	assert.Equal(t, ModeAutomatic, retrieved.Mode)
	assert.Equal(t, 7200, retrieved.TimeoutSeconds)
}

func TestPGApprovalStore_Approvers(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()
	tenantID := "test-tenant"

	// Save approvers
	approver1 := &Approver{
		UserID:   "user1",
		Name:     "Alice",
		Email:    "alice@example.com",
		Role:     "admin",
		Priority: 1,
		Enabled:  true,
	}
	err := store.SaveApprover(ctx, tenantID, approver1)
	require.NoError(t, err)

	approver2 := &Approver{
		UserID:   "user2",
		Name:     "Bob",
		Email:    "bob@example.com",
		Role:     "auditor",
		Priority: 2,
		Enabled:  true,
	}
	err = store.SaveApprover(ctx, tenantID, approver2)
	require.NoError(t, err)

	// Get approvers
	approvers, err := store.GetApprovers(ctx, tenantID)
	require.NoError(t, err)
	assert.Len(t, approvers, 2)
	assert.Equal(t, "Alice", approvers[0].Name) // Should be sorted by priority

	// Update approver
	approver1.Name = "Alice Updated"
	err = store.SaveApprover(ctx, tenantID, approver1)
	require.NoError(t, err)

	approvers, err = store.GetApprovers(ctx, tenantID)
	require.NoError(t, err)
	assert.Equal(t, "Alice Updated", approvers[0].Name)

	// Delete approver
	err = store.DeleteApprover(ctx, tenantID, "user1")
	require.NoError(t, err)

	approvers, err = store.GetApprovers(ctx, tenantID)
	require.NoError(t, err)
	assert.Len(t, approvers, 1)
	assert.Equal(t, "Bob", approvers[0].Name)
}

func TestPGApprovalStore_Rules(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()
	tenantID := "test-tenant"

	// Save rules
	rule1 := &ApprovalRule{
		Name:     "High Cost",
		Enabled:  true,
		Priority: 10,
		Conditions: []RuleCondition{
			{Field: "cost", Operator: "gt", Value: "10.0"},
		},
		Action: RuleAction{
			Type:      "require_approval",
			RiskLevel: RiskHigh,
			Reason:    "Cost exceeds $10",
		},
	}
	err := store.SaveRule(ctx, tenantID, rule1)
	require.NoError(t, err)

	rule2 := &ApprovalRule{
		Name:     "Sensitive Content",
		Enabled:  true,
		Priority: 20,
		Conditions: []RuleCondition{
			{Field: "message_content", Operator: "contains", Value: "password"},
		},
		Action: RuleAction{
			Type:      "require_approval",
			RiskLevel: RiskCritical,
			Reason:    "Contains sensitive keyword",
		},
	}
	err = store.SaveRule(ctx, tenantID, rule2)
	require.NoError(t, err)

	// Get rules
	rules, err := store.GetRules(ctx, tenantID)
	require.NoError(t, err)
	assert.Len(t, rules, 2)
	assert.Equal(t, "Sensitive Content", rules[0].Name) // Should be sorted by priority DESC

	// Update rule
	rule1.Priority = 30
	err = store.SaveRule(ctx, tenantID, rule1)
	require.NoError(t, err)

	rules, err = store.GetRules(ctx, tenantID)
	require.NoError(t, err)
	assert.Equal(t, "High Cost", rules[0].Name) // Now highest priority

	// Delete rule
	err = store.DeleteRule(ctx, tenantID, "High Cost")
	require.NoError(t, err)

	rules, err = store.GetRules(ctx, tenantID)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, "Sensitive Content", rules[0].Name)
}

func TestPGApprovalStore_ConcurrentUpdates(t *testing.T) {
	pool := setupTestDB(t)
	defer cleanupApprovalTables(t, pool)

	store := NewPGApprovalStore(pool, nil)
	ctx := context.Background()

	// Create request
	req := &ApprovalRequest{
		RequestID:     uuid.New().String(),
		SessionID:     uuid.New().String(),
		TenantID:      "test-tenant",
		TriggerType:   TriggerHighCost,
		TriggerReason: "Test concurrent updates",
		RiskLevel:     RiskMedium,
		Status:        StatusPending,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
	}

	err := store.CreateRequest(ctx, req)
	require.NoError(t, err)

	// Simulate concurrent approval attempts
	done := make(chan bool, 2)
	errors := make(chan error, 2)

	for i := 0; i < 2; i++ {
		go func(approver string) {
			req.Status = StatusApproved
			req.ApprovedBy = approver
			req.ApprovedAt = time.Now()
			err := store.UpdateRequest(ctx, req)
			if err != nil {
				errors <- err
			}
			done <- true
		}(fmt.Sprintf("approver%d", i))
	}

	// Wait for both goroutines
	<-done
	<-done

	// Both should succeed (last write wins in this simple implementation)
	// In a production system, you'd want optimistic locking
	assert.Len(t, errors, 0)

	// Verify final state
	retrieved, err := store.GetRequest(ctx, req.RequestID)
	require.NoError(t, err)
	assert.Equal(t, StatusApproved, retrieved.Status)
}

func TestApprovalStatus_String(t *testing.T) {
	tests := []struct {
		status   ApprovalStatus
		expected string
	}{
		{StatusPending, "pending"},
		{StatusApproved, "approved"},
		{StatusRejected, "rejected"},
		{StatusTimeout, "timeout"},
		{StatusCanceled, "canceled"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, string(tt.status))
	}
}

func TestRiskLevel_String(t *testing.T) {
	tests := []struct {
		level    RiskLevel
		expected string
	}{
		{RiskLow, "LOW"},
		{RiskMedium, "MEDIUM"},
		{RiskHigh, "HIGH"},
		{RiskCritical, "CRITICAL"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, string(tt.level))
	}
}
