package streaming

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/durable"
	"github.com/kaixuan/llm-gateway-go/provider"
)

type durableEndpointVerifier struct{ key *authentication.KeyInfo }

func (v durableEndpointVerifier) Enabled() bool { return true }
func (v durableEndpointVerifier) Verify(context.Context, string) (*authentication.KeyInfo, error) {
	return v.key, nil
}
func (v durableEndpointVerifier) VerifyByID(context.Context, int) (*authentication.KeyInfo, error) {
	return v.key, nil
}
func (durableEndpointVerifier) CheckBudget(context.Context, int) error { return nil }
func (durableEndpointVerifier) LookupKeyMeta(context.Context, string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

type durableEndpointResolver struct {
	err error
}

func (r durableEndpointResolver) Enabled() bool { return true }
func (r durableEndpointResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return r.GetCandidatesByModality(context.Background(), "", "", "", "")
}
func (r durableEndpointResolver) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	return []provider.Candidate{{ProviderID: 1, CredentialID: 2, Protocol: "openai"}}, provider.DefaultPolicy(), nil
}
func (durableEndpointResolver) ModelKnown(context.Context, string) bool { return true }

type durableEndpointExecutor struct {
	write  string
	cancel context.CancelFunc
	err    error
}

func (e durableEndpointExecutor) Execute(p *executors.ExecParams) (*executors.ExecuteResult, error) {
	if e.write != "" {
		if _, err := p.W.Write([]byte(e.write)); err != nil {
			return nil, err
		}
	}
	if e.cancel != nil {
		e.cancel()
		return nil, context.Canceled
	}
	if e.err != nil {
		return nil, e.err
	}
	return &executors.ExecuteResult{}, nil
}

func newDurableNonChatHandler(store *fakeForegroundStore, resolver durableEndpointResolver, exec AttemptExecutor) *ChatHandler {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	h.setRequestKeyVerifierForTest(durableEndpointVerifier{key: &authentication.KeyInfo{
		ID: 42, TenantID: "tenant-1", ApplicationID: 7,
	}})
	h.SetDurableExecution(store, func(string) bool { return true }, DurableExecutionOptions{})
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{})
	h.provider = resolver
	h.survivalAttemptExec = exec
	return h
}

func newDurableNonChatRequest(t *testing.T, path, body string, ctx context.Context) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer sk-test")
	r.Header.Set("X-Gw-Session-Id", "gw_durable_endpoint")
	r.Header.Set(GatewayCapabilitiesHeader, CapabilityDurableRecovery)
	return r.WithContext(ctx)
}

func TestDurableNonChatEndpointsReleaseCandidateResolutionFailure(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		body  string
		serve func(*ChatHandler) http.Handler
	}{
		{"messages", "/v1/messages", `{"model":"gpt-4o","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`,
			func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeForegroundStore{}
			h := newDurableNonChatHandler(store, durableEndpointResolver{err: errors.New("routing unavailable")}, nil)
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, newDurableNonChatRequest(t, tc.path, tc.body, context.Background()))
			if len(store.created) != 1 || len(store.resched) != 1 {
				t.Fatalf("durable calls created=%d rescheduled=%d, want 1/1", len(store.created), len(store.resched))
			}
			if store.resched[0].Reason != "candidate_resolution_failed" {
				t.Fatalf("reschedule reason = %q", store.resched[0].Reason)
			}
		})
	}
}

