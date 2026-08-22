package logging

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAsyncRawDataLogger_OverflowEmitsAnomaly covers the queue-full
// hook (2026-07-28 §5.8). A tiny queue is intentionally overflowed,
// the overflow reporter is wired via SetOverflowReporter, and the
// httptest endpoint must receive a raw_log_overflow anomaly that
// carries the correlation envelope (gw_session_id at minimum) so
// operators can correlate it with request_logs.
func TestAsyncRawDataLogger_OverflowEmitsAnomaly(t *testing.T) {
	dir := t.TempDir()

	// Capacity=1 forces every subsequent Enqueue to fall into the
	// overflow branch.
	logger, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 1)
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu      sync.Mutex
		capture []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		capture = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rep := NewLockFreeAnomalyReporter(srv.URL, true, 64)
	t.Cleanup(func() { _ = rep.Close() })
	logger.SetOverflowReporter(rep)

	env := RawCorrelationEnvelope{
		GWSessionID: "g-overflow",
		TenantID:    "tenant-a",
		APIKeyID:    9,
	}
	big := make([]byte, 8*1024)
	for i := range big {
		big[i] = 'x'
	}
	// Burst to overflow the queue (capacity 1).
	for i := 0; i < 32; i++ {
		logger.LogUpstreamRequestWithEnvelope("req-of", "openai-chat", big, "post_conversion", env)
	}

	rep.Flush(context.Background())

	mu.Lock()
	body := string(capture)
	mu.Unlock()
	if !strings.Contains(body, `"raw_log_overflow"`) {
		t.Fatalf("expected raw_log_overflow anomaly, body=%s", body)
	}
	if !strings.Contains(body, `"gw_session_id":"g-overflow"`) {
		t.Fatalf("expected gw_session_id, body=%s", body)
	}
	if !strings.Contains(body, `"tenant_id":"tenant-a"`) {
		t.Fatalf("expected tenant_id, body=%s", body)
	}
}

// TestAsyncRawDataLogger_CloseWritesDrainedStub covers the
// graceful-shutdown hook (2026-07-28 §5.8). Close() must write a
// final close_drained stub entry to the raw log file so operators
// can correlate the shutdown row with the request_logs they were
// observing at the time.
func TestAsyncRawDataLogger_CloseWritesDrainedStub(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 1)
	if err != nil {
		t.Fatal(err)
	}

	env := RawCorrelationEnvelope{GWSessionID: "g-close", TenantID: "t-close"}
	big := make([]byte, 16*1024)
	for i := range big {
		big[i] = 'y'
	}
	for i := 0; i < 32; i++ {
		logger.LogUpstreamRequestWithEnvelope("req-close", "openai-chat", big, "post_conversion", env)
	}

	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "raw_data_*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no raw_data file written")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"direction":"close_drained"`) {
		t.Fatalf("expected close_drained stub entry, file=%s", data)
	}
}
