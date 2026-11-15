package approval

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/casdoor"
)

// CasdoorClient 是审批提供者所需的 Casdoor 能力（依赖倒置）。
//
// ★ 抽接口的原因：直接持有具体类型 *casdoor.Client 时，单元测试无法注入替身
//
//	⇒ 这条路径没有测试。*casdoor.Client 的方法集天然满足该接口，生产行为不变。
type CasdoorClient interface {
	CreateTask(ctx context.Context, req *casdoor.CreateTaskRequest) (*casdoor.Task, error)
	GetTaskStatus(ctx context.Context, externalID string) (*casdoor.Task, error)
	CancelTask(ctx context.Context, externalID string) error
	BuildConsoleURL(taskID int) string
	IsEnabled() bool
}

// CasdoorProvider implements Provider interface for Casdoor approval platform.
type CasdoorProvider struct {
	client CasdoorClient
	// store is used to persist local mapping between requestID and Casdoor taskID
	store ApprovalStore
}

// ApprovalStore defines the persistence interface for approval records.
type ApprovalStore interface {
	// Save persists an approval record.
	Save(ctx context.Context, approval *Approval) error

	// Get retrieves an approval by request id.
	Get(ctx context.Context, requestID string) (*Approval, error)

	// Update updates an approval record.
	Update(ctx context.Context, approval *Approval) error
}

// NewCasdoorProvider creates a new Casdoor approval provider.
func NewCasdoorProvider(client CasdoorClient, store ApprovalStore) *CasdoorProvider {
	return &CasdoorProvider{
		client: client,
		store:  store,
	}
}

// Name returns "casdoor".
func (p *CasdoorProvider) Name() string {
	return "casdoor"
}

// IsEnabled returns whether Casdoor client is enabled.
func (p *CasdoorProvider) IsEnabled() bool {
	return p.client != nil && p.client.IsEnabled()
}

// CreateApproval creates a Casdoor approval task and persists the mapping.
func (p *CasdoorProvider) CreateApproval(ctx context.Context, req *CreateApprovalRequest) (*Approval, error) {
	if !p.IsEnabled() {
		return nil, fmt.Errorf("casdoor provider is not enabled")
	}

	// Build Casdoor task request
	payload, _ := json.Marshal(map[string]interface{}{
		"requestId":    req.RequestID,
		"sessionId":    req.SessionID,
		"tenantId":     req.TenantID,
		"userId":       req.UserID,
		"detectResult": req.DetectResult,
		"clientInfo":   req.ClientInfo,
	})

	// ExternalID: sessionId:requestId for correlation
	externalID := fmt.Sprintf("%s:%s", req.SessionID, req.RequestID)

	// Applicant: prefer Casdoor user id, fall back to llm-gateway user id
	applicant := req.CasdoorUserID
	if applicant == "" {
		applicant = req.UserID
	}

	taskReq := &casdoor.CreateTaskRequest{
		ExternalID:   externalID,
		Applicant:    applicant,
		Type:         "sensitive_content",
		ResourceType: "llm_session",
		ResourceID:   req.SessionID,
		Title:        p.buildTitle(req),
		Payload:      string(payload),
	}

	// Create task via Casdoor API
	task, err := p.client.CreateTask(ctx, taskReq)
	if err != nil {
		RecordApprovalCreation("casdoor", "failed")
		return nil, fmt.Errorf("create casdoor task: %w", err)
	}

	// Build approval record
	expiresAt := req.CreatedAt.Add(30 * time.Minute)
	if task.CreatedTime != "" {
		// Parse Casdoor's expiresAt if provided
		// For now, use default 30 minutes
	}

	approval := &Approval{
		ID:            req.RequestID,
		SessionID:     req.SessionID,
		TenantID:      req.TenantID,
		Status:        "pending",
		Provider:      "casdoor",
		CasdoorTaskID: task.ID,
		CreatedAt:     req.CreatedAt,
		ExpiresAt:     expiresAt,
	}

	// Persist mapping
	if err := p.store.Save(ctx, approval); err != nil {
		// Task created in Casdoor but mapping failed - log warning
		// The webhook callback can still work via externalID
		RecordApprovalError("casdoor", "save_mapping", err)
	}

	RecordApprovalCreation("casdoor", "success")
	return approval, nil
}

// GetApproval retrieves approval status.
func (p *CasdoorProvider) GetApproval(ctx context.Context, requestID string) (*Approval, error) {
	// Retrieve from local store
	approval, err := p.store.Get(ctx, requestID)
	if err != nil {
		return nil, fmt.Errorf("get approval from store: %w", err)
	}
	if approval == nil {
		return nil, nil
	}

	// If pending, optionally poll Casdoor for latest status
	if approval.Status == "pending" && p.IsEnabled() {
		externalID := fmt.Sprintf("%s:%s", approval.SessionID, requestID)
		if task, err := p.client.GetTaskStatus(ctx, externalID); err == nil && task != nil {
			p.syncTaskStatus(approval, task)
			p.store.Update(ctx, approval)
		}
	}

	return approval, nil
}

