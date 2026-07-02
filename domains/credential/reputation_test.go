package credential

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustDate(t *testing.T, daysAgo int) time.Time {
	t.Helper()
	base := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, -daysAgo)
}

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func newStoreWithFixedClock(t *testing.T, now time.Time) *InMemoryReputationStore {
	t.Helper()
	s := NewInMemoryReputationStore()
	s.now = fixedNow(now)
	return s
}

// ---------------------------------------------------------------------------
// InMemoryReputationStore
// ---------------------------------------------------------------------------

func TestInMemoryStore_SaveAndGetTimeseries(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	row := &TimeseriesRow{
		ProviderID:       "p1",
		Model:            "m1",
		Date:             now.AddDate(0, 0, -1),
		ReliabilityScore: 0.95,
		AvgLatencyMs:     200,
		ErrorRate:        0.05,
		RequestCount:     100,
		SuccessCount:     95,
	}
	if err := store.SaveTimeseries(ctx, row); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if row.ID == 0 {
		t.Errorf("expected ID assigned")
	}

	rows, err := store.GetTimeseries(ctx, "p1", "m1", 7)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if math.Abs(rows[0].ReliabilityScore-0.95) > 1e-9 {
		t.Errorf("reliability mismatch: %v", rows[0].ReliabilityScore)
	}
}

func TestInMemoryStore_SaveUpsertsSameDay(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	date := now.AddDate(0, 0, -1)
	if err := store.SaveTimeseries(ctx, &TimeseriesRow{ProviderID: "p1", Model: "m1", Date: date, ReliabilityScore: 0.9, RequestCount: 100}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTimeseries(ctx, &TimeseriesRow{ProviderID: "p1", Model: "m1", Date: date, ReliabilityScore: 0.8, RequestCount: 200}); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.GetTimeseries(ctx, "p1", "m1", 7)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (upsert), got %d", len(rows))
	}
	if math.Abs(rows[0].ReliabilityScore-0.8) > 1e-9 {
		t.Errorf("expected upsert to overwrite reliability: %v", rows[0].ReliabilityScore)
	}
	if rows[0].RequestCount != 200 {
		t.Errorf("expected request_count=200: %v", rows[0].RequestCount)
	}
}

func TestInMemoryStore_GetTimeseriesSortsAscending(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p", Model: "m",
			Date: now.AddDate(0, 0, -i), // 倒序插入
		})
	}
	rows, _ := store.GetTimeseries(ctx, "p", "m", 7)
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows, got %d", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Date.Before(rows[i-1].Date) {
			t.Errorf("rows not sorted ascending at %d", i)
		}
	}
}

func TestInMemoryStore_GetTimeseriesRespectsDaysCutoff(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p", Model: "m",
			Date: now.AddDate(0, 0, -i),
		})
	}
	rows, _ := store.GetTimeseries(ctx, "p", "m", 3)
	if len(rows) != 3 {
		t.Errorf("expected 3 rows (3-day window), got %d", len(rows))
	}
}

func TestInMemoryStore_ListProviderModelPairs(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	_ = store.SaveTimeseries(ctx, &TimeseriesRow{ProviderID: "a", Model: "m1", Date: now})
	_ = store.SaveTimeseries(ctx, &TimeseriesRow{ProviderID: "a", Model: "m2", Date: now})
	_ = store.SaveTimeseries(ctx, &TimeseriesRow{ProviderID: "b", Model: "m1", Date: now})
	pairs, err := store.ListProviderModelPairs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 3 {
		t.Errorf("expected 3 unique pairs, got %d: %+v", len(pairs), pairs)
	}
}

