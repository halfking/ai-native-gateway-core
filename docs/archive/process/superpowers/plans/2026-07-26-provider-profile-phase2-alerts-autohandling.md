# Provider Profile Phase 2: Alert Engine & Auto-Disable/Enable Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the alert detection engine and automatic disable/enable mechanism (design doc §8) so that low-scoring providers are auto-disabled and recovered providers are auto-re-enabled, with all decisions recorded as alerts and provider events.

**Architecture:** A new `AlertEngine` runs as a 4th background worker after the daily aggregator. It reads recent `provider_profile_daily` rows, evaluates 5 alert rules (score_drop, trend_drop, dimension_low, auto_disabled, auto_enabled) against configurable thresholds, and when a disable/enable condition is met it flips `credentials.lifecycle_status` ('active'↔'disabled') + `availability_state` and writes the `auto_*` audit columns, a `provider_profile_alerts` row, and a `provider_events` row. Whitelist (`provider_profile_whitelist`) protects critical providers from auto-disable. The aggregator also gets a first-run-on-start fix so `provider_profile_daily` populates without waiting a full day.

**Tech Stack:** Go 1.21+, PostgreSQL 17, pgx/v5, slog, existing `bg` worker framework, existing `domains/providerprofile` package.

---

## Integration surface (READ THIS — do not invent new patterns)

These are the **real** schemas and APIs (verified against local pg17 on port 55432 and 252 via tunnel on 15432). The design doc's abstract phrases map to concrete things here:

| Design doc phrase | Concrete reality |
|---|---|
| "set `credentials.enabled = FALSE`" | `UPDATE credentials SET lifecycle_status='disabled', availability_state='suspended', auto_disabled_at=now(), auto_disabled_reason=$1 WHERE id=$2 AND lifecycle_status='active'` |
| "set `credentials.enabled = TRUE`" | `UPDATE credentials SET lifecycle_status='active', availability_state='ready', auto_enabled_at=now(), auto_enabled_reason=$1, auto_disabled_at=NULL, auto_disabled_reason=NULL WHERE id=$2 AND manual_disabled=false AND lifecycle_status='disabled'` |
| "do not auto-enable manual_disabled providers" | Guard re-enable with `AND manual_disabled = false` (manual_disabled=true means an admin turned it off — never override) |
| "whitelist" | `SELECT 1 FROM provider_profile_whitelist WHERE provider_id=$1` — whitelisted providers skip auto-disable only (they CAN still be auto-enabled) |
| "record event to `provider_events`" | `INSERT INTO provider_events (id, credential_id, event_kind, payload_json, ts) VALUES (nextval('provider_events_id_seq'), $1, $2, $3::jsonb, now())` — table has NO default on `id`, must use sequence explicitly |

**Existing files to reuse (do NOT recreate):**
- `domains/providerprofile/types.go` — `DailyProfile`, `ProfileWeights`, `TimeSlot`
- `domains/providerprofile/pg_profile_store.go` — `GetDailyProfile`, `GetRecentProfiles`
- `domains/providerprofile/adapters.go` — `GatewayCredentialLister` (the active-credential query pattern at lines 248-275 is the template for all new credential queries)
- `bg/provider_profile_workers.go` — worker pattern (Start/Stop/run/ticker)
- `cmd/gateway/provider_profile_init.go` — worker wiring + feature gate

**Existing `provider_profile_alerts` schema (already deployed, do NOT migrate):**
```
id BIGSERIAL PK, credential_id, provider_id, alert_type, alert_level,
trigger_date, current_score, previous_score, score_change, dimension,
message, details JSONB, action_taken, resolved_at, created_at
```

