package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestRecordSuccessEmptyResponse verifies that RecordSuccessEmptyResponse
// increments the gateway_success_empty_response_total counter with correct labels.
func TestRecordSuccessEmptyResponse(t *testing.T) {
	// Create a new registry to isolate this test
	reg := prometheus.NewRegistry()
	recorder := &PrometheusRecorder{
		successEmptyResponseTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_success_empty_response_total",
				Help: "Test counter",
			},
			[]string{"provider_id"},
		),
	}
	reg.MustRegister(recorder.successEmptyResponseTotal)

	tests := []struct {
		name       string
		model      string
		providerID string
		tenantID   string
		wantCount  float64
	}{
		{
			name:       "basic increment",
			model:      "gpt-4",
			providerID: "123",
			tenantID:   "tenant-1",
			wantCount:  1,
		},
		{
			name:       "empty provider",
			model:      "claude-3",
			providerID: "",
			tenantID:   "tenant-2",
			wantCount:  1,
		},
		{
			name:       "multiple calls same provider",
			model:      "gpt-4",
			providerID: "456",
			tenantID:   "tenant-3",
			wantCount:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset counter before test if needed
			if tt.wantCount > 1 {
				for i := 0; i < int(tt.wantCount); i++ {
					recorder.RecordSuccessEmptyResponse(tt.model, tt.providerID, tt.tenantID)
				}
			} else {
				recorder.RecordSuccessEmptyResponse(tt.model, tt.providerID, tt.tenantID)
			}

			// Verify the counter value
			counter, err := recorder.successEmptyResponseTotal.GetMetricWithLabelValues(tt.providerID)
			if err != nil {
				t.Fatalf("failed to get metric: %v", err)
			}

			got := testutil.ToFloat64(counter)
			if got != tt.wantCount {
				t.Errorf("RecordSuccessEmptyResponse() counter = %v, want %v", got, tt.wantCount)
			}
		})
	}
}

// TestRecordJournalSnapshotStored verifies that RecordJournalSnapshotStored
// increments the gateway_journal_snapshot_stored_total counter.
func TestRecordJournalSnapshotStored(t *testing.T) {
	reg := prometheus.NewRegistry()
	recorder := &PrometheusRecorder{
		journalSnapshotStoredTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_stored_total",
				Help: "Test counter",
			},
		),
	}
	reg.MustRegister(recorder.journalSnapshotStoredTotal)

	tests := []struct {
		name      string
		tenantID  string
		calls     int
		wantCount float64
	}{
		{
			name:      "single store",
			tenantID:  "tenant-1",
			calls:     1,
			wantCount: 1,
		},
		{
			name:      "multiple stores",
			tenantID:  "tenant-2",
			calls:     5,
			wantCount: 6, // cumulative with previous test
		},
		{
			name:      "empty tenant",
			tenantID:  "",
			calls:     1,
			wantCount: 7, // cumulative
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < tt.calls; i++ {
				recorder.RecordJournalSnapshotStored(tt.tenantID)
			}

			got := testutil.ToFloat64(recorder.journalSnapshotStoredTotal)
			if got != tt.wantCount {
				t.Errorf("RecordJournalSnapshotStored() counter = %v, want %v", got, tt.wantCount)
			}
		})
	}
}

// TestRecordJournalSnapshotApplied verifies that RecordJournalSnapshotApplied
// increments the counter with correct success label.
func TestRecordJournalSnapshotApplied(t *testing.T) {
	reg := prometheus.NewRegistry()
	recorder := &PrometheusRecorder{
		journalSnapshotAppliedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_applied_total",
				Help: "Test counter",
			},
			[]string{"success"},
		),
	}
	reg.MustRegister(recorder.journalSnapshotAppliedTotal)

	tests := []struct {
		name      string
		tenantID  string
		success   bool
		calls     int
		wantCount float64
	}{
		{
			name:      "successful apply",
			tenantID:  "tenant-1",
			success:   true,
			calls:     1,
			wantCount: 1,
		},
		{
			name:      "failed apply",
			tenantID:  "tenant-2",
			success:   false,
			calls:     1,
			wantCount: 1,
		},
		{
			name:      "multiple successful applies",
			tenantID:  "tenant-3",
			success:   true,
			calls:     10,
			wantCount: 11, // cumulative with first success test
		},
		{
			name:      "multiple failed applies",
			tenantID:  "tenant-4",
			success:   false,
			calls:     3,
			wantCount: 4, // cumulative with first failure test
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < tt.calls; i++ {
				recorder.RecordJournalSnapshotApplied(tt.tenantID, tt.success)
			}

			successStr := "false"
			if tt.success {
				successStr = "true"
			}

			counter, err := recorder.journalSnapshotAppliedTotal.GetMetricWithLabelValues(successStr)
			if err != nil {
				t.Fatalf("failed to get metric: %v", err)
			}

			got := testutil.ToFloat64(counter)
			if got != tt.wantCount {
				t.Errorf("RecordJournalSnapshotApplied() counter = %v, want %v", got, tt.wantCount)
			}
		})
	}
}