func TestInMemoryStore_RecordAndGetIncidents(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	if err := store.RecordIncident(ctx, &Incident{ProviderID: "p", Model: "m", Type: IncidentOutage, Impact: ImpactHigh}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordIncident(ctx, &Incident{ProviderID: "p", Model: "m", Type: IncidentAuthFailure, Impact: ImpactCritical}); err != nil {
		t.Fatal(err)
	}
	incidents, _ := store.GetRecentIncidents(ctx, "p", "m", 30)
	if len(incidents) != 2 {
		t.Fatalf("expected 2 incidents, got %d", len(incidents))
	}
	// 倒序
	if incidents[0].Type != IncidentAuthFailure {
		t.Errorf("expected most recent first, got %s", incidents[0].Type)
	}
}

func TestInMemoryStore_ResolveIncident(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	if err := store.RecordIncident(ctx, &Incident{ProviderID: "p", Type: IncidentOutage, Impact: ImpactHigh}); err != nil {
		t.Fatal(err)
	}
	incidents, _ := store.GetUnresolvedIncidents(ctx, "p", "")
	if len(incidents) != 1 {
		t.Fatalf("expected 1 unresolved, got %d", len(incidents))
	}
	if err := store.ResolveIncident(ctx, incidents[0].ID, "fixed"); err != nil {
		t.Fatal(err)
	}
	unresolved, _ := store.GetUnresolvedIncidents(ctx, "p", "")
	if len(unresolved) != 0 {
		t.Errorf("expected 0 unresolved after resolve, got %d", len(unresolved))
	}
}

func TestInMemoryStore_ResolveIncidentNotFound(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	if err := store.ResolveIncident(ctx, 9999, "x"); !errors.Is(err, ErrReputationNotFound) {
		t.Errorf("expected ErrReputationNotFound, got %v", err)
	}
}

func TestInMemoryStore_DedupeIncident(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	inc := &Incident{ProviderID: "p", Model: "m", Type: IncidentOutage, Impact: ImpactHigh}
	created, err := store.RecordIncidentIfNotExists(ctx, inc, 1*time.Hour)
	if err != nil || !created {
		t.Fatalf("first should create: created=%v err=%v", created, err)
	}

	inc2 := &Incident{ProviderID: "p", Model: "m", Type: IncidentOutage, Impact: ImpactHigh}
	created2, err := store.RecordIncidentIfNotExists(ctx, inc2, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Errorf("second should be deduped (within window)")
	}
}

func TestInMemoryStore_DedupeDifferentType(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	_, _ = store.RecordIncidentIfNotExists(ctx, &Incident{ProviderID: "p", Model: "m", Type: IncidentOutage, Impact: ImpactHigh}, 1*time.Hour)
	created, _ := store.RecordIncidentIfNotExists(ctx, &Incident{ProviderID: "p", Model: "m", Type: IncidentAuthFailure, Impact: ImpactHigh}, 1*time.Hour)
	if !created {
		t.Errorf("different type should not be deduped")
	}
}

func TestInMemoryStore_ValidationErrors(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	if err := store.SaveTimeseries(ctx, nil); err == nil {
		t.Error("expected error for nil row")
	}
	if err := store.SaveTimeseries(ctx, &TimeseriesRow{Model: "m"}); err == nil {
		t.Error("expected error for missing provider")
	}
	if err := store.RecordIncident(ctx, nil); err == nil {
		t.Error("expected error for nil incident")
	}
	if err := store.RecordIncident(ctx, &Incident{ProviderID: "p"}); err == nil {
		t.Error("expected error for missing type")
	}
}

func TestInMemoryStore_ContextCancellation(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.SaveTimeseries(ctx, &TimeseriesRow{ProviderID: "p", Model: "m", Date: now}); err == nil {
		t.Error("expected ctx error")
	}
}

// ---------------------------------------------------------------------------
// Analyzer pure functions
// ---------------------------------------------------------------------------

func TestCalculateLongTermReliability(t *testing.T) {
	tests := []struct {
		name string
		rows []TimeseriesRow
		want float64
	}{
		{"empty", nil, 0},
		{"zero requests", []TimeseriesRow{{RequestCount: 0}}, 0},
		{
			name: "perfect",
			rows: []TimeseriesRow{
				{RequestCount: 100, SuccessCount: 100},
				{RequestCount: 50, SuccessCount: 50},
			},
			want: 1.0,
		},
		{
			name: "mixed",
			rows: []TimeseriesRow{
				{RequestCount: 80, SuccessCount: 70},
				{RequestCount: 20, SuccessCount: 18},
			},
			want: 88.0 / 100.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateLongTermReliability(tt.rows)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestCalculateStabilityScore(t *testing.T) {
	tests := []struct {
		name string
		rows []TimeseriesRow
		min  float64
		max  float64
	}{
		{"empty", nil, 1, 1},
		{"single", []TimeseriesRow{{ReliabilityScore: 0.9}}, 1, 1},
		{"constant", []TimeseriesRow{
			{ReliabilityScore: 0.9, RequestCount: 10},
			{ReliabilityScore: 0.9, RequestCount: 10},
			{ReliabilityScore: 0.9, RequestCount: 10},
		}, 0.999, 1.0},
		{"volatile", []TimeseriesRow{
			{ReliabilityScore: 1.0, RequestCount: 10},
			{ReliabilityScore: 0.5, RequestCount: 10},
			{ReliabilityScore: 0.9, RequestCount: 10},
			{ReliabilityScore: 0.3, RequestCount: 10},
		}, 0.0, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateStabilityScore(tt.rows)
			if got < tt.min || got > tt.max {
				t.Errorf("stability %v out of [%v, %v]", got, tt.min, tt.max)
			}
		})
	}
}

func TestCalculateUptime(t *testing.T) {
	// 完美运行时，uptime = reliability
	rows := []TimeseriesRow{
		{RequestCount: 100, SuccessCount: 100},
		{RequestCount: 100, SuccessCount: 95},
	}
	got := CalculateUptime(rows, nil, 7)
	want := 195.0 / 200.0
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("baseline uptime got %v want %v", got, want)
	}

	// 包含一个 critical 事件 → 应折扣
	ended := time.Now().Add(-1 * time.Hour)
	started := ended.Add(-30 * time.Minute)
	got2 := CalculateUptime(rows, []Incident{{
		StartedAt: started,
		EndedAt:   &ended,
		Impact:    ImpactCritical,
	}}, 7)
	if got2 >= got {
		t.Errorf("expected uptime to decrease after incident: got %v baseline %v", got2, got)
	}
}

// ---------------------------------------------------------------------------
// Analyzer w/ store
// ---------------------------------------------------------------------------

func TestAnalyzer_AnalyzeProviderReputation(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	for i := 0; i < 7; i++ {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p1", Model: "m1",
			Date:             now.AddDate(0, 0, -i),
			ReliabilityScore: 0.9,
			AvgLatencyMs:     250,
			ErrorRate:        0.1,
			RequestCount:     100,
			SuccessCount:     90,
			SuccessRate:      0.9,
		})
	}
	_ = store.RecordIncident(ctx, &Incident{
		ProviderID: "p1", Model: "m1",
		Type: IncidentOutage, Impact: ImpactCritical,
		StartedAt: now.Add(-2 * time.Hour),
	})

	a := NewReputationAnalyzer(store, nil)
	a.now = fixedNow(now)
	rep, err := a.AnalyzeProviderReputation(ctx, "p1", "m1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.ReliabilityTrend) != 7 {
		t.Errorf("expected 7 trend points, got %d", len(rep.ReliabilityTrend))
	}
	if rep.LongTermReliability < 0.85 || rep.LongTermReliability > 0.95 {
		t.Errorf("unexpected long-term reliability: %v", rep.LongTermReliability)
	}
	if rep.UnresolvedCount != 1 {
		t.Errorf("expected 1 unresolved incident, got %d", rep.UnresolvedCount)
	}
	if rep.UptimePercentage <= 0 || rep.UptimePercentage > 1 {
		t.Errorf("uptime out of [0,1]: %v", rep.UptimePercentage)
	}
	if rep.LastUpdated.IsZero() {
		t.Errorf("LastUpdated should be set")
	}
}

