package goal

import (
	"testing"
)

// TestGetPreset verifies the three cost mode presets return expected configurations.
func TestGetPreset(t *testing.T) {
	tests := []struct {
		mode                  string
		wantRetryEnabled      bool
		wantMaxRetry          int
		wantAutoContinue      bool
		wantMaxContinue       int
		wantUseAudit          bool
		wantAutoFix           bool
		wantMonthlyLimit      int
		wantSessionBudget     int
		wantDowngradeOnBudget bool
	}{
		{
			mode:                  "minimal",
			wantRetryEnabled:      true,
			wantMaxRetry:          2,
			wantAutoContinue:      false,
			wantMaxContinue:       0,
			wantUseAudit:          false,
			wantAutoFix:           false,
			wantMonthlyLimit:      500_000,
			wantSessionBudget:     30_000,
			wantDowngradeOnBudget: true,
		},
		{
			mode:                  "balanced",
			wantRetryEnabled:      true,
			wantMaxRetry:          3,
			wantAutoContinue:      true,
			wantMaxContinue:       5,
			wantUseAudit:          false,
			wantAutoFix:           false,
			wantMonthlyLimit:      2_000_000,
			wantSessionBudget:     100_000,
			wantDowngradeOnBudget: true,
		},
		{
			mode:                  "aggressive",
			wantRetryEnabled:      true,
			wantMaxRetry:          5,
			wantAutoContinue:      true,
			wantMaxContinue:       10,
			wantUseAudit:          true,
			wantAutoFix:           true,
			wantMonthlyLimit:      10_000_000,
			wantSessionBudget:     500_000,
			wantDowngradeOnBudget: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			preset := GetPreset(tt.mode)

			if preset.RetryEnabled != tt.wantRetryEnabled {
				t.Errorf("RetryEnabled = %v, want %v", preset.RetryEnabled, tt.wantRetryEnabled)
			}
			if preset.MaxRetryCount != tt.wantMaxRetry {
				t.Errorf("MaxRetryCount = %d, want %d", preset.MaxRetryCount, tt.wantMaxRetry)
			}
			if preset.AutoContinue != tt.wantAutoContinue {
				t.Errorf("AutoContinue = %v, want %v", preset.AutoContinue, tt.wantAutoContinue)
			}
			if preset.MaxContinueCount != tt.wantMaxContinue {
				t.Errorf("MaxContinueCount = %d, want %d", preset.MaxContinueCount, tt.wantMaxContinue)
			}
			if preset.UseAudit != tt.wantUseAudit {
				t.Errorf("UseAudit = %v, want %v", preset.UseAudit, tt.wantUseAudit)
			}
			if preset.AutoFixEnabled != tt.wantAutoFix {
				t.Errorf("AutoFixEnabled = %v, want %v", preset.AutoFixEnabled, tt.wantAutoFix)
			}
			if preset.MonthlyTokenLimit != tt.wantMonthlyLimit {
				t.Errorf("MonthlyTokenLimit = %d, want %d", preset.MonthlyTokenLimit, tt.wantMonthlyLimit)
			}
			if preset.SessionTokenBudget != tt.wantSessionBudget {
				t.Errorf("SessionTokenBudget = %d, want %d", preset.SessionTokenBudget, tt.wantSessionBudget)
			}
			if preset.DowngradeOnBudget != tt.wantDowngradeOnBudget {
				t.Errorf("DowngradeOnBudget = %v, want %v", preset.DowngradeOnBudget, tt.wantDowngradeOnBudget)
			}
		})
	}
}

// TestGetPreset_Fallback verifies unrecognized modes fall back to minimal.
func TestGetPreset_Fallback(t *testing.T) {
	preset := GetPreset("unknown_mode")

	if preset.MaxRetryCount != 2 {
		t.Errorf("fallback preset MaxRetryCount = %d, want 2 (minimal)", preset.MaxRetryCount)
	}
	if preset.AutoContinue {
		t.Errorf("fallback preset AutoContinue = true, want false (minimal)")
	}
}

// TestInferCostMode verifies backward compatibility inference logic.
func TestInferCostMode(t *testing.T) {
	tests := []struct {
		name         string
		explicitMode string
		autoFix      bool
		autoContinue bool
		retry        bool
		want         string
	}{
		{
			name:         "explicit mode wins",
			explicitMode: "aggressive",
			autoFix:      false,
			autoContinue: false,
			retry:        false,
			want:         "aggressive",
		},
		{
			name:         "auto_fix implies aggressive",
			explicitMode: "",
			autoFix:      true,
			autoContinue: false,
			retry:        false,
			want:         "aggressive",
		},
		{
			name:         "auto_continue implies balanced",
			explicitMode: "",
			autoFix:      false,
			autoContinue: true,
			retry:        false,
			want:         "balanced",
		},
		{
			name:         "retry implies minimal",
			explicitMode: "",
			autoFix:      false,
			autoContinue: false,
			retry:        true,
			want:         "minimal",
		},
		{
			name:         "nothing set defaults to minimal",
			explicitMode: "",
			autoFix:      false,
			autoContinue: false,
			retry:        false,
			want:         "minimal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InferCostMode(tt.explicitMode, tt.autoFix, tt.autoContinue, tt.retry)
			if got != tt.want {
				t.Errorf("InferCostMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCostModeConstants verifies the cost mode constants are as expected.
func TestCostModeConstants(t *testing.T) {
	if string(CostModeMinimal) != "minimal" {
		t.Errorf("CostModeMinimal = %q, want %q", CostModeMinimal, "minimal")
	}
	if string(CostModeBalanced) != "balanced" {
		t.Errorf("CostModeBalanced = %q, want %q", CostModeBalanced, "balanced")
	}
	if string(CostModeAggressive) != "aggressive" {
		t.Errorf("CostModeAggressive = %q, want %q", CostModeAggressive, "aggressive")
	}
}
