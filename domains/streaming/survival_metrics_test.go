package streaming

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

// SR-13 (doc 18 §15.1, doc 19 CO-1): the survival coordinator is the event
// producer for the nine survival series declared in
// metrics/survival_metrics.go. These tests run the coordinator's public Run
// loop (bounded fakes for executor/clock) and assert the exported
// Prometheus series actually move — the M0 skeleton stayed at zero forever,
// which is exactly the silent-data-loss failure mode pinned here.
//
// All assertions use before/after deltas because the series are process
// globals (promauto default registry) shared with the other coordinator
// tests in this package.

func survivalCounterDelta(t *testing.T, c interface{ Write(*dto.Metric) error }) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

func survivalHistogramDelta(t *testing.T, h interface{ Write(*dto.Metric) error }) (uint64, float64) {
	t.Helper()
	m := &dto.Metric{}
	if err := h.Write(m); err != nil {
		t.Fatalf("histogram.Write: %v", err)
	}
	return m.GetHistogram().GetSampleCount(), m.GetHistogram().GetSampleSum()
}

// survivalMetricCase bundles one scripted coordinator run.
type survivalMetricCase struct {
	name  string
	setup func() (*SurvivalCoordinator, *coordHarness, context.Context)
	// run executes the loop; split out so cancel-flavored cases can build
	// their own ctx.
	run func(c *SurvivalCoordinator, h *coordHarness, ctx context.Context) SurvivalResult
}

func runCase(t *testing.T, tc survivalMetricCase) SurvivalResult {
	t.Helper()
	co, h, ctx := tc.setup()
	if tc.run == nil {
		tc.run = func(c *SurvivalCoordinator, h *coordHarness, ctx context.Context) SurvivalResult {
			return c.Run(ctx, h.sw, &executors.ExecParams{})
		}
	}
	return tc.run(co, h, ctx)
}

// TestSurvivalMetricsRequestsTotalOutcome pins gateway_survival_requests_total:
// every coordinator task ends in exactly one terminal outcome from the closed
// enum {completed, permanent_failed, expired, cancelled, resume_safety_blocked,
// error}, labeled with the client protocol and durability.
func TestSurvivalMetricsRequestsTotalOutcome(t *testing.T) {
	cases := []struct {
		name    string
		outcome string
		tc      survivalMetricCase
	}{
		{
			name:    "succeed maps to completed",
			outcome: "completed",
			tc: survivalMetricCase{
				name: "succeed",
				setup: func() (*SurvivalCoordinator, *coordHarness, context.Context) {
					h := newCoordHarness(&scriptedExecutor{results: []*executors.ExecuteResult{{}}})
					return h.coordinator(), h, context.Background()
				},
			},
		},
		{
			name:    "terminal kind maps to permanent_failed",
			outcome: "permanent_failed",
			tc: survivalMetricCase{
				name: "terminal",
				setup: func() (*SurvivalCoordinator, *coordHarness, context.Context) {
					termErr := &executors.ExecuteError{
						LastKind: errorsx.KindContentFilter,
						Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: errorsx.KindContentFilter}},
					}
					h := newCoordHarness(&scriptedExecutor{errs: []error{termErr}})
					return h.coordinator(), h, context.Background()
				},
			},
		},
		{
			name:    "deadline exceeded maps to expired",
			outcome: "expired",
			tc: survivalMetricCase{
				name: "deadline",
				setup: func() (*SurvivalCoordinator, *coordHarness, context.Context) {
					h := newCoordHarness(&scriptedExecutor{errs: []error{rateLimitFailure(), rateLimitFailure()}})
					c := h.coordinator()
					c.Options.Deadline = 3 * time.Second
					return c, h, context.Background()
				},
			},
		},
		{
			name:    "client disconnect maps to cancelled",
			outcome: "cancelled",
			tc: survivalMetricCase{
				name: "disconnect",
				setup: func() (*SurvivalCoordinator, *coordHarness, context.Context) {
					h := newCoordHarness(&scriptedExecutor{errs: []error{rateLimitFailure()}})
					c := h.coordinator()
					ctx, cancel := context.WithCancel(context.Background())
					c.Sleep = func(ctx context.Context, d time.Duration) error {
						cancel()
						return ctx.Err()
					}
					return c, h, ctx
				},
			},
		},
		{
			name:    "committed recoverable maps to resume_safety_blocked",
			outcome: "resume_safety_blocked",
			tc: survivalMetricCase{
				name: "resume_blocked",
				setup: func() (*SurvivalCoordinator, *coordHarness, context.Context) {
					h := newCoordHarness(nil)
					c := h.coordinator()
					c.Exec = &committedExecutor{}
					return c, h, context.Background()
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The coordinator harness speaks Anthropic; the durable phase
			// (doc 18 §12) is not wired yet so durability stays false.
			lbl := metrics.SurvivalRequestsTotal.WithLabelValues("anthropic", "false", tc.outcome)
			before := survivalCounterDelta(t, lbl)

			runCase(t, tc.tc)

			if after := survivalCounterDelta(t, lbl); after != before+1 {
				t.Fatalf("gateway_survival_requests_total{anthropic,false,%s} delta = %v, want 1",
					tc.outcome, after-before)
			}
		})
	}
}

