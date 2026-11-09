package streaming

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

type largeProtocolCase struct {
	name, path, protocol, response string
	handler                        func(*ChatHandler) http.Handler
}

func largeProtocolCases() []largeProtocolCase {
	return []largeProtocolCase{
		{"chat", "/v1/chat/completions", "openai-completions",
			`{"id":"chat_1","object":"chat.completion","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			func(h *ChatHandler) http.Handler { return h }},
		{"messages", "/v1/messages", "anthropic-messages",
			`{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`,
			func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", "openai-responses",
			`{"id":"resp_1","object":"response","model":"m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2}}`,
			func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	}
}

type largeProtocolFixture struct {
	server *httptest.Server
	store  *largebody.Store
	done   chan struct{}
	ref    largebody.Ref
	mu     sync.Mutex
	logs   []*telemetry.RequestLogEntry
}

func newLargeProtocolFixture(t *testing.T, tc largeProtocolCase, window int, supplier http.Handler) *largeProtocolFixture {
	t.Helper()
	upstream := httptest.NewServer(supplier)
	t.Cleanup(upstream.Close)
	limiter := credential.NewLimiter()
	t.Cleanup(limiter.Stop)
	exec := executors.NewExecutor(executors.NewRouter(executors.NewStickyCache(), limiter),
		credential.NewManager(), limiter, pool.NewPoolManager(nil), nil, nil, nil, nil)
	exec.EnforceContextBudget = true
	exec.FitContext = sessioncontext.Fit
	pipeline := exec.NewDispatchPipeline(nil)
	pipeline.Start()
	t.Cleanup(pipeline.Stop)
	exec.SetDispatchPipeline(pipeline)
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.executor = exec
	h.provider = nativeOutputAuditResolver{candidate: provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL, Protocol: tc.protocol,
		CatalogCode: "native-test", RawModel: "m", OfferRawModel: "m", APIKey: "upstream-key",
		ContextWindow: &window, Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true,
		LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
		SupportsNativeResponses: tc.protocol == "openai-responses",
	}}
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{
		ID: 42, TenantID: "tenant-1", ApplicationID: 7,
	}})
	client := telemetry.NewClient()
	client.SetRequestLogSink(nativeOutputAuditSink{})
	t.Cleanup(client.Stop)
	h.SetTelemetry(client)
	store, err := largebody.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &largeProtocolFixture{store: store, done: make(chan struct{})}
	h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) {
		copy := *entry
		f.mu.Lock()
		f.logs = append(f.logs, &copy)
		f.mu.Unlock()
	})
	next := tc.handler(h)
	observed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.ref, _ = LargeRequestArchive(r.Context())
		next.ServeHTTP(w, r)
	})
	ingress := NewLargeIngress(h, store, LargeIngressConfig{}).Wrap(observed)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ingress.ServeHTTP(w, r)
		close(f.done)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *largeProtocolFixture) post(t *testing.T, path string, body []byte) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gw-Session-Id", "gw_large-protocol-test")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.done:
	case <-time.After(60 * time.Second):
		t.Fatal("HTTP handler did not finish archiving the response")
	}
	return resp.StatusCode, data, resp.Header
}

