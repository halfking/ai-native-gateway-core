// Package approval provides a dual-layer approval architecture:
//   L1: Native fast approval (llm-gateway-go's existing approval_queue)
//   L2: Casdoor approval platform (complex flows, cross-department)
//
// Architecture decision (2026-11-15):
//   - Do NOT replace the existing fast approval system
//   - Route by sensitivity level: low/medium → L1, high → L2
//   - Casdoor unavailable → degrade to L1
//   - Preserve existing performance characteristics
package approval

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SensitivityLevel classifies approval routing decisions.
type SensitivityLevel int

const (
	LevelLow    SensitivityLevel = 1 // fast approval
	LevelMedium SensitivityLevel = 2 // fast approval
	LevelHigh   SensitivityLevel = 3 // Casdoor approval
)

func (l SensitivityLevel) String() string {
	switch l {
	case LevelLow:
		return "low"
	case LevelMedium:
		return "medium"
	case LevelHigh:
		return "high"
	default:
		return "unknown"
	}
}

// DetectResult represents sensitive content detection output.
type DetectResult struct {
	Triggers          []string          `json:"triggers"`
	SensitivityScore  float64           `json:"sensitivityScore"`
	CrossDepartment   bool              `json:"crossDepartment"`
	Summary           string            `json:"summary"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}

// CreateApprovalRequest is the unified request to create an approval.
type CreateApprovalRequest struct {
	RequestID    string        `json:"requestId"`    // unique request id
	SessionID    string        `json:"sessionId"`
	TenantID     string        `json:"tenantId"`
	UserID       string        `json:"userId"`       // llm-gateway user id
	CasdoorUserID string       `json:"casdoorUserId,omitempty"` // "org/username" for Casdoor
	DetectResult *DetectResult `json:"detectResult"`
	ClientInfo   interface{}   `json:"clientInfo,omitempty"`
	CreatedAt    time.Time     `json:"createdAt"`
}

// Approval represents an approval task (unified across L1/L2).
type Approval struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"sessionId"`
	TenantID      string    `json:"tenantId"`
	Status        string    `json:"status"` // pending/approved/rejected/timeout/cancelled
	Provider      string    `json:"provider"` // "native" or "casdoor"
	CasdoorTaskID int       `json:"casdoorTaskId,omitempty"`
	ApprovedBy    string    `json:"approvedBy,omitempty"`
	ApprovedAt    *time.Time `json:"approvedAt,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// Provider is the interface for approval backends (L1 native / L2 Casdoor).
type Provider interface {
	// Name returns the provider name ("native" or "casdoor").
	Name() string

	// IsEnabled returns whether this provider is available.
	IsEnabled() bool

	// CreateApproval creates an approval task.
	CreateApproval(ctx context.Context, req *CreateApprovalRequest) (*Approval, error)

	// GetApproval retrieves approval status by request id.
	GetApproval(ctx context.Context, requestID string) (*Approval, error)

	// CancelApproval cancels a pending approval.
	CancelApproval(ctx context.Context, requestID string) error
}

// RouterConfig configures sensitivity-based routing.
type RouterConfig struct {
	// HighSensitivityThreshold: score >= this → high sensitivity
	HighSensitivityThreshold float64 `json:"highSensitivityThreshold"`

	// RequireCasdoorKeywords: triggers containing these → force Casdoor
	RequireCasdoorKeywords []string `json:"requireCasdoorKeywords"`

	// CrossDepartmentRequiresCasdoor: cross-dept detection → force Casdoor
	CrossDepartmentRequiresCasdoor bool `json:"crossDepartmentRequiresCasdoor"`
}

// DefaultRouterConfig returns sensible defaults.
func DefaultRouterConfig() RouterConfig {
	return RouterConfig{
		HighSensitivityThreshold: 0.8,
		RequireCasdoorKeywords: []string{
			"财务数据", "薪资信息", "商业机密", "客户隐私",
			"financial_data", "salary", "trade_secret", "customer_privacy",
		},
		CrossDepartmentRequiresCasdoor: true,
	}
}

// Router routes approval requests to L1 or L2 based on sensitivity.
type Router struct {
	config          RouterConfig
	nativeProvider  Provider
	casdoorProvider Provider
}

// NewRouter creates a new approval router.
func NewRouter(cfg RouterConfig, nativeProvider, casdoorProvider Provider) *Router {
	return &Router{
		config:          cfg,
		nativeProvider:  nativeProvider,
		casdoorProvider: casdoorProvider,
	}
}

// ClassifySensitivity determines the sensitivity level of a detection result.
func (r *Router) ClassifySensitivity(result *DetectResult) SensitivityLevel {
	if result == nil {
		return LevelLow
	}

	// 1. Check force-Casdoor keywords
	for _, kw := range r.config.RequireCasdoorKeywords {
		for _, trigger := range result.Triggers {
			if strings.Contains(strings.ToLower(trigger), strings.ToLower(kw)) {
				return LevelHigh
			}
		}
	}

	// 2. Check cross-department flag
	if r.config.CrossDepartmentRequiresCasdoor && result.CrossDepartment {
		return LevelHigh
	}

	// 3. Check sensitivity score
	if result.SensitivityScore >= r.config.HighSensitivityThreshold {
		return LevelHigh
	}

	// 4. Medium vs low
	if result.SensitivityScore > 0.5 {
		return LevelMedium
	}

	return LevelLow
}

// Route selects the appropriate provider for a request.
func (r *Router) Route(req *CreateApprovalRequest) (Provider, SensitivityLevel) {
	level := r.ClassifySensitivity(req.DetectResult)

	switch level {
	case LevelLow, LevelMedium:
		// Fast approval path
		return r.nativeProvider, level

	case LevelHigh:
		// Prefer Casdoor for high sensitivity
		if r.casdoorProvider != nil && r.casdoorProvider.IsEnabled() {
			return r.casdoorProvider, level
		}
		// Degradation: Casdoor unavailable → fall back to native
		return r.nativeProvider, level

	default:
		return r.nativeProvider, level
	}
}

// CreateApproval routes and creates an approval via the selected provider.
func (r *Router) CreateApproval(ctx context.Context, req *CreateApprovalRequest) (*Approval, error) {
	provider, level := r.Route(req)

	// Record routing decision
	RecordApprovalRouting(provider.Name(), level.String())

	approval, err := provider.CreateApproval(ctx, req)
	if err != nil {
		RecordApprovalError(provider.Name(), "create", err)
		return nil, fmt.Errorf("create approval via %s: %w", provider.Name(), err)
	}

	return approval, nil
}

// GetApproval retrieves approval from the appropriate provider.
// Tries both providers if necessary (for migration scenarios).
func (r *Router) GetApproval(ctx context.Context, requestID string) (*Approval, error) {
	// Try native first (most common)
	if approval, err := r.nativeProvider.GetApproval(ctx, requestID); err == nil && approval != nil {
		return approval, nil
	}

	// Try Casdoor
	if r.casdoorProvider != nil && r.casdoorProvider.IsEnabled() {
		if approval, err := r.casdoorProvider.GetApproval(ctx, requestID); err == nil && approval != nil {
			return approval, nil
		}
	}

	return nil, fmt.Errorf("approval not found: %s", requestID)
}
