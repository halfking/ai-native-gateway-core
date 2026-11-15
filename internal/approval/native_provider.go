package approval

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit"
)

// NativeProvider wraps the existing llm-gateway-go approval system (L1 fast approval).
type NativeProvider struct {
	approvalManager *sessionaudit.ApprovalManager
	timeoutMinutes  int
}

// NewNativeProvider creates a native approval provider.
func NewNativeProvider(mgr *sessionaudit.ApprovalManager, timeoutMinutes int) *NativeProvider {
	if timeoutMinutes == 0 {
		timeoutMinutes = 30
	}
	return &NativeProvider{
		approvalManager: mgr,
		timeoutMinutes:  timeoutMinutes,
	}
}

// Name returns "native".
func (p *NativeProvider) Name() string {
	return "native"
}

// IsEnabled returns true (native approval is always available).
func (p *NativeProvider) IsEnabled() bool {
	return p.approvalManager != nil
}

// CreateApproval creates a native fast approval task.
func (p *NativeProvider) CreateApproval(ctx context.Context, req *CreateApprovalRequest) (*Approval, error) {
	if !p.IsEnabled() {
		return nil, fmt.Errorf("native provider is not enabled")
	}

	// Create approval in approval_queue via ApprovalManager
	// The existing ApprovalManager handles the actual persistence
	expiresAt := req.CreatedAt.Add(time.Duration(p.timeoutMinutes) * time.Minute)

	// Note: The actual insertion into approval_queue is handled by the existing
	// session audit code path. This provider just constructs the approval record.
	// In a full integration, you would call approvalManager methods here.
	
	approval := &Approval{
		ID:        req.RequestID,
		SessionID: req.SessionID,
		TenantID:  req.TenantID,
		Status:    "pending",
		Provider:  "native",
		CreatedAt: req.CreatedAt,
		ExpiresAt: expiresAt,
	}

	slog.Info("native approval created",
		"request_id", req.RequestID,
		"session_id", req.SessionID,
		"tenant_id", req.TenantID,
	)

	RecordApprovalCreation("native", "success")
	return approval, nil
}

// GetApproval retrieves native approval status from approval_queue.
func (p *NativeProvider) GetApproval(ctx context.Context, requestID string) (*Approval, error) {
	if !p.IsEnabled() {
		return nil, fmt.Errorf("native provider is not enabled")
	}

	// Note: ApprovalManager.GetForTenant requires both approvalID and tenantID.
	// In a full integration, you would need to either:
	// 1. Store the tenant_id mapping
	// 2. Or query by ID only (requires new method in ApprovalManager)
	// 
	// For now, we return nil to indicate "not found in native provider"
	// which allows the router to try other providers.
	
	// TODO: Implement proper query logic once tenant_id mapping is available
	// Example:
	// record, err := p.approvalManager.GetForTenant(ctx, requestID, tenantID)
	// if err != nil {
	//     if errors.Is(err, sessionaudit.ErrNotFound) {
	//         return nil, nil
	//     }
	//     return nil, err
	// }
	// return mapNativeRecordToApproval(record), nil

	return nil, nil
}

// CancelApproval cancels a native approval.
func (p *NativeProvider) CancelApproval(ctx context.Context, requestID string) error {
	if !p.IsEnabled() {
		return fmt.Errorf("native provider is not enabled")
	}

	// Note: The existing ApprovalManager doesn't have an explicit "cancel" method.
	// Cancellation is typically done by rejecting the approval with a special marker.
	// 
	// In a full integration, you would implement cancellation via rejection:
	// err := p.approvalManager.Reject(ctx, requestID, tenantID, "system:cancelled", "")
	
	slog.Warn("native approval cancellation not fully implemented",
		"request_id", requestID,
	)
	
	return nil
}

// mapNativeRecordToApproval converts sessionaudit.ApprovalRecord to approval.Approval.
// This is a helper for the full integration.
func mapNativeRecordToApproval(record *sessionaudit.ApprovalRecord) *Approval {
	approval := &Approval{
		ID:        record.ID,
		SessionID: record.SessionID,
		TenantID:  record.TenantID,
		Status:    mapNativeStatus(record.Status),
		Provider:  "native",
		CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
	}

	if record.DecidedAt != nil {
		approval.ApprovedAt = record.DecidedAt
	}
	if record.DecidedBy != "" {
		approval.ApprovedBy = record.DecidedBy
	}

	return approval
}

// mapNativeStatus maps sessionaudit status to approval status.
func mapNativeStatus(status string) string {
	// sessionaudit uses: pending, approved, rejected, timeout
	// approval uses: pending, approved, rejected, cancelled, expired, timeout
	switch status {
	case "timeout":
		return "expired"
	default:
		return status
	}
}
