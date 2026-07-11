package sessionaudithook

import (
	"encoding/json"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// Config holds runtime configuration for session audit hook.
type Config struct {
	Enabled          bool
	EnforcementLevel string // "strict" | "advisory" | "audit_only"

	// Detection
	DetectorModels        []string
	ApprovalThreshold     int
	AutoBlockThreshold    int
	DetectPromptInjection bool
	DetectPIILeakage      bool
	DetectJailbreak       bool

	// Approval Workflow
	ApprovalTimeout time.Duration
	TimeoutAction   string // "deny" | "escalate" | "auto_approve"
	MinApprovals    int
	ApproverRoles   []string

	// Escalation
	EscalationEnabled   bool
	EscalationAfter     time.Duration
	EscalationApprovers []string

	// Notification
	NotifyChannels  []string
	NotifyOnPending bool
	NotifyOnTimeout bool

	// Module Integration
	RequireIntentAnalysis bool
	IntentWeight          float64

	// Audit Settings
	RetentionDays     int
	MaskSensitiveData bool
}

// loadConfig is replaceable in tests so hook gating does not depend on global settings.
var loadConfig = LoadConfig

// LoadConfig reads session audit configuration from settings.
func LoadConfig() *Config {
	cfg := &Config{
		Enabled:               getBool("session_audit.enabled", false),
		EnforcementLevel:      getString("session_audit.enforcement_level", "strict"),
		DetectorModels:        getStringArray("session_audit.detector_models", []string{"gpt-4o-mini"}),
		ApprovalThreshold:     getInt("session_audit.approval_threshold", 70),
		AutoBlockThreshold:    getInt("session_audit.auto_block_threshold", 90),
		DetectPromptInjection: getBool("session_audit.detect_prompt_injection", true),
		DetectPIILeakage:      getBool("session_audit.detect_pii_leakage", true),
		DetectJailbreak:       getBool("session_audit.detect_jailbreak", true),
		ApprovalTimeout:       getDuration("session_audit.approval_timeout", 4*time.Hour),
		TimeoutAction:         getString("session_audit.timeout_action", "deny"),
		MinApprovals:          getInt("session_audit.min_approvals", 1),
		ApproverRoles:         getStringArray("session_audit.approver_roles", []string{"security_admin"}),
		EscalationEnabled:     getBool("session_audit.escalation_enabled", false),
		EscalationAfter:       getDuration("session_audit.escalation_after", 2*time.Hour),
		EscalationApprovers:   getStringArray("session_audit.escalation_approvers", []string{"ciso", "cto"}),
		NotifyChannels:        getStringArray("session_audit.notify_channels", []string{"feishu"}),
		NotifyOnPending:       getBool("session_audit.notify_on_pending", true),
		NotifyOnTimeout:       getBool("session_audit.notify_on_timeout", true),
		RequireIntentAnalysis: getBool("session_audit.require_intent_analysis", false),
		IntentWeight:          getFloat("session_audit.intent_weight", 0.3),
		RetentionDays:         getInt("session_audit.retention_days", 90),
		MaskSensitiveData:     getBool("session_audit.mask_sensitive_data", true),
	}

	return cfg
}

// ShouldTriggerApproval determines if a detection result should trigger approval.
func (c *Config) ShouldTriggerApproval(score int) bool {
	return score >= c.ApprovalThreshold && score < c.AutoBlockThreshold
}

// ShouldAutoBlock determines if a detection result should be auto-blocked.
func (c *Config) ShouldAutoBlock(score int) bool {
	return score >= c.AutoBlockThreshold
}

// Helper functions to read from settings.Global

func getBool(key string, fallback bool) bool {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

func getString(key string, fallback string) string {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

func getInt(key string, fallback int) int {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

func getFloat(key string, fallback float64) float64 {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return fallback
	}
	return v
}

func getStringArray(key string, fallback []string) []string {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	// Try to unmarshal as JSON array first
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	// Fallback: try as single string (for backward compatibility)
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		// Try to parse as JSON array string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return arr
		}
		return []string{s}
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if settings.Global == nil {
		return fallback
	}
	sp := settings.Global.Spec(key)
	if sp == nil {
		return fallback
	}
	raw, _, err := settings.Global.EffectiveValue(sp.Scope, key, "")
	if err != nil || len(raw) == 0 {
		return fallback
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}