**Key constraint:** `provider_profile_alerts` has NO unique constraint — the engine MUST dedupe (don't emit the same `auto_disabled` alert twice for the same credential on the same day). See Task 6.

---

## Task 0: Prerequisite verification (no code)

- [ ] **Step 1: Confirm DB state**

Run (local 55432):
```bash
PGPASSWORD='maintain' psql -h 127.0.0.1 -p 55432 -U maintain -d llm_gateway -tAc "
SELECT 'alerts_table', to_regclass('provider_profile_alerts')
UNION ALL SELECT 'whitelist_table', to_regclass('provider_profile_whitelist')
UNION ALL SELECT 'events_table', to_regclass('provider_events')
UNION ALL SELECT 'cred_auto_cols', count(*)::text FROM information_schema.columns WHERE table_name='credentials' AND column_name LIKE 'auto_%';"
```

Expected: all 3 tables present, cred_auto_cols=4.

If `provider_events` is missing locally (it's on 252 but not always on local), apply Task 1 first. Otherwise skip Task 1.

- [ ] **Step 2: Confirm package builds clean**

```bash
go build ./domains/providerprofile/... ./bg/...
```

Expected: exit 0, no output.

---

## Task 1: Local migration — provider_events (only if missing locally)

**Files:**
- Create: `deploy/sql/migrations/2026-07-26-provider-events-local.sql`

The `provider_events` table exists on 252 but may be missing on local Docker pg17. This migration creates it idempotently.

- [ ] **Step 1: Write the migration**

Create `deploy/sql/migrations/2026-07-26-provider-events-local.sql`:
```sql
-- provider_events table (already on 252; ensure local parity for provider profile alerts)
BEGIN;
CREATE TABLE IF NOT EXISTS provider_events (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    event_kind text NOT NULL,
    payload_json jsonb,
    ts timestamp with time zone DEFAULT now() NOT NULL
);
CREATE SEQUENCE IF NOT EXISTS provider_events_id_seq
    AS bigint START WITH 1 INCREMENT BY 1 NO MINVALUE NO MAXVALUE CACHE 1;
ALTER SEQUENCE provider_events_id_seq OWNED BY provider_events.id;
ALTER TABLE provider_events ALTER COLUMN id SET DEFAULT nextval('provider_events_id_seq');
ALTER TABLE provider_events ADD CONSTRAINT provider_events_pkey PRIMARY KEY (id);
CREATE INDEX IF NOT EXISTS idx_provider_events_credential_ts ON provider_events (credential_id, ts DESC);
COMMIT;
```

- [ ] **Step 2: Apply locally**

```bash
PGPASSWORD='maintain' psql -h 127.0.0.1 -p 55432 -U maintain -d llm_gateway \
  -v ON_ERROR_STOP=1 -f deploy/sql/migrations/2026-07-26-provider-events-local.sql
```

Expected: `BEGIN … CREATE SEQUENCE … ALTER TABLE … CREATE INDEX … COMMIT` with no errors. (Statements with IF NOT EXISTS may no-op if already present — that's fine.)

- [ ] **Step 3: Verify**

```bash
PGPASSWORD='maintain' psql -h 127.0.0.1 -p 55432 -U maintain -d llm_gateway -tAc \
  "SELECT to_regclass('provider_events'), pg_get_serial_sequence('provider_events','id');"
```

Expected: `provider_events|public.provider_events_id_seq`

- [ ] **Step 4: Commit**

```bash
git add deploy/sql/migrations/2026-07-26-provider-events-local.sql
git commit -m "feat(migrations): add provider_events table for local pg17 parity"
```

---

## Task 2: Alert types and config

**Files:**
- Create: `domains/providerprofile/alerts.go`
- Test: `domains/providerprofile/alerts_test.go`

- [ ] **Step 1: Write failing test**

Create `domains/providerprofile/alerts_test.go`:
```go
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
	// 严重程度序：critical > warning > info，用 severityRank 比较（不能用字面值比较）。
	if severityRank(AlertLevelCritical) <= severityRank(AlertLevelWarning) {
		t.Error("critical must rank higher than warning")
	}
	if severityRank(AlertLevelWarning) <= severityRank(AlertLevelInfo) {
		t.Error("warning must rank higher than info")
	}
	// 字面值保持纯名称（与 schema 注释 critical/warning/info 一致）
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./domains/providerprofile -run 'TestDefaultAlertConfig|TestAlertType|TestHighestLevel|Test_isContiguousRecentDays' -v
```

Expected: FAIL — `undefined: DefaultAlertConfig`, `undefined: AlertTypeScoreDrop`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `domains/providerprofile/alerts.go`:
```go
package providerprofile

import "time"

// AlertType 告警类型（对应设计文档 §8.2）
type AlertType string

const (
	AlertTypeScoreDrop    AlertType = "score_drop"    // 24h下降≥20 或 7d下降≥30
	AlertTypeTrendDrop    AlertType = "trend_drop"    // 连续3天下降累计≥15
	AlertTypeDimensionLow AlertType = "dimension_low" // 可用性/稳定性<60
	AlertTypeAutoDisabled AlertType = "auto_disabled" // 已自动禁用
	AlertTypeAutoEnabled  AlertType = "auto_enabled"  // 已自动恢复
)

// AlertLevel 告警级别。值的大小关系用于取最严重级别（critical>warning>info）。
//
// IMPORTANT: do NOT compare AlertLevel values directly with </> — Go compares
// string-typed constants byte-wise, and 'c'(99)<'i'(105)<'w'(119), which is
// the OPPOSITE of severity order. Use severityRank() for any severity comparison.
// The string values MUST stay pure names ("info"/"warning"/"critical") because
// they are stored verbatim in provider_profile_alerts.alert_level (schema comment:
// "critical/warning/info") and shown in the UI.
type AlertLevel string

const (
	AlertLevelInfo     AlertLevel = "info"
	AlertLevelWarning  AlertLevel = "warning"
	AlertLevelCritical AlertLevel = "critical"
)

// severityRank 返回级别的严重程度序数（越大越严重）。未知级别视为 info。
func severityRank(l AlertLevel) int {
	switch l {
	case AlertLevelCritical:
		return 3
	case AlertLevelWarning:
		return 2
	default:
		return 1
	}
}

// AlertConfig 告警与自动处理阈值（设计文档 §8.1）
type AlertConfig struct {
	// 相对变化阈值
	ScoreDropThreshold24h float64 // 默认20
	ScoreDropThreshold7d  float64 // 默认30

	// 趋势告警阈值
	ContinuousDropDays int     // 默认3
	TrendDropTotal     float64 // 默认15

	// 自动禁用阈值
	AutoDisableThreshold      float64 // 默认40
	AutoDisableAvailThreshold float64 // 默认50（可用性立即触发）
	AutoDisableContinuousDays int     // 默认3

	// 自动恢复阈值
	AutoEnableThreshold      float64 // 默认70
	AutoEnableContinuousDays int     // 默认3
}

// DefaultAlertConfig 返回设计文档默认阈值
func DefaultAlertConfig() AlertConfig {
	return AlertConfig{
		ScoreDropThreshold24h:      20,
		ScoreDropThreshold7d:       30,
		ContinuousDropDays:         3,
		TrendDropTotal:             15,
		AutoDisableThreshold:       40,
		AutoDisableAvailThreshold:  50,
		AutoDisableContinuousDays:  3,
		AutoEnableThreshold:        70,
		AutoEnableContinuousDays:   3,
	}
}

// Alert 告警实体（映射到 provider_profile_alerts 表）
type Alert struct {
	ID            int64
	CredentialID  int64
	ProviderID    int64
	Type          AlertType
	Level         AlertLevel
	TriggerDate   time.Time
	CurrentScore  float64
	PreviousScore float64
	ScoreChange   float64
	Dimension     string
	Message       string
	Details       map[string]interface{}
	ActionTaken   string // disabled/enabled/degraded/none
}

// highestLevel 返回给定级别中最严重的一个（用 severityRank 比较，不依赖字面值）。
func highestLevel(levels ...AlertLevel) AlertLevel {
	best := AlertLevelInfo
	bestRank := severityRank(best)
	for _, l := range levels {
		if r := severityRank(l); r > bestRank {
			best = l
			bestRank = r
		}
	}
	return best
}

// isContiguousRecentDays 判断 profiles（按 ProfileDate 倒序，最新在前）是否从最新一天起
// 连续 N 天都满足 predicate。日期必须逐日递减（无空洞）。
func isContiguousRecentDays(profiles []DailyProfile, n int, pred func(DailyProfile) bool) bool {
	if len(profiles) < n {
		return false
	}
	for i := 0; i < n; i++ {
		// 相邻两条日期差应为 1 天（profiles[0]最新，profiles[1]应是前一天）
		if i > 0 {
			expected := profiles[i-1].ProfileDate.AddDate(0, 0, -1)
			if !profiles[i].ProfileDate.Equal(expected) {
				return false
			}
		}
		if !pred(profiles[i]) {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./domains/providerprofile -run 'TestDefaultAlertConfig|TestAlertType|TestHighestLevel|Test_isContiguousRecentDays' -v
```

Expected: PASS — all 4 tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w domains/providerprofile/alerts.go domains/providerprofile/alerts_test.go
git add domains/providerprofile/alerts.go domains/providerprofile/alerts_test.go
git commit -m "feat(providerprofile): add alert types, levels, and config defaults"
```

---

## Task 3: Alert rule evaluation (pure logic, no DB)

**Files:**
- Create: `domains/providerprofile/alert_rules.go`
- Test: `domains/providerprofile/alert_rules_test.go`

This is the pure rule engine — given recent daily profiles + config, return the set of triggered alerts (without action_taken). Keeping it pure makes it trivially testable.

- [ ] **Step 1: Write failing test**

Create `domains/providerprofile/alert_rules_test.go`:
```go
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

// 连续3天总分<40 → auto_disabled (critical) + dimension checks
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

// 连续3天总分>=70 且 当前是 disabled → auto_enabled (info)
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
	// NOTE: auto_enabled only fires when credential is currently disabled.
	// EvaluateAlerts is pure and assumes "currently disabled" when scores are
	// consistently high — the action layer (Task 6) gates the actual flip.
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./domains/providerprofile -run TestEvaluateAlerts -v
```

Expected: FAIL — `undefined: EvaluateAlerts`.

- [ ] **Step 3: Write minimal implementation**

Create `domains/providerprofile/alert_rules.go`:
```go
package providerprofile

import (
	"fmt"
	"time"
)

// EvaluateAlerts 在纯函数层评估所有告警规则，返回触发的告警列表（不含已采取动作）。
// profiles 必须按 ProfileDate 倒序排列（最新在前）。credentialID/providerID 用于填充告警实体。
//
// 规则（设计文档 §8.2 / §8.3）：
//  1. score_drop:   24h下降≥20 (warning) 或 7d下降≥30 (critical)
//  2. trend_drop:   连续3天下降，累计≥15 (critical)
//  3. dimension_low: 可用性或稳定性<60 (critical)
//  4. auto_disabled: 总分<40连续3天 或 可用性<50立即 (critical)
//  5. auto_enabled:  总分≥70连续3天 (info)
//
// NOTE: auto_enabled 由动作层（Task 6）根据 credential 当前是否 disabled 决定是否真正执行。
// 此函数在连续高分时也返回 auto_enabled 告警，由动作层过滤。
func EvaluateAlerts(profiles []DailyProfile, credentialID, providerID int64, cfg AlertConfig) []Alert {
	if len(profiles) == 0 {
		return nil
	}
	today := profiles[0]
	var alerts []Alert
	triggerDate := truncateDate(today.ProfileDate)

	// 1. score_drop
	if len(profiles) >= 2 {
		drop24 := profiles[1].TotalScore - today.TotalScore
		if drop24 >= cfg.ScoreDropThreshold7d {
			alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeScoreDrop,
				highestLevel(levelForDrop(drop24, cfg)), triggerDate, today.TotalScore, profiles[1].TotalScore, drop24,
				"", fmt.Sprintf("总分24小时内下降 %.1f 分", drop24), nil))
		} else if drop24 >= cfg.ScoreDropThreshold24h {
			alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeScoreDrop,
				AlertLevelWarning, triggerDate, today.TotalScore, profiles[1].TotalScore, drop24,
				"", fmt.Sprintf("总分24小时内下降 %.1f 分", drop24), nil))
		}
	}
	if len(profiles) >= 8 {
		drop7d := profiles[7].TotalScore - today.TotalScore
		if drop7d >= cfg.ScoreDropThreshold7d {
			alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeScoreDrop,
				AlertLevelCritical, triggerDate, today.TotalScore, profiles[7].TotalScore, drop7d,
				"", fmt.Sprintf("总分7天内下降 %.1f 分", drop7d), nil))
		}
	}

	// 2. trend_drop: 连续 N 天单调下降，累计≥TrendDropTotal
	if drops, total, ok := detectContinuousDrop(profiles, cfg.ContinuousDropDays); ok && total >= cfg.TrendDropTotal {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeTrendDrop,
			AlertLevelCritical, triggerDate, today.TotalScore, today.TotalScore+total, -total,
			"", fmt.Sprintf("连续 %d 天下降，累计 %.1f 分", len(drops), total), map[string]interface{}{"daily_drops": drops}))
	}

	// 3. dimension_low
	if today.AvailabilityScore < 60 {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeDimensionLow,
			AlertLevelCritical, triggerDate, today.TotalScore, 0, 0,
			"availability", fmt.Sprintf("可用性评分 %.1f 低于60", today.AvailabilityScore), nil))
	}
	if today.StabilityScore < 60 {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeDimensionLow,
			AlertLevelCritical, triggerDate, today.TotalScore, 0, 0,
			"stability", fmt.Sprintf("稳定性评分 %.1f 低于60", today.StabilityScore), nil))
	}

	// 4. auto_disabled: 总分<40连续N天 或 可用性<50立即
	lowScoreDays := isContiguousRecentDays(profiles, cfg.AutoDisableContinuousDays, func(p DailyProfile) bool {
		return p.TotalScore < cfg.AutoDisableThreshold
	})
	if lowScoreDays {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeAutoDisabled,
			AlertLevelCritical, triggerDate, today.TotalScore, 0, 0,
			"total_score", fmt.Sprintf("总分连续 %d 天低于 %.0f", cfg.AutoDisableContinuousDays, cfg.AutoDisableThreshold), nil))
	}
	if today.AvailabilityScore > 0 && today.AvailabilityScore < cfg.AutoDisableAvailThreshold {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeAutoDisabled,
			AlertLevelCritical, triggerDate, today.TotalScore, 0, 0,
			"availability", fmt.Sprintf("可用性 %.1f 低于 %.0f，立即禁用", today.AvailabilityScore, cfg.AutoDisableAvailThreshold), nil))
	}

	// 5. auto_enabled: 总分>=70连续N天
	highScoreDays := isContiguousRecentDays(profiles, cfg.AutoEnableContinuousDays, func(p DailyProfile) bool {
		return p.TotalScore >= cfg.AutoEnableThreshold
	})
	if highScoreDays {
		alerts = append(alerts, makeAlert(credentialID, providerID, AlertTypeAutoEnabled,
			AlertLevelInfo, triggerDate, today.TotalScore, 0, 0,
			"total_score", fmt.Sprintf("总分连续 %d 天达到 %.0f，可恢复", cfg.AutoEnableContinuousDays, cfg.AutoEnableThreshold), nil))
	}

	return alerts
}

