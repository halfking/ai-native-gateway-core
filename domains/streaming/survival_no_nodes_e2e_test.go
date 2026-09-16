package streaming

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// survival_no_nodes_e2e_test.go — SR-12 e2e: when a client requests a model
// that has no available nodes, the SurvivalCoordinator MUST hold the SSE
// connection open, retry on the server side, surface every retry attempt as
// a `: thinking:` chunk so the client sees activity, and stop immediately
// when the client disconnects. The four requirements pinned here map 1:1
// to the product spec:
//
//   1. connection stays open after no-candidates (no immediate error)
//   2. server-side retry, not client reconnect
//   3. each retry failure → `: thinking:` chunk visible on the wire
//   4. client disconnect cancels the loop regardless of remaining budget
//
// The test stubs `provider` + `survivalAttemptExec` so it does not depend on
// the live credential catalog. glm-5.2 is the symbolic model name (the
// scenario the user asked to verify) but the assertions are independent of
// which model actually fails to route.

// ---------------------------------------------------------------------------
// stubs
// ---------------------------------------------------------------------------

type noNodesResolver struct{}

func (noNodesResolver) Enabled() bool { return true }
func (noNodesResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, nil, nil
}
func (noNodesResolver) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, nil, nil
}
func (noNodesResolver) ModelKnown(context.Context, string) bool { return true }

type noNodesKeyVerifier struct{}

func (noNodesKeyVerifier) Enabled() bool { return true }
func (noNodesKeyVerifier) Verify(context.Context, string) (*authentication.KeyInfo, error) {
	return &authentication.KeyInfo{ID: 1, TenantID: "tenant-no-nodes", ApplicationID: 1}, nil
}
func (noNodesKeyVerifier) VerifyByID(context.Context, int) (*authentication.KeyInfo, error) {
	return &authentication.KeyInfo{ID: 1, TenantID: "tenant-no-nodes", ApplicationID: 1}, nil
}
func (noNodesKeyVerifier) CheckBudget(context.Context, int) error { return nil }
func (noNodesKeyVerifier) LookupKeyMeta(context.Context, string) (*authentication.KeyLookupMeta, error) {
	return nil, nil
}

// noNodesExecutor always fails the candidate walk with KindNoAvailableChannel;
// foldCandidateOutcomes synthesizes the corresponding wait-recovery outcome
// and the coordinator enters its retry loop. attempts counts every call so
// the test can assert the retry count precisely.
type noNodesExecutor struct {
	attempts int64
	calls    int64
}

func (e *noNodesExecutor) Execute(_ *executors.ExecParams) (*executors.ExecuteResult, error) {
	n := atomic.AddInt64(&e.calls, 1)
	atomic.StoreInt64(&e.attempts, n)
	return nil, &executors.ExecuteError{
		LastKind: errorsx.KindNoAvailableChannel,
		LastErr:  fmt.Errorf("synthetic no available nodes for glm-5.2 (attempt %d)", n),
	}
}

func newNoNodesHandler(t *testing.T, retryInterval time.Duration, attemptsCap int) (*ChatHandler, *noNodesExecutor) {
	t.Helper()
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	// The handler gates ServeHTTP on h.executor != nil; survival uses
	// h.survivalAttemptExec instead. A real (empty) Executor keeps the gate
	// open without ever being called.
	h.executor = &executors.Executor{}
	h.setRequestKeyVerifierForTest(noNodesKeyVerifier{})
	h.provider = noNodesResolver{}
	exec := &noNodesExecutor{}
	h.survivalAttemptExec = exec
	h.SetRequestSurvival(func(string) bool { return true }, SurvivalOptions{
		Deadline:          5 * time.Minute, // bounded for test, not 5h
		RetryBase:         retryInterval,   // both must be set; withDefaults forces 30s when RetryBase == 0
		RetryInterval:     retryInterval,
		MaxRetries:        attemptsCap, // small cap so the loop is bounded
		NightMaxRetries:   attemptsCap, // withDefaults forces 600 when zero — pin both day & night
		KeepaliveInterval: retryInterval / 2,
	})
	return h, exec
}

// ---------------------------------------------------------------------------
// helper: read SSE stream chunk-by-chunk with a hard timeout
// ---------------------------------------------------------------------------