// TestSurvivalMetricsAttemptsTotalPerCandidate pins
// gateway_survival_attempts_total{kind,provider}: one increment per candidate
// outcome the executor walked through (a single attempt may rotate several
// credentials), with kind "" on success.
func TestSurvivalMetricsAttemptsTotalPerCandidate(t *testing.T) {
	t.Run("failed attempt counts every candidate outcome", func(t *testing.T) {
		multi := &executors.ExecuteError{
			LastKind: errorsx.KindUpstreamDown,
			Attempts: []executors.AttemptRecord{
				{ProviderID: 7, CredentialID: 1, Kind: errorsx.KindRateLimit},
				{ProviderID: 9, CredentialID: 4, Kind: errorsx.KindUpstreamDown},
			},
		}
		h := newCoordHarness(&scriptedExecutor{errs: []error{multi, multi}})
		c := h.coordinator()
		// Wait-recovery: both attempts run before the deadline stops the loop.
		c.Options.Deadline = 3 * time.Second

		rlBefore := survivalCounterDelta(t, metrics.SurvivalAttemptsTotal.WithLabelValues("rate_limit", "7"))
		udBefore := survivalCounterDelta(t, metrics.SurvivalAttemptsTotal.WithLabelValues("upstream_down", "9"))

		c.Run(context.Background(), h.sw, &executors.ExecParams{})

		if d := survivalCounterDelta(t, metrics.SurvivalAttemptsTotal.WithLabelValues("rate_limit", "7")) - rlBefore; d != 2 {
			t.Fatalf("attempts_total{rate_limit,7} delta = %v, want 2 (one per attempt)", d)
		}
		if d := survivalCounterDelta(t, metrics.SurvivalAttemptsTotal.WithLabelValues("upstream_down", "9")) - udBefore; d != 2 {
			t.Fatalf("attempts_total{upstream_down,9} delta = %v, want 2 (one per attempt)", d)
		}
	})

	t.Run("successful attempt counts once with empty kind", func(t *testing.T) {
		ok := &executors.ExecuteResult{}
		h := newCoordHarness(&scriptedExecutor{results: []*executors.ExecuteResult{ok}})
		c := h.coordinator()

		before := survivalCounterDelta(t, metrics.SurvivalAttemptsTotal.WithLabelValues("", "unknown"))

		c.Run(context.Background(), h.sw, &executors.ExecParams{})

		if d := survivalCounterDelta(t, metrics.SurvivalAttemptsTotal.WithLabelValues("", "unknown")) - before; d != 1 {
			t.Fatalf("attempts_total{\"\",unknown} delta = %v, want 1", d)
		}
	})
}

// survivalTransitionDeltas reads the deltas of the from/to/reason series a
// scenario is expected to move, keyed "from>to|reason".
func survivalTransitionDeltas(t *testing.T, before map[string]float64, keys [][3]string) map[string]float64 {
	t.Helper()
	out := make(map[string]float64, len(keys))
	for _, k := range keys {
		lbl := metrics.SurvivalStateTransitionsTotal.WithLabelValues(k[0], k[1], k[2])
		after := survivalCounterDelta(t, lbl)
		out[k[0]+">"+k[1]+"|"+k[2]] = after - before[k[0]+">"+k[1]+"|"+k[2]]
	}
	return out
}

