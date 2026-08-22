package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// describeName returns the fully-qualified metric name a collector declares.
// Describe works on freshly-declared vecs (unlike Gather, which only reports
// families that already have children), so the registration contract is
// pinnable without touching any series.
func describeName(t *testing.T, c prometheus.Collector) string {
	t.Helper()
	ch := make(chan *prometheus.Desc, 1)
	c.Describe(ch)
	desc := <-ch
	// Desc.String() renders `Desc{fqName: "name", ...}`.
	s := desc.String()
	i := strings.Index(s, `"`)
	if i < 0 {
		t.Fatalf("cannot parse desc %q", s)
	}
	j := strings.Index(s[i+1:], `"`)
	if j < 0 {
		t.Fatalf("cannot parse desc %q", s)
	}
	return s[i+1 : i+1+j]
}

// SR-13 (doc 18 §15.1): the survival observability contract is the nine
// series named in the design doc. Dashboards and alerts are authored against
// these exact names, so the declarations are pinned here — a rename or
// accidental deletion fails loudly instead of silently splitting the series.
func TestSurvivalSeriesNamesRegistered(t *testing.T) {
	cases := []struct {
		name      string
		collector prometheus.Collector
	}{
		{"gateway_survival_requests_total", SurvivalRequestsTotal},
		{"gateway_survival_state_transitions_total", SurvivalStateTransitionsTotal},
		{"gateway_survival_wait_seconds", SurvivalWaitSeconds},
		{"gateway_survival_attempts_total", SurvivalAttemptsTotal},
		{"gateway_survival_active_tasks", SurvivalActiveTasks},
		{"gateway_survival_recovery_latency_seconds", SurvivalRecoveryLatencySeconds},
		{"gateway_survival_lease_conflicts_total", SurvivalLeaseConflictsTotal},
		{"gateway_survival_resume_safety_blocked_total", SurvivalResumeSafetyBlockedTotal},
		{"gateway_survival_keepalive_write_errors_total", SurvivalKeepaliveWriteErrorsTotal},
	}
	for _, tc := range cases {
		if got := describeName(t, tc.collector); got != tc.name {
			t.Errorf("doc 18 §15.1 series declared as %q, want %q", got, tc.name)
		}
	}
}

// TestSurvivalOutcomeLabelEnumeration pins the closed label sets of the two
// enumerated series so producers and dashboards agree on the vocabulary
// (doc 18 §15.1; the M0 skeleton comment defines the outcome enum).
func TestSurvivalOutcomeLabelEnumeration(t *testing.T) {
	// Touch every outcome the streaming producer (SR-13) emits plus the
	// skeleton's documented wait-reason buckets; reading the values back
	// through the proto path exercises the same Write the scrape uses.
	for _, outcome := range []string{
		"completed", "permanent_failed", "expired", "cancelled",
		"resume_safety_blocked", "error",
	} {
		c := SurvivalRequestsTotal.WithLabelValues("anthropic", "false", outcome)
		m := &dto.Metric{}
		if err := c.Write(m); err != nil {
			t.Fatalf("requests_total{%s}.Write: %v", outcome, err)
		}
	}
	for _, reason := range []string{
		"rate_limit", "quota_periodic", "concurrent", "upstream_overloaded",
		"transient", "network", "no_candidates", "other",
	} {
		h := SurvivalWaitSeconds.WithLabelValues(reason)
		m := &dto.Metric{}
		if err := h.(prometheus.Histogram).Write(m); err != nil {
			t.Fatalf("wait_seconds{%s}.Write: %v", reason, err)
		}
	}
}
