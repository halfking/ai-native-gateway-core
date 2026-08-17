package executors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"  //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/identity"    //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/pool"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func TestUpstreamContext_SessionStreamDetachesCancellationAndRetainsTenant(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	req.Header.Set("X-Gw-Session-Id", "session-1")
	params := &ExecParams{
		R:        req,
		IsStream: true,
		TenantID: "tenant-a",
	}
	params.R = params.R.WithContext(session.SetTenantID(params.R.Context(), params.TenantID))
	cancel()

	upstreamCtx, upstreamCancel := (&Executor{}).upstreamContext(params, time.Second)
	defer upstreamCancel()

	if err := upstreamCtx.Err(); err != nil {
		t.Fatalf("detached upstream context cancelled with client: %v", err)
	}
	if got := session.GetTenantIDFromContext(upstreamCtx); got != "tenant-a" {
		t.Fatalf("tenant ID = %q, want tenant-a", got)
	}
	// 2026-08-04: streaming contexts deliberately carry NO wall-clock
	// deadline. A long-running agent task is bounded by inactivity signals
	// (ResponseHeaderTimeout on the transport, streamChunkTimeout in the
	// bridge read loop) rather than total age, so it is not falsely
	// interrupted simply for running longer than 15 minutes.
	if _, ok := upstreamCtx.Deadline(); ok {
		t.Fatal("streaming upstream context must NOT carry a wall-clock deadline (stall-based timeout only)")
	}
}

func TestUpstreamContext_OrdinaryStreamFollowsClientCancellationWithoutDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	params := &ExecParams{R: req, IsStream: true}

	upstreamCtx, upstreamCancel := (&Executor{}).upstreamContext(params, time.Second)
	defer upstreamCancel()
	if _, ok := upstreamCtx.Deadline(); ok {
		t.Fatal("streaming upstream context must not carry a wall-clock deadline")
	}

	cancel()
	select {
	case <-upstreamCtx.Done():
		if !errors.Is(upstreamCtx.Err(), context.Canceled) {
			t.Fatalf("upstream context error = %v, want context.Canceled", upstreamCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("ordinary stream upstream context did not follow client cancellation")
	}
}

func TestUpstreamContext_SurvivalStreamDetachesCancellationWithoutDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	params := &ExecParams{R: req, IsStream: true, SurvivalAttempt: true}

	upstreamCtx, upstreamCancel := (&Executor{}).upstreamContext(params, time.Second)
	defer upstreamCancel()
	cancel()

	if err := upstreamCtx.Err(); err != nil {
		t.Fatalf("survival stream upstream context cancelled with client: %v", err)
	}
	if _, ok := upstreamCtx.Deadline(); ok {
		t.Fatal("survival stream upstream context must not carry a wall-clock deadline")
	}
}