func TestAnalyzer_RequiresProviderID(t *testing.T) {
	a := NewReputationAnalyzer(NewInMemoryReputationStore(), nil)
	if _, err := a.AnalyzeProviderReputation(context.Background(), "", "m", 7); err == nil {
		t.Error("expected error for empty provider_id")
	}
	if _, err := a.DetectAnomalies(context.Background(), "", "m"); err == nil {
		t.Error("expected error for empty provider_id")
	}
}

func TestAnalyzer_DetectAnomalies_ErrorRateSpike(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	dates := []time.Time{
		now.AddDate(0, 0, -5), now.AddDate(0, 0, -4),
		now.AddDate(0, 0, -3), now.AddDate(0, 0, -2),
		now.AddDate(0, 0, -1),
	}
	errorRates := []float64{0.05, 0.04, 0.06, 0.05, 0.50}
	for i, d := range dates {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p", Model: "m", Date: d,
			ErrorRate:    errorRates[i],
			SuccessRate:  1 - errorRates[i],
			RequestCount: 100, SuccessCount: int64(100 * (1 - errorRates[i])),
		})
	}
	a := NewReputationAnalyzer(store, nil)
	anomalies, err := a.DetectAnomalies(ctx, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	foundSpike := false
	for _, an := range anomalies {
		if an.Type == AnomalyErrorRateSpike {
			foundSpike = true
			if an.Severity != ImpactCritical {
				t.Errorf("expected critical severity for 0.5 error rate, got %v", an.Severity)
			}
		}
	}
	if !foundSpike {
		t.Errorf("expected to detect error_rate_spike anomaly")
	}
}

