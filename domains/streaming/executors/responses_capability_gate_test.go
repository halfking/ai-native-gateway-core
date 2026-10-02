package executors

// responses_capability_gate_test.go — the durable-verdict gate.
//
// Before this change the native-Responses gate could only ever be CLOSED by a
// probe verdict, never opened: cand.SupportsNativeResponses comes from the SQL
// table credential_model_capabilities, which no code in the tree writes, while
// the probe's positive verdict lives in the Redis node-state capability key.
// Measured on vapEUR/cred 126 that left /v1/responses traffic going out on
// /chat/completions 54/54 times.
//
// These tests drive executeOpenAI over miniredis so the verdict store is real,
// and assert WHICH upstream leg is chosen — a green result alone could not tell
// an enabled gate from a disabled one that happened to succeed.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
)

func newCapabilityGateExecutor(t *testing.T, supported bool) (*Executor, *credentialfpslot.Manager) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	mgr := credentialfpslot.New(credentialfpslot.Config{Enabled: true, DefaultLimit: 5}, client)
	if err := mgr.SetSupportsResponses(t.Context(), 126, "gpt-5.6-terra", supported); err != nil {
		t.Fatalf("seed durable verdict: %v", err)
	}
	e := newOverloadTestExecutor()
	e.FpSlots = mgr
	return e, mgr
}

type legRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (l *legRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.paths = append(l.paths, r.URL.Path)
		l.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(upstreamCodexResponsesBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (l *legRecorder) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.paths) == 0 {
		return ""
	}
	return l.paths[len(l.paths)-1]
}

// A known-positive durable verdict must put a Responses-shaped client request
// onto the /v1/responses leg even though the SQL capability flag is false.
func TestExecuteOpenAI_DurableVerdictEnablesNativeResponses(t *testing.T) {
	rec := &legRecorder{}
	upstream := rec.server(t)
	e, _ := newCapabilityGateExecutor(t, true)

	cand := bridgeCandidate(upstream.URL, "gpt-5.6-terra")
	cand.SupportsNativeResponses = false // the real production state

	_, err := e.executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  httptest.NewRecorder(),
		BodyBytes:          []byte(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hi"}]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-5.6-terra","input":"hi"}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-5.6-terra",
		ClientID:           identity.ClientIdentity{IdentityHash: "cap-gate-on"},
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}
	if got := rec.last(); !strings.HasSuffix(got, "/responses") {
		t.Fatalf("upstream leg = %q, want /v1/responses (durable positive verdict must open the gate)", got)
	}
}

// A known-negative verdict must keep the request on chat — the pre-existing
// downgrade path must still hold now that the gate can also open.
func TestExecuteOpenAI_DurableVerdictKeepsChatWhenUnsupported(t *testing.T) {
	rec := &legRecorder{}
	upstream := rec.server(t)
	e, _ := newCapabilityGateExecutor(t, false)

	cand := bridgeCandidate(upstream.URL, "gpt-5.6-terra")
	cand.SupportsNativeResponses = true // SQL says yes, the verdict says no

	_, err := e.executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  httptest.NewRecorder(),
		BodyBytes:          []byte(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hi"}]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-5.6-terra","input":"hi"}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-5.6-terra",
		ClientID:           identity.ClientIdentity{IdentityHash: "cap-gate-off"},
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}
	if got := rec.last(); !strings.HasSuffix(got, "/chat/completions") {
		t.Fatalf("upstream leg = %q, want /chat/completions (durable negative verdict must keep the downgrade)", got)
	}
}

// The probe's verdict is non-stream evidence. It must not be extrapolated to the
// streaming leg, which keeps requiring the explicit SQL capability flag.
func TestExecuteOpenAI_DurableVerdictDoesNotEnableStreaming(t *testing.T) {
	rec := &legRecorder{}
	upstream := rec.server(t)
	e, _ := newCapabilityGateExecutor(t, true)

	cand := bridgeCandidate(upstream.URL, "gpt-5.6-terra")
	cand.SupportsNativeResponses = false
	cand.SupportsNativeResponsesStream = false

	_, _ = e.executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		W:                  httptest.NewRecorder(),
		BodyBytes:          []byte(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hi"}]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-5.6-terra","input":"hi"}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-5.6-terra",
		IsStream:           true,
		ClientID:           identity.ClientIdentity{IdentityHash: "cap-gate-stream"},
	}, cand, 0, time.Now(), nil)

	if got := rec.last(); strings.HasSuffix(got, "/responses") {
		t.Fatalf("upstream leg = %q: a non-stream probe verdict must not enable the streaming native path", got)
	}
}

// A chat-shaped client must not be moved onto the responses leg by this gate:
// there is no Responses body to send, and the chat→responses bridge (a separate
// mechanism, fired only on an upstream refusal) owns that direction.
func TestExecuteOpenAI_DurableVerdictDoesNotAffectChatClients(t *testing.T) {
	rec := &legRecorder{}
	upstream := rec.server(t)
	e, _ := newCapabilityGateExecutor(t, true)

	cand := bridgeCandidate(upstream.URL, "gpt-5.6-terra")
	cand.SupportsNativeResponses = false

	_, _ = e.executeOpenAI(&ExecParams{
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		W:              httptest.NewRecorder(),
		BodyBytes:      []byte(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hi"}]}`),
		ClientProtocol: "openai-completions",
		ClientModel:    "gpt-5.6-terra",
		ClientID:       identity.ClientIdentity{IdentityHash: "cap-gate-chat"},
	}, cand, 0, time.Now(), nil)

	if got := rec.last(); !strings.HasSuffix(got, "/chat/completions") {
		t.Fatalf("upstream leg = %q, want /chat/completions for a chat-shaped client", got)
	}
}
