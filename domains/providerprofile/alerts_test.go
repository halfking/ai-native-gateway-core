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
	// 严重程度序：critical > warning > info。
	// 注意 AlertLevel 是纯名称字符串（"critical"/"warning"/"info"），不能用字面值
	// 比较严重度（'c'<'i'<'w' 与严重度相反）；必须用 severityRank 比较。
	if severityRank(AlertLevelCritical) <= severityRank(AlertLevelWarning) {
		t.Error("critical must rank higher than warning")
	}
	if severityRank(AlertLevelWarning) <= severityRank(AlertLevelInfo) {
		t.Error("warning must rank higher than info")
	}
	// 字面值保持纯名称（与 schema 注释 critical/warning/info 一致），便于直接入库展示
	if string(AlertLevelCritical) != "critical" || string(AlertLevelWarning) != "warning" || string(AlertLevelInfo) != "info" {
		t.Error("alert level strings must be pure names for storage/display")
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