// makeAlert 减少重复字段的构造样板
func makeAlert(credID, provID int64, typ AlertType, level AlertLevel, date time.Time,
	current, previous, change float64, dimension, msg string, details map[string]interface{}) Alert {
	return Alert{
		CredentialID:  credID,
		ProviderID:    provID,
		Type:          typ,
		Level:         level,
		TriggerDate:   date,
		CurrentScore:  current,
		PreviousScore: previous,
		ScoreChange:   change,
		Dimension:     dimension,
		Message:       msg,
		Details:       details,
	}
}

// levelForDrop 根据下降幅度是否达到7d阈值决定 critical
func levelForDrop(drop float64, cfg AlertConfig) AlertLevel {
	if drop >= cfg.ScoreDropThreshold7d {
		return AlertLevelCritical
	}
	return AlertLevelWarning
}

// detectContinuousDrop 检查从最新一天起是否连续 N 天都在下降，返回每日下降量与累计下降。
// profiles[0] 最新。下降 = profiles[i+1].TotalScore - profiles[i].TotalScore（前一天的分数减今天的）。
func detectContinuousDrop(profiles []DailyProfile, n int) (drops []float64, total float64, ok bool) {
	if len(profiles) < n+1 {
		return nil, 0, false
	}
	for i := 0; i < n; i++ {
		// 日期连续性
		if i > 0 {
			expected := profiles[i-1].ProfileDate.AddDate(0, 0, -1)
			if !profiles[i].ProfileDate.Equal(expected) {
				return nil, 0, false
			}
		}
		// 检查 profiles[i] 到 profiles[i+1] 这一段是否下降
		// 注意：需要 profiles[i+1]（更早一天）存在
		if i+1 >= len(profiles) {
			return nil, 0, false
		}
		// 连续性：profiles[i+1] 必须是 profiles[i] 的前一天
		if !profiles[i+1].ProfileDate.Equal(profiles[i].ProfileDate.AddDate(0, 0, -1)) {
			return nil, 0, false
		}
		d := profiles[i+1].TotalScore - profiles[i].TotalScore
		if d <= 0 {
			return nil, 0, false // 非下降即中断
		}
		drops = append(drops, d)
		total += d
	}
	return drops, total, true
}

// truncateDate 截断到当天 00:00:00
func truncateDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./domains/providerprofile -run TestEvaluateAlerts -v
```

Expected: PASS — all 6 tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w domains/providerprofile/alert_rules.go domains/providerprofile/alert_rules_test.go
git add domains/providerprofile/alert_rules.go domains/providerprofile/alert_rules_test.go
git commit -m "feat(providerprofile): add pure alert rule evaluation engine"
```

---

## Task 4: Credential lifecycle actor (disable/enable + whitelist check)

**Files:**
- Create: `domains/providerprofile/credential_actor.go`
- Test: `domains/providerprofile/credential_actor_test.go`

This wraps the credential lifecycle SQL (disable/enable) + whitelist + provider_events insert behind a testable interface. DB tests use the real pg17 via `setupTestDB` (already in `pg_store_test.go`, connects to `localhost:55432`). Gate them behind `TEST_DATABASE_URL` like other suites so CI without DB skips.

- [ ] **Step 1: Write failing test**

Create `domains/providerprofile/credential_actor_test.go`:
```go
package providerprofile_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipIfNoDB skips tests that need the real pg17. Mirrors internal/quality convention.
func skipIfNoDB(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB test")
	}
}

// dbPoolFromTestURL builds a pool from TEST_DATABASE_URL (e.g.
// "postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable").
func dbPoolFromTestURL(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// seedCredential inserts a throwaway active credential and returns its id.
// NOTE: credentials has NO "name" column — it's "label" (NOT NULL, no default).
// fp_slot_limit is NOT NULL with no default, so must be supplied.
// credentials.id has DEFAULT nextval('credentials_id_seq') (parity with 252; if the
// local DB lacks this default, the sequence must be added first — see Task 1's parity note).
func seedCredential(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO credentials (provider_id, label, status, lifecycle_status, manual_disabled, fp_slot_limit)
		VALUES ($1, $2, 'active', 'active', false, 0) RETURNING id`,
		99990001, "pp-test-cred").Scan(&id)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM credentials WHERE id=$1`, id)
	})
	return id
}

func TestPGCredentialActor_DisableThenEnable(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	credID := seedCredential(t, pool)

	// Disable
	err := actor.Disable(ctx, credID, "总分连续3天低于40")
	require.NoError(t, err)

	var lifecycle, avail, reason string
	var disabledAt *time.Time
	err = pool.QueryRow(ctx, `
		SELECT lifecycle_status, availability_state, auto_disabled_reason, auto_disabled_at
		FROM credentials WHERE id=$1`, credID).
		Scan(&lifecycle, &avail, &reason, &disabledAt)
	require.NoError(t, err)
	assert.Equal(t, "disabled", lifecycle)
	assert.Equal(t, "suspended", avail)
	assert.Equal(t, "总分连续3天低于40", reason)
	require.NotNil(t, disabledAt)

	// Enable (manual_disabled is false → should succeed)
	err = actor.Enable(ctx, credID, "总分连续3天达到70")
	require.NoError(t, err)

	err = pool.QueryRow(ctx, `
		SELECT lifecycle_status, availability_state, auto_enabled_reason, auto_disabled_at
		FROM credentials WHERE id=$1`, credID).
		Scan(&lifecycle, &avail, &reason, &disabledAt)
	require.NoError(t, err)
	assert.Equal(t, "active", lifecycle)
	assert.Equal(t, "ready", avail)
	assert.Equal(t, "总分连续3天达到70", reason)
	assert.Nil(t, disabledAt, "auto_disabled_at must be cleared on enable")
}

func TestPGCredentialActor_EnableRefusesManualDisabled(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	credID := seedCredential(t, pool)
	// admin manually disables
	_, err := pool.Exec(ctx, `UPDATE credentials SET manual_disabled=true, lifecycle_status='disabled' WHERE id=$1`, credID)
	require.NoError(t, err)

	// auto-enable must refuse
	err = actor.Enable(ctx, credID, "should not apply")
	assert.ErrorIs(t, err, providerprofile.ErrManualDisabled)
}

func TestPGCredentialActor_Whitelist(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	const provID int64 = 99990002
	// add to whitelist
	_, err := pool.Exec(ctx, `DELETE FROM provider_profile_whitelist WHERE provider_id=$1`, provID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO provider_profile_whitelist (provider_id, reason) VALUES ($1, 'test') ON CONFLICT (provider_id) DO NOTHING`, provID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_whitelist WHERE provider_id=$1`, provID)
	})

	whitelisted, err := actor.IsWhitelisted(ctx, provID)
	require.NoError(t, err)
	assert.True(t, whitelisted, "provider should be whitelisted")

	whitelisted, err = actor.IsWhitelisted(ctx, 99999999)
	require.NoError(t, err)
	assert.False(t, whitelisted)
}

func TestPGCredentialActor_CurrentLifecycle(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	credID := seedCredential(t, pool)

	lc, err := actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "active", lc.Status)
	assert.False(t, lc.ManualDisabled)

	_, err = pool.Exec(ctx, `UPDATE credentials SET manual_disabled=true WHERE id=$1`, credID)
	require.NoError(t, err)
	lc, err = actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.True(t, lc.ManualDisabled)
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
TEST_DATABASE_URL="postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable" \
  go test ./domains/providerprofile -run TestPGCredentialActor -v
```

