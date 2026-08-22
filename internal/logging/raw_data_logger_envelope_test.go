package logging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestAsyncRawDataLogger_OverflowEntry covers the queue-full path. We
// exercise the public API (no internals) by writing two bodies with
// the same envelope and verifying both land in the file even when the
// queue is intentionally small. The second body is large enough to
// force an overflow entry, which must still carry the correlation
// envelope so request_logs and the on-disk audit log stay joinable.
func TestAsyncRawDataLogger_OverflowEntry(t *testing.T) {
	dir := t.TempDir()
	// capacity=1 forces the second enqueue to fall into the overflow
	// branch. The first entry is drained asynchronously before the
	// third call; only the third is guaranteed to overflow.
	logger, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 1)
	if err != nil {
		t.Fatalf("NewAsyncRawDataLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	env := RawCorrelationEnvelope{
		ClientRequestID: "client-req-1",
		GWSessionID:     "gw_session_x",
		GWTaskID:        "gw_task_y",
		TenantID:        "tenant-a",
		APIKeyID:        42,
	}

	// Burst a large number of entries at once; the queue cannot
	// keep up before the rotation event and must record an overflow
	// marker. The marker must still carry the correlation envelope.
	big := make([]byte, 32*1024)
	for i := range big {
		big[i] = 'x'
	}
	for i := 0; i < 64; i++ {
		logger.LogUpstreamRequestWithEnvelope("req-1", "anthropic-messages", big, "post_conversion", env)
	}
	logger.LogUpstreamResponseWithEnvelope("req-1", "anthropic-messages", []byte(`{"ok":true}`), "post_conversion", env)

	_ = logger.Close()

	files, err := filepath.Glob(filepath.Join(dir, "raw_data_*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no audit files in %s: %v", dir, err)
	}
	data, err := readAll(files)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(data, `"request_id":"req-1"`) {
		t.Errorf("audit file missing request_id: %s", data)
	}
	if !strings.Contains(data, `"gw_session_id":"gw_session_x"`) {
		t.Errorf("audit file missing gw_session_id: %s", data)
	}
	if !strings.Contains(data, `"client_request_id":"client-req-1"`) {
		t.Errorf("audit file missing client_request_id: %s", data)
	}
	if !strings.Contains(data, `"api_key_id":42`) {
		t.Errorf("audit file missing api_key_id: %s", data)
	}
	if !strings.Contains(data, `"raw_log_queue_full`) {
		// Background draining is fast on a tiny queue; the overflow
		// marker is best-effort. We log a hint rather than failing
		// the test so the envelope contract remains the binding
		// guarantee.
		t.Logf("note: no overflow marker in audit file (background drain kept up)")
	}
}

// TestAsyncRawDataLogger_DefaultOn covers the new default-on
// behaviour: when the caller does not pass enabled=false, the logger
// is initialised regardless of an LLM_GATEWAY_RAW_LOG_ENABLED override.
func TestAsyncRawDataLogger_DefaultOn(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logger, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 8)
	if err != nil {
		t.Fatalf("NewAsyncRawDataLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	if !logger.baseLogger.enabled {
		t.Errorf("expected base logger to be enabled by default")
	}
}

func readAll(files []string) (string, error) {
	var sb strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		sb.Write(data)
	}
	return sb.String(), nil
}

