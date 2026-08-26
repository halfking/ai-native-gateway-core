package streaming

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// SR-06 (doc 18 §5.1 SurvivalCoordinator, §8): the in-connection recovery
// loop. It owns the commit gate per attempt, aggregates task outcomes and
// dispatches retry-now / wait-recovery / terminal. All seams (executor,
// candidate refresh, sleep, terminal render) are injected so the loop logic
// is verifiable without IO.

// scriptedExecutor returns queued results in order.
type scriptedExecutor struct {
	results []*executors.ExecuteResult
	errs    []error
	calls   int
}

func (s *scriptedExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	i := s.calls
	s.calls++
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	if i < len(s.results) {
		return s.results[i], nil
	}
	return nil, errors.New("scripted executor exhausted")
}

type coordHarness struct {
	exec       *scriptedExecutor
	flusher    *trackingFlusher
	sw         *SerializedStreamWriter
	refreshes  int
	sleeps     []time.Duration
	terminals  []TaskDecision
	committeds []bool
	clock      time.Time
}

func newCoordHarness(exec *scriptedExecutor) *coordHarness {
	h := &coordHarness{
		exec:    exec,
		flusher: &trackingFlusher{},
		clock:   time.Now(),
	}
	h.sw = NewSerializedStreamWriter(h.flusher)
	return h
}

func (h *coordHarness) coordinator() *SurvivalCoordinator {
	return &SurvivalCoordinator{
		Exec:     h.exec,
		Protocol: ProtocolAnthropic,
		Options:  SurvivalOptions{Deadline: 30 * time.Minute, RetryBase: 5 * time.Second, RetryMax: 2 * time.Minute},
		// Deterministic ±20% jitter seam (T3): 0.5 keeps the scripted
		// backoff steps exact for the assertions below.
		JitterRand: func() float64 { return 0.5 },
		Now: func() time.Time {
			return h.clock
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.sleeps = append(h.sleeps, d)
			h.clock = h.clock.Add(d)
			return ctx.Err()
		},
		Refresh: func(ctx context.Context) {
			h.refreshes++
		},
		Terminal: func(d TaskDecision, committed bool) {
			h.terminals = append(h.terminals, d)
			h.committeds = append(h.committeds, committed)
		},
	}
}

func transientFailure() error {
	return &executors.ExecuteError{
		LastKind: errorsx.KindTransient,
		Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: errorsx.KindTransient}},
	}
}

func rateLimitFailure() error {
	return &executors.ExecuteError{
		LastKind: errorsx.KindRateLimit,
		Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: errorsx.KindRateLimit}},
	}
}

func TestSurvivalCoordinatorFirstAttemptSucceeds(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{results: []*executors.ExecuteResult{{}}})
	c := h.coordinator()

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{IsStream: true})

	if !res.Succeed {
		t.Fatalf("expected success, decision=%v err=%v", res.Decision.Action, res.FinalAttempt.FinalError)
	}
	if h.exec.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", h.exec.calls)
	}
	if len(h.terminals) != 0 || len(h.sleeps) != 0 || h.refreshes != 0 {
		t.Fatalf("clean success must not retry/render: terminals=%v sleeps=%v refreshes=%d",
			h.terminals, h.sleeps, h.refreshes)
	}
}

func TestSurvivalCoordinatorRetryNowRecoversOnSecondAttempt(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{
		errs:    []error{transientFailure(), nil},
		results: []*executors.ExecuteResult{nil, {}},
	})
	c := h.coordinator()

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if !res.Succeed {
		t.Fatalf("expected recovery, decision=%v", res.Decision.Action)
	}
	if h.exec.calls != 2 {
		t.Fatalf("executor calls = %d, want 2", h.exec.calls)
	}
	if h.refreshes != 1 {
		t.Fatalf("retry-now must refresh candidates once, got %d", h.refreshes)
	}
	if len(h.terminals) != 0 {
		t.Fatalf("recovered request must not render terminal, got %v", h.terminals)
	}
}

