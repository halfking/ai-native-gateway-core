package streaming

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	outputhook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
	"github.com/redis/go-redis/v9"
)

type nativePassingChecker struct{}

func (nativePassingChecker) Check(_ context.Context, _, output string) (*outputcompliance.ComplianceResult, error) {
	return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
}

type nativeJoinedFailureChecker struct{}

func (nativeJoinedFailureChecker) Check(_ context.Context, _, output string) (*outputcompliance.ComplianceResult, error) {
	if output == "topsecretAtopsecretB" {
		return nil, errors.New("joined lane check failed")
	}
	return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
}

func nativeComplianceDeltaFrame(protocol, text string) []byte {
	quoted := strconv.Quote(text)
	switch protocol {
	case "anthropic-messages":
		return []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":" + quoted + "}}\n\n")
	case "openai-responses":
		return []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":" + quoted + "}\n\n")
	default:
		return []byte("data: {\"choices\":[{\"delta\":{\"content\":" + quoted + "}}]}\n\n")
	}
}

func TestRealComplianceStreamCapAndFlushFailureRenderOneProtocolTerminal(t *testing.T) {
	for _, tc := range []struct{ protocol, terminal string }{
		{"anthropic-messages", "event: error\n"},
		{"openai-responses", "event: response.failed\n"},
		{"openai-completions", "data: [DONE]\n\n"},
	} {
		for _, failure := range []string{"pending cap", "flush check"} {
			t.Run(tc.protocol+"/"+failure, func(t *testing.T) {
				var checker interface {
					Check(context.Context, string, string) (*outputcompliance.ComplianceResult, error)
				} = nativePassingChecker{}
				if failure == "flush check" {
					checker = nativeJoinedFailureChecker{}
				}
				rec := httptest.NewRecorder()
				writer := newInterceptingStreamWriter(rec,
					response.NewInterceptorChain(outputhook.NewOutputComplianceInterceptor(checker, nil)),
					context.Background(), response.StreamMeta{TenantID: "tenant-1", SessionID: "gw-native", ClientProtocol: tc.protocol})
				if failure == "pending cap" {
					if _, err := writer.Write(nativeComplianceDeltaFrame(tc.protocol, strings.Repeat("sensitive", 140000))); err == nil {
						t.Fatal("pending cap did not stop oversized policy state")
					}
				} else {
					for _, fragment := range []string{"topsecretA", "topsecretB"} {
						if _, err := writer.Write(nativeComplianceDeltaFrame(tc.protocol, fragment)); err != nil {
							t.Fatalf("fragment rejected before flush: %v", err)
						}
					}
				}
				writer.finish()
				if !writer.blocked || writer.writeErr == nil || strings.Count(rec.Body.String(), tc.terminal) != 1 ||
					strings.Contains(rec.Body.String(), "sensitive") || strings.Contains(rec.Body.String(), "topsecret") {
					t.Fatalf("policy failure leaked or terminal missing: %q", rec.Body.String())
				}
			})
		}
	}
}

type nativePhoneChecker struct{}

var nativePhonePattern = regexp.MustCompile(`1[3-9][0-9]{9}`)

func (nativePhoneChecker) Check(_ context.Context, _, output string) (*outputcompliance.ComplianceResult, error) {
	match := nativePhonePattern.FindStringIndex(output)
	if match == nil {
		return &outputcompliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
	}
	return &outputcompliance.ComplianceResult{
		Issues:         []outputcompliance.ComplianceIssue{{Type: "pii", Subtype: "phone", Location: "char:" + strconv.Itoa(match[0]) + "-" + strconv.Itoa(match[1])}},
		RedactedOutput: output[:match[0]] + "[PHONE]" + output[match[1]:],
	}, nil
}