func TestDurableNonChatEndpointsPostContentDisconnectBlocksReplay(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		body  string
		frame string
		serve func(*ChatHandler) http.Handler
	}{
		{"messages", "/v1/messages", `{"model":"gpt-4o","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n",
			func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`,
			"event: response.output_text.delta\ndata: {\"delta\":\"hi\"}\n\n",
			func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
		{"messages tool", "/v1/messages", `{"model":"gpt-4o","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"event: content_block_start\ndata: {\"content_block\":{\"type\":\"tool_use\"}}\n\n",
			func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses tool", "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`,
			"event: response.function_call_arguments.delta\ndata: {\"delta\":\"{}\"}\n\n",
			func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeForegroundStore{}
			ctx, cancel := context.WithCancel(context.Background())
			h := newDurableNonChatHandler(store, durableEndpointResolver{}, durableEndpointExecutor{write: tc.frame, cancel: cancel})
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, newDurableNonChatRequest(t, tc.path, tc.body, ctx))
			if len(store.resched) != 0 || len(store.terminals) != 0 {
				t.Fatalf("post-content disconnect must leave safety reaper ownership: reschedules=%+v terminals=%+v", store.resched, store.terminals)
			}
			if rec.Body.Len() == 0 {
				t.Fatal("semantic frame was not emitted")
			}
		})
	}
}

func TestDurableNonChatEndpointsLeaseLossRendersOneNativeTerminal(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		body  string
		frame string
		term  string
		serve func(*ChatHandler) http.Handler
	}{
		{"messages", "/v1/messages", `{"model":"gpt-4o","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n", "event: error\n",
			func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`,
			"event: response.output_text.delta\ndata: {\"delta\":\"hi\"}\n\n", "event: response.failed\n",
			func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeForegroundStore{checkErr: durable.ErrLeaseLost}
			h := newDurableNonChatHandler(store, durableEndpointResolver{}, durableEndpointExecutor{write: tc.frame})
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, newDurableNonChatRequest(t, tc.path, tc.body, context.Background()))
			out := rec.Body.String()
			if strings.Contains(out, "hi") || strings.Count(out, tc.term) != 1 {
				t.Fatalf("lease-loss terminal mismatch: %q", out)
			}
		})
	}
}

func TestDurableNonChatEndpointsCheckpointFailureRendersOneNativeTerminal(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		body  string
		frame string
		term  string
		serve func(*ChatHandler) http.Handler
	}{
		{"messages", "/v1/messages", `{"model":"gpt-4o","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n", "event: error\n",
			func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`,
			"event: response.output_text.delta\ndata: {\"delta\":\"hi\"}\n\n", "event: response.failed\n",
			func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeForegroundStore{checkErr: errors.New("checkpoint unavailable")}
			h := newDurableNonChatHandler(store, durableEndpointResolver{}, durableEndpointExecutor{write: tc.frame})
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, newDurableNonChatRequest(t, tc.path, tc.body, context.Background()))
			out := rec.Body.String()
			if strings.Contains(out, `"text":"hi"`) {
				t.Fatalf("checkpoint failure must block semantic output: %q", out)
			}
			if strings.Count(out, tc.term) != 1 {
				t.Fatalf("native terminal count = %d, want 1: %q", strings.Count(out, tc.term), out)
			}
		})
	}
}

func TestDurableNonChatEndpointsDeadlineRendersOneNativeTerminal(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		body  string
		term  string
		serve func(*ChatHandler) http.Handler
	}{
		{"messages", "/v1/messages", `{"model":"gpt-4o","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			"event: error\n", func(h *ChatHandler) http.Handler { return NewMessagesHandler(h) }},
		{"responses", "/v1/responses", `{"model":"gpt-4o","stream":true,"input":"hi"}`,
			"event: response.failed\n", func(h *ChatHandler) http.Handler { return NewResponsesHandler(h) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeForegroundStore{}
			h := newDurableNonChatHandler(store, durableEndpointResolver{}, durableEndpointExecutor{
				err: &executors.ExecuteError{LastKind: "transient"},
			})
			h.survivalOptions = SurvivalOptions{Deadline: time.Nanosecond, RetryBase: time.Millisecond, RetryMax: time.Millisecond}
			rec := httptest.NewRecorder()
			tc.serve(h).ServeHTTP(rec, newDurableNonChatRequest(t, tc.path, tc.body, context.Background()))
			out := rec.Body.String()
			if !strings.Contains(out, "deadline_exceeded") || strings.Count(out, tc.term) != 1 {
				t.Fatalf("deadline terminal mismatch: %q", out)
			}
		})
	}
}