Expected: FAIL — `undefined: PGCredentialActor`, `undefined: NewPGCredentialActor`, `undefined: ErrManualDisabled`.

- [ ] **Step 3: Write minimal implementation**

Create `domains/providerprofile/credential_actor.go`:
```go
package providerprofile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrManualDisabled 自动恢复被拒绝：该凭证被管理员手动禁用。
var ErrManualDisabled = errors.New("credential is manually disabled; cannot auto-enable")

// CredentialLifecycle 凭证当前生命周期快照
type CredentialLifecycle struct {
	ID             int64
	Status         string // credentials.status
	Lifecycle      string // credentials.lifecycle_status
	Availability   string // credentials.availability_state
	ManualDisabled bool
}

// CredentialActor 抽象对 credentials 表的禁用/启用副作用，便于测试替换。
type CredentialActor interface {
	// Disable 将凭证 lifecycle_status 置为 disabled 并记录 auto_disabled_*。
	// 幂等：已是 disabled 时直接返回 nil。
	Disable(ctx context.Context, credentialID int64, reason string) error

	// Enable 将凭证恢复为 active。若 manual_disabled=true 则返回 ErrManualDisabled。
	// 幂等：已是 active 时直接返回 nil。
	Enable(ctx context.Context, credentialID int64, reason string) error

	// IsWhitelisted 判断 provider 是否在自动禁用白名单中。
	IsWhitelisted(ctx context.Context, providerID int64) (bool, error)

	// CurrentLifecycle 读取当前生命周期快照。
	CurrentLifecycle(ctx context.Context, credentialID int64) (*CredentialLifecycle, error)

	// RecordEvent 向 provider_events 插入一条事件（可选实现，nil 时跳过）。
	RecordEvent(ctx context.Context, credentialID int64, kind string, payload map[string]interface{}) error
}

// PGCredentialActor PostgreSQL 实现
type PGCredentialActor struct {
	db *pgxpool.Pool
}

// NewPGCredentialActor 创建
func NewPGCredentialActor(db *pgxpool.Pool) *PGCredentialActor {
	return &PGCredentialActor{db: db}
}

// Disable 见接口文档
func (a *PGCredentialActor) Disable(ctx context.Context, credentialID int64, reason string) error {
	tag, err := a.db.Exec(ctx, `
		UPDATE credentials
		SET lifecycle_status     = 'disabled',
		    availability_state   = 'suspended',
		    auto_disabled_at     = now(),
		    auto_disabled_reason = $1,
		    state_updated_at     = now()
		WHERE id = $2
		  AND lifecycle_status = 'active'`,
		reason, credentialID)
	if err != nil {
		return fmt.Errorf("disable credential %d: %w", credentialID, err)
	}
	// tag.RowsAffected()==0 means already disabled or not found — treat as no-op success
	_ = tag
	return nil
}

// Enable 见接口文档
func (a *PGCredentialActor) Enable(ctx context.Context, credentialID int64, reason string) error {
	tag, err := a.db.Exec(ctx, `
		UPDATE credentials
		SET lifecycle_status     = 'active',
		    availability_state   = 'ready',
		    auto_enabled_at      = now(),
		    auto_enabled_reason  = $1,
		    auto_disabled_at     = NULL,
		    auto_disabled_reason = NULL,
		    state_updated_at     = now()
		WHERE id = $2
		  AND manual_disabled = false
		  AND lifecycle_status = 'disabled'`,
		reason, credentialID)
	if err != nil {
		return fmt.Errorf("enable credential %d: %w", credentialID, err)
	}
	if tag.RowsAffected() == 0 {
		// 区分：是不存在/已active，还是 manual_disabled 阻挡
		lc, lerr := a.CurrentLifecycle(ctx, credentialID)
		if lerr != nil {
			return lerr
		}
		if lc.ManualDisabled {
			return ErrManualDisabled
		}
		// 否则已经是 active，幂等成功
	}
	return nil
}

// IsWhitelisted 见接口文档
func (a *PGCredentialActor) IsWhitelisted(ctx context.Context, providerID int64) (bool, error) {
	var exists bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_profile_whitelist WHERE provider_id=$1)`, providerID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check whitelist for provider %d: %w", providerID, err)
	}
	return exists, nil
}

// CurrentLifecycle 见接口文档
func (a *PGCredentialActor) CurrentLifecycle(ctx context.Context, credentialID int64) (*CredentialLifecycle, error) {
	var lc CredentialLifecycle
	err := a.db.QueryRow(ctx, `
		SELECT id, COALESCE(status,'active'), COALESCE(lifecycle_status,'active'),
		       COALESCE(availability_state,'unknown'), COALESCE(manual_disabled,false)
		FROM credentials WHERE id=$1`, credentialID).
		Scan(&lc.ID, &lc.Status, &lc.Lifecycle, &lc.Availability, &lc.ManualDisabled)
	if err != nil {
		return nil, fmt.Errorf("read lifecycle for credential %d: %w", credentialID, err)
	}
	return &lc, nil
}

// RecordEvent 见接口文档
func (a *PGCredentialActor) RecordEvent(ctx context.Context, credentialID int64, kind string, payload map[string]interface{}) error {
	// provider_events.id 无默认值，必须显式取序列
	var payloadJSON []byte
	if payload != nil {
		b, err := jsonMarshal(payload)
		if err != nil {
			return fmt.Errorf("marshal event payload: %w", err)
		}
		payloadJSON = b
	}
	_, err := a.db.Exec(ctx, `
		INSERT INTO provider_events (id, credential_id, event_kind, payload_json, ts)
		VALUES (nextval('provider_events_id_seq'), $1, $2, $3, now())`,
		credentialID, kind, payloadJSON)
	if err != nil {
		return fmt.Errorf("insert provider_event: %w", err)
	}
	return nil
}

// jsonMarshal 复用包内已有的 json_helpers，避免在这里直接 import encoding/json 造成不一致
func jsonMarshal(v interface{}) ([]byte, error) {
	return marshalJSON(v) // 见 json_helpers.go
}

// truncateDate exported helper for actor consumers (re-exposed to avoid import cycle in tests)
var _ = time.Now // keep time import if future timestamps needed
```

> **Note:** `marshalJSON` must already exist in `domains/providerprofile/json_helpers.go`. Verify before this step; if the helper has a different name, adjust. (It does — checked: `json_helpers.go` exists.)

- [ ] **Step 4: Run test to verify it passes**

```bash
TEST_DATABASE_URL="postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable" \
  go test ./domains/providerprofile -run TestPGCredentialActor -v
```

Expected: PASS — 4 tests (DisableThenEnable, EnableRefusesManualDisabled, Whitelist, CurrentLifecycle).

If `marshalJSON` name mismatch: read `json_helpers.go`, fix the call, re-run.

- [ ] **Step 5: Commit**

```bash
gofmt -w domains/providerprofile/credential_actor.go domains/providerprofile/credential_actor_test.go
git add domains/providerprofile/credential_actor.go domains/providerprofile/credential_actor_test.go
git commit -m "feat(providerprofile): add credential lifecycle actor (disable/enable/whitelist/events)"
```

---

## Task 5: Alert store (persist alerts + dedupe)

**Files:**
- Create: `domains/providerprofile/pg_alert_store.go`
- Test: `domains/providerprofile/pg_alert_store_test.go`

`provider_profile_alerts` has NO unique constraint, so the store must dedupe: don't insert a second `auto_disabled` for the same (credential, trigger_date, alert_type). Strategy: check existence before insert for action-type alerts (`auto_disabled`, `auto_enabled`); for informational alerts (`score_drop`, `dimension_low`, `trend_drop`) we still insert (they're historical records) but cap at one per type per day.

- [ ] **Step 1: Write failing test**

Create `domains/providerprofile/pg_alert_store_test.go`:
```go
package providerprofile_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAlertStore(t *testing.T) providerprofile.AlertStore {
	skipIfNoDB(t)
	return providerprofile.NewPGAlertStore(dbPoolFromTestURL(t))
}