func largeProtocolBody(t *testing.T, tc largeProtocolCase, historical string) []byte {
	t.Helper()
	messages := []map[string]any{{"role": "user", "content": "Maintain billing safely."}}
	if historical != "" {
		messages = append(messages, map[string]any{"role": "assistant", "content": historical})
	}
	messages = append(messages, map[string]any{"role": "user", "content": "Find billing port 6432 in /srv/billing/db.yaml."})
	body := map[string]any{"model": "m", "max_tokens": 16, "messages": messages}
	if tc.name == "responses" {
		body = map[string]any{"model": "m", "max_output_tokens": 16, "input": messages}
	}
	wire, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestLargeProtocolBudgetRejectionIsHTTP422AndAudited(t *testing.T) {
	for _, tc := range largeProtocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			var supplierCalls atomic.Int32
			f := newLargeProtocolFixture(t, tc, 200, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				supplierCalls.Add(1)
				http.Error(w, "budget rejection reached supplier", http.StatusInternalServerError)
			}))
			status, body, _ := f.post(t, tc.path, largeProtocolBody(t, tc, ""))
			if status != http.StatusUnprocessableEntity || !bytes.Contains(body, []byte("context_budget_exceeded")) {
				t.Fatalf("client status=%d body=%s", status, body)
			}
			if supplierCalls.Load() != 0 {
				t.Fatalf("local budget rejection made %d supplier HTTP calls", supplierCalls.Load())
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			failures := 0
			for _, entry := range f.logs {
				if entry.ErrorKind == nil || *entry.ErrorKind != "context_budget_exceeded" {
					continue
				}
				failures++
				if entry.Success || entry.ClientStatusCode == nil || *entry.ClientStatusCode != status {
					t.Fatalf("budget audit disagrees with actual client status: %+v", entry)
				}
				var meta struct {
					Large struct{ Archive largebody.Ref } `json:"large_request"`
				}
				if err := json.Unmarshal(entry.CompressionMeta, &meta); err != nil || meta.Large.Archive != f.ref {
					t.Fatalf("failure audit lost RAW reference: meta=%s ref=%+v err=%v", entry.CompressionMeta, f.ref, err)
				}
			}
			if failures != 1 {
				t.Fatalf("budget failure log count=%d logs=%+v", failures, f.logs)
			}
		})
	}
}

func TestLargeProtocolRawRecoveryAndActualSupplierBudget(t *testing.T) {
	for _, tc := range largeProtocolCases() {
		t.Run(tc.name, func(t *testing.T) {
			const window = 1_000_000
			var supplierCalls atomic.Int32
			actualBodies := make(chan []byte, 1)
			f := newLargeProtocolFixture(t, tc, window, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				supplierCalls.Add(1)
				actual, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				actualBodies <- actual
				if _, err := contextbudget.Check(actual, contextbudget.Spec{ContextWindow: window}); err != nil {
					t.Errorf("supplier actually received a body outside its budget: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			raw := largeProtocolBody(t, tc, strings.Repeat("old historical noise ", 60_000))
			status, body, headers := f.post(t, tc.path, raw)
			if status != http.StatusOK || !bytes.Contains(body, []byte("ok")) || supplierCalls.Load() != 1 {
				t.Fatalf("large request failed: status=%d calls=%d body=%s", status, supplierCalls.Load(), body)
			}
			actual := <-actualBodies
			if len(raw) <= 1<<20 || len(actual) >= len(raw) || headers.Get("X-Gw-Context-Reduced") == "" {
				t.Fatalf("large history was not reduced: raw=%d supplier=%d headers=%v", len(raw), len(actual), headers)
			}
			for _, want := range []string{"Maintain billing safely.", "6432", "/srv/billing/db.yaml"} {
				if !bytes.Contains(actual, []byte(want)) {
					t.Errorf("supplier body lost protected task %q", want)
				}
			}
			reopened, err := largebody.NewStore(f.store.RootPath())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			ref, err := reopened.Recover(context.Background(), f.ref.Identity)
			if err != nil || ref != f.ref || headers.Get("X-Gw-Archive-Id") != ref.RequestID {
				t.Fatalf("restart recovery lost RAW identity: recovered=%+v expected=%+v err=%v", ref, f.ref, err)
			}
			archived, err := reopened.Open(context.Background(), ref)
			if err != nil {
				t.Fatal(err)
			}
			defer archived.Close()
			hash := sha256.New()
			size, err := io.Copy(hash, archived)
			expected := sha256.Sum256(raw)
			if err != nil || size != int64(len(raw)) || ref.SHA256 != hex.EncodeToString(expected[:]) || ref.SHA256 != hex.EncodeToString(hash.Sum(nil)) {
				t.Fatalf("RAW recovery changed original bytes: size=%d ref=%+v err=%v", size, ref, err)
			}
		})
	}
}
