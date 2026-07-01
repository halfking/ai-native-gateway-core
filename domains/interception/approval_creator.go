// Package interception — ApprovalCreator adapter (PR-V4-08).
//
// 适配 sessionaudit.ApprovalManager → interception.ApprovalCreator，使 V4
// dispatch gate 在 Suspend 决策时直接写 approval_queue。
package interception

import (
	"context"
	"errors"

	"github.com/kaixuan/llm-gateway-go/domain"               //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domain/governance"    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// AuditResultKey env.Metadata 中 sessionaudit.DetectResult 的 key。
const AuditResultKey = "audit_result"

// ApprovalManagerCreator 适配器。SessionID/RequestID 从 governance.ApprovalRequest 取，
// fallback 到 env；TenantID 走 env.TenantID；Snapshot 若是 *sessionaudit.RequestSnapshot 直接使用；
// DetectResult 优先 env.Metadata["audit_result"]，否则按 governance.RiskLevel 折算。
type ApprovalManagerCreator struct {
	mgr *sessionaudit.ApprovalManager
}

// NewApprovalManagerCreator 构造适配器。mgr 为 nil 时 Create 始终返回 error。
func NewApprovalManagerCreator(mgr *sessionaudit.ApprovalManager) *ApprovalManagerCreator {
	return &ApprovalManagerCreator{mgr: mgr}
}

// Create 实现 interception.ApprovalCreator。
func (c *ApprovalManagerCreator) Create(ctx context.Context, env *domain.PipelineRequest, req *governance.ApprovalRequest) (string, error) {
	if c == nil || c.mgr == nil {
		return "", errors.New("interception: nil approval manager")
	}
	if env == nil || env.TenantID == "" {
		return "", errors.New("interception: nil envelope or missing tenant")
	}
	if req == nil {
		req = &governance.ApprovalRequest{}
	}

	saReq := &sessionaudit.ApprovalRequest{
		SessionID:    pickString(req.SessionID, env.SessionID),
		TenantID:     env.TenantID,
		RequestID:    pickString(req.RequestID, envelopeRequestID(env)),
		Snapshot:     pickSnapshot(req.Snapshot),
		DetectResult: pickDetectResult(env),
	}
	if saReq.DetectResult == nil {
		saReq.DetectResult = riskLevelToDetectResult(req.RiskLevel, req.Reason)
	}
	return c.mgr.Create(ctx, saReq)
}

func pickString(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func pickSnapshot(snap any) *sessionaudit.RequestSnapshot {
	if snap == nil {
		return nil
	}
	if rs, ok := snap.(*sessionaudit.RequestSnapshot); ok {
		return rs
	}
	return nil
}

func pickDetectResult(env *domain.PipelineRequest) *sessionaudit.DetectResult {
	if env == nil || env.Metadata == nil {
		return nil
	}
	v, ok := env.Metadata[AuditResultKey]
	if !ok || v == nil {
		return nil
	}
	if dr, ok := v.(*sessionaudit.DetectResult); ok {
		return dr
	}
	return nil
}

// riskLevelToDetectResult 折算 governance.RiskLevel → sessionaudit.DetectResult。
func riskLevelToDetectResult(riskLevel, reason string) *sessionaudit.DetectResult {
	score := map[string]int{"low": 2, "medium": 5, "high": 7, "critical": 9}[riskLevel]
	return &sessionaudit.DetectResult{
		Score:    score,
		Decision: sessionaudit.DecisionNeedApproval,
		Reason:   reason,
	}
}