func TestSurvivalCoordinatorWaitRecoveryBacksOffAndKeepalives(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{
		errs:    []error{rateLimitFailure(), nil},
		results: []*executors.ExecuteResult{nil, {}},
	})
	c := h.coordinator()

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if !res.Succeed {
		t.Fatalf("expected recovery, decision=%v", res.Decision.Action)
	}
	if len(h.sleeps) != 1 || h.sleeps[0] != 5*time.Second {
		t.Fatalf("first wait must sleep the base backoff, got %v", h.sleeps)
	}
	if h.flusher.buf.Len() == 0 {
		t.Fatal("waiting for recovery must emit a keepalive comment to the client")
	}
	if h.refreshes != 1 {
		t.Fatalf("wait-recovery must refresh candidates, got %d", h.refreshes)
	}
}

func TestSurvivalCoordinatorFailTerminalRendersOnce(t *testing.T) {
	termErr := &executors.ExecuteError{
		LastKind: errorsx.KindContentFilter,
		Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: errorsx.KindContentFilter}},
	}
	h := newCoordHarness(&scriptedExecutor{errs: []error{termErr, termErr}})
	c := h.coordinator()

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if res.Succeed {
		t.Fatal("terminal failure must not succeed")
	}
	if res.Decision.Action != TaskActionFailTerminal {
		t.Fatalf("decision = %v, want fail_terminal", res.Decision.Action)
	}
	if h.exec.calls != 1 {
		t.Fatalf("terminal kinds must not retry, executor calls = %d", h.exec.calls)
	}
	if len(h.terminals) != 1 || h.terminals[0].Action != TaskActionFailTerminal {
		t.Fatalf("terminal render = %+v", h.terminals)
	}
	if h.committeds[0] {
		t.Fatal("uncommitted terminal must render as uncommitted")
	}
}

// committedExecutor simulates an attempt that committed content before
// failing with a recoverable kind — the aggregator upgrades to
// ResumeBlocked and the coordinator must render a well-formed ending.
type committedExecutor struct{ calls int }

func (e *committedExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	e.calls++
	if gw, ok := params.W.(*GateWriter); ok {
		_, _ = gw.Write([]byte("event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
	}
	return nil, &executors.ExecuteError{
		LastKind: errorsx.KindRateLimit,
		Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: errorsx.KindRateLimit}},
	}
}

func TestSurvivalCoordinatorResumeBlockedRendersCommittedEnding(t *testing.T) {
	h := newCoordHarness(nil)
	h.exec = nil
	c := h.coordinator()
	c.Exec = &committedExecutor{}

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if res.Succeed {
		t.Fatal("resume-blocked must not succeed")
	}
	if res.Decision.Action != TaskActionResumeBlocked {
		t.Fatalf("decision = %v, want resume_blocked", res.Decision.Action)
	}
	if c.Exec.(*committedExecutor).calls != 1 {
		t.Fatal("resume-blocked must never retry")
	}
	if len(h.terminals) != 1 || !h.committeds[0] {
		t.Fatalf("committed ending required, terminals=%v committeds=%v", h.terminals, h.committeds)
	}
	if got := h.flusher.buf.String(); got == "" || got == ": gw-survival-keepalive\n\n" {
		t.Fatalf("committed content must have reached the wire, got %q", got)
	}
}

func TestSurvivalCoordinatorDeadlineStopsLoop(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{errs: []error{rateLimitFailure(), rateLimitFailure()}})
	c := h.coordinator()
	// Tight deadline: expires before the second retry.
	c.Options.Deadline = 6 * time.Second // first sleep = 5s, clock advances past deadline

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if res.Succeed {
		t.Fatal("deadline must stop recovery")
	}
	if res.Decision.Reason != "deadline_exceeded" {
		t.Fatalf("decision reason = %q, want deadline_exceeded", res.Decision.Reason)
	}
	if len(h.terminals) != 1 {
		t.Fatalf("deadline terminal must render once, got %v", h.terminals)
	}
}

func TestSurvivalCoordinatorClientCancelStopsLoop(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{errs: []error{rateLimitFailure()}})
	c := h.coordinator()
	ctx, cancel := context.WithCancel(context.Background())
	c.Sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}

	res := c.Run(ctx, h.sw, &executors.ExecParams{})

	if res.Succeed {
		t.Fatal("cancelled recovery must not succeed")
	}
	if res.Decision.Reason != "client_disconnected" {
		t.Fatalf("decision reason = %q, want client_disconnected", res.Decision.Reason)
	}
	if len(h.terminals) != 0 {
		t.Fatal("client is gone — no terminal render")
	}
}