func survivalTransitionsBefore(t *testing.T, keys [][3]string) map[string]float64 {
	t.Helper()
	out := make(map[string]float64, len(keys))
	for _, k := range keys {
		out[k[0]+">"+k[1]+"|"+k[2]] = survivalCounterDelta(t,
			metrics.SurvivalStateTransitionsTotal.WithLabelValues(k[0], k[1], k[2]))
	}
	return out
}

// TestSurvivalMetricsStateTransitions pins gateway_survival_state_transitions_total:
// the decision dispatch and the wait loop emit the task-state transitions the
// dashboards replay the state machine from.
func TestSurvivalMetricsStateTransitions(t *testing.T) {
	keys := [][3]string{
		{"running", "retry_now", "recoverable_candidate"},
		{"running", "waiting_recovery", "wait_recovery_window"},
		{"waiting_recovery", "running", "retry"},
		{"running", "succeed", "success"},
		{"running", "fail_terminal", "terminal_candidate"},
		{"running", "expired", "deadline_exceeded"},
		{"waiting_recovery", "cancelled", "client_disconnected"},
	}

	t.Run("retry-now then success walks running->retry_now->running->succeed", func(t *testing.T) {
		h := newCoordHarness(&scriptedExecutor{
			errs:    []error{transientFailure(), nil},
			results: []*executors.ExecuteResult{nil, {}},
		})
		c := h.coordinator()
		before := survivalTransitionsBefore(t, keys)

		c.Run(context.Background(), h.sw, &executors.ExecParams{})

		got := survivalTransitionDeltas(t, before, keys)
		want := map[string]float64{
			"running>retry_now|recoverable_candidate":        1,
			"running>succeed|success":                        1,
			"waiting_recovery>running|retry":                 0,
			"running>waiting_recovery|wait_recovery_window":  0,
			"running>fail_terminal|terminal_candidate":       0,
			"running>expired|deadline_exceeded":              0,
			"waiting_recovery>cancelled|client_disconnected": 0,
		}
		for k, w := range want {
			if got[k] != w {
				t.Fatalf("transition %s delta = %v, want %v (all: %v)", k, got[k], w, got)
			}
		}
	})

	t.Run("wait-recovery emits running->waiting_recovery and waiting->running on retry", func(t *testing.T) {
		h := newCoordHarness(&scriptedExecutor{
			errs:    []error{rateLimitFailure(), nil},
			results: []*executors.ExecuteResult{nil, {}},
		})
		c := h.coordinator()
		before := survivalTransitionsBefore(t, keys)

		c.Run(context.Background(), h.sw, &executors.ExecParams{})

		got := survivalTransitionDeltas(t, before, keys)
		if got["running>waiting_recovery|wait_recovery_window"] != 1 {
			t.Fatalf("entering the wait must emit running->waiting_recovery, got %v", got)
		}
		if got["waiting_recovery>running|retry"] != 1 {
			t.Fatalf("leaving the wait for the next attempt must emit waiting_recovery->running, got %v", got)
		}
		if got["running>succeed|success"] != 1 {
			t.Fatalf("final success must emit running->succeed, got %v", got)
		}
		if got["running>retry_now|recoverable_candidate"] != 0 {
			t.Fatalf("wait-recovery must not emit retry_now, got %v", got)
		}
	})

	t.Run("deadline while waiting emits running->expired", func(t *testing.T) {
		h := newCoordHarness(&scriptedExecutor{errs: []error{rateLimitFailure(), rateLimitFailure()}})
		c := h.coordinator()
		c.Options.Deadline = 3 * time.Second
		before := survivalTransitionsBefore(t, keys)

		c.Run(context.Background(), h.sw, &executors.ExecParams{})

		got := survivalTransitionDeltas(t, before, keys)
		if got["running>expired|deadline_exceeded"] != 1 {
			t.Fatalf("deadline must emit running->expired exactly once, got %v", got)
		}
	})

	t.Run("client disconnect during wait emits waiting_recovery->cancelled", func(t *testing.T) {
		h := newCoordHarness(&scriptedExecutor{errs: []error{rateLimitFailure()}})
		c := h.coordinator()
		ctx, cancel := context.WithCancel(context.Background())
		c.Sleep = func(ctx context.Context, d time.Duration) error {
			cancel()
			return ctx.Err()
		}
		before := survivalTransitionsBefore(t, keys)

		c.Run(ctx, h.sw, &executors.ExecParams{})

		got := survivalTransitionDeltas(t, before, keys)
		if got["waiting_recovery>cancelled|client_disconnected"] != 1 {
			t.Fatalf("disconnect during wait must emit waiting_recovery->cancelled, got %v", got)
		}
		if got["waiting_recovery>running|retry"] != 0 {
			t.Fatalf("a disconnecting wait must not emit the retry transition, got %v", got)
		}
	})

	t.Run("terminal candidate emits running->fail_terminal", func(t *testing.T) {
		termErr := &executors.ExecuteError{
			LastKind: errorsx.KindContentFilter,
			Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: errorsx.KindContentFilter}},
		}
		h := newCoordHarness(&scriptedExecutor{errs: []error{termErr}})
		c := h.coordinator()
		before := survivalTransitionsBefore(t, keys)

		c.Run(context.Background(), h.sw, &executors.ExecParams{})

		got := survivalTransitionDeltas(t, before, keys)
		if got["running>fail_terminal|terminal_candidate"] != 1 {
			t.Fatalf("terminal decision must emit running->fail_terminal, got %v", got)
		}
	})
}