func TestPGAlertStore_SaveAndDedupe(t *testing.T) {
	store := newAlertStore(t)
	ctx := context.Background()
	date := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	a := providerprofile.Alert{
		CredentialID: 99990010, ProviderID: 99990011,
		Type: providerprofile.AlertTypeAutoDisabled, Level: providerprofile.AlertLevelCritical,
		TriggerDate: date, CurrentScore: 30, Message: "总分连续3天<40",
		Dimension: "total_score", ActionTaken: "disabled",
	}
	t.Cleanup(func() {
		_, _ = dbPoolFromTestURL(t).Exec(context.Background(),
			`DELETE FROM provider_profile_alerts WHERE credential_id=$1`, a.CredentialID)
	})

	require.NoError(t, store.SaveIfNew(ctx, &a))
	// 第二次同 (credential,date,type) 必须被去重，不报错也不新增
	require.NoError(t, store.SaveIfNew(ctx, &a))

	var n int
	err := dbPoolFromTestURL(t).QueryRow(ctx,
		`SELECT count(*) FROM provider_profile_alerts WHERE credential_id=$1 AND alert_type='auto_disabled' AND trigger_date=$2`,
		a.CredentialID, date).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "dedupe: only one auto_disabled per credential/day/type")
}

func TestPGAlertStore_RecentExists(t *testing.T) {
	store := newAlertStore(t)
	ctx := context.Background()
	date := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	credID := int64(99990020)
	t.Cleanup(func() {
		_, _ = dbPoolFromTestURL(t).Exec(context.Background(),
			`DELETE FROM provider_profile_alerts WHERE credential_id=$1`, credID)
	})

	require.NoError(t, store.SaveIfNew(ctx, &providerprofile.Alert{
		CredentialID: credID, ProviderID: 99990021,
		Type: providerprofile.AlertTypeScoreDrop, Level: providerlevel(),
		TriggerDate: date, Message: "drop",
	}))

	got, err := store.HasUnresolved(ctx, credID, providerprofile.AlertTypeScoreDrop, date)
	require.NoError(t, err)
	assert.True(t, got)

	got, err = store.HasUnresolved(ctx, credID, providerprofile.AlertTypeAutoDisabled, date)
	require.NoError(t, err)
	assert.False(t, got)
}

func providerlevel() providerprofile.AlertLevel { return providerprofile.AlertLevelWarning }
```

- [ ] **Step 2: Run test to verify it fails**

```bash
TEST_DATABASE_URL="postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable" \
  go test ./domains/providerprofile -run 'TestPGAlertStore' -v
```

Expected: FAIL — `undefined: AlertStore`, `undefined: NewPGAlertStore`.

- [ ] **Step 3: Write minimal implementation**

Create `domains/providerprofile/pg_alert_store.go`:
```go
package providerprofile

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AlertStore 告警持久化接口
type AlertStore interface {
	// SaveIfNew 保存告警；若同一 (credential, trigger_date, alert_type) 已存在则跳过（幂等）。
	SaveIfNew(ctx context.Context, a *Alert) error

	// HasUnresolved 查询指定 credential/date/type 是否已有未解决告警。
	HasUnresolved(ctx context.Context, credentialID int64, typ AlertType, date time.Time) (bool, error)
}

// PGAlertStore PostgreSQL 实现
type PGAlertStore struct {
	db *pgxpool.Pool
}

// NewPGAlertStore 创建
func NewPGAlertStore(db *pgxpool.Pool) *PGAlertStore {
	return &PGAlertStore{db: db}
}

// SaveIfNew 见接口文档
func (s *PGAlertStore) SaveIfNew(ctx context.Context, a *Alert) error {
	var detailsJSON []byte
	if a.Details != nil {
		b, err := marshalJSON(a.Details)
		if err != nil {
			return fmt.Errorf("marshal alert details: %w", err)
		}
		detailsJSON = b
	}
	// 先查重，再插入。provider_profile_alerts 无唯一约束，应用层去重。
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM provider_profile_alerts
		              WHERE credential_id=$1 AND trigger_date=$2 AND alert_type=$3)`,
		a.CredentialID, a.TriggerDate, a.Type).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check existing alert: %w", err)
	}
	if exists {
		return nil
	}

	_, err = s.db.Exec(ctx, `
		INSERT INTO provider_profile_alerts (
			credential_id, provider_id, alert_type, alert_level, trigger_date,
			current_score, previous_score, score_change, dimension,
			message, details, action_taken)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		a.CredentialID, a.ProviderID, a.Type, a.Level, a.TriggerDate,
		nullFloat(a.CurrentScore), nullFloat(a.PreviousScore), nullFloat(a.ScoreChange),
	_nullableString(a.Dimension), a.Message, detailsJSON, _nullableString(a.ActionTaken))
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	return nil
}

// HasUnresolved 见接口文档
func (s *PGAlertStore) HasUnresolved(ctx context.Context, credentialID int64, typ AlertType, date time.Time) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM provider_profile_alerts
		              WHERE credential_id=$1 AND alert_type=$2 AND trigger_date=$3
		                AND resolved_at IS NULL)`,
		credentialID, typ, date).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check unresolved alert: %w", err)
	}
	return exists, nil
}

func nullFloat(f float64) interface{} {
	if f == 0 {
		return nil
	}
	return f
}

func _nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
TEST_DATABASE_URL="postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable" \
  go test ./domains/providerprofile -run 'TestPGAlertStore' -v
```

Expected: PASS — 2 tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w domains/providerprofile/pg_alert_store.go domains/providerprofile/pg_alert_store_test.go
git add domains/providerprofile/pg_alert_store.go domains/providerprofile/pg_alert_store_test.go
git commit -m "feat(providerprofile): add alert store with per-day dedupe"
```

---

## Task 6: AlertEngine — orchestrate evaluate → act → persist

**Files:**
- Create: `domains/providerprofile/alert_engine.go`
- Test: `domains/providerprofile/alert_engine_test.go`

The engine ties together: for each active credential → load recent daily profiles → `EvaluateAlerts` → apply actions (disable/enable with whitelist guard) → persist alerts + events. Whitelist suppresses auto-disable (alert still recorded as `action_taken='none'` with a note). `auto_enabled` only acts if the credential is currently disabled.

- [ ] **Step 1: Write failing test**

Create `domains/providerprofile/alert_engine_test.go`:
```go
package providerprofile_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubActor is an in-memory CredentialActor for engine unit tests (no DB).
type stubActor struct {
	disabled       map[int64]string
	enabled        map[int64]string
	manualDisabled map[int64]bool
	whitelist      map[int64]bool
	events         int
}

func newStubActor() *stubActor {
	return &stubActor{
		disabled: map[int64]string{}, enabled: map[int64]string{},
		manualDisabled: map[int64]bool{}, whitelist: map[int64]bool{},
	}
}

func (s *stubActor) Disable(_ context.Context, id int64, reason string) error {
	s.disabled[id] = reason
	return nil
}
func (s *stubActor) Enable(_ context.Context, id int64, reason string) error {
	if s.manualDisabled[id] {
		return providerprofile.ErrManualDisabled
	}
	s.enabled[id] = reason
	return nil
}
func (s *stubActor) IsWhitelisted(_ context.Context, pid int64) (bool, error) {
	return s.whitelist[pid], nil
}
func (s *stubActor) CurrentLifecycle(_ context.Context, id int64) (*providerprofile.CredentialLifecycle, error) {
	return &providerprofile.CredentialLifecycle{ID: id, Lifecycle: "active", ManualDisabled: s.manualDisabled[id]}, nil
}
func (s *stubActor) RecordEvent(context.Context, int64, string, map[string]interface{}) error {
	s.events++
	return nil
}

// stubAlertStore records saved alerts in memory.
type stubAlertStore struct {
	saved []*providerprofile.Alert
}

func (s *stubAlertStore) SaveIfNew(_ context.Context, a *providerprofile.Alert) error {
	s.saved = append(s.saved, a)
	return nil
}
func (s *stubAlertStore) HasUnresolved(context.Context, int64, providerprofile.AlertType, time.Time) (bool, error) {
	return false, nil
}

