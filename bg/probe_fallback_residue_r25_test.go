package bg

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// R25-T2 (audit round 25, test debt from 228f7e1da): the fallback-success
// path replaces ErrMsg with the downgrade annotation instead of appending,
// so the raw upstream 400 body (which can echo credential fragments on
// other providers) must not survive into admin-visible probe faces. The
// pre-existing test only asserted the annotation was present, which the
// old `res.ErrMsg +=` implementation also satisfied.
func TestActiveProbeExecutorFallbackSuccessDropsOriginalResponsesFailureText(t *testing.T) {
	var chatHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(vapeurClaudeResponses400))
		case "/v1/chat/completions":
			chatHits.Add(1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"total_tokens":9}}`))
		default:
			t.Errorf("unexpected probe path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	e := &ActiveProbeExecutor{httpClient: &http.Client{Timeout: 10 * time.Second}}
	res := e.Run(t.Context(), &ProbeTarget{
		CredentialID: 126,
		ProviderID:   36,
		RawModel:     "claude-sonnet-5",
		BaseURL:      srv.URL,
		Protocol:     "openai-responses",
		APIKey:       "sk-test",
	})

	if chatHits.Load() != 1 {
		t.Fatalf("chat fallback hits = %d, want 1", chatHits.Load())
	}
	if res.Status != ProbeStatusSuccess || res.HTTPStatus != 200 {
		t.Fatalf("fallback leg must win: status=%q http=%d errMsg=%q", res.Status, res.HTTPStatus, res.ErrMsg)
	}
	// ErrCode is cleared on downgrade success — a stale "Bad Request" would
	// make admin tooling render the probe as failed.
	if res.ErrCode != "" {
		t.Fatalf("ErrCode = %q, want empty after successful fallback", res.ErrCode)
	}
	// The original upstream 400 body must be fully replaced, not appended.
	for _, residue := range []string{"invalid_request_error", "unsupported_operation", `"error"`} {
		if strings.Contains(res.ErrMsg, residue) {
			t.Fatalf("ErrMsg still carries original responses failure body (%q): %q", residue, res.ErrMsg)
		}
	}
	if !strings.Contains(res.ErrMsg, "chat fallback probe OK") {
		t.Fatalf("ErrMsg missing fallback annotation: %q", res.ErrMsg)
	}
}