// TestSurvivalMetricsWaitSeconds pins gateway_survival_wait_seconds{reason}:
// a completed recovery wait is observed once, in seconds, labeled by the
// dominant recovery reason bucket derived from the failing error kind.
func TestSurvivalMetricsWaitSeconds(t *testing.T) {
	cases := []struct {
		name   string
		kind   errorsx.ErrorKind
		reason string
	}{
		{"rate limit waits under rate_limit", errorsx.KindRateLimit, "rate_limit"},
		{"quota periodic waits under quota_periodic", errorsx.KindQuotaPeriodic, "quota_periodic"},
		{"upstream down waits under upstream_overloaded", errorsx.KindUpstreamDown, "upstream_overloaded"},
		{"no channel waits under no_candidates", errorsx.KindNoAvailableChannel, "no_candidates"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fail := &executors.ExecuteError{
				LastKind: tc.kind,
				Attempts: []executors.AttemptRecord{{ProviderID: 1, CredentialID: 1, Kind: tc.kind}},
			}
			h := newCoordHarness(&scriptedExecutor{
				errs:    []error{fail, nil},
				results: []*executors.ExecuteResult{nil, {}},
			})
			c := h.coordinator()
			hist := metrics.SurvivalWaitSeconds.WithLabelValues(tc.reason).(prometheus.Histogram)
			cb, sb := survivalHistogramDelta(t, hist)

			c.Run(context.Background(), h.sw, &executors.ExecParams{})

			ca, sa := survivalHistogramDelta(t, hist)
			if ca-cb != 1 {
				t.Fatalf("wait_seconds{%s} sample count delta = %d, want 1", tc.reason, ca-cb)
			}
			if got := (sa - sb) * 1e9; got != 2*1e9 {
				t.Fatalf("wait_seconds{%s} observed %vns, want the 2s base backoff", tc.reason, got)
			}
		})
	}

	t.Run("retry-now does not record a recovery wait", func(t *testing.T) {
		h := newCoordHarness(&scriptedExecutor{
			errs:    []error{transientFailure(), nil},
			results: []*executors.ExecuteResult{nil, {}},
		})
		c := h.coordinator()
		cb, _ := survivalHistogramDelta(t, metrics.SurvivalWaitSeconds.WithLabelValues("transient").(prometheus.Histogram))

		c.Run(context.Background(), h.sw, &executors.ExecParams{})

		if ca, _ := survivalHistogramDelta(t, metrics.SurvivalWaitSeconds.WithLabelValues("transient").(prometheus.Histogram)); ca != cb {
			t.Fatal("immediate retry-now backoff must not be recorded as a recovery wait")
		}
	})
}
