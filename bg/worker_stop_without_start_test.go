package bg

import (
	"testing"
	"time"
)

// TestStopReturnsWhenTheWorkerWasNeverStarted is the behavioural gate.
//
// 2026-10-21 (R71), measured on llm-gateway-go: the gateway's graceful
// shutdown hung for 97 minutes. The shutdown sequence is one goroutine, so
// a single Stop() that waits on a channel nobody will ever close prevents
// every later Stop() from running — including the one that owns the
// long-lived LISTEN connection. The deferred db.Close() then blocked
// forever inside pgxpool.Close(), and the process stayed alive as a zombie
// holding every Redis connection it had opened.
//
// The concrete trigger was the DEFAULT probe mode: cmd/gateway/main.go
// constructs the legacy passive-probe listener and skips Start() when
// LLM_GATEWAY_USE_NEW_PROBE_MODE is true.
//
// ⇒ The rule: Stop() on a worker that was constructed but never started
// must return promptly.
//
// This gate is deliberately behavioural rather than textual: the scanner in
// internal/stopguard proves the SHAPE, this proves the EFFECT. A future
// refactor that keeps the shape but breaks the behaviour still goes red.
func TestStopReturnsWhenTheWorkerWasNeverStarted(t *testing.T) {
	type stopper interface{ Stop() }

	cases := []struct {
		name string
		new  func() stopper
	}{
		// The proven victim: constructed unconditionally in cmd/gateway,
		// started only when LLM_GATEWAY_USE_NEW_PROBE_MODE is false.
		{"PassiveProbeListener", func() stopper { return NewPassiveProbeListener(nil, nil) }},
		// Second victim found by the same sweep: cmd/gateway constructs
		// the cycler and skips Start() in the same way.
		{"CredentialCycler", func() stopper { return NewCredentialCycler(nil, nil) }},
		{"AssetHealthProbe", func() stopper { return NewAssetHealthProbe(nil, nil) }},
		{"ApprovalTimeoutWorker", func() stopper { return NewApprovalTimeoutWorker(nil) }},
		{"AutoIndexRefresher", func() stopper { return NewAutoIndexRefresher(nil, nil) }},
		{"DefaultProbePicker", func() stopper { return NewDefaultProbePicker(nil) }},
		{"EnvelopeCleaner", func() stopper { return NewEnvelopeCleaner(nil) }},
		{"FeatureStatsWorker", func() stopper { return NewFeatureStatsWorker(nil, time.Minute) }},
		{"FeedbackAnalyzer", func() stopper { return NewFeedbackAnalyzer(nil) }},
		{"MaterializedViewRefresher", func() stopper { return NewMaterializedViewRefresher(nil) }},
		{"ModelConfigValidator", func() stopper { return NewModelConfigValidator(nil) }},
		{"ModelProbeRunner", func() stopper { return NewModelProbeRunner(nil, nil) }},
		{"ReportRollupWorker", func() stopper { return NewReportRollupWorker(nil) }},
		{"SessionHealthWorker", func() stopper { return NewSessionHealthWorker(nil) }},
		{"SettingsAuditCleaner", func() stopper { return NewSettingsAuditCleaner(nil) }},
		{"SlotSuggester", func() stopper { return NewSlotSuggester(nil) }},
		{"StickyCleaner", func() stopper { return NewStickyCleaner(nil) }},
		{"TaxonomySync", func() stopper { return NewTaxonomySync(nil, "") }},
		{"WeeklyPeakRollup", func() stopper { return NewWeeklyPeakRollup(nil) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.new()
			done := make(chan struct{})
			go func() {
				defer close(done)
				w.Stop()
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatalf("%s.Stop() blocked for >3s although the worker was never started — "+
					"the shutdown sequence runs in one goroutine, so this single block also "+
					"skips every later Stop() and lets the process linger as a zombie",
					tc.name)
			}
		})
	}
}
