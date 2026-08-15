package streaming

import (
	"context"
	"testing"
	"time"

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
