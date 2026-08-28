package executors

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/identity"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
	upstreampkg "github.com/kaixuan/llm-gateway-go/upstream"
)

func nativeResponsesStreamCandidate(baseURL string, enabled bool) provider.Candidate {
	return provider.Candidate{
		CredentialID: 11, ProviderID: 22, BaseURL: baseURL,
		Protocol: "openai-responses", CatalogCode: "openai", RawModel: "gpt-responses",
		APIKey: "sk-native-test", SupportsNativeResponsesStream: enabled,
		Routable: true, LifecycleStatus: "active", AvailabilityState: "ready",
		QuotaState: "ok", CircuitState: "closed",
	}
}

func nativeResponsesCandidate(baseURL string, enabled bool) provider.Candidate {
	return provider.Candidate{
		CredentialID:            11,
		ProviderID:              22,
		BaseURL:                 baseURL,
		Protocol:                "openai-responses",
		CatalogCode:             "openai",
		RawModel:                "gpt-responses",
		APIKey:                  "sk-native-test",
		SupportsNativeResponses: enabled,
		Routable:                true,
		LifecycleStatus:         "active",
		AvailabilityState:       "ready",
		QuotaState:              "ok",
		CircuitState:            "closed",
	}
}

func TestExecuteOpenAI_NativeResponsesStreamUsesNativeHandlerAndBody(t *testing.T) {
	requestBody := []byte(`{"model":"gpt-responses","input":"native stream"}`)
	streamBody := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
	var gotPath string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, streamBody)
	}))
	defer upstream.Close()

	exec := newOverloadTestExecutor()
	called := false
	exec.NativeResponsesStream = func(_ context.Context, w http.ResponseWriter, resp *http.Response, _ string, _ *audit.StreamCapture) StreamOutcome {
		called = true
		defer resp.Body.Close()
		_, _ = io.Copy(w, resp.Body)
		return StreamOutcome{ChunkCount: 1}
	}
	rec := httptest.NewRecorder()
	result, err := exec.executeOpenAI(&ExecParams{
		W: rec, R: httptest.NewRequest(http.MethodPost, "/v1/responses", nil), IsStream: true,
		BodyBytes: []byte(`{"model":"gpt-responses","messages":[]}`), ResponsesBodyBytes: requestBody,
		ClientProtocol: "openai-responses", ClientModel: "gpt-responses", OutboundModel: "gpt-responses",
		ClientID: identity.ClientIdentity{IdentityHash: "native-stream-route-test"},
	}, nativeResponsesStreamCandidate(upstream.URL, true), 0, time.Now(), nil)
	if err != nil || result == nil || !called {
		t.Fatalf("executeOpenAI() = (%#v, %v), native_handler_called=%v", result, err, called)
	}
	if gotPath != "/v1/responses" || !bytes.Equal(gotBody, requestBody) {
		t.Fatalf("upstream path/body = %q/%s, want /v1/responses/%s", gotPath, gotBody, requestBody)
	}
	if rec.Body.String() != streamBody {
		t.Fatalf("client stream changed: got %q want %q", rec.Body.String(), streamBody)
	}
}

func TestExecuteOpenAI_NativeResponsesStreamRequiresIndependentCapability(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R: httptest.NewRequest(http.MethodPost, "/v1/responses", nil), IsStream: true,
		BodyBytes:          []byte(`{"model":"gpt-responses","messages":[]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-responses","input":"hello","stream":true}`),
		ClientProtocol:     "openai-responses", ClientModel: "gpt-responses",
	}, nativeResponsesStreamCandidate(upstream.URL, false), 0, time.Now(), nil)
	if err == nil || called {
		t.Fatalf("executeOpenAI() = %v, upstream_called=%v; want capability rejection", err, called)
	}
}

func TestExecuteOpenAI_NativeResponsesNonStreamPreservesRequestAndResponse(t *testing.T) {
	requestBody := []byte(`{"model":"gpt-responses","input":"hello","instructions":"be concise","previous_response_id":"resp_previous","temperature":0.2,"metadata":{"trace":"keep"}}`)
	responseBody := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]},{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{}"}],"usage":{"input_tokens":12,"output_tokens":4}}`)
	var gotPath string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		if got := r.Header.Get("Authorization"); got != "Bearer sk-native-test" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	params := &ExecParams{
		W:                  rec,
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		BodyBytes:          []byte(`{"model":"gpt-responses","messages":[{"role":"user","content":"wrong fallback"}]}`),
		ResponsesBodyBytes: requestBody,
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-responses",
		OutboundModel:      "gpt-responses",
		ClientID:           identity.ClientIdentity{IdentityHash: "native-response-test"},
	}
	result, err := newOverloadTestExecutor().executeOpenAI(params, nativeResponsesCandidate(upstream.URL, true), 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}
	if result == nil || !bytes.Equal(result.ResponseBody, responseBody) {
		t.Fatalf("ResponseBody = %s, want byte-preserved native body", result.ResponseBody)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("upstream path = %q, want /v1/responses", gotPath)
	}
	if !bytes.Equal(gotBody, requestBody) {
		t.Fatalf("upstream body = %s, want %s", gotBody, requestBody)
	}
	if !bytes.Equal(rec.Body.Bytes(), responseBody) {
		t.Fatalf("client body = %s, want %s", rec.Body.Bytes(), responseBody)
	}
	if got := rec.Header().Get("Content-Length"); got != "" && got != strconv.Itoa(len(responseBody)) {
		t.Fatalf("Content-Length = %q", got)
	}
}