// stubProfileSource returns canned daily profiles per credential.
type stubProfileSource struct {
	profiles map[int64][]providerprofile.DailyProfile
	provider map[int64]int64
}

func (s *stubProfileSource) Recent(_ context.Context, credID int64, days int) ([]providerprofile.DailyProfile, error) {
	return s.profiles[credID], nil
}

func TestAlertEngine_DisablesLowScore(t *testing.T) {
	actor := newStubActor()
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{
		profiles: map[int64][]providerprofile.DailyProfile{
			42: {mkPub(now, 30), mkPub(now.AddDate(0, 0, -1), 30), mkPub(now.AddDate(0, 0, -2), 30)},
		},
		provider: map[int64]int64{42: 4242},
	}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	got, err := eng.EvaluateCredential(context.Background(), 42)
	require.NoError(t, err)

	assert.Equal(t, "disabled", got.Action, "should auto-disable")
	assert.Contains(t, actor.disabled, int64(42), "Disable called")
	assert.NotEmpty(t, store.saved, "alert saved")
}

func TestAlertEngine_WhitelistBlocksDisable(t *testing.T) {
	actor := newStubActor()
	actor.whitelist[4242] = true
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{
		profiles: map[int64][]providerprofile.DailyProfile{
			42: {mkPub(now, 30), mkPub(now.AddDate(0, 0, -1), 30), mkPub(now.AddDate(0, 0, -2), 30)},
		},
		provider: map[int64]int64{42: 4242},
	}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })

	got, err := eng.EvaluateCredential(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "none", got.Action, "whitelist suppresses disable")
	assert.NotContains(t, actor.disabled, int64(42), "Disable NOT called for whitelisted provider")
}

func TestAlertEngine_EnablesRecoveredButOnlyIfDisabled(t *testing.T) {
	actor := newStubActor()
	actor.disabled[42] = "previously auto-disabled" // simulate currently-disabled
	actor.manualDisabled[42] = false
	// CurrentLifecycle needs to report disabled for Enable path to trigger
	store := &stubAlertStore{}
	now := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	src := &stubProfileSource{
		profiles: map[int64][]providerprofile.DailyProfile{
			42: {mkPub(now, 75), mkPub(now.AddDate(0, 0, -1), 75), mkPub(now.AddDate(0, 0, -2), 75)},
		},
		provider: map[int64]int64{42: 4242},
	}
	eng := providerprofile.NewAlertEngine(src, store, actor, providerprofile.DefaultAlertConfig())
	eng.SetClock(func() time.Time { return now })
	// make CurrentLifecycle report disabled
	actor2 := &disabledReportingActor{stub: actor, disabledIDs: map[int64]bool{42: true}}

	eng2 := providerprofile.NewAlertEngine(src, store, actor2, providerprofile.DefaultAlertConfig())
	eng2.SetClock(func() time.Time { return now })
	got, err := eng2.EvaluateCredential(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, "enabled", got.Action)
	assert.Contains(t, actor2.enabled, int64(42))
}

// disabledReportingActor wraps stubActor to report lifecycle=disabled for given ids.
type disabledReportingActor struct {
	stub        *stubActor
	disabledIDs map[int64]bool
}

func (d *disabledReportingActor) Disable(ctx context.Context, id int64, r string) error {
	return d.stub.Disable(ctx, id, r)
}
func (d *disabledReportingActor) Enable(ctx context.Context, id int64, r string) error {
	return d.stub.Enable(ctx, id, r)
}
func (d *disabledReportingActor) IsWhitelisted(ctx context.Context, p int64) (bool, error) {
	return d.stub.IsWhitelisted(ctx, p)
}
func (d *disabledReportingActor) CurrentLifecycle(_ context.Context, id int64) (*providerprofile.CredentialLifecycle, error) {
	lc := &providerprofile.CredentialLifecycle{ID: id, ManualDisabled: d.stub.manualDisabled[id]}
	if d.disabledIDs[id] {
		lc.Lifecycle = "disabled"
	} else {
		lc.Lifecycle = "active"
	}
	return lc, nil
}
func (d *disabledReportingActor) RecordEvent(ctx context.Context, id int64, k string, p map[string]interface{}) error {
	return d.stub.RecordEvent(ctx, id, k, p)
}

func mkPub(date time.Time, total float64) providerprofile.DailyProfile {
	return providerprofile.DailyProfile{ProfileDate: date, TotalScore: total, AvailabilityScore: 90}
}

// ensure unused import guard
var _ = os.Getenv
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./domains/providerprofile -run TestAlertEngine -v
```

Expected: FAIL — `undefined: AlertEngine`, `NewAlertEngine`, etc.

- [ ] **Step 3: Write minimal implementation**

Create `domains/providerprofile/alert_engine.go`:
```go
package providerprofile

import (
	"context"
	"fmt"
	"time"
)

// ProfileSource 向告警引擎提供"某 credential 最近 N 天的 daily profiles"。
// 由聚合后的 provider_profile_daily 表实现（见 PGProfileSource）。
type ProfileSource interface {
	Recent(ctx context.Context, credentialID int64, days int) ([]DailyProfile, error)
}

// EvaluateResult 单次评估的结果（供日志/测试断言）
type EvaluateResult struct {
	CredentialID int64
	ProviderID   int64
	Action       string // disabled / enabled / none
	Alerts       []Alert
}

// AlertEngine 评估并执行自动处理。
type AlertEngine struct {
	profiles ProfileSource
	alerts   AlertStore
	actor    CredentialActor
	cfg      AlertConfig
	now      func() time.Time
}

// NewAlertEngine 创建告警引擎
func NewAlertEngine(profiles ProfileSource, alerts AlertStore, actor CredentialActor, cfg AlertConfig) *AlertEngine {
	return &AlertEngine{profiles: profiles, alerts: alerts, actor: actor, cfg: cfg, now: time.Now}
}

// SetClock 注入时钟（测试用）
func (e *AlertEngine) SetClock(f func() time.Time) { e.now = f }

// EvaluateAll 对所有活跃凭证评估。credentialIDs 由调用方传入（来自 GatewayCredentialLister）。
func (e *AlertEngine) EvaluateAll(ctx context.Context, credentialIDs []int64, providerOf func(int64) int64) []EvaluateResult {
	var results []EvaluateResult
	for _, credID := range credentialIDs {
		res, err := e.EvaluateCredential(ctx, credID)
		if err != nil {
			// 单个失败不影响其他；调用方记录日志即可
			res = EvaluateResult{CredentialID: credID, Action: "none"}
		}
		res.ProviderID = providerOf(credID)
		results = append(results, res)
	}
	return results
}

