package providerprofile

import (
	"testing"
	"time"
)

func mkProfile(date time.Time, total, avail, stability float64) DailyProfile {
	return DailyProfile{
		ProfileDate:       date,
		TotalScore:        total,
		AvailabilityScore: avail,
		StabilityScore:    stability,
	}
}

func TestEvaluateAlerts_NoData(t *testing.T) {
	cfg := DefaultAlertConfig()
	alerts := EvaluateAlerts(nil, 1, 1, cfg)
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts for no data, got %d", len(alerts))
	}
}

// 连续3天总分<40 → auto_disabled (critical)
func TestEvaluateAlerts_AutoDisableLowScore3Days(t *testing.T) {
	cfg := DefaultAlertConfig()
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	profiles := []DailyProfile{
		mkProfile(now.AddDate(0, 0, 0), 30, 90, 90),
		mkProfile(now.AddDate(0, 0, -1), 30, 90, 90),
		mkProfile(now.AddDate(0, 0, -2), 30, 90, 90),
	}
	alerts := EvaluateAlerts(profiles, 7, 3, cfg)
	found := false
	for _, a := range alerts {
		if a.Type == AlertTypeAutoDisabled {
			found = true
			if a.Level != AlertLevelCritical {
				t.Errorf("auto_disabled level = %v, want critical", a.Level)
			}
		}
	}
	if !found {
		t.Errorf("expected auto_disabled alert for 3 continuous low-score days, got %v", alerts)
	}
}

// 可用性<50 立即触发 auto_disabled（哪怕只有1天）
func TestEvaluateAlerts_AutoDisableAvailImmediate(t *testing.T) {
	cfg := DefaultAlertConfig()
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	profiles := []DailyProfile{
		mkProfile(now, 80, 40, 90), // total high but availability low
	}
	alerts := EvaluateAlerts(profiles, 9, 1, cfg)
	found := false
	for _, a := range alerts {
		if a.Type == AlertTypeAutoDisabled && a.Dimension == "availability" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected immediate auto_disabled (availability) alert, got %v", alerts)
	}
}

// 连续3天总分>=70 → auto_enabled (info)
func TestEvaluateAlerts_AutoEnable(t *testing.T) {
	cfg := DefaultAlertConfig()
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	profiles := []DailyProfile{
		mkProfile(now.AddDate(0, 0, 0), 75, 95, 95),
		mkProfile(now.AddDate(0, 0, -1), 72, 95, 95),
		mkProfile(now.AddDate(0, 0, -2), 70, 95, 95),
	}
	alerts := EvaluateAlerts(profiles, 9, 1, cfg)
	found := false
	for _, a := range alerts {
		if a.Type == AlertTypeAutoEnabled {
			found = true
			if a.Level != AlertLevelInfo {
				t.Errorf("auto_enabled level = %v, want info", a.Level)
			}
		}
	}
	// auto_enabled is emitted by this pure function whenever 3 continuous days >=70;
	// the action layer (later task) gates whether to actually flip lifecycle.
	if !found {
		t.Errorf("expected auto_enabled alert for 3 continuous high-score days, got %v", alerts)
	}
}

// score_drop: 24h下降>=20 → warning
func TestEvaluateAlerts_ScoreDrop24h(t *testing.T) {
	cfg := DefaultAlertConfig()
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	profiles := []DailyProfile{
		mkProfile(now, 60, 90, 90),
		mkProfile(now.AddDate(0, 0, -1), 85, 90, 90), // -25
	}
	alerts := EvaluateAlerts(profiles, 7, 3, cfg)
	found := false
	for _, a := range alerts {
		if a.Type == AlertTypeScoreDrop && a.Level == AlertLevelWarning {
			found = true
		}
	}
	if !found {
		t.Errorf("expected score_drop warning, got %v", alerts)
	}
}

// dimension_low: availability<60 → critical
func TestEvaluateAlerts_DimensionLowAvailability(t *testing.T) {
	cfg := DefaultAlertConfig()
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	profiles := []DailyProfile{
		mkProfile(now, 80, 55, 90),
	}
	alerts := EvaluateAlerts(profiles, 7, 3, cfg)
	found := false
	for _, a := range alerts {
		if a.Type == AlertTypeDimensionLow && a.Dimension == "availability" && a.Level == AlertLevelCritical {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dimension_low (availability, critical), got %v", alerts)
	}
}

// helper used by integration of store tests later; defined here to keep package-internal.
func providerlevel() AlertLevel { return AlertLevelWarning }