func TestExecuteOpenAI_NativeResponsesRetriesAndReplaysBody(t *testing.T) {
	requestBody := []byte(`{"model":"gpt-responses","input":"retry me","metadata":{"keep":true}}`)
	responseBody := []byte(`{"id":"resp_retry","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	var calls int
	var bodies [][]byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	result, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		W: rec, R: httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		ResponsesBodyBytes: requestBody, BodyBytes: []byte(`{"model":"gpt-responses","messages":[]}`),
		ClientProtocol: "openai-responses", ClientModel: "gpt-responses", OutboundModel: "gpt-responses",
		ClientID: identity.ClientIdentity{IdentityHash: "native-retry-test"},
	}, nativeResponsesCandidate(upstream.URL, true), 1, time.Now(), nil)
	if err != nil || result == nil {
		t.Fatalf("executeOpenAI() = (%#v, %v), want retry success", result, err)
	}
	if calls != 2 || !bytes.Equal(bodies[0], requestBody) || !bytes.Equal(bodies[1], requestBody) {
		t.Fatalf("calls=%d bodies=%q, want two identical Responses bodies", calls, bodies)
	}
}

func TestExecuteOpenAI_NativeResponsesEmptyOutputFailsBeforeWrite(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_empty","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	result, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		W:                  rec,
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		BodyBytes:          []byte(`{"model":"gpt-responses","messages":[]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-responses","input":"hello"}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-responses",
		ClientID:           identity.ClientIdentity{IdentityHash: "native-empty-test"},
	}, nativeResponsesCandidate(upstream.URL, true), 0, time.Now(), nil)
	if result != nil {
		t.Fatalf("result = %#v, want nil", result)
	}
	if err == nil {
		t.Fatal("executeOpenAI() error = nil, want empty-response error")
	}
	upstreamErr, ok := err.(*upstreampkg.Error)
	if !ok || upstreamErr.Kind != errorsx.KindEmptyResponse {
		t.Fatalf("error = %#v, want upstream empty_response", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("client received %q before validation", rec.Body.String())
	}
}

func TestExecuteOpenAI_NativeResponsesCapabilityOffDoesNotCallUpstream(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	_, err := newOverloadTestExecutor().executeOpenAI(&ExecParams{
		R:                  httptest.NewRequest(http.MethodPost, "/v1/responses", nil),
		BodyBytes:          []byte(`{"model":"gpt-responses","messages":[]}`),
		ResponsesBodyBytes: []byte(`{"model":"gpt-responses","input":"hello"}`),
		ClientProtocol:     "openai-responses",
		ClientModel:        "gpt-responses",
	}, nativeResponsesCandidate(upstream.URL, false), 0, time.Now(), nil)
	if err == nil {
		t.Fatal("executeOpenAI() error = nil, want capability error")
	}
	if called {
		t.Fatal("capability-off native candidate contacted upstream")
	}
}

func TestValidateNativeResponsesBodyRejectsFailedEnvelope(t *testing.T) {
	body := []byte(`{"object":"response","status":"failed","error":{"code":"provider_error"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`)
	if err := validateNativeResponsesBody(body); err == nil {
		t.Fatal("validateNativeResponsesBody() = nil for failed envelope")
	} else if upstreamErr, ok := err.(*upstreampkg.Error); !ok || upstreamErr.Kind != errorsx.KindTransient {
		t.Fatalf("error = %#v, want transient upstream error", err)
	}
}

func TestNativeResponsesUsage(t *testing.T) {
	in, out := nativeResponsesUsage([]byte(`{"object":"response","usage":{"input_tokens":12,"output_tokens":4}}`))
	if in == nil || out == nil || *in != 12 || *out != 4 {
		t.Fatalf("usage = (%v, %v), want (12, 4)", in, out)
	}
}

func TestValidateNativeResponsesBodyRejectsMalformedEnvelope(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"object":"chat.completion","output":[{"type":"message"}]}`),
		[]byte(`{"object":"response","output":null}`),
		[]byte(`{"object":"response","output":[}`),
	} {
		if err := validateNativeResponsesBody(body); err == nil {
			t.Fatalf("validateNativeResponsesBody(%s) = nil", body)
		}
	}
}
