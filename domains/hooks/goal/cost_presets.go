package goal

// cost_presets.go — Cost mode presets for goal mode.
//
// This file defines three cost tiers (minimal, balanced, aggressive) as
// complete configuration bundles. Users select a tier via a single setting
// (goal.cost_mode), and the entire hook behavior adjusts accordingly.
//
// Design rationale (per docs/会话优化v2/18-Goal模式成本控制与分级方案.md):
//  - minimal:    +20% cost,  retry only
//  - balanced:   +140% cost, retry + auto-continue + loop detection
//  - aggressive: +252% cost, full automation (retry + continue + audit + fix)
//
// Usage:
//   preset := goal.GetPreset("balanced")
//   cfg.MaxRetryCount = preset.MaxRetryCount
//   cfg.AutoContinueOnPause = preset.AutoContinue
//   ...

// CostMode enumerates the three cost tiers.
type CostMode string

const (
	CostModeMinimal    CostMode = "minimal"
	CostModeBalanced   CostMode = "balanced"
	CostModeAggressive CostMode = "aggressive"
)

// ModePreset defines the complete configuration for a cost mode.
// All fields are explicitly set; no "use default" ambiguity.
type ModePreset struct {
	// --- Retry settings ---
	RetryEnabled      bool
	MaxRetryCount     int
	RetryDelaySeconds int
	RetryTotalTimeout int // seconds; prevents client timeout
	RetriableErrors   string

	// --- Auto-continue settings ---
	AutoContinue         bool
	MaxContinueCount     int
	CompletionConfidence float64
	DetectionMode        string // "keyword" | "heuristic" | "llm" | "hybrid"

	// --- Loop detection & model switching ---
	LoopDetectionEnabled bool
	LoopThreshold        int // hash collisions before switching
	MaxModelSwitch       int

	// --- Audit & Fix ---
	UseAudit          bool
	AutoFixEnabled    bool
	AutoFixSeverity   string // "high" | "medium" | "low"
	UseAutorouteAudit bool

	// --- Cost limits & budget ---
	MonthlyTokenLimit  int     // tenant-level monthly cap
	SessionTokenBudget int     // single goal session cap
	CostAlertThreshold float64 // 0.0-1.0, trigger warning at this ratio
	DowngradeOnBudget  bool    // auto-downgrade when budget approaches limit
}

// ModePresets is the canonical preset table. Add new modes here.
var ModePresets = map[CostMode]ModePreset{
	CostModeMinimal: {
		// --- Retry only (no auto-continue, no audit) ---
		RetryEnabled:      true,
		MaxRetryCount:     2,
		RetryDelaySeconds: 15,
		RetryTotalTimeout: 40, // under typical client timeout (50-60s)
		RetriableErrors:   "5xx,timeout,no_candidates,rate_limit,overloaded",

		// --- No auto-continue ---
		AutoContinue:     false,
		MaxContinueCount: 0,

		// --- No loop detection (not needed without continue) ---
		LoopDetectionEnabled: false,
		LoopThreshold:        0,
		MaxModelSwitch:       0,

		// --- No audit/fix ---
		UseAudit:          false,
		AutoFixEnabled:    false,
		UseAutorouteAudit: false,

		// --- Conservative limits ---
		MonthlyTokenLimit:  500_000, // 500K tokens/month
		SessionTokenBudget: 30_000,  // 30K tokens/session
		CostAlertThreshold: 0.85,    // warn at 85%
		DowngradeOnBudget:  true,    // auto-downgrade if approaching limit
	},

	CostModeBalanced: {
		// --- Retry + Auto-continue ---
		RetryEnabled:      true,
		MaxRetryCount:     3,
		RetryDelaySeconds: 20,
		RetryTotalTimeout: 50,
		RetriableErrors:   "5xx,timeout,no_candidates,rate_limit,overloaded",

		AutoContinue:         true,
		MaxContinueCount:     5,
		CompletionConfidence: 0.75,
		DetectionMode:        "hybrid",

		// --- Loop detection + model switching ---
		LoopDetectionEnabled: true,
		LoopThreshold:        2,
		MaxModelSwitch:       3,

		// --- Manual audit only (no auto-fix) ---
		UseAudit:          false, // user must explicitly trigger
		AutoFixEnabled:    false,
		UseAutorouteAudit: true,

		// --- Moderate limits with auto-downgrade ---
		MonthlyTokenLimit:  2_000_000, // 2M tokens/month
		SessionTokenBudget: 100_000,   // 100K tokens/session
		CostAlertThreshold: 0.85,
		DowngradeOnBudget:  true, // downgrade to minimal at 85%
	},

	CostModeAggressive: {
		// --- Full automation: retry + continue + audit + fix ---
		RetryEnabled:      true,
		MaxRetryCount:     5,
		RetryDelaySeconds: 30,
		RetryTotalTimeout: 120, // allow longer wait for critical tasks
		RetriableErrors:   "5xx,timeout,no_candidates,rate_limit,overloaded",

		AutoContinue:         true,
		MaxContinueCount:     10,
		CompletionConfidence: 0.6, // lower threshold = more aggressive
		DetectionMode:        "hybrid",

		// --- Aggressive loop detection + more switches ---
		LoopDetectionEnabled: true,
		LoopThreshold:        3,
		MaxModelSwitch:       5,

		// --- Full audit + auto-fix (high + medium severity) ---
		UseAudit:          true,
		AutoFixEnabled:    true,
		AutoFixSeverity:   "medium", // fix both high and medium issues
		UseAutorouteAudit: true,

		// --- High limits, no auto-downgrade ---
		MonthlyTokenLimit:  10_000_000, // 10M tokens/month
		SessionTokenBudget: 500_000,    // 500K tokens/session
		CostAlertThreshold: 0.95,       // only warn at 95%
		DowngradeOnBudget:  false,      // allow exceeding budget
	},
}

// GetPreset returns the preset for the given mode, falling back to minimal
// if the mode is unrecognized.
func GetPreset(mode string) ModePreset {
	m := CostMode(mode)
	if preset, ok := ModePresets[m]; ok {
		return preset
	}
	// Safe fallback: minimal (lowest cost)
	return ModePresets[CostModeMinimal]
}

// InferCostMode infers the cost mode from legacy environment variables or
// explicit config. This provides backward compatibility for deployments that
// set individual goal.* flags before cost_mode was introduced.
//
// Precedence:
//  1. Explicit goal.cost_mode setting (if set)
//  2. Infer from goal.auto_fix_enabled: true → aggressive
//  3. Infer from goal.auto_continue_on_pause: true → balanced
//  4. Infer from goal.retry_on_error: true → minimal
//  5. Default: minimal
//
// Usage in buildGoalConfig:
//
//	mode := goal.InferCostMode(explicitMode, cfg)
//	preset := goal.GetPreset(mode)
func InferCostMode(explicitMode string, autoFix, autoContinue, retry bool) string {
	// 1. Explicit mode wins
	if explicitMode != "" {
		return explicitMode
	}

	// 2. auto_fix → aggressive
	if autoFix {
		return string(CostModeAggressive)
	}

	// 3. auto_continue → balanced
	if autoContinue {
		return string(CostModeBalanced)
	}

	// 4. retry → minimal
	if retry {
		return string(CostModeMinimal)
	}

	// 5. Default: minimal (safest)
	return string(CostModeMinimal)
}