func TestNativeHandlersApplyRealOutputComplianceBeforeNonStreamWrite(t *testing.T) {
	for _, tc := range []struct {
		name, protocol string
		write          func(*ChatHandler, *httptest.ResponseRecorder, nativeResponseInterception) []byte
	}{
		{"messages", "anthropic-messages", func(ch *ChatHandler, w *httptest.ResponseRecorder, opts nativeResponseInterception) []byte {
			return NewMessagesHandler(ch).writeNonStreamResponse(w, []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"call 13800138000"}]}`), "m", "req", 0, opts)
		}},
		{"responses", "openai-responses", func(ch *ChatHandler, w *httptest.ResponseRecorder, opts nativeResponseInterception) []byte {
			return NewResponsesHandler(ch).writeNonStreamResponse(w, []byte(`{"object":"response","output":[{"type":"message","content":[{"type":"output_text","text":"call 13800138000"}]}]}`), "m", "req", opts)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, nil)
			ch := &ChatHandler{responseInterceptor: response.NewInterceptorChain(hook)}
			rec := httptest.NewRecorder()
			body := tc.write(ch, rec, nativeResponseInterception{
				ctx:     context.Background(),
				request: response.InterceptRequest{TenantID: "tenant-1", SessionID: "gw-native", ClientProtocol: tc.protocol},
			})
			if rec.Code != 200 || rec.Body.String() != string(body) || strings.Contains(string(body), "13800138000") ||
				!strings.Contains(string(body), "[PHONE]") {
				t.Fatalf("real hook did not govern wire bytes: status=%d body=%q returned=%q", rec.Code, rec.Body.String(), body)
			}
		})
	}
}

func TestNativeStreamRealOutputComplianceRedactsCrossFramePhoneBeforeWire(t *testing.T) {
	for _, tc := range []struct{ protocol, event, terminal string }{
		{"anthropic-messages", "content_block_delta", "event: message_stop\n"},
		{"openai-responses", "response.output_text.delta", "event: response.completed\n"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			hook := outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, nil)
			writer := newInterceptingStreamWriter(httptest.NewRecorder(), response.NewInterceptorChain(hook), context.Background(),
				response.StreamMeta{TenantID: "tenant-1", SessionID: "gw-native", ClientProtocol: tc.protocol})
			rec := writer.w.(*httptest.ResponseRecorder)
			for _, fragment := range []string{"138", "001", "38000 "} {
				var frame string
				if tc.protocol == "anthropic-messages" {
					frame = "event: " + tc.event + "\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" + fragment + "\"}}\n\n"
				} else {
					frame = "event: " + tc.event + "\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + fragment + "\"}\n\n"
				}
				_, _ = writer.Write([]byte(frame))
			}
			if tc.protocol == "anthropic-messages" {
				_, _ = writer.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
			} else {
				_, _ = writer.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
			}
			writer.finish()
			if got := rec.Body.String(); strings.Contains(got, "138") || strings.Contains(got, "001") ||
				strings.Contains(got, "38000") || !strings.Contains(got, "[PHONE]") || strings.Count(got, tc.terminal) != 1 {
				t.Fatalf("cross-frame PII leaked or terminal missing: %q", got)
			}
		})
	}
}

func TestRestoredSensitiveFrameIsHeldUntilRealOutputComplianceApproval(t *testing.T) {
	for _, tc := range []struct {
		protocol, first, terminal string
	}{
		{"openai-completions", "data: {\"choices\":[{\"delta\":{\"content\":\"call {SENSITIVE:phone:1}\"}}]}\n\n", "data: [DONE]\n\n"},
		{"anthropic-messages", "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"call {SENSITIVE:phone:1}\"}}\n\n", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{"openai-responses", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"call {SENSITIVE:phone:1}\"}\n\n", "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			mini, err := miniredis.Run()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(mini.Close)
			rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
			t.Cleanup(func() { _ = rdb.Close() })
			ctx := context.Background()
			const sessionID = "gw-restored-policy"
			if err := rdb.HSet(ctx, sanitize.SanitizeRedisKey(sanitize.HashTenant("tenant-1"), sessionID), "{SENSITIVE:phone:1}", "13800138000").Err(); err != nil {
				t.Fatal(err)
			}
			sanitizer, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
			if err != nil {
				t.Fatal(err)
			}
			restore, err := sanitize.NewSanitizeRestoreInterceptor(sanitizer, rdb, 30*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			chain := response.NewInterceptorChain(restore, outputhook.NewOutputComplianceInterceptor(nativePhoneChecker{}, nil))
			rec := httptest.NewRecorder()
			writer := newInterceptingStreamWriter(rec, chain, ctx, response.StreamMeta{SessionID: sessionID, TenantID: "tenant-1", ClientProtocol: tc.protocol})
			if _, err := writer.Write([]byte(tc.first)); err != nil {
				t.Fatalf("first frame rejected: %v", err)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("restored unapproved frame leaked before terminal: %q", rec.Body.String())
			}
			if _, err := writer.Write([]byte(tc.terminal)); err != nil {
				t.Fatalf("terminal frame rejected: %v", err)
			}
			writer.finish()
			got := rec.Body.String()
			if writer.writeErr != nil || strings.Contains(got, "13800138000") || strings.Contains(got, "{SENSITIVE:") || !strings.Contains(got, "[PHONE]") {
				t.Fatalf("restored output was not safely released: err=%v wire=%q", writer.writeErr, got)
			}
		})
	}
}

type nativeTerminalResolver struct{ candidate provider.Candidate }

func (r nativeTerminalResolver) Enabled() bool                           { return true }
func (r nativeTerminalResolver) ModelKnown(context.Context, string) bool { return true }
func (r nativeTerminalResolver) GetCandidates(ctx context.Context, model, profile, tenant string) ([]provider.Candidate, *provider.Policy, error) {
	return r.GetCandidatesByModality(ctx, model, profile, tenant, "text")
}
func (r nativeTerminalResolver) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return []provider.Candidate{r.candidate}, provider.DefaultPolicy(), nil
}

type nativePolicyBlockAttemptExecutor struct {
	protocol string
	calls    int
}

func (e *nativePolicyBlockAttemptExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	e.calls++
	frame := nativeComplianceDeltaFrame(e.protocol, "provider-secret")
	_, writeErr := params.W.Write(frame)
	if writeErr == nil {
		writeErr = errors.New("synthetic stream ended after policy frame")
	}
	return nil, &executors.ExecuteError{
		LastKind: errorsx.KindContentFilter,
		LastErr:  writeErr,
		Tried:    1,
	}
}

func TestNativeSurvivalStreamBlockWritesOneProtocolTerminalAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, protocol, terminal string
		serve                                func(*ChatHandler) http.Handler
	}{
		{
			name: "messages", path: "/v1/messages", protocol: "anthropic-messages", terminal: "event: error\n",
			body:  `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			serve: func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) },
		},
		{
			name: "responses", path: "/v1/responses", protocol: "openai-responses", terminal: "event: response.failed\n",
			body:  `{"model":"m","stream":true,"input":"hi"}`,
			serve: func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := &nativePolicyBlockAttemptExecutor{protocol: tc.protocol}
			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.executor = &executors.Executor{}
			h.provider = nativeTerminalResolver{candidate: provider.Candidate{
				ProviderID: 11, CredentialID: 22, Protocol: "openai-completions", CatalogCode: "native-survival-test",
				RawModel: "m", OfferRawModel: "m", Tier: 1, Weight: 100, Routable: true,
				LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
			}}
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", ApplicationID: 7}})
			h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{
				Deadline: time.Second, RetryBase: time.Millisecond, RetryInterval: time.Millisecond,
				MaxRetries: 1, NightMaxRetries: 1, KeepaliveInterval: time.Hour,
			})
			h.survivalAttemptExec = attempt
			h.SetResponseInterceptor(response.NewInterceptorChain(&nativeNonStreamInterceptor{blocked: true}))
			var entries []*telemetry.RequestLogEntry
			h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) { entries = append(entries, entry) })

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-test")
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, req)
			wire := strings.TrimPrefix(rec.Body.String(), ": keep-alive\n\n")
			if attempt.calls != 1 {
				t.Fatalf("survival attempts=%d, want one policy-rejected upstream attempt", attempt.calls)
			}
			if strings.Count(wire, tc.terminal) != 1 || !strings.HasPrefix(wire, tc.terminal) ||
				strings.Contains(wire, "provider-secret") || strings.Contains(wire, "gateway_survival_") {
				t.Fatalf("survival block emitted a mixed/duplicate terminal: %q", wire)
			}
			failures := 0
			for _, entry := range entries {
				if entry.Success {
					t.Fatalf("blocked survival stream emitted success audit: %+v", entry)
				}
				if entry.ErrorKind != nil && *entry.ErrorKind == "output_policy_blocked" {
					failures++
				}
			}
			if failures != 1 {
				t.Fatalf("output_policy_blocked audit rows=%d entries=%+v", failures, entries)
			}
		})
	}
}