func TestSurvivalCoordinatorBackoffDoublesAndCaps(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{errs: []error{
		transientFailure(), transientFailure(), transientFailure(), nil,
	},
		results: []*executors.ExecuteResult{nil, nil, nil, {}},
	})
	c := h.coordinator()

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if !res.Succeed {
		t.Fatalf("expected recovery on 4th attempt, decision=%v", res.Decision.Action)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	if len(h.sleeps) != len(want) {
		t.Fatalf("sleeps = %v, want %v", h.sleeps, want)
	}
	for i, w := range want {
		if h.sleeps[i] != w {
			t.Fatalf("sleep[%d] = %v, want %v (full: %v)", i, h.sleeps[i], w, h.sleeps)
		}
	}
}

func TestSurvivalOptionsClampRetryMaxToTwoMinutes(t *testing.T) {
	opts := (SurvivalOptions{RetryMax: 10 * time.Minute}).withDefaults()
	if opts.RetryMax != 2*time.Minute {
		t.Fatalf("retry max = %v, want 2m", opts.RetryMax)
	}
}

func TestSurvivalCoordinatorEmitsKeepaliveThroughoutWait(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{
		errs:    []error{transientFailure(), nil},
		results: []*executors.ExecuteResult{nil, {}},
	})
	c := h.coordinator()
	c.Sleep = nil
	c.Options.RetryBase = 5 * time.Second
	c.Options.KeepaliveInterval = time.Second

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})
	if !res.Succeed {
		t.Fatalf("expected recovery, decision=%v", res.Decision.Action)
	}
	if got := strings.Count(h.flusher.buf.String(), "event: ping\ndata: {\"type\":\"ping\"}\n\n"); got < 3 {
		t.Fatalf("keepalive count = %d, want at least 3 during 5s wait", got)
	}
}

func TestSurvivalKeepaliveUsesClientProtocol(t *testing.T) {
	tests := []struct {
		name     string
		protocol ClientProtocol
		want     string
	}{
		{name: "anthropic ping", protocol: ProtocolAnthropic, want: "event: ping\ndata: {\"type\":\"ping\"}\n\n"},
		{name: "openai chat comment", protocol: ProtocolOpenAIChat, want: ": gw-survival-keepalive\n\n"},
		{name: "openai responses comment", protocol: ProtocolOpenAIResponses, want: ": gw-survival-keepalive\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &trackingFlusher{}
			c := &SurvivalCoordinator{Protocol: tt.protocol}
			if err := c.keepalive(NewSerializedStreamWriter(f)); err != nil {
				t.Fatalf("keepalive: %v", err)
			}
			if got := f.buf.String(); got != tt.want {
				t.Fatalf("keepalive = %q, want %q", got, tt.want)
			}
			if class := ClassifyClientFrame(tt.protocol, tt.want); class != FrameClassKeepalive {
				t.Fatalf("keepalive class = %v, want FrameClassKeepalive", class)
			}
		})
	}
}

func TestSurvivalCoordinatorStopsAfterKeepaliveFlushFailure(t *testing.T) {
	h := newCoordHarness(&scriptedExecutor{errs: []error{transientFailure()}})
	failing := &failingErrorFlusher{flushErr: errors.New("client disconnected")}
	h.sw = NewSerializedStreamWriter(failing)
	c := h.coordinator()

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if res.Decision.Reason != "client_disconnected" {
		t.Fatalf("decision reason = %q, want client_disconnected", res.Decision.Reason)
	}
	if res.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", res.Attempts)
	}
}

type alwaysTransientExecutor struct{ calls int }

func (e *alwaysTransientExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	if !params.UpstreamAttempts.TryConsume() {
		return nil, executors.ErrUpstreamAttemptLimit
	}
	e.calls++
	return nil, transientFailure()
}

// holdbackWriteExecutor writes a fixed semantic frame into the attempt gate
// (via params.W, the GateWriter) and then either succeeds or fails with the
// configured kind. With the FR-12 L1 holdback window open the frame is held
// in the gate's uncommitted buffer, never advancing commit state — exactly
// the pre-finish condition the coordinator's flush logic must handle.
type holdbackWriteExecutor struct {
	content  string
	fail     bool
	failKind errorsx.ErrorKind
	calls    int
}