// TestUpstreamContext_NonStreamRetainsDeadline verifies the 2026-08-04
// change did not affect non-streaming requests: those still get the timeout
// as a hard deadline (there is no streaming read loop to provide stall
// detection, so a total deadline remains the correct backstop).
func TestUpstreamContext_NonStreamRetainsDeadline(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	params := &ExecParams{
		R:        req,
		IsStream: false,
		TenantID: "tenant-a",
	}
	upstreamCtx, upstreamCancel := (&Executor{}).upstreamContext(params, 20*time.Millisecond)
	defer upstreamCancel()
	if _, ok := upstreamCtx.Deadline(); !ok {
		t.Fatal("non-streaming upstream context must retain a timeout deadline")
	}
	select {
	case <-upstreamCtx.Done():
		if !errors.Is(upstreamCtx.Err(), context.DeadlineExceeded) {
			t.Fatalf("non-streaming upstream context error = %v, want context.DeadlineExceeded", upstreamCtx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("non-streaming upstream context did not enforce its timeout")
	}
}
func TestShouldAsyncFallback_DisabledWhenPreStreamPrepared(t *testing.T) {
	exec := &Executor{
		AsyncShortTimeout: 1 * time.Second,
		AsyncLongTimeout:  10 * time.Second,
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Session-Id", "gw_test")
	params := &ExecParams{
		R:                 req,
		PreStreamPrepared: true,
	}
	if exec.shouldAsyncFallback(params, time.Now().Add(-2*time.Second), 1, errorsx.KindTransient) {
		t.Fatal("expected async fallback to be disabled after pre-stream response commit")
	}
}

// TestExecuteOpenAI_StreamPreStreamStopOrdering is the focused
// e2e-level check for the pre-stream keepalive contract:
//  1. The first byte seen by the client is the SSE keep-alive comment.
//  2. The OnStreamReady callback is fired exactly once, before any
//     content chunk is forwarded.
//  3. The stream body delivered to the client still contains the
//     upstream payload in order.
func TestExecuteOpenAI_Q2BridgeOwnsResponseBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	var genericCalls atomic.Int32
	var bridgeCalls atomic.Int32
	exec := NewExecutor(
		NewRouter(NewStickyCache(), credential.NewLimiter()), credential.NewManager(), credential.NewLimiter(),
		pool.NewPoolManager(nil), nil, func(chunk []byte, isStream bool) []byte { return chunk }, nil, nil,
	)
	exec.OpenAIToAnthropicStream = func(_ http.ResponseWriter, resp *http.Response, _, _, _ string, _ *audit.StreamCapture, _ any) StreamOutcome {
		bridgeCalls.Add(1)
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read bridge body: %v", err)
		}
		if !strings.Contains(string(body), `"content":"hi"`) {
			t.Fatalf("bridge body = %q", body)
		}
		return StreamOutcome{}
	}
	cand := provider.Candidate{ProviderID: 1, CredentialID: 1, BaseURL: upstream.URL, Protocol: "openai-completions", RawModel: "gpt-test", APIKey: "key"}
	_, err := exec.executeOpenAI(&ExecParams{
		W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
		BodyBytes: []byte(`{"stream":true}`), IsStream: true, ClientProtocol: "anthropic-messages",
		ClientModel: "claude-test", ClientID: identity.ClientIdentity{IdentityHash: "test"},
		StreamWrapper: func(http.ResponseWriter, *http.Response, NormalizerFunc, *audit.StreamCapture) StreamOutcome {
			genericCalls.Add(1)
			return StreamOutcome{}
		},
	}, cand, 0, time.Now(), nil)
	if err != nil {
		t.Fatalf("executeOpenAI: %v", err)
	}
	if genericCalls.Load() != 0 || bridgeCalls.Load() != 1 {
		t.Fatalf("generic calls=%d bridge calls=%d", genericCalls.Load(), bridgeCalls.Load())
	}
}

func TestExecuteOpenAI_StreamPreStreamStopOrdering(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			// First attempt fails with a transient 500. The classifier
			// will route this to KindTransient, which goes through the
			// retry path without invoking fpLease-only branches.
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary"}}`))
			return
		}
		// Second attempt returns a valid SSE stream.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	var readyCount atomic.Int32
	var firstContentBeforeReady atomic.Bool

	exec := NewExecutor(
		NewRouter(NewStickyCache(), credential.NewLimiter()),
		credential.NewManager(),
		credential.NewLimiter(),
		pool.NewPoolManager(nil),
		nil,
		func(chunk []byte, isStream bool) []byte { return chunk },
		func(w http.ResponseWriter, resp *http.Response, clientModel, outboundModel, catalogCode string, norm NormalizerFunc, capture *audit.StreamCapture, toolsRequested bool) StreamOutcome {
			// This is the production StreamChat injection point. It is
			// called AFTER OnStreamReady has already been fired. We
			// verify the order and forward the body verbatim.
			if readyCount.Load() == 0 {
				firstContentBeforeReady.Store(true)
			}
			defer func() { _ = resp.Body.Close() }()
			buf := make([]byte, 4096)
			n, _ := resp.Body.Read(buf)
			_, _ = w.Write(buf[:n])
			return StreamOutcome{}
		},
		nil,
	)

	cand := provider.Candidate{
		CredentialID:      101,
		ProviderID:        7,
		Tier:              1,
		BaseURL:           upstream.URL,
		Protocol:          "openai-completions",
		CatalogCode:       "openai",
		RawModel:          "gpt-4o",
		Weight:            100,
		BillingMode:       "token_plan",
		Routable:          true,
		LifecycleStatus:   "active",
		AvailabilityState: "ready",
		QuotaState:        "ok",
		CircuitState:      "closed",
		APIKey:            "sk-test",
	}

	policy := provider.DefaultPolicy()
	policy.RetryPerCredential = 1

	result, err := exec.executeOpenAI(
		&ExecParams{
			W:                 rec,
			R:                 req,
			BodyBytes:         []byte(`{"model":"gpt-4o","messages":[],"stream":true}`),
			IsStream:          true,
			PreStreamPrepared: true,
			OnStreamReady: func() {
				readyCount.Add(1)
			},
			ClientProtocol: "openai-completions",
			ClientModel:    "gpt-4o",
			ClientID:       identity.ClientIdentity{IdentityHash: "test"},
		},
		cand,
		policy.RetryPerCredential,
		time.Now(),
		nil,
	)
	if err != nil {
		t.Fatalf("executeOpenAI() error = %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2 (one failed + one success)", calls.Load())
	}
	if firstContentBeforeReady.Load() {
		t.Fatal("OnStreamReady must fire before any stream content is forwarded")
	}
	if readyCount.Load() != 1 {
		t.Fatalf("OnStreamReady fired %d times, want 1", readyCount.Load())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"hi"`) {
		t.Fatalf("stream body = %q, want final stream chunk", body)
	}
}

type executionRecorderSpy struct {
	mu       sync.Mutex
	outcomes []ExecutionOutcome
}

func (s *executionRecorderSpy) RecordOutcome(_ context.Context, outcome ExecutionOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes = append(s.outcomes, outcome)
	return nil
}

func (s *executionRecorderSpy) snapshot() []ExecutionOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ExecutionOutcome(nil), s.outcomes...)
}

func TestExecuteOpenAI_StreamSuccessRecordedOnlyAfterBodyCompletes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	for _, tc := range []struct {
		name       string
		outcome    StreamOutcome
		streamWait time.Duration
		wantErr    bool
		wantRecord int
		wantChunks int
	}{
		{
			name: "network interruption",
			outcome: StreamOutcome{
				Interrupted: true,
				Reason:      "network_error",
				Kind:        errorsx.KindNetwork,
				Resumable:   true,
			},
			wantErr: true,
		},
		{
			name: "client disconnected after capture",
			outcome: StreamOutcome{
				Interrupted: true,
				Reason:      "client_disconnected",
				Kind:        errorsx.KindCanceled,
				ChunkCount:  3,
			},
			wantErr: true,
		},
		{
			name:       "completed stream",
			outcome:    StreamOutcome{ChunkCount: 3},
			streamWait: 30 * time.Millisecond,
			wantRecord: 1,
			wantChunks: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &executionRecorderSpy{}
			exec := NewExecutor(
				NewRouter(NewStickyCache(), credential.NewLimiter()), credential.NewManager(), credential.NewLimiter(),
				pool.NewPoolManager(nil), nil, func(chunk []byte, _ bool) []byte { return chunk }, nil, nil,
			)
			exec.PostExecutionHook = spy
			exec.StreamRetryThreshold = 50
			exec.StreamChat = func(http.ResponseWriter, *http.Response, string, string, string, NormalizerFunc, *audit.StreamCapture, bool) StreamOutcome {
				time.Sleep(tc.streamWait)
				return tc.outcome
			}

			cand := provider.Candidate{
				ProviderID: 1, CredentialID: 1, BaseURL: upstream.URL,
				Protocol: "openai-completions", RawModel: "gpt-test", APIKey: "key",
			}
			_, err := exec.executeOpenAI(&ExecParams{
				W: httptest.NewRecorder(), R: httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
				BodyBytes: []byte(`{"model":"gpt-test","messages":[],"stream":true}`),
				IsStream:  true, ClientProtocol: "openai-completions", ClientModel: "gpt-test",
				ClientID: identity.ClientIdentity{IdentityHash: "test"},
			}, cand, 0, time.Now(), nil)

			if tc.wantErr && err == nil {
				t.Fatal("expected interrupted stream error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("executeOpenAI() error = %v", err)
			}
			outcomes := spy.snapshot()
			if len(outcomes) != tc.wantRecord {
				t.Fatalf("recorded outcomes = %d, want %d: %+v", len(outcomes), tc.wantRecord, outcomes)
			}
			if tc.wantRecord > 0 {
				if !outcomes[0].Success {
					t.Fatal("completed stream must record success")
				}
				if outcomes[0].ChunkCount != tc.wantChunks {
					t.Fatalf("recorded chunk count = %d, want %d", outcomes[0].ChunkCount, tc.wantChunks)
				}
				if tc.streamWait > 0 && outcomes[0].LatencyMs < tc.streamWait.Milliseconds() {
					t.Fatalf("recorded latency = %dms, want at least %dms body duration", outcomes[0].LatencyMs, tc.streamWait.Milliseconds())
				}
			}
		})
	}
}

func TestExecuteOpenAI_NetworkStreamFailureFailsOverToNextCandidate(t *testing.T) {
	var streamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	exec := NewExecutor(
		NewRouter(NewStickyCache(), credential.NewLimiter()), credential.NewManager(), credential.NewLimiter(),
		pool.NewPoolManager(nil), nil, func(chunk []byte, _ bool) []byte { return chunk }, nil, nil,
	)
	exec.StreamRetryThreshold = 50
	exec.StreamChat = func(http.ResponseWriter, *http.Response, string, string, string, NormalizerFunc, *audit.StreamCapture, bool) StreamOutcome {
		if streamCalls.Add(1) == 1 {
			return StreamOutcome{
				Interrupted: true,
				Reason:      "network_error",
				Kind:        errorsx.KindNetwork,
				Resumable:   true,
				ChunkCount:  0,
			}
		}
		return StreamOutcome{ChunkCount: 1}
	}

	first := overloadTestCandidate(upstream.URL)
	first.ProviderID = 1
	first.CredentialID = 101
	first.RawModel = "gpt-test"
	first.APIKey = "key-1"
	second := first
	second.ProviderID = 2
	second.CredentialID = 102
	second.APIKey = "key-2"
	candidates := []provider.Candidate{first, second}

	result, err := exec.Execute(&ExecParams{
		W:              httptest.NewRecorder(),
		R:              httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		BodyBytes:      []byte(`{"model":"gpt-test","messages":[],"stream":true}`),
		IsStream:       true,
		ClientProtocol: "openai-completions",
		ClientModel:    "gpt-test",
		ClientID:       identity.ClientIdentity{IdentityHash: "network-failover-test"},
		Candidates:     candidates,
		Policy:         &provider.Policy{TierFallbackMax: 4, RetryPerCredential: 0},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Candidate.CredentialID != 102 {
		t.Fatalf("result candidate = %+v, want credential 102", result)
	}
	if got := streamCalls.Load(); got != 2 {
		t.Fatalf("stream calls = %d, want exactly 2 candidate attempts", got)
	}
}

// SR-05 (doc 18 §17 Phase 0): under request survival the executor performs a
// single bounded pass — the SurvivalCoordinator owns every retry decision.
func TestShouldAsyncFallback_DisabledWhenSurvivalAttempt(t *testing.T) {
	exec := &Executor{
		AsyncShortTimeout: 1 * time.Second,
		AsyncLongTimeout:  10 * time.Second,
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Gw-Session-Id", "gw_test")
	params := &ExecParams{
		R:               req,
		SurvivalAttempt: true,
	}
	if exec.shouldAsyncFallback(params, time.Now().Add(-2*time.Second), 1, errorsx.KindTransient) {
		t.Fatal("expected async fallback to be disabled for survival-owned attempts")
	}
}
