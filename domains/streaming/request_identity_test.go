package streaming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/requestjourney"
	"github.com/kaixuan/llm-gateway-go/internal/streamretry" //nolint:depguard // retry-loop journey carrier under test
)

func TestInitializeRequestIdentityUsesMiddlewareContract(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Request-Id", "server-id")
	req.Header.Set("X-Gw-Client-Request-Id", "client-id")
	req.Header.Set("X-Gw-Session-Id", "gw_existing")
	req.Header.Set("X-Gw-Client-Type", "zcode")
	got := initializeRequestIdentity(req)
	if got.RequestID != "server-id" || got.ClientRequestID != "client-id" || got.SessionID != "gw_existing" {
		t.Fatalf("identity mismatch: %+v", got)
	}
}

func TestExplicitStreamSessionSourceSurvivesProvisionalHeaderInjection(t *testing.T) {
	req := markExplicitStreamSession(httptest.NewRequest("POST", "/v1/responses", nil))
	if explicitStreamSession(req.Context()) {
		t.Fatal("request without a client session header was marked explicit")
	}
	_ = initializeRequestIdentity(req)
	req = markExplicitStreamSession(req)
	if explicitStreamSession(req.Context()) {
		t.Fatal("system-generated provisional session changed cancellation ownership")
	}
}

func TestExplicitStreamSessionSourceRecognizesClientHeader(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("X-Gw-Session-Id", "gw_client")
	req = markExplicitStreamSession(req)
	if !explicitStreamSession(req.Context()) {
		t.Fatal("client-provided session header was not marked explicit")
	}
}

func TestInitializeRequestIdentityDirectInvocationProducesStableIDs(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/responses", nil)
	first := initializeRequestIdentity(request)
	second := initializeRequestIdentity(request)
	if first.RequestID == "" || first.SessionID == "" {
		t.Fatalf("missing ids: %+v", first)
	}
	if first.RequestID != second.RequestID || first.SessionID != second.SessionID {
		t.Fatalf("ids changed: first=%+v second=%+v", first, second)
	}
	if request.Header.Get("X-Request-Id") != first.RequestID || request.Header.Get("X-Gw-Session-Id") != first.SessionID {
		t.Fatalf("request headers not updated")
	}
}

type requestJourneyVerifier struct {
	key *authentication.KeyInfo
	err error
}

func (v requestJourneyVerifier) Enabled() bool { return true }
func (v requestJourneyVerifier) Verify(context.Context, string) (*authentication.KeyInfo, error) {
	return v.key, v.err
}
func (v requestJourneyVerifier) VerifyByID(context.Context, int) (*authentication.KeyInfo, error) {
	return v.key, v.err
}
func (requestJourneyVerifier) CheckBudget(context.Context, int) error { return nil }
func (requestJourneyVerifier) LookupKeyMeta(context.Context, string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

func TestRequestJourneyAuthFailureOnlyEntersGlobalIngress(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "chat", path: "/v1/chat/completions"},
		{name: "messages", path: "/v1/messages"},
		{name: "responses", path: "/v1/responses"},
		{name: "gemini", path: "/v1beta/models/gemini:generateContent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
			recorder := requestjourney.NewRecorder(projection, nil, nil)
			t.Cleanup(func() { _ = recorder.Close(context.Background()) })
			handler := NewChatHandler(nil, nil, nil, nil, nil, nil)
			handler.SetRequestJourney(recorder, "gateway-test")
			handler.keyVerifier = requestJourneyVerifier{err: &authentication.InvalidKeyError{Message: "invalid"}}
			requestID := "request-invalid-" + test.name
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Header.Set("X-Request-Id", requestID)
			response := httptest.NewRecorder()

			writer, request, statusWriter := beginRequestJourney(response, request, handler)
			markRequestJourneyFailure(request, nil, "invalid_key")
			writer.WriteHeader(http.StatusUnauthorized)
			finishRequestJourney(request, statusWriter)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", response.Code)
			}
			if _, err := projection.Detail("default", requestID); !errors.Is(err, requestjourney.ErrJourneyNotFound) {
				t.Fatalf("pre-auth failure entered default tenant: %v", err)
			}
			ingress := projection.RecentIngress()
			if len(ingress) != 1 || ingress[0].RequestID != requestID || ingress[0].Protocol == "" || ingress[0].Status != requestjourney.IngressStatusFailed {
				t.Fatalf("ingress = %#v", ingress)
			}
		})
	}
}