// CancelApproval cancels a pending Casdoor task.
func (p *CasdoorProvider) CancelApproval(ctx context.Context, requestID string) error {
	approval, err := p.store.Get(ctx, requestID)
	if err != nil || approval == nil {
		return fmt.Errorf("approval not found: %s", requestID)
	}

	if approval.Status != "pending" {
		return nil // already resolved
	}

	externalID := fmt.Sprintf("%s:%s", approval.SessionID, requestID)
	if err := p.client.CancelTask(ctx, externalID); err != nil {
		return fmt.Errorf("cancel casdoor task: %w", err)
	}

	approval.Status = "cancelled"
	p.store.Update(ctx, approval)

	return nil
}

// HandleWebhook processes a Casdoor approval decision webhook.
func (p *CasdoorProvider) HandleWebhook(ctx context.Context, event *casdoor.WebhookEvent) error {
	// Parse externalID: "sessionId:requestId"
	parts := splitExternalID(event.ExternalID)
	if len(parts) != 2 {
		return fmt.Errorf("invalid externalId format: %s", event.ExternalID)
	}
	sessionID, requestID := parts[0], parts[1]

	// Retrieve approval record
	approval, err := p.store.Get(ctx, requestID)
	if err != nil {
		return fmt.Errorf("get approval: %w", err)
	}
	if approval == nil {
		return fmt.Errorf("approval not found: %s", requestID)
	}

	// 完整性校验：webhook 的 externalID 里带的 sessionId 必须与库里那条审批
	// 的 session 一致。externalID 是 "sessionId:requestId"，而查询是按
	// requestID 做的 —— 若两者不匹配，说明这条 webhook 指向的是**另一条**
	// 审批（或 externalID 被串改）⇒ 拒绝落库，避免把 A 会话的裁决写进 B 会话。
	if approval.SessionID != "" && sessionID != "" && approval.SessionID != sessionID {
		return fmt.Errorf("externalId session mismatch: webhook=%s stored=%s", sessionID, approval.SessionID)
	}

	// Idempotency check
	if approval.Status != "pending" {
		return nil // already processed
	}

	// Update status from webhook event
	switch event.Status {
	case "approved":
		approval.Status = "approved"
		approval.ApprovedBy = event.Actor
		now := time.Now()
		approval.ApprovedAt = &now
		// Resume session (caller responsibility via callback)

	case "rejected":
		approval.Status = "rejected"
		approval.ApprovedBy = event.Actor
		approval.Reason = event.Comment

	case "cancelled", "expired":
		approval.Status = event.Status
		approval.Reason = event.Comment
	}

	// Persist update
	if err := p.store.Update(ctx, approval); err != nil {
		return fmt.Errorf("update approval: %w", err)
	}

	// Record metrics
	duration := time.Since(approval.CreatedAt).Seconds()
	RecordApprovalDecision("casdoor", approval.Status, duration)

	return nil
}

// buildTitle generates a human-readable title for Casdoor console.
func (p *CasdoorProvider) buildTitle(req *CreateApprovalRequest) string {
	if req.DetectResult != nil && req.DetectResult.Summary != "" {
		return fmt.Sprintf("敏感内容审批：%s", req.DetectResult.Summary)
	}
	return fmt.Sprintf("会话审批：%s", req.SessionID)
}

// syncTaskStatus syncs Casdoor task status to local approval record.
func (p *CasdoorProvider) syncTaskStatus(approval *Approval, task *casdoor.Task) {
	if task.Status != "pending" {
		approval.Status = task.Status
		approval.ApprovedBy = task.DecidedBy
		approval.Reason = task.Comment

		if task.DecidedTime != "" {
			if t, err := time.Parse(time.RFC3339, task.DecidedTime); err == nil {
				approval.ApprovedAt = &t
			}
		}
	}
}

// splitExternalID splits "sessionId:requestId" format.
func splitExternalID(externalID string) []string {
	// Simple split, could be more robust
	parts := make([]string, 0, 2)
	if idx := len(externalID); idx > 0 {
		// Find last colon to handle session IDs that might contain colons
		for i := len(externalID) - 1; i >= 0; i-- {
			if externalID[i] == ':' {
				parts = append(parts, externalID[:i], externalID[i+1:])
				break
			}
		}
	}
	return parts
}
