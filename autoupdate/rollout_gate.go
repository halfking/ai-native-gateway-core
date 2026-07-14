package autoupdate

import "fmt"

const (
	MinRolloutSuccessRate  = 98.0
	MaxRolloutRollbackRate = 2.0
)

// RolloutStats summarizes terminal upgrade outcomes for a release version.
type RolloutStats struct {
	Total           int     `json:"total"`
	SuccessCount    int     `json:"success_count"`
	FailedCount     int     `json:"failed_count"`
	RolledBackCount int     `json:"rolled_back_count"`
	SuccessRatePct  float64 `json:"success_rate_pct"`
	RollbackRatePct float64 `json:"rollback_rate_pct"`
}

// RolloutGateResult is the allow/deny decision for advancing gray rollout.
type RolloutGateResult struct {
	Allowed bool         `json:"allowed"`
	Reason  string       `json:"reason,omitempty"`
	Stats   RolloutStats `json:"stats"`
}

// EvaluateRolloutGate applies the center rollout safety thresholds.
func EvaluateRolloutGate(stats RolloutStats) RolloutGateResult {
	terminal := stats.SuccessCount + stats.FailedCount + stats.RolledBackCount
	if terminal == 0 {
		return RolloutGateResult{
			Allowed: true,
			Reason:  "no_terminal_upgrade_samples",
			Stats:   stats,
		}
	}
	if stats.SuccessRatePct < MinRolloutSuccessRate {
		return RolloutGateResult{
			Allowed: false,
			Reason: fmt.Sprintf(
				"success rate %.1f%% below minimum %.1f%%",
				stats.SuccessRatePct, MinRolloutSuccessRate,
			),
			Stats: stats,
		}
	}
	if stats.RollbackRatePct >= MaxRolloutRollbackRate {
		return RolloutGateResult{
			Allowed: false,
			Reason: fmt.Sprintf(
				"rollback rate %.1f%% at or above maximum %.1f%%",
				stats.RollbackRatePct, MaxRolloutRollbackRate,
			),
			Stats: stats,
		}
	}
	return RolloutGateResult{Allowed: true, Stats: stats}
}