func TestRequestJourneyCoversAllProtocolEntryPoints(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		handler func(*ChatHandler) http.Handler
		status  int
	}{
		{name: "chat", path: "/v1/chat/completions", handler: func(h *ChatHandler) http.Handler { return h }, status: http.StatusMethodNotAllowed},
		{name: "messages", path: "/v1/messages", handler: func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }, status: http.StatusMethodNotAllowed},
		{name: "responses", path: "/v1/responses", handler: func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }, status: http.StatusMethodNotAllowed},
		{name: "gemini", path: "/v1beta/models/bad-path", handler: func(h *ChatHandler) http.Handler { return NewGeminiHandler(h) }, status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
			recorder := requestjourney.NewRecorder(projection, nil, nil)
			handler := NewChatHandler(nil, nil, nil, nil, nil, nil)
			handler.SetRequestJourney(recorder, "gateway-test")
			request := httptest.NewRequest(http.MethodPut, test.path, nil)
			request.Header.Set("X-Request-Id", "request-"+test.name)
			response := httptest.NewRecorder()

			test.handler(handler).ServeHTTP(response, request)

			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			ingress := projection.RecentIngress()
			if len(ingress) != 1 || ingress[0].RequestID != "request-"+test.name || ingress[0].Status != requestjourney.IngressStatusFailed {
				t.Fatalf("ingress = %#v", ingress)
			}
			journey, err := projection.Detail("default", "request-"+test.name)
			if err != nil {
				t.Fatalf("Detail: %v", err)
			}
			if len(journey.Events) < 2 || journey.Events[0].Type != requestjourney.EventRequestReceived {
				t.Fatalf("events = %+v", journey.Events)
			}
			last := journey.Events[len(journey.Events)-1]
			if last.Type != requestjourney.EventRequestFailed || last.Stage != requestjourney.StageTerminal {
				t.Fatalf("terminal = %+v", last)
			}
			for i, event := range journey.Events {
				if event.Seq != int64(i+1) {
					t.Fatalf("event %d seq = %d", i, event.Seq)
				}
			}
		})
	}
}

// TestRequestJourneySurvivesStreamretryReentry drives two handler passes
// through the real streamretry entry (ServeHTTP reuses the request carrier on
// re-entry), which is how a retry re-invokes the whole handler. Before B3-PR1
// the second pass built a fresh lifecycle whose sequence restarted at 1, so
// every attempt-2 event was rejected by the journey projection as a sequence
// conflict and the request's journey silently froze after attempt 1. The
// carrier must now carry the lifecycle binding across passes: one ordered,
// valid stream with a terminal per attempt.
func TestRequestJourneySurvivesStreamretryReentry(t *testing.T) {
	projection := requestjourney.NewProjection(requestjourney.DefaultConfig())
	recorder := requestjourney.NewRecorder(projection, nil, nil)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	handler := NewChatHandler(nil, nil, nil, nil, nil, nil)
	handler.SetRequestJourney(recorder, "gateway-test")

	var attempts int32
	var reentryCtx context.Context
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The wrapper's retry loop re-invokes the handler with the context it
		// originally passed in — the derived lifecycle context never escapes
		// back to the wrapper. Capture that entry context here.
		reentryCtx = r.Context()
		writer, r, statusWriter := beginRequestJourney(w, r, handler)
		resolveRequestJourney(r, "default", "auto", "provider-a/standard")
		if atomic.AddInt32(&attempts, 1) == 1 {
			markRequestJourneyFailure(r, nil, "upstream_error")
			writer.WriteHeader(http.StatusServiceUnavailable)
			finishRequestJourney(r, statusWriter)
			return
		}
		writer.WriteHeader(http.StatusOK)
		finishRequestJourney(r, statusWriter)
	})

	executor := streamretry.NewDefaultStreamExecutor(inner, streamretry.DefaultConfig())

	requestID := "request-retry-reentry"
	newRequest := func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request.Header.Set("X-Request-Id", requestID)
		return request
	}

	response := httptest.NewRecorder()
	executor.ServeHTTP(response, newRequest())
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("attempt-1 status = %d, want 503", response.Code)
	}

	// The retry pass: same request context (carrier intact), fresh pass
	// through ServeHTTP exactly like the wrapper's retry re-invocation.
	response2 := httptest.NewRecorder()
	executor.ServeHTTP(response2, newRequest().WithContext(reentryCtx))
	if response2.Code != http.StatusOK {
		t.Fatalf("attempt-2 status = %d, want 200", response2.Code)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}

	// keyVerifier is nil in this construction, so the lifecycle binds the
	// default tenant (same as TestRequestJourneyCoversAllProtocolEntryPoints).
	journey, err := projection.Detail("default", requestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := journey.Validate(); err != nil {
		t.Fatalf("retried journey is not a valid ordered stream: %v", err)
	}
	var terminals int
	for _, event := range journey.Events {
		if event.Type == requestjourney.EventRequestFailed || event.Type == requestjourney.EventRequestSucceeded {
			terminals++
		}
	}
	if terminals != 2 {
		t.Fatalf("attempt terminals = %d, want 2 (one per attempt)", terminals)
	}
	if len(journey.Events) < 6 {
		t.Fatalf("journey events = %d, want both attempts fully recorded (≥6)", len(journey.Events))
	}
}