// TestRecordJournalSnapshotDeduplicated verifies that RecordJournalSnapshotDeduplicated
// increments the counter with correct reason label.
func TestRecordJournalSnapshotDeduplicated(t *testing.T) {
	reg := prometheus.NewRegistry()
	recorder := &PrometheusRecorder{
		journalSnapshotDeduplicatedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_deduplicated_total",
				Help: "Test counter",
			},
			[]string{"reason"},
		),
	}
	reg.MustRegister(recorder.journalSnapshotDeduplicatedTotal)

	tests := []struct {
		name      string
		tenantID  string
		reason    string
		calls     int
		wantCount float64
	}{
		{
			name:      "already completed",
			tenantID:  "tenant-1",
			reason:    "already_completed",
			calls:     1,
			wantCount: 1,
		},
		{
			name:      "version conflict",
			tenantID:  "tenant-2",
			reason:    "version_conflict",
			calls:     1,
			wantCount: 1,
		},
		{
			name:      "not claimed",
			tenantID:  "tenant-3",
			reason:    "not_claimed",
			calls:     2,
			wantCount: 2,
		},
		{
			name:      "multiple same reason",
			tenantID:  "tenant-4",
			reason:    "already_completed",
			calls:     5,
			wantCount: 6, // cumulative with first already_completed test
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < tt.calls; i++ {
				recorder.RecordJournalSnapshotDeduplicated(tt.tenantID, tt.reason)
			}

			counter, err := recorder.journalSnapshotDeduplicatedTotal.GetMetricWithLabelValues(tt.reason)
			if err != nil {
				t.Fatalf("failed to get metric: %v", err)
			}

			got := testutil.ToFloat64(counter)
			if got != tt.wantCount {
				t.Errorf("RecordJournalSnapshotDeduplicated() counter = %v, want %v", got, tt.wantCount)
			}
		})
	}
}

// TestNoopRecorderEmptyResponseAndJournal verifies that NoopRecorder methods
// don't panic and can be called safely.
func TestNoopRecorderEmptyResponseAndJournal(t *testing.T) {
	noop := NewNoopRecorder()

	// These should not panic
	t.Run("RecordSuccessEmptyResponse", func(t *testing.T) {
		noop.RecordSuccessEmptyResponse("model", "provider", "tenant")
	})

	t.Run("RecordJournalSnapshotStored", func(t *testing.T) {
		noop.RecordJournalSnapshotStored("tenant")
	})

	t.Run("RecordJournalSnapshotApplied", func(t *testing.T) {
		noop.RecordJournalSnapshotApplied("tenant", true)
		noop.RecordJournalSnapshotApplied("tenant", false)
	})

	t.Run("RecordJournalSnapshotDeduplicated", func(t *testing.T) {
		noop.RecordJournalSnapshotDeduplicated("tenant", "already_completed")
		noop.RecordJournalSnapshotDeduplicated("tenant", "version_conflict")
		noop.RecordJournalSnapshotDeduplicated("tenant", "not_claimed")
	})
}

// TestMetricsLabelCardinality ensures that labels don't cause cardinality issues.
func TestMetricsLabelCardinality(t *testing.T) {
	reg := prometheus.NewRegistry()
	recorder := &PrometheusRecorder{
		successEmptyResponseTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_success_empty_response_total",
				Help: "Test counter",
			},
			[]string{"provider_id"},
		),
		journalSnapshotStoredTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_stored_total",
				Help: "Test counter",
			},
		),
		journalSnapshotAppliedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_applied_total",
				Help: "Test counter",
			},
			[]string{"success"},
		),
		journalSnapshotDeduplicatedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "gateway_journal_snapshot_deduplicated_total",
				Help: "Test counter",
			},
			[]string{"reason"},
		),
	}
	reg.MustRegister(
		recorder.successEmptyResponseTotal,
		recorder.journalSnapshotStoredTotal,
		recorder.journalSnapshotAppliedTotal,
		recorder.journalSnapshotDeduplicatedTotal,
	)

	// Simulate realistic usage with multiple providers (low cardinality)
	for i := 0; i < 10; i++ {
		tenantID := "tenant-" + string(rune('A'+i))
		modelName := "model-" + string(rune('1'+i))
		providerID := string(rune('0' + i))

		recorder.RecordSuccessEmptyResponse(modelName, providerID, tenantID)
		recorder.RecordJournalSnapshotStored(tenantID)
		recorder.RecordJournalSnapshotApplied(tenantID, i%2 == 0)
		recorder.RecordJournalSnapshotDeduplicated(tenantID, []string{"already_completed", "version_conflict", "not_claimed"}[i%3])
	}

	// Verify metrics are collected properly
	count, err := reg.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	if len(count) != 4 {
		t.Errorf("expected 4 metric families, got %d", len(count))
	}
}

