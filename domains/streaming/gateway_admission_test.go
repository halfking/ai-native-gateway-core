package streaming

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/kaixuan/llm-gateway-go/internal/largebody"
	"github.com/kaixuan/llm-gateway-go/middleware"
)

func TestGatewayAdmissionRecordsAuthRejectionsOnlyForInference(t *testing.T) {
	for _, tc := range []struct{ method, path, bearer, reason string }{
		{"POST", "/v1/chat/completions", "", "missing_key"},
		{"POST", "/v1beta/models/model:generateContent", "wrong-global-key", "invalid_key"},
		{"POST", "/v1/images/generations", "", "missing_key"},
		{"GET", "/v1/models", "", ""},
		{"OPTIONS", "/v1/chat/completions", "", ""},
		{"POST", "/v1/chat/completions", "sk-database-key", ""},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("X-Request-Id", "spoofed")
		r.Header.Set("X-Tenant-ID", "spoofed-tenant")
		if tc.bearer != "" {
			r.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		w := httptest.NewRecorder()
		wal := &memoryModalityWAL{}
		h := middleware.Chain{middleware.NewRequestIDMiddleware(), gatewayAdmissionObserver{wal: wal}, middleware.NewAuthMiddleware("global-test-key")}
		h.Then(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			if tc.bearer == "sk-database-key" && writer != w {
				t.Fatal("observer replaced the streaming response writer")
			}
			writer.WriteHeader(http.StatusNoContent)
		})).ServeHTTP(w, r)
		if tc.reason == "" {
			if len(wal.initial) != 0 {
				t.Fatalf("%s %s: unexpected receipts %+v", tc.method, tc.path, wal.initial)
			}
			continue
		}
		if w.Code != http.StatusUnauthorized || len(wal.initial) != 1 {
			t.Fatalf("%s %s: status=%d receipts=%+v", tc.method, tc.path, w.Code, wal.initial)
		}
		assertAdmission(t, wal.initial[0], w.Header().Get("X-Request-Id"), "default", telemetry.StatusFailure, "unassigned:"+tc.reason)
	}
}

func TestGatewayAdmissionRecordsPreHandlerGateOutcomes(t *testing.T) {
	key := &authentication.KeyInfo{ID: 5, TenantID: "verified-tenant"}
	chat := NewChatHandler(nil, nil, nil, nil, nil, nil)
	chat.keyVerifier = &stubKeyVerifier{info: key}
	limit := 1
	key.RateLimitConcurrent = &limit
	gate := NewKeyConcurrentGate()
	gate.used[key.ID] = limit
	for _, tc := range []struct {
		name, path, status, reason string
		wrap                       func(http.Handler) http.Handler
		httpStatus                 int
	}{
		{"body limit", "/v1/chat/completions", telemetry.StatusFailure, "prompt_too_large",
			NewLargeIngress(chat, nil, LargeIngressConfig{}).Wrap, http.StatusRequestEntityTooLarge},
		{"key concurrency", "/v1/responses", telemetry.StatusRateLimited, "key_concurrent_limit",
			gate.Wrap, http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wal := &memoryModalityWAL{}
			body := &forbiddenRead{}
			r := httptest.NewRequest(http.MethodPost, tc.path, body)
			r.Header.Set("Authorization", "Bearer sk-verified")
			r.Header.Set("X-LLM-Origin-Stage", "self_check")
			r.ContentLength = largebody.LimitBytes + 1
			r = r.WithContext(WithVerifiedKeyInfoForTest(r.Context(), key))
			w := httptest.NewRecorder()
			h := middleware.Chain{middleware.NewRequestIDMiddleware(), gatewayAdmissionObserver{wal: wal}}
			h.Then(tc.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("rejected request reached protocol handler")
			}))).ServeHTTP(w, r)
			if w.Code != tc.httpStatus || body.read || len(wal.initial) != 1 || len(wal.updates) != 0 {
				t.Fatalf("status=%d body read=%v receipts=%+v updates=%+v", w.Code, body.read, wal.initial, wal.updates)
			}
			assertAdmission(t, wal.initial[0], w.Header().Get("X-Request-Id"), key.TenantID, tc.status, "tenant:"+tc.reason)
		})
	}
	wal := &memoryModalityWAL{}
	r := gatePost("/v1/embeddings", WithVerifiedKeyInfoForTest(t.Context(), key))
	w := httptest.NewRecorder()
	h := middleware.Chain{middleware.NewRequestIDMiddleware(), gatewayAdmissionObserver{wal: wal}}
	h.Then(ObserveModalityIngress(gate.Wrap(http.NotFoundHandler()), wal, nil)).ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || len(wal.updates) != 1 ||
		wal.updates[0].Status != telemetry.StatusRateLimited || len(wal.initial) != 2 || wal.initial[1].Rejection != nil {
		t.Fatalf("modality concurrency double-counted: status=%d receipts=%+v updates=%+v", w.Code, wal.initial, wal.updates)
	}
}

func TestGatewayAdmissionKeepsTrustedPlatformAndUnverifiedSystemSeparate(t *testing.T) {
	for _, tc := range []struct{ owner, scope string }{
		{"credential-selfcheck-worker", "platform"},
		{"unrecognized-system-owner", "unassigned"},
	} {
		key := &authentication.KeyInfo{ID: 9, TenantID: "ops", IsSystem: true, OwnerUser: &tc.owner}
		wal := &memoryModalityWAL{}
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		r.Header.Set("X-LLM-Origin-Stage", "self_check")
		w := httptest.NewRecorder()
		h := middleware.Chain{middleware.NewRequestIDMiddleware(), gatewayAdmissionObserver{wal: wal}}
		h.Then(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			RecordGatewayAdmissionRejection(r, http.StatusServiceUnavailable, "session_archive_unavailable", key)
			RecordGatewayAdmissionRejection(r, http.StatusForbidden, "other_gate", key)
		})).ServeHTTP(w, r)
		if len(wal.initial) != 1 {
			t.Fatalf("owner=%s receipts=%+v", tc.owner, wal.initial)
		}
		assertAdmission(t, wal.initial[0], w.Header().Get("X-Request-Id"), "ops", telemetry.StatusFailure, tc.scope+":session_archive_unavailable")
	}
}

func assertAdmission(t *testing.T, initial telemetry.InitialRequest, id, tenant, status, reason string) {
	t.Helper()
	if initial.RequestID != id || id == "" || id == "spoofed" || initial.TenantID != tenant ||
		initial.SessionID != "" || initial.ReceivedAt.IsZero() || initial.Rejection == nil ||
		initial.Rejection.CompletedAt.Before(initial.ReceivedAt) || initial.Rejection.Status != status ||
		initial.Rejection.Reason != "gateway_admission:"+reason {
		t.Fatalf("unexpected rejection receipt for %q: %+v, terminal %+v", id, initial, initial.Rejection)
	}
}
