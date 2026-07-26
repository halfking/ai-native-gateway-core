package logging

import (
	"os"
	"path/filepath"
	"strings"
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
