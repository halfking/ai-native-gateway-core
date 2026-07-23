package main

import (
	"encoding/json"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/goal"
	"github.com/kaixuan/llm-gateway-go/domains/streaming"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// settingsGetter is the minimal interface needed to resolve retry policy.
// This allows testing without full settings.Global infrastructure.
type settingsGetter interface {
	GetBool(tenantID, key string, def bool) bool
	GetInt(tenantID, key string, def int) int
	GetFloat(tenantID, key string, def float64) float64
	GetString(tenantID, key string, def string) string
}

// goalRetryPolicyResolver implements streaming.GoalRetryPolicyResolver by
// reading tenant-scoped goal.cost_mode and applying the corresponding preset,
// with optional per-setting overrides.
type goalRetryPolicyResolver struct {
	getter settingsGetter
}

func newGoalRetryPolicyResolver(getter settingsGetter) *goalRetryPolicyResolver {
	return &goalRetryPolicyResolver{getter: getter}
}

// ResolveGoalRetryPolicy resolves the effective retry policy for a tenant.
// Resolution order:
//   1. Read goal.cost_mode (tenant-scoped), default to "minimal"
//   2. Load the corresponding preset
//   3. Apply individual setting overrides (goal.retry_on_error, goal.max_retry_count, etc.)
//   4. Normalize and return
func (r *goalRetryPolicyResolver) ResolveGoalRetryPolicy(tenantID string) streaming.GoalRetryPolicy {
	// Step 1: Read cost_mode
	costMode := "minimal" // safe default
	if settings.Global != nil {
		val, _, err := settings.Global.EffectiveValue(settings.ScopeTenant, "goal.cost_mode", tenantID)
		if err == nil && len(val) > 0 {
			var mode string
			if json.Unmarshal(val, &mode) == nil && mode != "" {
				costMode = mode
			}
		}
	}

	// Step 2: Load preset
	preset := goal.GetPreset(costMode)

	// Step 3: Build policy from preset
	policy := streaming.GoalRetryPolicy{
		CostMode:     costMode,
		Enabled:      r.getter.GetBool(tenantID, "goal.retry_on_error", preset.RetryEnabled),
		MaxRetries:   r.getter.GetInt(tenantID, "goal.max_retry_count", preset.MaxRetryCount),
		TotalTimeout: time.Duration(r.getter.GetInt(tenantID, "goal.retry_total_timeout_seconds", preset.RetryTotalTimeout)) * time.Second,
		// Keep existing exponential backoff defaults (100ms / 5s) for now
		// until design clarifies fixed-delay vs exponential semantics
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  5 * time.Second,
	}

	// Step 4: Normalize
	return policy.Normalize()
}