func TestNativeNormalStreamBlockWritesOneProtocolTerminalAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, protocol, terminal string
		serve                                func(*ChatHandler) http.Handler
	}{
		{
			name: "messages", path: "/v1/messages", protocol: "anthropic-messages", terminal: "event: error\n",
			body:  `{"model":"m","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			serve: func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) },
		},
		{
			name: "responses", path: "/v1/responses", protocol: "openai-responses", terminal: "event: response.failed\n",
			body:  `{"model":"m","stream":true,"input":"hi"}`,
			serve: func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var upstreamCalls int
			var upstreamMu sync.Mutex
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				upstreamMu.Lock()
				upstreamCalls++
				upstreamMu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"provider-secret\"},\"finish_reason\":null}]}\n\n")
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer upstream.Close()

			limiter := credential.NewLimiter()
			defer limiter.Stop()
			executor := executors.NewExecutor(
				executors.NewRouter(executors.NewStickyCache(), limiter),
				credential.NewManager(), limiter, pool.NewPoolManager(nil), nil, nil, nil, nil,
			)
			// NewExecutor deliberately leaves protocol bridges unset; main.go
			// wires them in production. Exercise the same OpenAI→client bridges
			// here so the request reaches the intercepting writer with converted
			// protocol frames instead of an empty stream.
			executor.OpenAIToAnthropicStream = func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any, inputTokensEstimate int, toolsRequested bool) executors.StreamOutcome {
				return StreamOpenAIToAnthropicSSE(ctx, w, resp, clientModel, outboundModel, requestID, capture, nil)
			}
			executor.OpenAIToResponsesStream = func(ctx context.Context, w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, requestID string, capture *audit.StreamCapture, pc any, toolsRequested bool) executors.StreamOutcome {
				return StreamOpenAIToResponsesSSE(ctx, w, resp, clientModel, outboundModel, requestID, capture, nil)
			}
			pipeline := executor.NewDispatchPipeline(nil)
			pipeline.Start()
			defer pipeline.Stop()
			executor.SetDispatchPipeline(pipeline)

			h := NewChatHandler(nil, nil, nil, nil, nil, nil)
			h.executor = executor
			h.provider = nativeTerminalResolver{candidate: provider.Candidate{
				ProviderID: 11, CredentialID: 22, BaseURL: upstream.URL, Protocol: "openai-completions",
				CatalogCode: "native-terminal-test", RawModel: "m", OfferRawModel: "m", APIKey: "upstream-key",
				Tier: 1, Weight: 100, BillingMode: "token_plan", Routable: true,
				LifecycleStatus: "active", AvailabilityState: "ready", QuotaState: "ok", CircuitState: "closed",
			}}
			h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{ID: 42, TenantID: "tenant-1", ApplicationID: 7}})
			blocker := &nativeNonStreamInterceptor{blocked: true}
			h.SetResponseInterceptor(response.NewInterceptorChain(blocker))
			var entries []*telemetry.RequestLogEntry
			h.SetRequestLogHook(func(entry *telemetry.RequestLogEntry) { entries = append(entries, entry) })

			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer sk-test")
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("stream status=%d body=%q", rec.Code, rec.Body.String())
			}
			wire := strings.TrimPrefix(rec.Body.String(), ": keep-alive\n\n")
			if strings.Count(wire, tc.terminal) != 1 || !strings.HasPrefix(wire, tc.terminal) ||
				strings.Contains(wire, "provider-secret") || strings.Contains(wire, "Upstream request failed") ||
				strings.Contains(wire, `"error":{"message"`) {
				upstreamMu.Lock()
				calls := upstreamCalls
				upstreamMu.Unlock()
				t.Fatalf("blocked native response did not produce one clean protocol terminal: status=%d wire=%q original=%q calls=%d interceptor_calls=%d entries=%+v", rec.Code, wire, rec.Body.String(), calls, blocker.seenCalls, entries)
			}
			upstreamMu.Lock()
			calls := upstreamCalls
			upstreamMu.Unlock()
			if calls != 1 {
				t.Fatalf("upstream calls=%d, want one", calls)
			}
			if breaker := executor.Circuit.Get(11, 22); breaker != nil && breaker.ConsecutiveFailures() != 0 {
				t.Fatalf("gateway output-policy block counted as provider failure: consecutive=%d", breaker.ConsecutiveFailures())
			}
			failures := 0
			for _, entry := range entries {
				if entry.Success {
					t.Fatalf("blocked stream emitted success audit: %+v", entry)
				}
				if entry.ErrorKind != nil && *entry.ErrorKind == "output_policy_blocked" {
					failures++
					if entry.ProviderID == nil || *entry.ProviderID != 11 || entry.CredentialID == nil || *entry.CredentialID != 22 {
						t.Fatalf("failure lost provider attribution: %+v", entry)
					}
				}
			}
			if failures != 1 {
				t.Fatalf("output_policy_blocked audit rows=%d entries=%+v", failures, entries)
			}
		})
	}
}
