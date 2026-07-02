// Package webhooks provides webhook handlers for external integrations.
package webhooks

import "context"

// ApprovalManager defines the interface for approval operations.
// This interface is shared across all webhook handlers.
type ApprovalManager interface {
	Approve(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error
	Reject(ctx context.Context, approvalID, tenantID, approvedBy, reason string) error
	GetApprovalByRequestID(ctx context.Context, requestID string) (tenantID string, err error)
}