func TestAnalyzer_DetectAnomalies_LatencySpike(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	dates := []time.Time{
		now.AddDate(0, 0, -3), now.AddDate(0, 0, -2), now.AddDate(0, 0, -1),
	}
	latencies := []float64{500, 600, 1500}
	for i, d := range dates {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p", Model: "m", Date: d,
			AvgLatencyMs: latencies[i],
		})
	}
	a := NewReputationAnalyzer(store, nil)
	anomalies, err := a.DetectAnomalies(ctx, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	foundLatency := false
	for _, an := range anomalies {
		if an.Type == AnomalyLatencySpike {
			foundLatency = true
		}
	}
	if !foundLatency {
		t.Errorf("expected latency_spike anomaly, got %+v", anomalies)
	}
}

func TestAnalyzer_DetectAnomalies_SuccessDrop(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	dates := []time.Time{now.AddDate(0, 0, -2), now.AddDate(0, 0, -1)}
	successRates := []float64{0.95, 0.5}
	for i, d := range dates {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p", Model: "m", Date: d,
			SuccessRate: successRates[i],
		})
	}
	a := NewReputationAnalyzer(store, nil)
	anomalies, _ := a.DetectAnomalies(ctx, "p", "m")
	found := false
	for _, an := range anomalies {
		if an.Type == AnomalySuccessDrop {
			found = true
		}
	}
	if !found {
		t.Errorf("expected success_drop anomaly")
	}
}

func TestAnalyzer_DetectAnomalies_NoAnomaly(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	for i := 0; i < 7; i++ {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p", Model: "m",
			Date:      now.AddDate(0, 0, -i-1),
			ErrorRate: 0.02, AvgLatencyMs: 200, SuccessRate: 0.98,
		})
	}
	a := NewReputationAnalyzer(store, nil)
	anomalies, _ := a.DetectAnomalies(ctx, "p", "m")
	if len(anomalies) != 0 {
		t.Errorf("expected no anomalies for healthy provider, got %+v", anomalies)
	}
}

