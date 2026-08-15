package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
	"github.com/stretchr/testify/require"
)

// TestAnomalyReporterAdapter_HttpPayloadIncludesEnvelope covers
// 2026-07-28 §5.7: when LockFreeAnomalyReporter is called with a
// context carrying an AnomalyReportEnvelope (via
// logging.WithAnomalyEnvelope), the JSON payload sent to the
// external endpoint must contain the envelope fields. Pre-fix the
// adapter passed context.Background() and the fields were empty.
func TestAnomalyReporterAdapter_HttpPayloadIncludesEnvelope(t *testing.T) {
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

	rep := logging.NewLockFreeAnomalyReporter(srv.URL, true, 16)
	t.Cleanup(func() { _ = rep.Close() })

	env := logging.AnomalyReportEnvelope{
		ClientRequestID: "cr-1",
		GWSessionID:     "sess-1",
		ProviderID:      18,
		CredentialID:    42,
		TraceID:         "trace-1",
	}
	ctx := logging.WithAnomalyEnvelope(context.Background(), env)

	rep.ReportConversionError(ctx, "req-1", "openai-chat", "anthropic-messages", "pre_serialize", []byte("{}"), errors.New("boom"))

	// Flush forces an immediate send so the test does not have to
	// wait for the 5s ticker.
	rep.Flush(context.Background())

	mu.Lock()
	body := string(capture)
	mu.Unlock()
	require.NotEmpty(t, body, "expected anomaly endpoint to receive a body")

	// The envelope fields must be in the JSON payload.
	for _, expected := range []string{
		`"client_request_id":"cr-1"`,
		`"gw_session_id":"sess-1"`,
		`"provider_id":18`,
		`"credential_id":42`,
		`"trace_id":"trace-1"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("expected payload to contain %s\nbody=%s", expected, body)
		}
	}
}

// TestAnomalyReporterAdapter_NilEnvelopeLeavesFieldsEmpty covers
// the pre-fix path: when no envelope is attached to ctx (the legacy
// context.Background() default), the envelope fields are empty.
func TestAnomalyReporterAdapter_NilEnvelopeLeavesFieldsEmpty(t *testing.T) {
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

	rep := logging.NewLockFreeAnomalyReporter(srv.URL, true, 16)
	t.Cleanup(func() { _ = rep.Close() })

	rep.ReportConversionError(context.Background(), "req-2", "openai-chat", "anthropic-messages", "pre_serialize", []byte("{}"), errors.New("boom"))

	rep.Flush(context.Background())

	mu.Lock()
	body := string(capture)
	mu.Unlock()
	require.NotEmpty(t, body)
	if strings.Contains(body, `"client_request_id"`) || strings.Contains(body, `"gw_session_id"`) {
		t.Errorf("envelope should be empty in legacy path, body=%s", body)
	}
}
