package dispatch

// 会话优化 v4 T3-4 — combination exhaustion tests (UT-FO-05).
//
// When every candidate model × node combination has been tried, the ladder
// terminates IMMEDIATELY (before the 100-attempt budget can matter) with an
// aggregate error carrying the tried model/node/reason summary. The error
// form follows ADR-Disp-006: the executor maps errors.Is(err, ErrNoRoute) or
// concrete causes to *ExecuteError{Exhausted} (503 + Retry-After).

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// TestCombinationExhaustionAggregateError (UT-FO-05): 2 models × 2 nodes,
// all failing, RetryPerCredential=0 → exactly 4 attempts (== combination
// count, far below the 100 budget) and a *ExhaustedError summarizing every
// tried combination while preserving the concrete upstream cause.
func TestCombinationExhaustionAggregateError(t *testing.T) {
	upstreamErr := errors.New("persistent-502")
	var forwards atomic.Int64
	cfg := DefaultConfig()
	cfg.RetryPerCredential = 0 // each node tried once: attempts == combos
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)

	p := NewPipeline(Deps{
		RouteFunc: func(_ context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			switch qr.ResolvedModel {
			case "model-a":
				return []CredentialRef{credWithProvider(1, 10, 5), credWithProvider(2, 10, 5)}, nil
			case "model-b":
				return []CredentialRef{credWithProvider(3, 20, 5), credWithProvider(4, 20, 5)}, nil
			default:
				return nil, nil
			}
		},
		ModelResolveFunc: func(_ context.Context, requested string, _ []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			forwards.Add(1)
			return ForwardOutcome{Err: upstreamErr, ErrorKind: "upstream_error"}
		},
		AllowModelChange: true,
		HotCfg:           hotCfg,
	})
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("exhaust-1", "t", "model-a", context.Background(), nil)
	qr.AllowModelChange = true
	qr.ModelAlternatives = []string{"model-b"}
	_, err := p.Submit(context.Background(), qr)
	if err == nil {
		t.Fatal("expected terminal failure")
	}

	// Combination exhaustion terminated at the combination count (4), long
	// before the 100-attempt budget.
	if got := forwards.Load(); got != 4 {
		t.Fatalf("forward attempts = %d, want 4 (2 models × 2 nodes, < budget %d)", got, maxAttempts)
	}
	if qr.AttemptCount != 4 {
		t.Fatalf("AttemptCount = %d, want 4", qr.AttemptCount)
	}

	// Aggregate error shape: *ExhaustedError with the full summary.
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("expected *ExhaustedError, got %T: %v", err, err)
	}
	if len(exhausted.Attempts) != 4 {
		t.Fatalf("summary attempts = %d, want 4: %s", len(exhausted.Attempts), exhausted.Summary())
	}
	seenCombos := map[string]bool{}
	for _, attempt := range exhausted.Attempts {
		seenCombos[attempt.Model+"#"+itoa(int(attempt.CredentialID))] = true
		if attempt.Reason != "upstream_error" {
			t.Fatalf("attempt reason = %q, want upstream_error", attempt.Reason)
		}
	}
	for _, combo := range []string{"model-a#1", "model-a#2", "model-b#3", "model-b#4"} {
		if !seenCombos[combo] {
			t.Fatalf("summary missing combination %s: %s", combo, exhausted.Summary())
		}
	}
	if !strings.Contains(exhausted.Summary(), "model-b#4") {
		t.Fatalf("summary should render combinations: %s", exhausted.Summary())
	}

	// The concrete upstream cause stays preserved (legacy behavior pinned by
	// TestLastUpstreamErrorPreserved); the wrapper must not mask it.
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("exhaustion error must preserve the upstream cause, got %v", err)
	}
	if err.Error() != upstreamErr.Error() {
		t.Fatalf("Error() should delegate to the cause: %q", err.Error())
	}

	// Executor-side extraction helper.
	if summary, ok := AsExhaustedError(err); !ok || len(summary.Attempts) != 4 {
		t.Fatalf("AsExhaustedError helper = %#v (ok=%v)", summary, ok)
	}
}

// TestNeverRoutedKeepsPlainNoRoute: zero-attempt no-route terminals keep the
// plain sentinel (no misleading exhaustion summary).
func TestNeverRoutedKeepsPlainNoRoute(t *testing.T) {
	p := NewPipeline(Deps{
		RouteFunc: func(context.Context, *QueuedRequest) ([]CredentialRef, error) { return nil, nil },
		ModelResolveFunc: func(_ context.Context, requested string, _ []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome { return ForwardOutcome{} },
	})
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("noroute-1", "t", "m", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute, got %v", err)
	}
	var exhausted *ExhaustedError
	if errors.As(err, &exhausted) {
		t.Fatalf("never-routed terminal must not carry an exhaustion summary: %v", err)
	}
}

// TestAttemptCapStillBounds (regression): with candidates remaining, the
// 100-attempt budget still terminates the request (exhaustion priority only
// applies when the combination space is genuinely done).
func TestAttemptCapStillBounds(t *testing.T) {
	creds := make([]CredentialRef, 0, 60)
	for i := 1; i <= 60; i++ {
		creds = append(creds, cred(i, ModeConcurrency, 1))
	}
	var forwards atomic.Int64
	cfg := DefaultConfig()
	cfg.RetryPerCredential = 0
	hotCfg := &atomic.Value{}
	hotCfg.Store(&cfg)
	p := NewPipeline(Deps{
		RouteFunc: func(_ context.Context, qr *QueuedRequest) ([]CredentialRef, error) {
			out := make([]CredentialRef, 0, len(creds))
			for _, ref := range creds {
				if !qr.HasTriedCredential(ref.CredentialID) {
					out = append(out, ref)
				}
			}
			return out, nil
		},
		ModelResolveFunc: func(_ context.Context, requested string, _ []string) (string, []string, error) {
			return requested, nil, nil
		},
		ForwardFunc: func(context.Context, *QueuedRequest, CredentialRef) ForwardOutcome {
			forwards.Add(1)
			return ForwardOutcome{Err: errors.New("fail")}
		},
		HotCfg: hotCfg,
	})
	p.Start()
	defer p.Stop()

	qr := NewQueuedRequest("cap-1", "t", "m", context.Background(), nil)
	_, err := p.Submit(context.Background(), qr)
	if err == nil {
		t.Fatal("expected terminal failure")
	}
	if got := forwards.Load(); got > maxAttempts {
		t.Fatalf("attempt cap violated: %d forwards (cap=%d)", got, maxAttempts)
	}
	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("budget termination should reuse the aggregate envelope, got %T", err)
	}
}