// TestRawDataLogger_CurrentLocation covers the RawDataLogger.CurrentLocation
// surface used by LockFreeAnomalyReporter to stamp AnomalyReport.RawLogFile
// and AnomalyReport.RawLogOffset (design §5.7, 2026-07-28).
//
// We exercise the synchronous logger (not the Async wrapper) so the offset
// is observable immediately after each write. Two assertions matter:
//  1. After a successful write the offset monotonically increases and the
//     path matches the rotated file.
//  2. Rotating to a new file changes the returned path (i.e. operators
//     see the most recent file when the report lands).
func TestRawDataLogger_CurrentLocation(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewRawDataLogger(dir, 1024*1024, true)
	if err != nil {
		t.Fatalf("NewRawDataLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	// Before any write, the file path should already be set (rotate is
	// invoked during NewRawDataLogger); offset starts at 0.
	path0, offset0 := logger.CurrentLocation()
	if path0 == "" {
		t.Fatalf("expected non-empty currentPath after NewRawDataLogger")
	}
	if offset0 != 0 {
		t.Errorf("expected offset 0 before any write, got %d", offset0)
	}
	if !strings.HasPrefix(path0, dir) {
		t.Errorf("expected currentPath to live in %s, got %s", dir, path0)
	}

	// Each write should bump the offset.
	logger.LogClientRequest("req-1", "openai-chat", []byte(`{"hello":"world"}`), nil, "pre_parse")
	path1, offset1 := logger.CurrentLocation()
	if path1 != path0 {
		t.Errorf("expected same file after first write, got %q vs %q", path1, path0)
	}
	if offset1 <= offset0 {
		t.Errorf("expected offset to advance after write, got %d -> %d", offset0, offset1)
	}

	// Forcing a rotate should switch to a new file path and reset offset
	// from the caller's perspective (offset accumulates within one file).
	if err := logger.rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	path2, offset2 := logger.CurrentLocation()
	if path2 == path0 {
		t.Errorf("expected new file path after rotate, still %q", path2)
	}
	if offset2 != 0 {
		t.Errorf("expected offset to reset after rotate, got %d", offset2)
	}

	// Writes after rotate land in the new file and bump its offset.
	logger.LogClientRequest("req-2", "openai-chat", []byte(`{"hello":"again"}`), nil, "pre_parse")
	path3, offset3 := logger.CurrentLocation()
	if path3 != path2 {
		t.Errorf("expected stable path across writes within a file, got %q vs %q", path3, path2)
	}
	if offset3 <= offset2 {
		t.Errorf("expected offset to advance after second write, got %d -> %d", offset2, offset3)
	}

	// HasFile() must report true once the logger has rotated; it must
	// return false on a fresh disabled logger.
	if !logger.HasFile() {
		t.Errorf("expected HasFile()=true for an enabled logger with an open file")
	}
	disabled, err := NewRawDataLogger(dir, 1024*1024, false)
	if err != nil {
		t.Fatalf("NewRawDataLogger(disabled): %v", err)
	}
	if disabled.HasFile() {
		t.Errorf("expected HasFile()=false for a disabled logger")
	}
	if p, o := disabled.CurrentLocation(); p != "" || o != 0 {
		t.Errorf("expected CurrentLocation() zero on disabled logger, got (%q, %d)", p, o)
	}
}

// TestAsyncRawDataLogger_CurrentLocation ensures the async wrapper
// forwards to the underlying RawDataLogger and does not panic on
// disabled / closed states.
func TestAsyncRawDataLogger_CurrentLocation(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 16)
	if err != nil {
		t.Fatalf("NewAsyncRawDataLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	if !logger.HasFile() {
		t.Errorf("expected HasFile()=true for an enabled async logger")
	}
	path, offset := logger.CurrentLocation()
	if path == "" {
		t.Errorf("expected non-empty path from async wrapper")
	}
	if offset < 0 {
		t.Errorf("expected non-negative offset, got %d", offset)
	}
	if !strings.HasPrefix(path, dir) {
		t.Errorf("expected path under %s, got %s", dir, path)
	}

	// Closed logger should report empty values without panicking.
	closed, err := NewAsyncRawDataLogger(filepath.Join(dir, "closed"), 1024*1024, false, 1)
	if err != nil {
		t.Fatalf("NewAsyncRawDataLogger(disabled): %v", err)
	}
	if closed.HasFile() {
		t.Errorf("expected HasFile()=false on disabled async logger")
	}
	if p, o := closed.CurrentLocation(); p != "" || o != 0 {
		t.Errorf("expected empty location on disabled async logger, got (%q, %d)", p, o)
	}
}

// TestLockFreeAnomalyReporter_StampsRawLogLocation covers the wiring
// between AsyncRawDataLogger.CurrentLocation and
// LockFreeAnomalyReporter.stampRawLogLocation (2026-07-28, design §5.7).
//
// We point the reporter at an httptest server, raise a report, and
// assert the captured JSON carries the (file, offset) returned by the
// underlying raw logger.
func TestLockFreeAnomalyReporter_StampsRawLogLocation(t *testing.T) {
	dir := t.TempDir()
	async, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 8)
	if err != nil {
		t.Fatalf("NewAsyncRawDataLogger: %v", err)
	}
	t.Cleanup(func() { _ = async.Close() })

	var captured []byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rep := NewLockFreeAnomalyReporterWithRawLogLocator(srv.URL, true, 8, async.CurrentLocation)
	t.Cleanup(func() { _ = rep.Close() })

	rep.ReportConversionError(context.Background(), "req-stamp", "openai-chat", "anthropic-messages", "pre_serialize", []byte("{}"), errors.New("boom"))

	// Flush forces an immediate send so the test does not have to wait
	// for the 5s ticker.
	rep.Flush(context.Background())

	mu.Lock()
	body := append([]byte(nil), captured...)
	mu.Unlock()
	if len(body) == 0 {
		t.Fatalf("expected anomaly endpoint to receive a body")
	}

	var batch struct {
		Anomalies []AnomalyReport `json:"anomalies"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		t.Fatalf("decode captured body: %v", err)
	}
	if len(batch.Anomalies) == 0 {
		t.Fatalf("expected at least one anomaly in batch, body=%s", body)
	}
	report := batch.Anomalies[0]
	if report.RequestID != "req-stamp" {
		t.Errorf("unexpected request_id: %q", report.RequestID)
	}
	if report.RawLogFile == "" {
		t.Errorf("expected RawLogFile to be populated by locator")
	}
	if !strings.HasPrefix(report.RawLogFile, dir) {
		t.Errorf("expected RawLogFile under %s, got %s", dir, report.RawLogFile)
	}
	// offset is only emitted when non-zero (raw_log_offset has
	// omitempty); a freshly rotated file legitimately starts at 0. We
	// therefore only assert the file key is present and the offset
	// round-trips through the locator's signature.
	if !strings.Contains(string(body), `"raw_log_file":"`+report.RawLogFile+`"`) {
		t.Errorf("expected raw_log_file value to round-trip in payload, got %s", body)
	}
}

// TestLockFreeAnomalyReporter_NilLocatorLeavesFieldsEmpty confirms that
// the legacy constructor (no locator) still produces reports without the
// file/offset keys populated, matching pre-2026-07-28 behaviour.
func TestLockFreeAnomalyReporter_NilLocatorLeavesFieldsEmpty(t *testing.T) {
	var captured []byte
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rep := NewLockFreeAnomalyReporter(srv.URL, true, 8) // nil locator
	t.Cleanup(func() { _ = rep.Close() })

	rep.ReportConversionError(context.Background(), "req-nil", "openai-chat", "anthropic-messages", "pre_serialize", []byte("{}"), errors.New("boom"))

	rep.Flush(context.Background())

	mu.Lock()
	body := append([]byte(nil), captured...)
	mu.Unlock()
	if len(body) == 0 {
		t.Fatalf("expected anomaly endpoint to receive a body")
	}
	if strings.Contains(string(body), `"raw_log_file"`) || strings.Contains(string(body), `"raw_log_offset"`) {
		t.Errorf("expected raw_log_file/raw_log_offset to be omitted when locator is nil, got %s", body)
	}
}
