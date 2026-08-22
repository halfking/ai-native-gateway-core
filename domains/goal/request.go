package goal

import (
	"encoding/json"
	"fmt"
	"time"
)

// GoalRequest represents a versioned goal object from the client request.
// It is parsed after authentication and tenant/session ownership validation,
// before durable snapshot cut point.
type GoalRequest struct {
	Version          int              `json:"version"`
	Enabled          bool             `json:"enabled"`
	RootGoalID       string           `json:"root_goal_id,omitempty"`
	Instruction      string           `json:"instruction"`
	ExecutionMode    ExecutionMode    `json:"execution_mode"`
	Durability       DurabilityMode   `json:"durability"`
	CompletionPolicy CompletionPolicy `json:"completion_policy"`
	Limits           GoalLimits       `json:"limits"`
	Delivery         DeliveryConfig   `json:"delivery"`
}

type ExecutionMode string

const (
	ExecutionModeContinuous ExecutionMode = "continuous"
	ExecutionModeSingleShot ExecutionMode = "single_shot"
)

type DurabilityMode string

const (
	DurabilityDurable   DurabilityMode = "durable"
	DurabilityEphemeral DurabilityMode = "ephemeral"
)

type CompletionPolicy struct {
	Detector                string  `json:"detector"`
	MinConfidence           float64 `json:"min_confidence"`
	RequireTerminalEvidence bool    `json:"require_terminal_evidence"`
}

type GoalLimits struct {
	MaxWallTimeSeconds int `json:"max_wall_time_seconds"`
	MaxTurns           int `json:"max_turns"`
	MaxFollowUps       int `json:"max_follow_ups"`
	MaxModelSwitches   int `json:"max_model_switches"`
	MaxHandoffs        int `json:"max_handoffs"`
}

type DeliveryConfig struct {
	Mode string `json:"mode"`
}

// PolicySnapshot represents server-tightened policy returned to client
type PolicySnapshot struct {
	Version         int        `json:"version"`
	EffectiveLimits GoalLimits `json:"effective_limits"`
	TenantID        string     `json:"tenant_id"`
	APIKeyID        string     `json:"api_key_id"`
	CreatedAt       time.Time  `json:"created_at"`
}

// ParseGoalRequest parses and validates a goal request from JSON body.
// Returns error for unknown version, invalid limits, or missing required fields.
func ParseGoalRequest(data []byte) (*GoalRequest, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("goal request: empty data")
	}

	var req GoalRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("goal request: invalid JSON: %w", err)
	}

	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("goal request: validation failed: %w", err)
	}

	return &req, nil
}

// Validate checks goal request invariants.
// Fail-closed for unknown version, negative limits, or unsupported modes.
func (r *GoalRequest) Validate() error {
	if r.Version != 1 {
		return fmt.Errorf("unsupported goal version: %d (expected 1)", r.Version)
	}

	if !r.Enabled {
		return fmt.Errorf("goal enabled=false not allowed in explicit goal request")
	}

	if r.Instruction == "" {
		return fmt.Errorf("instruction required")
	}

	if len(r.Instruction) > 10000 {
		return fmt.Errorf("instruction exceeds 10000 characters")
	}

	switch r.ExecutionMode {
	case ExecutionModeContinuous, ExecutionModeSingleShot:
		// valid
	default:
		return fmt.Errorf("unsupported execution_mode: %s", r.ExecutionMode)
	}

	switch r.Durability {
	case DurabilityDurable, DurabilityEphemeral:
		// valid
	default:
		return fmt.Errorf("unsupported durability: %s", r.Durability)
	}

	if err := r.Limits.Validate(); err != nil {
		return fmt.Errorf("limits: %w", err)
	}

	if err := r.CompletionPolicy.Validate(); err != nil {
		return fmt.Errorf("completion_policy: %w", err)
	}

	if r.Delivery.Mode == "" {
		return fmt.Errorf("delivery mode required")
	}

	return nil
}

// Validate checks goal limits invariants.
func (l *GoalLimits) Validate() error {
	if l.MaxWallTimeSeconds <= 0 {
		return fmt.Errorf("max_wall_time_seconds must be positive")
	}

	if l.MaxWallTimeSeconds > 604800 {
		return fmt.Errorf("max_wall_time_seconds exceeds 7 days")
	}

	if l.MaxTurns <= 0 {
		return fmt.Errorf("max_turns must be positive")
	}

	if l.MaxTurns > 500 {
		return fmt.Errorf("max_turns exceeds 500")
	}

	if l.MaxFollowUps < 0 {
		return fmt.Errorf("max_follow_ups cannot be negative")
	}

	if l.MaxFollowUps > 500 {
		return fmt.Errorf("max_follow_ups exceeds 500")
	}

	if l.MaxModelSwitches < 0 {
		return fmt.Errorf("max_model_switches cannot be negative")
	}

	if l.MaxModelSwitches > 10 {
		return fmt.Errorf("max_model_switches exceeds 10")
	}

	if l.MaxHandoffs < 0 {
		return fmt.Errorf("max_handoffs cannot be negative")
	}

	if l.MaxHandoffs > 20 {
		return fmt.Errorf("max_handoffs exceeds 20")
	}

	return nil
}

// Validate checks completion policy invariants.
func (p *CompletionPolicy) Validate() error {
	if p.Detector == "" {
		return fmt.Errorf("detector required")
	}

	if p.MinConfidence < 0.0 || p.MinConfidence > 1.0 {
		return fmt.Errorf("min_confidence must be in [0.0, 1.0]")
	}

	return nil
}

// TightenLimits applies tenant/platform policy to client-requested limits.
// Returns server-tightened limits that must be saved in policy_snapshot.
func TightenLimits(requested GoalLimits, tenantMaxWallTime, tenantMaxTurns, tenantMaxFollowUps int) GoalLimits {
	result := requested

	if tenantMaxWallTime > 0 && (requested.MaxWallTimeSeconds > tenantMaxWallTime || requested.MaxWallTimeSeconds <= 0) {
		result.MaxWallTimeSeconds = tenantMaxWallTime
	}

	if tenantMaxTurns > 0 && (requested.MaxTurns > tenantMaxTurns || requested.MaxTurns <= 0) {
		result.MaxTurns = tenantMaxTurns
	}

	if tenantMaxFollowUps > 0 && (requested.MaxFollowUps > tenantMaxFollowUps || requested.MaxFollowUps < 0) {
		result.MaxFollowUps = tenantMaxFollowUps
	}

	// Platform hard caps
	if result.MaxWallTimeSeconds > 604800 {
		result.MaxWallTimeSeconds = 604800 // 7 days
	}

	if result.MaxTurns > 500 {
		result.MaxTurns = 500
	}

	if result.MaxFollowUps > 500 {
		result.MaxFollowUps = 500
	}

	if result.MaxModelSwitches > 10 {
		result.MaxModelSwitches = 10
	}

	if result.MaxHandoffs > 20 {
		result.MaxHandoffs = 20
	}

	return result
}

// CreatePolicySnapshot creates server policy snapshot from tightened limits.
func CreatePolicySnapshot(tenantID, apiKeyID string, effectiveLimits GoalLimits) PolicySnapshot {
	return PolicySnapshot{
		Version:         1,
		EffectiveLimits: effectiveLimits,
		TenantID:        tenantID,
		APIKeyID:        apiKeyID,
		CreatedAt:       time.Now().UTC(),
	}
}
