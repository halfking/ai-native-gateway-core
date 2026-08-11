package streamretry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	dto "github.com/prometheus/client_model/go"
)

func counterValue(t *testing.T, counter interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	metric := &dto.Metric{}
	if err := counter.Write(metric); err != nil {
		t.Fatalf("write counter metric: %v", err)
	}
	return metric.GetCounter().GetValue()
}

func TestRecordExecutionMetrics_UpdatesCounters(t *testing.T) {
	beforeAttempts := counterValue(t, streamRetryAttemptsTotal)
	beforeRetries := counterValue(t, streamRetryRetriesTotal)
	beforeSuccess := counterValue(t, streamRetrySuccessTotal)
	beforeExhausted := counterValue(t, streamRetryExhaustedTotal)

	recordExecutionMetrics(WrapperMetrics{
		TotalAttempts:  3,
		TotalRetries:   2,
		SuccessAttempt: 2,
	}, nil)
	recordExecutionMetrics(WrapperMetrics{
		TotalAttempts:  3,
		TotalRetries:   2,
		SuccessAttempt: -1,
	}, errors.New("retry budget exhausted"))

	if got := counterValue(t, streamRetryAttemptsTotal) - beforeAttempts; got != 6 {
		t.Errorf("attempt counter delta = %v, want 6", got)
	}
	if got := counterValue(t, streamRetryRetriesTotal) - beforeRetries; got != 4 {
		t.Errorf("retry counter delta = %v, want 4", got)
	}
	if got := counterValue(t, streamRetrySuccessTotal) - beforeSuccess; got != 1 {
		t.Errorf("success counter delta = %v, want 1", got)
	}
	if got := counterValue(t, streamRetryExhaustedTotal) - beforeExhausted; got != 1 {
		t.Errorf("exhausted counter delta = %v, want 1", got)
	}
}

func TestDefaultStreamExecutor_NonStreamingRequestDoesNotRetry(t *testing.T) {
	attempts := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "bad request", http.StatusBadRequest)
	})

	cfg := DefaultConfig()
	cfg.BaseDelayMs = 1
	exec := NewDefaultStreamExecutor(handler, cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", http.NoBody)

	_, err := exec.ExecuteStreamWithMetrics(context.Background(), httptest.NewRecorder(), req)
	if err == nil {
		t.Fatal("ExecuteStreamWithMetrics() error = nil, want HTTP error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 for non-stream request", attempts)
	}
}