// EvaluateCredential 评估并执行单个凭证。
func (e *AlertEngine) EvaluateCredential(ctx context.Context, credentialID int64) (EvaluateResult, error) {
	// 取最近 8 天（7d 对比 + 今天）
	profiles, err := e.profiles.Recent(ctx, credentialID, 8)
	if err != nil {
		return EvaluateResult{CredentialID: credentialID, Action: "none"}, fmt.Errorf("load profiles: %w", err)
	}
	if len(profiles) == 0 {
		return EvaluateResult{CredentialID: credentialID, Action: "none"}, nil
	}
	providerID := profiles[0].ProviderID

	alerts := EvaluateAlerts(profiles, credentialID, providerID, e.cfg)
	res := EvaluateResult{CredentialID: credentialID, ProviderID: providerID, Alerts: alerts, Action: "none"}

	// 1. 先处理 auto_disabled
	for _, a := range alerts {
		if a.Type != AlertTypeAutoDisabled {
			continue
		}
		// 白名单保护：仍记录告警但不执行禁用
		wl, werr := e.actor.IsWhitelisted(ctx, providerID)
		if werr != nil {
			return res, werr
		}
		if wl {
			a.ActionTaken = "none"
			a.Details = map[string]interface{}{"suppressed": "whitelist"}
			_ = e.alerts.SaveIfNew(ctx, &a)
			_ = e.actor.RecordEvent(ctx, credentialID, "profile_alert_suppressed", map[string]interface{}{"type": string(a.Type), "reason": "whitelist"})
			res.Action = "none"
			return res, nil
		}
		if err := e.actor.Disable(ctx, credentialID, a.Message); err != nil {
			return res, fmt.Errorf("disable credential: %w", err)
		}
		a.ActionTaken = "disabled"
		_ = e.alerts.SaveIfNew(ctx, &a)
		_ = e.actor.RecordEvent(ctx, credentialID, "profile_auto_disabled", map[string]interface{}{"reason": a.Message, "dimension": a.Dimension, "score": a.CurrentScore})
		res.Action = "disabled"
		return res, nil
	}

	// 2. 处理 auto_enabled：仅当当前是 disabled 才执行
	for _, a := range alerts {
		if a.Type != AlertTypeAutoEnabled {
			continue
		}
		lc, lerr := e.actor.CurrentLifecycle(ctx, credentialID)
		if lerr != nil {
			return res, lerr
		}
		if lc.Lifecycle != "disabled" {
			// 当前未禁用，无需恢复；丢弃该告警
			continue
		}
		if lc.ManualDisabled {
			// 管理员手动禁用，不自动恢复；记录但不动作
			a.ActionTaken = "none"
			a.Details = map[string]interface{}{"suppressed": "manual_disabled"}
			_ = e.alerts.SaveIfNew(ctx, &a)
			continue
		}
		if err := e.actor.Enable(ctx, credentialID, a.Message); err != nil {
			// ErrManualDisabled 等视为"不动作"
			a.ActionTaken = "none"
			_ = e.alerts.SaveIfNew(ctx, &a)
			continue
		}
		a.ActionTaken = "enabled"
		_ = e.alerts.SaveIfNew(ctx, &a)
		_ = e.actor.RecordEvent(ctx, credentialID, "profile_auto_enabled", map[string]interface{}{"reason": a.Message, "score": a.CurrentScore})
		res.Action = "enabled"
		return res, nil
	}

	// 3. 记录其余告警（score_drop / trend_drop / dimension_low）—— 仅告警，不动作
	for _, a := range alerts {
		if a.Type == AlertTypeAutoDisabled || a.Type == AlertTypeAutoEnabled {
			continue
		}
		a.ActionTaken = "none"
		_ = e.alerts.SaveIfNew(ctx, &a)
	}

	return res, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./domains/providerprofile -run TestAlertEngine -v
```

Expected: PASS — 3 tests.

- [ ] **Step 5: Commit**

```bash
gofmt -w domains/providerprofile/alert_engine.go domains/providerprofile/alert_engine_test.go
git add domains/providerprofile/alert_engine.go domains/providerprofile/alert_engine_test.go
git commit -m "feat(providerprofile): add AlertEngine orchestrating evaluate→act→persist"
```

---

## Task 7: PGProfileSource + wire aggregator first-run fix

**Files:**
- Modify: `domains/providerprofile/pg_profile_store.go` (add `Recent` method if missing)
- Modify: `bg/provider_profile_workers.go` (aggregator runs once on start)
- Test: `domains/providerprofile/pg_profile_store_test.go` (extend)

- [ ] **Step 1: Verify GetRecentProfiles exists**

```bash
grep -n "GetRecentProfiles\|func.*Recent" domains/providerprofile/pg_profile_store.go
```

If a method `Recent(ctx, credentialID, days)` matching `ProfileSource` exists, skip to Step 3. Otherwise add a `PGProfileSource` adapter.

- [ ] **Step 2: Add PGProfileSource adapter (if needed)**

Add to `domains/providerprofile/pg_profile_store.go`. **Note:** `GetRecentProfiles` returns `[]*DailyProfile` (pointers), but `ProfileSource.Recent` uses `[]DailyProfile` (values) — dereference in the adapter.

```go
// PGProfileSource adapts PGProfileStore to the AlertEngine's ProfileSource interface.
type PGProfileSource struct {
	store *PGProfileStore
}

// NewPGProfileSource creates a ProfileSource backed by PGProfileStore.
func NewPGProfileSource(store *PGProfileStore) *PGProfileSource {
	return &PGProfileSource{store: store}
}

// Recent returns the last N daily profiles ordered by date DESC.
// GetRecentProfiles returns []*DailyProfile; we dereference to []DailyProfile
// to satisfy the ProfileSource interface (value semantics are fine for read-only evaluation).
func (s *PGProfileSource) Recent(ctx context.Context, credentialID int64, days int) ([]DailyProfile, error) {
	profiles, err := s.store.GetRecentProfiles(ctx, credentialID, days)
	if err != nil {
		return nil, err
	}
	out := make([]DailyProfile, len(profiles))
	for i, p := range profiles {
		out[i] = *p
	}
	return out, nil
}
```

`GetRecentProfiles` is already defined (pg_profile_store.go:142) with `ORDER BY profile_date DESC` — exactly the ordering `EvaluateAlerts` needs (most recent first).

- [ ] **Step 3: Fix aggregator first-run (no data until ticker fires)**

In `bg/provider_profile_workers.go`, modify `ProfileAggregator.run` to aggregate once on start (like the collector does). Edit the `run` method:

Current (lines ~164-178):
```go
func (a *ProfileAggregator) run(ctx context.Context) {
	defer close(a.done)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.aggregate(ctx)
		}
	}
}
```

Change to aggregate **today** once on start (so today's partial data produces a profile), then continue aggregating **yesterday** on each tick:
```go
func (a *ProfileAggregator) run(ctx context.Context) {
	defer close(a.done)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	// Run once on start for TODAY so a freshly-enabled system populates
	// provider_profile_daily without waiting a full day. Subsequent ticks
	// aggregate yesterday (the normal daily cadence).
	a.aggregateAt(ctx, time.Now())

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.aggregate(ctx) // aggregates yesterday
		}
	}
}

// aggregateAt aggregates a specific date.
func (a *ProfileAggregator) aggregateAt(ctx context.Context, date time.Time) {
	aggregateCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := a.aggregator.AggregateDailyProfiles(aggregateCtx, date); err != nil {
		slog.Error("provider profile aggregation failed", "error", err, "date", date.Format("2006-01-02"))
		return
	}
	slog.Info("provider profile aggregation completed", "date", date.Format("2006-01-02"))
}
```

And simplify `aggregate` to call `aggregateAt` with yesterday:
```go
func (a *ProfileAggregator) aggregate(ctx context.Context) {
	a.aggregateAt(ctx, time.Now().AddDate(0, 0, -1))
}
```

- [ ] **Step 4: Build + run aggregator tests**

```bash
go build ./bg/...
go test ./bg/... -run ProfileAggregator -v
```

Expected: build clean; existing aggregator tests pass.

- [ ] **Step 5: Commit**

```bash
gofmt -w bg/provider_profile_workers.go domains/providerprofile/pg_profile_store.go
git add bg/provider_profile_workers.go domains/providerprofile/pg_profile_store.go
git commit -m "fix(providerprofile): aggregate today on start so daily profiles populate same-day"
```

---

## Task 8: ProfileAlertWorker background worker

**Files:**
- Modify: `bg/provider_profile_workers.go` (add `ProfileAlertWorker`)
- Modify: `cmd/gateway/provider_profile_init.go` (wire it in)

- [ ] **Step 1: Add the worker**

Append to `bg/provider_profile_workers.go`:
```go
// ProfileAlertWorker runs alert evaluation + auto-handling daily, after the aggregator.
type ProfileAlertWorker struct {
	engine     *providerprofile.AlertEngine
	lister     *providerprofile.GatewayCredentialLister
	interval   time.Duration
	cancel     context.CancelFunc
	done       chan struct{}
}

// NewProfileAlertWorker creates the alert worker.
// db is used to build the PG-backed profile source, alert store, and credential actor.
func NewProfileAlertWorker(db *pgxpool.Pool, interval time.Duration) *ProfileAlertWorker {
	profileStore := providerprofile.NewPGProfileStore(db)
	profileSource := providerprofile.NewPGProfileSource(profileStore)
	alertStore := providerprofile.NewPGAlertStore(db)
	actor := providerprofile.NewPGCredentialActor(db)
	lister := providerprofile.NewGatewayCredentialLister(db)
	engine := providerprofile.NewAlertEngine(profileSource, alertStore, actor, providerprofile.DefaultAlertConfig())
	return &ProfileAlertWorker{engine: engine, lister: lister, interval: interval, done: make(chan struct{})}
}

// Start begins the alert loop.
func (w *ProfileAlertWorker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.run(ctx)
	slog.Info("provider profile alert worker started", "interval", w.interval)
}

// Stop gracefully stops.
func (w *ProfileAlertWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
		slog.Info("provider profile alert worker stopped")
	}
}

func (w *ProfileAlertWorker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	// Run once shortly after start (give aggregator first run a moment to finish).
	time.AfterFunc(2*time.Minute, func() { w.evaluate(ctx) })

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.evaluate(ctx)
		}
	}
}