// streamRead reads from body into out until either maxBytes have been read,
// the read loop observes an error, or the deadline elapses. It returns the
// bytes collected and the deadline-relative remaining time (for chained
// assertions).
func streamRead(t *testing.T, body io.Reader, maxBytes int, deadline time.Duration) string {
	t.Helper()
	out := &bytes.Buffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for out.Len() < maxBytes {
			n, err := body.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					return
				}
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(deadline):
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// TestSurvivalNoNodesKeepsConnectionOpenAndEmitsThink — the core scenario
// ---------------------------------------------------------------------------

func TestSurvivalNoNodesKeepsConnectionOpenAndEmitsThink(t *testing.T) {
	const (
		retryInterval = 80 * time.Millisecond
		attemptsCap   = 4 // gives us 4 retries before the loop gives up
		minAttempts   = 2 // we expect at least 2 retries to fire before client disconnect
	)
	h, exec := newNoNodesHandler(t, retryInterval, attemptsCap)

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	body := strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (connection must stay open, not error immediately)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Read SSE stream until we see the terminal frame or the deadline elapses.
	// We expect at least two retries worth of think chunks + a terminal frame
	// once the budget is hit.
	got := streamRead(t, resp.Body, 1<<20, retryInterval*15)

	// Requirement 1: connection stayed open — response was 200 with SSE.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("connection should stay open: status=%d body=%q", resp.StatusCode, got)
	}

	// Requirement 3: each retry emitted a `: thinking:` chunk with the wait-recovery
	// notice. The exact attempt number is in the message.
	wantThinkingSubstr := "正在等待可用节点并重试"
	thinkCount := strings.Count(got, wantThinkingSubstr)
	if thinkCount < minAttempts {
		t.Fatalf("expected at least %d `: thinking:` chunks with %q, got %d.\nstream=%q",
			minAttempts, wantThinkingSubstr, thinkCount, got)
	}

	// The per-retry think must surface the underlying failure kind
	// (no_available_channel) so the client knows what is actually being waited
	// on — the generic "wait_recovery_window" alone is not enough for the spec
	// requirement "每次重试的失败结果都作为think返回".
	if !strings.Contains(got, "no_available_channel") {
		t.Fatalf("per-retry think should carry the underlying failure kind, got stream=%q", got)
	}

	// Requirement 2: server-side retry — the executor should have been called
	// at least `minAttempts` times.
	gotCalls := atomic.LoadInt64(&exec.calls)
	if gotCalls < int64(minAttempts) {
		t.Fatalf("executor should have retried at least %d times, got %d", minAttempts, gotCalls)
	}

	// Requirement 4 (partial): connection ends only when the loop gives up
	// (budget exhausted) or the client disconnects — in this test we let it
	// run to budget exhaustion. The terminal frame should arrive.
	if !strings.Contains(got, "gateway_survival_") {
		t.Fatalf("expected terminal frame after budget exhaustion; stream=%q", got)
	}

	// Cancel and ensure server-side loop is unblocked.
	cancel()
}

func TestSurvivalNoNodesStopsImmediatelyOnClientDisconnect(t *testing.T) {
	const (
		retryInterval = 60 * time.Millisecond
		attemptsCap   = 200 // intentionally large — disconnect should fire first
	)
	h, exec := newNoNodesHandler(t, retryInterval, attemptsCap)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	body := strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions", body)
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	// Drain until we see at least two retries worth of think chunks, then
	// cancel the context to simulate the client going away.
	got := streamRead(t, resp.Body, 1<<16, retryInterval*8)
	if strings.Count(got, "正在等待可用节点并重试") < 2 {
		t.Fatalf("expected ≥2 retries before cancel; stream=%q", got)
	}

	callsAtCancel := atomic.LoadInt64(&exec.calls)
	if callsAtCancel < 2 {
		t.Fatalf("expected ≥2 retries before cancel, got %d. stream=%q", callsAtCancel, got)
	}

	// Client disconnects.
	cancel()
	_ = resp.Body.Close()

	// Give the server a moment to observe ctx.Done() and exit the loop. The
	// coordinator's loop checks ctx.Err() at the top of each iteration; once
	// the client cancels, no further executor calls should happen.
	time.Sleep(retryInterval * 3)
	callsAfterCancel := atomic.LoadInt64(&exec.calls)
	if callsAfterCancel-callsAtCancel > 2 {
		t.Fatalf("executor kept running after client disconnect: before=%d after=%d (delta=%d, want ≤2)",
			callsAtCancel, callsAfterCancel, callsAfterCancel-callsAtCancel)
	}
}

func TestSurvivalNoNodesRespectsConfiguredBudgetBounds(t *testing.T) {
	// With retryInterval=40ms and MaxRetries=5, the loop should produce
	// exactly 6 executor calls (initial + 5 retries) before the budget
	// limit forces the terminal fail-closed decision.
	const (
		retryInterval = 40 * time.Millisecond
		attemptsCap   = 5
	)
	h, exec := newNoNodesHandler(t, retryInterval, attemptsCap)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body := strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/chat/completions", body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sk-test")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	got := streamRead(t, resp.Body, 1<<20, 2*time.Second)

	gotCalls := atomic.LoadInt64(&exec.calls)
	if gotCalls != int64(attemptsCap+1) {
		t.Fatalf("executor calls = %d, want exactly %d (initial + %d retries)",
			gotCalls, attemptsCap+1, attemptsCap)
	}
	if !strings.Contains(got, "retry_limit_exceeded") {
		t.Fatalf("terminal frame should report retry_limit_exceeded; stream=%q", got)
	}
}