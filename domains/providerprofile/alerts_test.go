package providerprofile

import (
	"testing"
	"time"
)

func TestDefaultAlertConfig(t *testing.T) {
	cfg := DefaultAlertConfig()

	if cfg.ScoreDropThreshold24h != 20 {
		t.Errorf("ScoreDropThreshold24h = %v, want 20", cfg.ScoreDropThreshold24h)
	}
	if cfg.AutoDisableThreshold != 40 {
		t.Errorf("AutoDisableThreshold = %v, want 40", cfg.AutoDisableThreshold)
	}
	if cfg.AutoDisableContinuousDays != 3 {
		t.Errorf("AutoDisableContinuousDays = %v, want 3", cfg.AutoDisableContinuousDays)
	}
	if cfg.AutoEnableThreshold != 70 {
		t.Errorf("AutoEnableThreshold = %v, want 70", cfg.AutoEnableThreshold)
	}
	if cfg.AutoEnableContinuousDays != 3 {
		t.Errorf("AutoEnableContinuousDays = %v, want 3", cfg.AutoEnableContinuousDays)
	}
}

func TestAlertTypeConstants(t *testing.T) {
	cases := []struct{ got, want string }{
		{string(AlertTypeScoreDrop), "score_drop"},
		{string(AlertTypeTrendDrop), "trend_drop"},
		{string(AlertTypeDimensionLow), "dimension_low"},
		{string(AlertTypeAutoDisabled), "auto_disabled"},
		{string(AlertTypeAutoEnabled), "auto_enabled"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q want %q", c.got, c.want)
		}
	}
}

func TestAlertLevelOrdering(t *testing.T) {
	// critical > warning > info — used to pick the highest level when multiple rules fire
	if AlertLevelCritical <= AlertLevelWarning || AlertLevelWarning <= AlertLevelInfo {
		t.Error("alert levels must be ordered critical > warning > info")
	}
}

// TestHighestLevel confirms we can reduce a set of levels to the most severe.
func TestHighestLevel(t *testing.T) {
	if got := highestLevel(AlertLevelInfo, AlertLevelCritical, AlertLevelWarning); got != AlertLevelCritical {
		t.Errorf("highestLevel = %v, want critical", got)
	}
}

// Test_isContiguousRecentDays verifies the "continuous N days" helper.
// Given a slice of daily scores ordered by date DESC (most recent first),
// it returns true if there are >= N contiguous days present counting back
// from the most recent, all satisfying predicate.
func Test_isContiguousRecentDays(t *testing.T) {
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	mkDays := func(offsets []int) []DailyProfile {
		var out []DailyProfile
		for _, d := range offsets {
			out = append(out, DailyProfile{ProfileDate: now.AddDate(0, 0, -d), TotalScore: 30})
		}
		return out // already desc by construction when offsets ascending
	}
	// 3 contiguous days back from now → true for N=3
	if !isContiguousRecentDays(mkDays([]int{0, 1, 2}), 3, func(p DailyProfile) bool { return true }) {
		t.Error("3 contiguous days should satisfy N=3")
	}
	// gap: days 0,1,3 (missing day 2) → false for N=3
	if isContiguousRecentDays(mkDays([]int{0, 1, 3}), 3, func(p DailyProfile) bool { return true }) {
		t.Error("non-contiguous days should not satisfy N=3")
	}
	// only 2 days → false for N=3
	if isContiguousRecentDays(mkDays([]int{0, 1}), 3, func(p DailyProfile) bool { return true }) {
		t.Error("2 days should not satisfy N=3")
	}
}
