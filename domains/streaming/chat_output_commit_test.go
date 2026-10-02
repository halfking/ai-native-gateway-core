package streaming

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	outputhook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

const chatOutputProviderBody = `{"id":"chat_1","object":"chat.completion","model":"m","choices":[{"message":{"role":"assistant","content":"call 13800138000"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`

type chatMandatoryErrorInterceptor struct{ nativeNonStreamInterceptor }

func (*chatMandatoryErrorInterceptor) FailClosed() bool { return true }

type chatUpstreamFixture struct {
	body, contentType string
}

func runChatOutputCommit(t *testing.T, interceptor response.ResponseInterceptor, redact func([]byte, string, string) []byte) (*httptest.ResponseRecorder, []*telemetry.RequestLogEntry) {
	return runChatOutputCommitWith(t, interceptor, redact,
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil)
}

func runChatOutputCommitWith(t *testing.T, interceptor response.ResponseInterceptor, redact func([]byte, string, string) []byte,
	requestBody string, configure func(*executors.Executor, *ChatHandler, *http.Request), upstreamFixture ...chatUpstreamFixture,
) (*httptest.ResponseRecorder, []*telemetry.RequestLogEntry) {
	t.Helper()
	fixture := chatUpstreamFixture{body: chatOutputProviderBody, contentType: "application/json"}
	if len(upstreamFixture) > 0 {
		fixture = upstreamFixture[0]
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", fixture.contentType)
		w.Header().Set("X-Provider-Hint", "retained")
		_, _ = w.Write([]byte(fixture.body))
	}))
	t.Cleanup(upstream.Close)
	limiter := credential.NewLimiter()
	t.Cleanup(limiter.Stop)
	executor := executors.NewExecutor(executors.NewRouter(executors.NewStickyCache(), limiter),
		credential.NewManager(), limiter, pool.NewPoolManager(nil), nil, nil, nil, nil)
	executor.RedactBodyFn = redact
	pipeline := executor.NewDispatchPipeline(nil)
	pipeline.Start()
	t.Cleanup(pipeline.Stop)
	executor.SetDispatchPipeline(pipeline)
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.executor = executor
	h.provider = nativeOutputAuditResolver{candidate: provider.Candidate{
		ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL, Protocol: "openai-completions",
		CatalogCode: "openai", RawModel: "m", OfferRawModel: "m", APIKey: "upstream-key",
		Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true,
		LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
	}}
	owner := "alice"
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", OwnerUser: &owner}})
	client := telemetry.NewClient()
	client.SetRequestLogSink(nativeOutputAuditSink{})
	t.Cleanup(client.Stop)
	h.SetTelemetry(client)
	h.SetResponseInterceptor(interceptor)
	var entries []*telemetry.RequestLogEntry
	h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) {
		copy := *entry
		entries = append(entries, &copy)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(requestBody))
	req.Header.Set("Authorization", "Bearer sk-test")
	if configure != nil {
		configure(executor, h, req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, entries
}

type chatFlushFailureInterceptor struct{}

func (*chatFlushFailureInterceptor) FailClosed() bool { return true }
func (*chatFlushFailureInterceptor) InterceptNonStream(context.Context, *response.InterceptRequest) (*response.InterceptResult, error) {
	return nil, nil
}
func (*chatFlushFailureInterceptor) InterceptStreamChunk(context.Context, []byte, *response.StreamMeta) (*response.ChunkResult, error) {
	return &response.ChunkResult{SuppressChunk: true}, nil
}
func (*chatFlushFailureInterceptor) InterceptStreamEnd(context.Context, *response.StreamMeta) (*response.EndResult, error) {
	return nil, nil
}
func (*chatFlushFailureInterceptor) FlushStreamPending(context.Context, *response.StreamMeta) ([]byte, error) {
	return nil, errors.New("checker failed at stream finish")
}

func TestChatStreamFlushFailureHasOneTerminalAndFailureAudit(t *testing.T) {
	const sse = "data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"private provider body\"}}]}\n\n" +
		"data: {\"id\":\"chunk-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	hook := response.NewInterceptorChain(&chatFlushFailureInterceptor{})
	rec, entries := runChatOutputCommitWith(t, hook, nil,
		`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil,
		chatUpstreamFixture{body: sse, contentType: "text/event-stream"})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "private provider body") ||
		strings.Count(rec.Body.String(), "Response blocked by output policy") != 1 ||
		strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
		t.Fatalf("stream policy terminal invalid: status=%d body=%q", rec.Code, rec.Body.String())
	}
	failures := 0
	for _, entry := range entries {
		if entry.Success || entry.ResponseBody != nil && strings.Contains(*entry.ResponseBody, "private provider body") {
			t.Fatalf("failed stream recorded success or provider body: %+v", entry)
		}
		if entry.ErrorKind != nil && *entry.ErrorKind == "output_policy_blocked" {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("output_policy_blocked audit rows=%d, entries=%+v", failures, entries)
	}
}

type chatPendingReplaySource struct{ calls int }

func (s *chatPendingReplaySource) Get(context.Context, string, string) (*pending.Response, bool, error) {
	return nil, false, nil
}

func (s *chatPendingReplaySource) GetLatest(context.Context, string) (*pending.Response, string, bool, error) {
	s.calls++
	return &pending.Response{
		Body:   `{"choices":[{"message":{"content":"cached unchecked body"}}]}`,
		Status: pending.StatusCompleted, ContentType: "application/json",
	}, "old-request", true, nil
}

func TestChatDeferredOutputDoesNotBypassHookViaCachedReplay(t *testing.T) {
	source := &chatPendingReplaySource{}
	interceptor := response.NewInterceptorChain(outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, nil))
	rec, _ := runChatOutputCommitWith(t, interceptor, nil,
		`{"model":"m","messages":[{"role":"user","content":"retry"}]}`,
		func(executor *executors.Executor, _ *ChatHandler, req *http.Request) {
			executor.PendingStore = pending.NewStoreWithFallback(nil, time.Minute, source)
			req.Header.Set("X-Gw-Session-Id", "gw_cached-replay-test")
		})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "[PHONE]") ||
		strings.Contains(rec.Body.String(), "cached unchecked body") || source.calls != 0 {
		t.Fatalf("cached body bypassed output governance: status=%d calls=%d body=%q", rec.Code, source.calls, rec.Body.String())
	}
}

func TestChatDeferredOutputRealComplianceGovernsClientBytesAndAudit(t *testing.T) {
	interceptor := response.NewInterceptorChain(outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, nil))
	rec, entries := runChatOutputCommit(t, interceptor, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "13800138000") || !strings.Contains(rec.Body.String(), "[PHONE]") {
		t.Fatalf("client received ungoverned body: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("length = %s, wire bytes = %d", got, rec.Body.Len())
	}
	if got := rec.Header().Get("X-Provider-Hint"); got != "retained" {
		t.Fatalf("upstream end-to-end header lost: %q", got)
	}
	if len(entries) == 0 || !entries[len(entries)-1].Success || entries[len(entries)-1].ResponseBody == nil ||
		strings.Contains(*entries[len(entries)-1].ResponseBody, "13800138000") {
		t.Fatalf("approved client body missing from success audit: %+v", entries)
	}
}

func TestChatDeferredOutputWithoutHookStillWritesBodyAndRedacts(t *testing.T) {
	rec, _ := runChatOutputCommit(t, nil, func(body []byte, _, _ string) []byte {
		return bytes.ReplaceAll(body, []byte("13800138000"), []byte("[REDACTED]"))
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "[REDACTED]") || strings.Contains(rec.Body.String(), "13800138000") {
		t.Fatalf("deferred success did not apply executor redactor: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("incorrect length after write-time redaction: %v", rec.Header())
	}
}

func TestChatDeferredOutputBlockAndMandatoryFailureHaveNoSuccessAudit(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
		hook       response.ResponseInterceptor
	}{
		{"block", "output_policy_blocked", http.StatusForbidden, &nativeNonStreamInterceptor{blocked: true}},
		{"mandatory error", "response_validation_failed", http.StatusBadGateway,
			response.NewInterceptorChain(&chatMandatoryErrorInterceptor{nativeNonStreamInterceptor{err: errors.New("checker failed")}})},
		{"invalid replacement", "response_validation_failed", http.StatusBadGateway,
			&nativeNonStreamInterceptor{modified: []byte(`{"choices":"invalid"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, entries := runChatOutputCommit(t, tc.hook, nil)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) || strings.Contains(rec.Body.String(), "13800138000") {
				t.Fatalf("invalid client terminal: status=%d body=%q", rec.Code, rec.Body.String())
			}
			failures := 0
			for _, entry := range entries {
				if entry.Success || entry.ResponseBody != nil && strings.Contains(*entry.ResponseBody, "13800138000") {
					t.Fatalf("rejected provider body persisted as success: %+v", entry)
				}
				if entry.ErrorKind != nil && *entry.ErrorKind == tc.code {
					failures++
					if entry.ProviderID == nil || *entry.ProviderID != 11 || entry.CredentialID == nil || *entry.CredentialID != 22 {
						t.Fatalf("failure attribution lost: %+v", entry)
					}
				}
			}
			if failures != 1 {
				t.Fatalf("failure audit rows = %d, entries=%+v", failures, entries)
			}
		})
	}
}