func (e *holdbackWriteExecutor) Execute(params *executors.ExecParams) (*executors.ExecuteResult, error) {
	e.calls++
	if gw, ok := params.W.(*GateWriter); ok {
		_, _ = gw.Write([]byte(e.content))
	}
	if e.fail {
		return nil, &executors.ExecuteError{
			LastKind: e.failKind,
			Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: e.failKind}},
		}
	}
	return &executors.ExecuteResult{}, nil
}

// TestSurvivalCoordinatorFlushHeldFramesOnSuccessUnderHoldback is the
// coordinator-level regression for the success-path holdback flush fix
// (FR-12 L1). An attempt that finishes successfully while the L1 window still
// holds its semantic frames (gate uncommitted) must release those held frames
// to the client via GateWriter.Finish → FinishAttempt. The buggy path reused
// finishGateWriter's committed-guard on success, which swallowed the held
// buffer and left the client with an empty body.
func TestSurvivalCoordinatorFlushHeldFramesOnSuccessUnderHoldback(t *testing.T) {
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", "5000")
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS", "5")

	const marker = "held-but-flushed-on-success"
	content := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"" + marker + "\"}}\n\n"
	exec := &holdbackWriteExecutor{content: content}
	h := newCoordHarness(nil)
	h.exec = nil
	c := h.coordinator()
	c.Exec = exec

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{IsStream: true})

	if !res.Succeed {
		t.Fatalf("expected success, decision=%v reason=%v", res.Decision.Action, res.Decision.Reason)
	}
	if exec.calls != 1 {
		t.Fatalf("clean success must run one attempt, got %d", exec.calls)
	}
	// The marker only reaches the wire if the success branch flushed the
	// uncommitted held buffer. With the regression this body is empty.
	if got := h.flusher.buf.String(); !strings.Contains(got, marker) {
		t.Fatalf("success path must flush held semantic frames to the client; wire=%q", got)
	}
}

// TestSurvivalCoordinatorDiscardsHeldFramesOnTerminalFailureUnderHoldback is
// the complementary check: a terminal failure while the L1 window holds
// uncommitted semantic frames must NOT leak those frames to the client. The
// terminal branch uses finishGateWriter, whose committed-guard (gate not
// committed) skips the flush, so the held buffer is dropped.
func TestSurvivalCoordinatorDiscardsHeldFramesOnTerminalFailureUnderHoldback(t *testing.T) {
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_WINDOW_MS", "5000")
	t.Setenv("LLM_GATEWAY_RECOVERY_HOLDBACK_MAX_CHUNKS", "5")

	const marker = "must-not-leak-on-failure"
	content := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"" + marker + "\"}}\n\n"
	exec := &holdbackWriteExecutor{content: content, fail: true, failKind: errorsx.KindContentFilter}
	h := newCoordHarness(nil)
	h.exec = nil
	c := h.coordinator()
	c.Exec = exec

	res := c.Run(context.Background(), h.sw, &executors.ExecParams{})

	if res.Succeed {
		t.Fatal("content-filter failure must not succeed")
	}
	if res.Decision.Action != TaskActionFailTerminal {
		t.Fatalf("decision = %v, want fail_terminal", res.Decision.Action)
	}
	if exec.calls != 1 {
		t.Fatalf("terminal failure must not retry, got %d attempts", exec.calls)
	}
	// The held frames were never committed, so finishGateWriter's guard skips
	// the flush and the marker must not reach the wire.
	if got := h.flusher.buf.String(); strings.Contains(got, marker) {
		t.Fatalf("terminal failure must not leak held semantic frames; wire=%q", got)
	}
}

func TestSurvivalCoordinatorStopsWhenSharedUpstreamBudgetIsExhausted(t *testing.T) {
	exec := &alwaysTransientExecutor{}
	h := newCoordHarness(nil)
	c := h.coordinator()
	c.Exec = exec
	c.Options.MaxRetries = 100
	params := &executors.ExecParams{UpstreamAttempts: executors.NewUpstreamAttemptBudget(2)}

	res := c.Run(context.Background(), h.sw, params)

	if res.Decision.Reason != "attempt_limit_exceeded" {
		t.Fatalf("decision reason = %q, want attempt_limit_exceeded", res.Decision.Reason)
	}
	if exec.calls != 2 {
		t.Fatalf("executor calls = %d, want 2", exec.calls)
	}
}
