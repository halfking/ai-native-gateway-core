package streaming

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/middleware"
	"github.com/kaixuan/llm-gateway-go/upstream"
)

type memoryModalityWAL struct {
	initial []telemetry.InitialRequest
	updates []telemetry.LogUpdate
}

func (w *memoryModalityWAL) CreateInitial(_ context.Context, req *telemetry.InitialRequest) error {
	w.initial = append(w.initial, *req)
	return nil
}

func (w *memoryModalityWAL) Update(update *telemetry.LogUpdate) {
	w.updates = append(w.updates, *update)
}

func TestModalityIngressRecordsRejectionsAndProtocolFailure(t *testing.T) {
	owner := "credential-selfcheck-worker"
	limited := NewEmbeddingsHandler(&embeddingResolverStub{}, upstream.NewWithRetries(0))
	limited.keyVerifier = &stubKeyVerifier{info: &authentication.KeyInfo{ID: 7, TenantID: "a", Status: "throttled"}}
	audio := NewAudioService(&embeddingResolverStub{}, upstream.NewWithRetries(0))
	for _, tc := range []struct {
		name, body, tenant, outcome, reason, origin string
		httpStatus, keyID                           int
		handler                                     http.Handler
	}{
		{"gateway 429", "", "a", "rate_limited", "key_throttled", "", 429, 7, limited},
		{"method rejection", "", "default", "failure", "http_405", "", 405, 0, limited},
		{"MCP parse error", "{", "default", "failure", "mcp_parse_error", "", 200, 0, NewAudioMCPHandler(audio)},
		{"upstream 429", "", "default", "failure", "http_429", "", 429, 0, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})},
		{"trusted platform", "", "ops", "success", "", "self_check", 204, 9, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			observeModalityIdentity(r, &authentication.KeyInfo{ID: 9, TenantID: "ops", IsSystem: true, OwnerUser: &owner})
			w.WriteHeader(http.StatusNoContent)
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := http.MethodPost
			if tc.name == "method rejection" {
				method = http.MethodGet
			}
			wal := &memoryModalityWAL{}
			var attrs *telemetry.ContextAttrsEntry
			observe := ObserveModalityIngress(tc.handler, wal, func(entry *telemetry.ContextAttrsEntry) { attrs = entry })
			r := httptest.NewRequest(method, "/v1/mcp", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer fake-test-key")
			r.Header.Set("X-Request-Id", "client-chosen-id")
			r.Header.Set("X-Gw-Session-Id", "client-chosen-session")
			w := httptest.NewRecorder()
			middleware.NewRequestIDMiddleware().Wrap(observe).ServeHTTP(w, r)
			id := w.Header().Get("X-Request-Id")
			if id == "" || id == "client-chosen-id" || w.Code != tc.httpStatus {
				t.Fatalf("response id=%q status=%d body=%s", id, w.Code, w.Body)
			}
			if len(wal.initial) == 0 || wal.initial[0].TenantID != "default" || wal.initial[len(wal.initial)-1].TenantID != tc.tenant {
				t.Fatalf("initial rows = %+v", wal.initial)
			}
			for _, req := range wal.initial {
				if req.RequestID != id || req.SessionID != "" || !req.Provisional {
					t.Fatalf("unexpected initial identity %+v", req)
				}
			}
			if len(wal.updates) != 1 || wal.updates[0].RequestID != id || wal.updates[0].Status != tc.outcome || wal.updates[0].Error != tc.reason {
				t.Fatalf("terminal records %+v", wal.updates)
			}
			if tc.keyID == 0 && attrs != nil {
				t.Fatalf("unverified identity produced attrs %+v", attrs)
			}
			if tc.keyID != 0 {
				if attrs == nil || attrs.APIKeyID == nil || *attrs.APIKeyID != tc.keyID {
					t.Fatalf("verified identity attrs %+v", attrs)
				}
				if attrs.ClientRequestID == nil || *attrs.ClientRequestID != "client-chosen-id" || attrs.RequestID != id {
					t.Fatalf("client ID missing or replaced the server ID: %+v", attrs)
				}
				if tc.origin == "" && attrs.OriginStage != nil || tc.origin != "" && (attrs.OriginStage == nil || *attrs.OriginStage != tc.origin) {
					t.Fatalf("trusted origin attrs %+v", attrs)
				}
			}
		})
	}
}

func TestModalityIngressPreservesFlushAndRecordsDisconnect(t *testing.T) {
	wal := &memoryModalityWAL{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := ObserveModalityIngress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher := w.(http.Flusher)
		flusher.Flush()
		cancel()
	}), wal, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/audio/transcriptions", nil))
	if !w.Flushed || len(wal.updates) != 1 || wal.updates[0].Status != telemetry.StatusCancelled {
		t.Fatalf("flushed=%v updates=%+v", w.Flushed, wal.updates)
	}
}

func TestTranscriptionStreamOutcomeRequiresProtocolTerminal(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`data: [DONE]`, "unknown"},
		{"event: transcript.text.done\ndata: {\"text\":\"hi\"}\n\n", "success"},
		{"data: {\"type\":\"transcript.text.done\"}\n\nevent: error\ndata: {}\n\n", "failure"},
		{"data: {\"error\":{\"message\":\"opaque\"}}\n\n", "failure"},
	} {
		if got, _ := transcriptionStreamEvidence(context.Background(), []byte(tc.body)); got != tc.want {
			t.Errorf("outcome=%q want %q for %q", got, tc.want, tc.body)
		}
	}
}
