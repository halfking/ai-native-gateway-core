package logging

import (
	"strings"
	"testing"
)

// TestAsyncRawDataLogger_LookupFrame covers the per-request frame
// index (2026-07-28 §5.7 / §5.8). LookupFrame returns the (file,
// offset) of the most recent raw entry written for the given
// (requestID, direction) pair so the LockFreeAnomalyReporter can
// populate AnomalyReport.RawLogFile/RawLogOffset without falling
// back to the global CurrentLocation() racy path.
func TestAsyncRawDataLogger_LookupFrame(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewAsyncRawDataLogger(dir, 1024*1024, true, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	env := RawCorrelationEnvelope{GWSessionID: "s1", TenantID: "t1"}
	// Three entries with non-trivial bodies so the recorded offsets
	// are guaranteed to be positive and distinguishable.
	body1 := []byte(`{"messages":[{"role":"user","content":"hello world"}]}`)
	body2 := []byte(`{"messages":[{"role":"user","content":"different body"}],"stream":true}`)
	body3 := []byte(`{"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}]}`)
	logger.LogClientRequestWithEnvelope("req-A", "openai-chat", body1, nil, "pre_conversion", env)
	logger.LogUpstreamRequestWithEnvelope("req-A", "openai-chat", body2, "post_conversion", env)
	logger.LogUpstreamResponseWithEnvelope("req-B", "openai-chat", body3, "post_conversion", env)

	// Drain to disk so the file/offset are populated.
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	// Both req-A directions should resolve to the same file, with
	// upstream_request strictly after client_request (they were
	// written in that order).
	clientFile, clientOffset, ok := logger.LookupFrame("req-A", "client_request")
	if !ok {
		t.Fatal("req-A/client_request expected hit")
	}
	upFile, upOffset, ok := logger.LookupFrame("req-A", "upstream_request")
	if !ok {
		t.Fatal("req-A/upstream_request expected hit")
	}
	bFile, bOffset, ok := logger.LookupFrame("req-B", "upstream_response")
	if !ok {
		t.Fatal("req-B/upstream_response expected hit")
	}

	for _, c := range []struct {
		name   string
		file   string
		offset int64
	}{
		{"req-A/client_request", clientFile, clientOffset},
		{"req-A/upstream_request", upFile, upOffset},
		{"req-B/upstream_response", bFile, bOffset},
	} {
		if !strings.HasSuffix(c.file, ".jsonl") {
			t.Errorf("%s file %q does not look like a rotated raw log", c.name, c.file)
		}
		if c.offset < 0 {
			t.Errorf("%s offset %d negative", c.name, c.offset)
		}
	}

	// Ordering: client_request was written first, so its offset
	// must be strictly less than upstream_request's. (upstream_request
	// immediately follows client_request in the same file.)
	if upOffset <= clientOffset {
		t.Errorf("ordering broken: client=%d upstream=%d (upstream must be after client)", clientOffset, upOffset)
	}

	// req-A/upstream_response should miss (req-B got it).
	if _, _, ok := logger.LookupFrame("req-A", "upstream_response"); ok {
		t.Error("req-A/upstream_response should miss; req-B got it")
	}

	_, _, ok = logger.LookupFrame("req-NONE", "client_request")
	if ok {
		t.Error("expected miss for unknown request_id")
	}
}

// TestAsyncRawDataLogger_LookupFrame_Nil is the defensive guarantee
// for callers that hold an optional async logger.
func TestAsyncRawDataLogger_LookupFrame_Nil(t *testing.T) {
	var l *AsyncRawDataLogger
	if file, offset, ok := l.LookupFrame("r", "client_request"); ok || file != "" || offset != 0 {
		t.Errorf("nil receiver returned (%q,%d,%v) want (\"\",0,false)", file, offset, ok)
	}
}