func (w *ProfileAlertWorker) evaluate(ctx context.Context) {
	evalCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	credIDs, err := w.lister.ListActiveCredentials(evalCtx)
	if err != nil {
		slog.Error("provider profile alert: list credentials failed", "error", err)
		return
	}
	// Also evaluate credentials that are currently auto-disabled (for recovery),
	// which are NOT in ListActiveCredentials (lifecycle_status='disabled').
	disabledIDs, err := w.listAutoDisabledCredentials(evalCtx)
	if err != nil {
		slog.Error("provider profile alert: list disabled credentials failed", "error", err)
	} else {
		credIDs = append(credIDs, disabledIDs...)
	}

	var disabled, enabled, alerted int
	for _, credID := range credIDs {
		res, err := w.engine.EvaluateCredential(evalCtx, credID)
		if err != nil {
			slog.Warn("provider profile alert: evaluate failed", "credential_id", credID, "error", err)
			continue
		}
		switch res.Action {
		case "disabled":
			disabled++
		case "enabled":
			enabled++
		}
		alerted += len(res.Alerts)
	}
	slog.Info("provider profile alert evaluation completed",
		"credentials_evaluated", len(credIDs), "disabled", disabled, "enabled", enabled, "alerts", alerted)
}

// listAutoDisabledCredentials returns credential ids currently auto-disabled
// (lifecycle_status='disabled' AND manual_disabled=false AND auto_disabled_at IS NOT NULL).
func (w *ProfileAlertWorker) listAutoDisabledCredentials(ctx context.Context) ([]int64, error) {
	rows, err := providerprofile.QueryAutoDisabledCredentials(ctx, w.lister)
	if err != nil {
		return nil, err
	}
	return rows, nil
}
```

Then add the helper to `domains/providerprofile/adapters.go` (it needs the pool; expose it via the lister's DB):
```go
// QueryAutoDisabledCredentials returns credential ids that were auto-disabled
// (so the alert engine can evaluate them for recovery).
func QueryAutoDisabledCredentials(ctx context.Context, l *GatewayCredentialLister) ([]int64, error) {
	rows, err := l.db.Query(ctx, `
		SELECT id FROM credentials
		WHERE lifecycle_status = 'disabled'
		  AND manual_disabled = false
		  AND auto_disabled_at IS NOT NULL
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query auto-disabled credentials: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan credential id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
```

- [ ] **Step 2: Wire into cmd/gateway/provider_profile_init.go**

Modify `ProviderProfileWorkers` struct and `initProviderProfile`/`stopProviderProfile`:

In the struct (add field):
```go
type ProviderProfileWorkers struct {
	collector  *bg.ProfileCollector
	aggregator *bg.ProfileAggregator
	cleaner    *bg.ProfileCleaner
	alerts     *bg.ProfileAlertWorker
}
```

In `initProviderProfile`, after creating cleaner, add alert interval + worker:
```go
	alertSeconds := int64(86400) // daily, after aggregator
	if envVal := os.Getenv("LLM_GATEWAY_PROVIDER_PROFILE_ALERT_INTERVAL"); envVal != "" {
		if v, err := strconv.ParseInt(envVal, 10, 64); err == nil {
			alertSeconds = v
		}
	}
	if alertSeconds <= 0 {
		alertSeconds = int64(settings.GetPlatformDuration("provider_profile.alert_interval", 86400))
	}
	alertInterval := time.Duration(alertSeconds) * time.Second

	alerts := bg.NewProfileAlertWorker(pool, alertInterval)
	alerts.Start()
```

Add `alerts: alerts` to the returned struct.

In `stopProviderProfile` add:
```go
	if workers.alerts != nil {
		workers.alerts.Stop()
	}
```

- [ ] **Step 3: Build + vet**

```bash
go build ./...
go vet ./domains/providerprofile/... ./bg/... ./cmd/gateway/...
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
gofmt -w bg/provider_profile_workers.go domains/providerprofile/adapters.go cmd/gateway/provider_profile_init.go
git add bg/provider_profile_workers.go domains/providerprofile/adapters.go cmd/gateway/provider_profile_init.go
git commit -m "feat(providerprofile): add ProfileAlertWorker and wire into gateway startup"
```

---

## Task 9: Integration test (DB-backed end-to-end)

**Files:**
- Modify: `domains/providerprofile/integration_test.go` (add alert-engine scenario)

- [ ] **Step 1: Add integration test**

Append to `domains/providerprofile/integration_test.go` (inside the `providerprofile_test` package, gated on `TEST_DATABASE_URL`):
```go
func TestIntegration_AlertEngineAutoDisableEnable(t *testing.T) {
	skipIfNoDB(t) // reuse from credential_actor_test.go (same package)
	pool := dbPoolFromTestURL(t)
	ctx := context.Background()

	// seed a credential + 3 days of low-score daily profiles
	credID := seedCredential(t, pool)
	now := time.Now().UTC()
	for _, off := range []int{0, -1, -2} {
		_, err := pool.Exec(ctx, `
			INSERT INTO provider_profile_daily
			  (credential_id, provider_id, profile_date, total_score, availability_score, stability_score)
			VALUES ($1, 99990001, $2, 30, 90, 90)
			ON CONFLICT (credential_id, profile_date) DO UPDATE SET total_score=EXCLUDED.total_score`,
			credID, now.AddDate(0, 0, off).Truncate(24*time.Hour))
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_alerts WHERE credential_id=$1`, credID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_daily WHERE credential_id=$1`, credID)
	})

	profileStore := providerprofile.NewPGProfileStore(pool)
	src := providerprofile.NewPGProfileSource(profileStore)
	alertStore := providerprofile.NewPGAlertStore(pool)
	actor := providerprofile.NewPGCredentialActor(pool)
	eng := providerprofile.NewAlertEngine(src, alertStore, actor, providerprofile.DefaultAlertConfig())

	res, err := eng.EvaluateCredential(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "disabled", res.Action)

	// verify lifecycle flipped
	lc, err := actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "disabled", lc.Lifecycle)

	// now seed 3 high-score days and verify recovery
	for _, off := range []int{0, -1, -2} {
		_, err := pool.Exec(ctx, `
			UPDATE provider_profile_daily SET total_score=75 WHERE credential_id=$1 AND profile_date=$2`,
			credID, now.AddDate(0, 0, off).Truncate(24*time.Hour))
		require.NoError(t, err)
	}
	res, err = eng.EvaluateCredential(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "enabled", res.Action)

	lc, err = actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "active", lc.Lifecycle)
}
```

- [ ] **Step 2: Run integration test**

```bash
TEST_DATABASE_URL="postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable" \
  go test ./domains/providerprofile -run TestIntegration_AlertEngineAutoDisableEnable -v
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -w domains/providerprofile/integration_test.go
git add domains/providerprofile/integration_test.go
git commit -m "test(providerprofile): integration test for auto-disable/enable cycle"
```

---

## Task 10: Finalize — full suite + docs

- [ ] **Step 1: Run the full providerprofile suite**

```bash
go test ./domains/providerprofile/... -v
```

Pure tests pass without DB. DB tests run with TEST_DATABASE_URL set:
```bash
TEST_DATABASE_URL="postgres://maintain:maintain@localhost:55432/llm_gateway?sslmode=disable" \
  go test ./domains/providerprofile/... -v
```

Expected: all PASS.

- [ ] **Step 2: Run whole project build + vet**

```bash
go build ./... && go vet ./...
```

Expected: clean.

- [ ] **Step 3: Update README**

Append a section to `domains/providerprofile/README.md` documenting:
- AlertEngine + ProfileAlertWorker (4th worker, runs daily)
- Auto-disable/auto-enable behavior + whitelist table
- New env vars: `LLM_GATEWAY_PROVIDER_PROFILE_ALERT_INTERVAL`
- How to add a provider to the whitelist: `INSERT INTO provider_profile_whitelist (provider_id, reason, added_by) VALUES (...)`

- [ ] **Step 4: Commit**

```bash
git add domains/providerprofile/README.md
git commit -m "docs(providerprofile): document alert engine and auto-handling"
```

---

## Self-review checklist (run after writing — already done by plan author)

- [x] Spec coverage (design §8): score_drop ✓, trend_drop ✓, dimension_low ✓, auto_disabled ✓, auto_enabled ✓, whitelist ✓, manual_disabled guard ✓, provider_events ✓, alerts dedupe ✓
- [x] No placeholders — every code step has full code
- [x] Type consistency: `AlertType`/`AlertLevel` constants used identically across tasks; `CredentialActor` interface implemented identically by `PGCredentialActor` and test stubs; `ProfileSource.Recent` signature matches `PGProfileSource.Recent`
- [x] Integration points verified against real schema (credentials.lifecycle_status, provider_events.id sequence, provider_profile_alerts no-unique-constraint)