func TestAnalyzer_DetectAnomalies_InsufficientData(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	_ = store.SaveTimeseries(ctx, &TimeseriesRow{ProviderID: "p", Model: "m", Date: now})
	a := NewReputationAnalyzer(store, nil)
	anomalies, err := a.DetectAnomalies(ctx, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(anomalies) != 0 {
		t.Errorf("expected empty with insufficient data, got %d", len(anomalies))
	}
}

func TestAnalyzer_WithThresholds(t *testing.T) {
	a := NewReputationAnalyzer(NewInMemoryReputationStore(), nil)
	a.WithThresholds(AnomalyThresholds{
		ErrorRateJumpAbove: 0.5,
		LatencyMinMs:       2000,
	})
	got := a.Thresholds()
	if got.ErrorRateJumpAbove != 0.5 {
		t.Errorf("override failed: %v", got.ErrorRateJumpAbove)
	}
	if got.LatencyMinMs != 2000 {
		t.Errorf("override failed: %v", got.LatencyMinMs)
	}
}

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

type testCredStore struct {
	creds []*Credential
}

func (t *testCredStore) List(tenantID string) ([]*Credential, error) {
	return t.creds, nil
}

func TestWorker_AggregateDaily(t *testing.T) {
	now := time.Date(2026, 6, 30, 3, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	scorer := NewBanditScorer()

	// 添加 2 个相同 provider/model 的凭据
	for i := 0; i < 8; i++ {
		scorer.RecordSuccess("cred-a", 200)
	}
	scorer.RecordFailure("cred-a")
	for i := 0; i < 5; i++ {
		scorer.RecordSuccess("cred-b", 300)
	}
	for i := 0; i < 5; i++ {
		scorer.RecordFailure("cred-b")
	}
	creds := []*Credential{
		{ID: "cred-a", ProviderID: "p1", Model: "m1", Status: StatusActive},
		{ID: "cred-b", ProviderID: "p1", Model: "m1", Status: StatusActive},
		{ID: "cred-disabled", ProviderID: "p2", Model: "m2", Status: StatusDisabled},
	}
	// disabled 的不应该被聚合
	for i := 0; i < 10; i++ {
		scorer.RecordSuccess("cred-disabled", 100)
	}
	cs := &testCredStore{creds: creds}

	w := NewReputationWorker(ReputationWorkerConfig{
		Store: store, Scorer: scorer, Creds: cs, Logger: nil,
	})
	w.now = fixedNow(now)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.GetTimeseries(context.Background(), "p1", "m1", 1)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row for p1/m1, got %d", len(rows))
	}
	r := rows[0]
	if r.RequestCount != 19 {
		t.Errorf("expected request_count=19 (9+10), got %d", r.RequestCount)
	}
	if r.SuccessCount != 13 {
		t.Errorf("expected success_count=13 (8+5), got %d", r.SuccessCount)
	}
	if r.ErrorRate < 0.30 || r.ErrorRate > 0.33 {
		t.Errorf("unexpected error_rate: %v", r.ErrorRate)
	}
	if !r.Date.Equal(now.UTC().Truncate(24 * time.Hour)) {
		t.Errorf("date mismatch: %v", r.Date)
	}

	// disabled 的不应该写入
	rows2, _ := store.GetTimeseries(context.Background(), "p2", "m2", 1)
	if len(rows2) != 0 {
		t.Errorf("disabled credential should be skipped, got %d rows", len(rows2))
	}
}

func TestWorker_NextRunAfter(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	w := NewReputationWorker(ReputationWorkerConfig{
		Store: NewInMemoryReputationStore(), Scorer: NewBanditScorer(),
		Creds: &testCredStore{}, RunHour: 2, Location: loc,
	})
	now := time.Date(2026, 6, 30, 1, 30, 0, 0, time.UTC)
	next := w.NextRunAfter(now)
	want := time.Date(2026, 6, 30, 2, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("next run got %v want %v", next, want)
	}
	// 当 runHour 已过，第二天 2:00
	now2 := time.Date(2026, 6, 30, 5, 0, 0, 0, time.UTC)
	next2 := w.NextRunAfter(now2)
	want2 := time.Date(2026, 7, 1, 2, 0, 0, 0, time.UTC)
	if !next2.Equal(want2) {
		t.Errorf("next-day run got %v want %v", next2, want2)
	}
}

func TestWorker_NotConfigured(t *testing.T) {
	w := NewReputationWorker(ReputationWorkerConfig{
		Store: NewInMemoryReputationStore(),
		Creds: &testCredStore{},
	})
	if err := w.Run(context.Background()); err == nil {
		t.Error("expected error when scorer is nil")
	}
}

func TestWorker_NoCredentials(t *testing.T) {
	now := time.Date(2026, 6, 30, 3, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	cs := &testCredStore{creds: nil}
	w := NewReputationWorker(ReputationWorkerConfig{
		Store: store, Scorer: NewBanditScorer(), Creds: cs,
	})
	w.now = fixedNow(now)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorker_SkipEmptyCreds(t *testing.T) {
	now := time.Date(2026, 6, 30, 3, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	scorer := NewBanditScorer()
	// 凭据存在但无请求 → 应被跳过
	cs := &testCredStore{creds: []*Credential{
		{ID: "x", ProviderID: "p", Model: "m", Status: StatusActive},
	}}
	w := NewReputationWorker(ReputationWorkerConfig{
		Store: store, Scorer: scorer, Creds: cs,
	})
	w.now = fixedNow(now)
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := store.GetTimeseries(context.Background(), "p", "m", 1)
	if len(rows) != 0 {
		t.Errorf("expected no rows when creds have no requests")
	}
}

func TestWorker_Start_NilConfig(t *testing.T) {
	w := NewReputationWorker(ReputationWorkerConfig{
		Store: nil, Scorer: nil, Creds: nil,
	})
	w.Start(context.Background())
	// Start with nil scorer should immediately close doneCh
	select {
	case <-w.doneCh:
	case <-time.After(1 * time.Second):
		t.Error("expected doneCh to be closed when scorer is nil")
	}
}

func TestWorker_Start_StopLoop(t *testing.T) {
	now := time.Date(2026, 6, 30, 2, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	scorer := NewBanditScorer()
	for i := 0; i < 5; i++ {
		scorer.RecordSuccess("c1", 100)
	}
	cs := &testCredStore{creds: []*Credential{
		{ID: "c1", ProviderID: "p", Model: "m", Status: StatusActive},
	}}

	// 关键技巧：让 RunHour = now.Hour()，NextRunAfter 就会落在明天同一时间，
	// 等待时间约为 24h。因此我们使用 ctx cancel 来立即退出 loop。
	w := NewReputationWorker(ReputationWorkerConfig{
		Store: store, Scorer: scorer, Creds: cs, RunHour: now.Hour(),
	})
	w.now = fixedNow(now)
	w.location = time.UTC

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	// 让 goroutine 跑一会儿，然后取消 ctx
	time.Sleep(50 * time.Millisecond)
	cancel()
	// 等 doneCh 关闭（Stop 后会关闭）
	select {
	case <-w.doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop within 2s after ctx cancel")
	}
}

func TestWorker_Stop_Idempotent(t *testing.T) {
	w := NewReputationWorker(ReputationWorkerConfig{
		Store:  NewInMemoryReputationStore(),
		Scorer: NewBanditScorer(),
		Creds:  &testCredStore{},
	})
	w.Stop()
	w.Stop() // 第二次调用不应该 panic
}

// ---------------------------------------------------------------------------
// IncidentTracker
// ---------------------------------------------------------------------------

func TestIncidentTracker_CheckAndRecordIncidents(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	// 制造一个错误率飙升
	dates := []time.Time{now.AddDate(0, 0, -3), now.AddDate(0, 0, -2), now.AddDate(0, 0, -1)}
	errorRates := []float64{0.05, 0.07, 0.6}
	for i, d := range dates {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p", Model: "m", Date: d,
			ErrorRate:    errorRates[i],
			SuccessRate:  1 - errorRates[i],
			RequestCount: 100, SuccessCount: int64(100 * (1 - errorRates[i])),
		})
	}

	analyzer := NewReputationAnalyzer(store, nil)
	tracker := NewIncidentTracker(IncidentTrackerConfig{
		Store: store, Analyzer: analyzer,
	})
	tracker.now = fixedNow(now)
	if err := tracker.CheckAndRecordIncidents(ctx); err != nil {
		t.Fatal(err)
	}
	incidents, _ := store.GetRecentIncidents(ctx, "p", "m", 1)
	if len(incidents) == 0 {
		t.Errorf("expected at least 1 incident recorded, got 0")
	}
}

func TestIncidentTracker_DedupeAcrossScans(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()

	dates := []time.Time{now.AddDate(0, 0, -2), now.AddDate(0, 0, -1)}
	errorRates := []float64{0.05, 0.6}
	for i, d := range dates {
		_ = store.SaveTimeseries(ctx, &TimeseriesRow{
			ProviderID: "p", Model: "m", Date: d,
			ErrorRate:    errorRates[i],
			SuccessRate:  1 - errorRates[i],
			RequestCount: 100,
		})
	}
	analyzer := NewReputationAnalyzer(store, nil)
	tracker := NewIncidentTracker(IncidentTrackerConfig{
		Store: store, Analyzer: analyzer,
	})
	tracker.now = fixedNow(now)

	// 第一次扫描创建
	if err := tracker.CheckAndRecordIncidents(ctx); err != nil {
		t.Fatal(err)
	}
	first, _ := store.GetRecentIncidents(ctx, "p", "m", 1)
	firstCount := len(first)

	// 第二次扫描应被 dedupe
	if err := tracker.CheckAndRecordIncidents(ctx); err != nil {
		t.Fatal(err)
	}
	second, _ := store.GetRecentIncidents(ctx, "p", "m", 1)
	if len(second) != firstCount {
		t.Errorf("expected dedupe, got %d -> %d incidents", firstCount, len(second))
	}
}

func TestIncidentTracker_RecordManual(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	ctx := context.Background()
	tracker := NewIncidentTracker(IncidentTrackerConfig{
		Store: store, Analyzer: NewReputationAnalyzer(store, nil),
	})
	tracker.now = fixedNow(now)
	created, err := tracker.RecordManualIncident(ctx, &Incident{
		ProviderID: "p", Model: "m", Type: IncidentOutage, Impact: ImpactCritical,
		Description: "manual outage",
	})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	incidents, _ := store.GetRecentIncidents(ctx, "p", "m", 1)
	if len(incidents) != 1 {
		t.Errorf("expected 1 incident, got %d", len(incidents))
	}
	if incidents[0].Description != "manual outage" {
		t.Errorf("description mismatch: %s", incidents[0].Description)
	}
}

func TestIncidentTracker_NilStore(t *testing.T) {
	tracker := NewIncidentTracker(IncidentTrackerConfig{
		Store: nil, Analyzer: NewReputationAnalyzer(NewInMemoryReputationStore(), nil),
	})
	if err := tracker.CheckAndRecordIncidents(context.Background()); err == nil {
		t.Error("expected error for nil store")
	}
}

func TestIncidentTracker_NilAnalyzer(t *testing.T) {
	tracker := NewIncidentTracker(IncidentTrackerConfig{
		Store: NewInMemoryReputationStore(), Analyzer: nil,
	})
	if err := tracker.CheckAndRecordIncidents(context.Background()); err == nil {
		t.Error("expected error for nil analyzer")
	}
}

func TestIncidentTracker_AnomalyToIncident(t *testing.T) {
	now := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	store := newStoreWithFixedClock(t, now)
	tracker := NewIncidentTracker(IncidentTrackerConfig{
		Store: store, Analyzer: NewReputationAnalyzer(store, nil),
	})
	tracker.now = fixedNow(now)

	for _, c := range []struct {
		at       AnomalyType
		wantType IncidentType
	}{
		{AnomalyErrorRateSpike, IncidentErrorRateSpike},
		{AnomalyLatencySpike, IncidentLatencySpike},
		{AnomalySuccessDrop, IncidentDegradedPerformance},
	} {
		got := tracker.anomalyToIncident("p", "m", Anomaly{
			Type:     c.at,
			Severity: ImpactHigh,
			Date:     now,
			Message:  "x",
		})
		if got.Type != c.wantType {
			t.Errorf("anomaly %v -> incident %v, want %v", c.at, got.Type, c.wantType)
		}
	}
}

// ---------------------------------------------------------------------------
// Incident helpers
// ---------------------------------------------------------------------------

func TestIncident_ResolvedDuration(t *testing.T) {
	started := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	ended := started.Add(15 * time.Minute)
	inc := &Incident{StartedAt: started, EndedAt: &ended}
	if got := inc.ResolvedDuration(); got != 15*time.Minute {
		t.Errorf("expected 15m, got %v", got)
	}

	// 未结束 → 使用 time.Since（至少非零）
	inc2 := &Incident{StartedAt: time.Now().Add(-1 * time.Minute)}
	if inc2.ResolvedDuration() < 0 {
		t.Errorf("expected non-negative for open incident")
	}

	// nil 安全
	var nilInc *Incident
	if got := nilInc.ResolvedDuration(); got != 0 {
		t.Errorf("nil incident should return 0, got %v", got)
	}
}

func TestProviderReputation_IsEmpty(t *testing.T) {
	if !(&ProviderReputation{}).IsEmpty() {
		t.Error("zero-value reputation should be empty")
	}
	r := &ProviderReputation{ReliabilityTrend: []DailyMetric{{Value: 0.9}}}
	if r.IsEmpty() {
		t.Error("non-empty trend should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Postgres store — 测试 Enabled 守卫与错误处理
// ---------------------------------------------------------------------------

func TestPostgresStore_DisabledReturnsNil(t *testing.T) {
	s := NewPostgresReputationStore(nil)
	if s.Enabled() {
		t.Error("nil pool should be disabled")
	}
	ctx := context.Background()
	if rows, err := s.GetTimeseries(ctx, "p", "m", 7); err != nil || rows != nil {
		t.Errorf("expected nil/no-error when disabled, got rows=%v err=%v", rows, err)
	}
	if err := s.SaveTimeseries(ctx, &TimeseriesRow{ProviderID: "p", Model: "m"}); err != ErrNoDatabase {
		t.Errorf("expected ErrNoDatabase, got %v", err)
	}
	if err := s.RecordIncident(ctx, &Incident{ProviderID: "p", Type: IncidentOutage}); err != ErrNoDatabase {
		t.Errorf("expected ErrNoDatabase, got %v", err)
	}
}

func TestPostgresStore_ValidationErrors(t *testing.T) {
	s := NewPostgresReputationStore(nil)
	ctx := context.Background()
	if err := s.SaveTimeseries(ctx, nil); err == nil {
		t.Error("expected error for nil row")
	}
	if err := s.RecordIncident(ctx, nil); err == nil {
		t.Error("expected error for nil incident")
	}
}

// ---------------------------------------------------------------------------
// Default thresholds
// ---------------------------------------------------------------------------

func TestDefaultAnomalyThresholds(t *testing.T) {
	th := DefaultAnomalyThresholds()
	if th.ErrorRateJumpAbove != 0.30 {
		t.Errorf("expected 0.30, got %v", th.ErrorRateJumpAbove)
	}
	if th.LatencyMultiplier != 2.0 {
		t.Errorf("expected 2.0, got %v", th.LatencyMultiplier)
	}
	if th.SuccessDropBelow != 0.80 {
		t.Errorf("expected 0.80, got %v", th.SuccessDropBelow)
	}
}
