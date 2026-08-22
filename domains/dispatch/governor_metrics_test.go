package dispatch

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestGovernorMetricsAllowlistRejectsOpenEnum verifies that the typed
// wrappers panic when the caller asks for a variableLabels set that does
// NOT match the closed-enum shape the allowlist enforces.
func TestGovernorMetricsAllowlistRejectsOpenEnum(t *testing.T) {
	cases := []struct {
		name    string
		attempt func()
		wantSub string
	}{
		{
			name: "backend wrapper with credential label",
			attempt: func() {
				MustNewBackendCounterVec(prometheus.CounterOpts{
					Name: "dispatch_test_backend_credl_mismatch",
					Help: "should never register",
				}, []string{"credential"})
			},
			wantSub: `"backend"`,
		},
		{
			name: "mode wrapper with open label",
			attempt: func() {
				MustNewModeHistogramVec(prometheus.HistogramOpts{
					Name: "dispatch_test_mode_open_mismatch",
					Help: "should never register",
				}, []string{"model"})
			},
			wantSub: `"mode"`,
		},
		{
			name: "state wrapper with extra label",
			attempt: func() {
				MustNewStateGaugeVec(prometheus.GaugeOpts{
					Name: "dispatch_test_state_extra_mismatch",
					Help: "should never register",
				}, []string{"state", "backend"})
			},
			wantSub: `"state"`,
		},
		{
			name: "result wrapper with reordered labels",
			attempt: func() {
				MustNewResultCounterVec(prometheus.CounterOpts{
					Name: "dispatch_test_result_reordered_mismatch",
					Help: "should never register",
				}, []string{"result", "backend"})
			},
			wantSub: `"result"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic; got none")
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("panic value not string: %T %v", r, r)
				}
				if !strings.Contains(msg, tc.wantSub) {
					t.Fatalf("panic message %q does not contain %q", msg, tc.wantSub)
				}
			}()
			tc.attempt()
		})
	}
}

// TestGovernorMetricsAllowlistAcceptsClosedEnum verifies that the typed
// wrappers accept the closed-enum label set and that every known value
// is registered (warming succeeds).
func TestGovernorMetricsAllowlistAcceptsClosedEnum(t *testing.T) {
	// Use unique names per attempt to avoid promauto duplicate-name panics.
	t.Run("backend", func(t *testing.T) {
		vec := MustNewBackendCounterVec(prometheus.CounterOpts{
			Name: "dispatch_test_accept_backend",
			Help: "should register",
		}, []string{"backend"})
		if vec == nil {
			t.Fatal("nil vec")
		}
	})
	t.Run("mode", func(t *testing.T) {
		vec := MustNewModeHistogramVec(prometheus.HistogramOpts{
			Name: "dispatch_test_accept_mode",
			Help: "should register",
		}, []string{"mode"})
		if vec == nil {
			t.Fatal("nil vec")
		}
	})
	t.Run("state", func(t *testing.T) {
		vec := MustNewStateGaugeVec(prometheus.GaugeOpts{
			Name: "dispatch_test_accept_state",
			Help: "should register",
		}, []string{"state"})
		if vec == nil {
			t.Fatal("nil vec")
		}
	})
	t.Run("result", func(t *testing.T) {
		vec := MustNewResultCounterVec(prometheus.CounterOpts{
			Name: "dispatch_test_accept_result",
			Help: "should register",
		}, []string{"result"})
		if vec == nil {
			t.Fatal("nil vec")
		}
	})
	t.Run("backend+mode histogram", func(t *testing.T) {
		vec := MustNewBackendModeHistogramVec(prometheus.HistogramOpts{
			Name: "dispatch_test_accept_backend_mode_h",
			Help: "should register",
		}, []string{"backend", "mode"})
		if vec == nil {
			t.Fatal("nil vec")
		}
	})
	t.Run("backend+mode+state gauge", func(t *testing.T) {
		vec := MustNewBackendModeStateGaugeVec(prometheus.GaugeOpts{
			Name: "dispatch_test_accept_backend_mode_state",
			Help: "should register",
		}, []string{"backend", "mode", "state"})
		if vec == nil {
			t.Fatal("nil vec")
		}
	})
	t.Run("backend+mode+result counter", func(t *testing.T) {
		vec := MustNewBackendModeResultCounterVec(prometheus.CounterOpts{
			Name: "dispatch_test_accept_backend_mode_result",
			Help: "should register",
		}, []string{"backend", "mode", "result"})
		if vec == nil {
			t.Fatal("nil vec")
		}
	})
}

// counterValueFromGatherer reads the value of a *Counter metric by name
// from the default gatherer. Returns 0 if the metric has not been
// registered yet. Distinct from the dispatch_test.go helper which is
// hard-coded to metricFailover.
func counterValueFromGatherer(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// TestRecordAcquisitionResultSkipsUnknownLabel verifies that emit-time
// helpers reject off-list values with a slog warn + drop counter bump
// rather than emitting a metric series outside the allowlist.
func TestRecordAcquisitionResultSkipsUnknownLabel(t *testing.T) {
	// Capture slog output so we can assert the warn fired.
	original := slog.Default()
	defer slog.SetDefault(original)
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	before := counterValueFromGatherer(t, "dispatch_governor_unknown_label_drops_total")

	// off-list backend, mode, result — all three must trip the drop.
	RecordAcquisitionResult(GovernorBackendKind("bogus"), ModeRPM, "ready", 0.001)
	RecordAcquisitionResult(BackendLocal, "bogus", "ready", 0.001)
	RecordAcquisitionResult(BackendLocal, ModeRPM, "bogus", 0.001)

	after := counterValueFromGatherer(t, "dispatch_governor_unknown_label_drops_total")
	if after-before != 3 {
		t.Fatalf("expected 3 unknown-label drops; got %v", after-before)
	}
	if !strings.Contains(buf.String(), "off-list label") {
		t.Fatalf("expected slog warn to mention off-list label; got:\n%s", buf.String())
	}
}

// TestRecordAcquisitionResultAcceptsClosedEnum verifies the happy path
// emits into the counter and the drop counter does NOT increment.
func TestRecordAcquisitionResultAcceptsClosedEnum(t *testing.T) {
	before := counterValueFromGatherer(t, "dispatch_governor_unknown_label_drops_total")
	RecordAcquisitionResult(BackendLocal, ModeRPM, "ready", 0.005)
	RecordAcquisitionResult(BackendRedisEnforce, ModeConcurrency, "saturated", 0.012)
	after := counterValueFromGatherer(t, "dispatch_governor_unknown_label_drops_total")
	if after-before != 0 {
		t.Fatalf("closed-enum emit must not increment drop counter; got %v", after-before)
	}
}

// TestRecordSnapshotEmitsStateAndAge verifies that a well-formed snapshot
// produces an exposed series in dispatch_governor_snapshot_state with the
// expected label set.
func TestRecordSnapshotEmitsStateAndAge(t *testing.T) {
	snap := GovernorSnapshot{
		Backend: string(BackendLocal),
		Mode:    ModeRPM,
		State:   SnapshotStateReady,
		AgeMS:   150,
	}
	RecordSnapshot(snap)

	// Round-trip via the default gatherer.
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "dispatch_governor_snapshot_state" {
			continue
		}
		for _, m := range mf.GetMetric() {
			got := map[string]string{}
			for _, lp := range m.GetLabel() {
				got[lp.GetName()] = lp.GetValue()
			}
			if got["backend"] == "local" && got["mode"] == "rpm" && got["state"] == "ready" && m.GetGauge().GetValue() == 1 {
				// hit — done
				return
			}
		}
	}
	t.Fatalf("expected (local, rpm, ready)=1 series; not found")
}

// TestRecordSnapshotDropsUnknownState verifies the bidirectional
// invariant: State=Unknown is dropped (BackendErr!=nil path), so no
// series is emitted for it.
func TestRecordSnapshotDropsUnknownState(t *testing.T) {
	// Avoid triggering Validate() panic — fill the snapshot with
	// consistent fields; the helper itself checks State==Unknown.
	snap := GovernorSnapshot{
		Backend:    string(BackendLocal),
		Mode:       ModeRPM,
		State:      SnapshotStateUnknown,
		BackendErr: errTestBackendFault,
	}
	RecordSnapshot(snap) // must not panic

	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "dispatch_governor_snapshot_state" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "state" && lp.GetValue() == "unknown" && m.GetGauge().GetValue() == 1 {
					t.Fatalf("RecordSnapshot must NOT emit state=unknown series")
				}
			}
		}
	}
}

// errTestBackendFault is a sentinel used only by the drop-unknown-state
// test above.
var errTestBackendFault = &testFaultError{}

type testFaultError struct{}

func (*testFaultError) Error() string { return "test: backend fault" }
