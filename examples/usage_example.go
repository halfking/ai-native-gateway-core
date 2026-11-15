package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/approval"
)

// Example: Using the approval router in your application

// handleSensitiveRequest demonstrates how to create an approval when
// sensitive content is detected in a user request.
func handleSensitiveRequest(
	ctx context.Context,
	router *approval.Router,
	sessionID string,
	userID string,
	tenantID string,
	detectResult *SensitiveDetectionResult,
) error {
	// 1. Generate a unique request ID
	requestID := generateRequestID()
	
	// 2. Get Casdoor user ID (if using Casdoor auth integration)
	casdoorUserID := getCasdoorUserID(userID)
	
	// 3. Create approval request
	approvalReq := &approval.CreateApprovalRequest{
		RequestID:     requestID,
		SessionID:     sessionID,
		TenantID:      tenantID,
		UserID:        userID,
		CasdoorUserID: casdoorUserID,
		DetectResult: &approval.DetectResult{
			Triggers:         detectResult.Triggers,
			SensitivityScore: detectResult.Score,
			CrossDepartment:  detectResult.CrossDepartment,
			Summary:          detectResult.Summary,
			Metadata:         detectResult.Metadata,
		},
		ClientInfo: map[string]interface{}{
			"ip":         ctx.Value("client_ip"),
			"user_agent": ctx.Value("user_agent"),
		},
		CreatedAt: time.Now(),
	}
	
	// 4. Create approval via router (automatic L1/L2 routing)
	approvalRecord, err := router.CreateApproval(ctx, approvalReq)
	if err != nil {
		slog.Error("failed to create approval",
			"error", err,
			"request_id", requestID,
			"session_id", sessionID,
		)
		return fmt.Errorf("create approval: %w", err)
	}
	
	// 5. Log which provider was selected
	slog.Info("approval created",
		"request_id", approvalRecord.ID,
		"provider", approvalRecord.Provider,
		"status", approvalRecord.Status,
		"session_id", sessionID,
	)
	
	// 6. Return appropriate response to client
	if approvalRecord.Provider == "casdoor" {
		// For Casdoor approvals, provide a link to the console
		// The actual URL would come from the Casdoor client
		slog.Info("approval requires Casdoor review",
			"request_id", requestID,
			"casdoor_task_id", approvalRecord.CasdoorTaskID,
		)
	}
	
	return nil
}

// SensitiveDetectionResult represents the output from sensitive content detection.
type SensitiveDetectionResult struct {
	Triggers        []string
	Score           float64
	CrossDepartment bool
	Summary         string
	Metadata        map[string]string
}

// generateRequestID generates a unique request ID.
func generateRequestID() string {
	// Use UUID or similar
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

// getCasdoorUserID gets the Casdoor user ID for a local user.
// This assumes Phase 2-4 Casdoor auth integration is complete.
func getCasdoorUserID(userID string) string {
	// Example: lookup from user database
	// user := getUserByID(userID)
	// if user.CasdoorOrg != "" && user.CasdoorUsername != "" {
	//     return user.CasdoorOrg + "/" + user.CasdoorUsername
	// }
	
	// Fallback: use local user ID
	return userID
}

// Example: Querying approval status
func getApprovalStatus(
	ctx context.Context,
	router *approval.Router,
	requestID string,
) (*approval.Approval, error) {
	approval, err := router.GetApproval(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("get approval: %w", err)
	}
	
	if approval == nil {
		return nil, fmt.Errorf("approval not found: %s", requestID)
	}
	
	slog.Info("approval status",
		"request_id", requestID,
		"status", approval.Status,
		"provider", approval.Provider,
	)
	
	return approval, nil
}

// Example: Manual cancellation
func cancelApproval(
	ctx context.Context,
	router *approval.Router,
	requestID string,
) error {
	// Get approval first to determine provider
	approval, err := router.GetApproval(ctx, requestID)
	if err != nil {
		return fmt.Errorf("get approval: %w", err)
	}
	
	if approval == nil {
		return fmt.Errorf("approval not found: %s", requestID)
	}
	
	if approval.Status != "pending" {
		slog.Warn("cannot cancel non-pending approval",
			"request_id", requestID,
			"status", approval.Status,
		)
		return nil
	}
	
	// Cancel via appropriate provider
	// The router will delegate to the right provider
	// Note: You may need to implement CancelApproval on the router
	
	slog.Info("approval cancelled",
		"request_id", requestID,
		"provider", approval.Provider,
	)
	
	return nil
}
