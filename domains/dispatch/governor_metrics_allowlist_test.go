package dispatch

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestGovernorMetricsLabelAllowlistEnforced pins the closed-enum
// invariant for the dispatch_governor_* metric family. It lives in the
// dispatch package so that the registration of governor metrics is
// guaranteed to have happened (var blocks at package init) before this
// test runs, while the metrics/ package's TestNoHighCardinalityLabels
// (which already exists) continues to enforce the global forbidden-label
// list independently.
//
// Allowed labels: backend / mode / result / state. Allowed values:
// mirror the constants in governor_backend.go, queued_request.go,
// governor_snapshot.go. Drift is a coordinate break for dashboards.
func TestGovernorMetricsLabelAllowlistEnforced(t *testing.T) {
	allowedLabels := map[string]bool{
		"backend": true,
		"mode":    true,
		"result":  true,
		"state":   true,
	}
	allowedValues := map[string]map[string]bool{
		"backend": {
			string(BackendLocal):        true,
			string(BackendRedisEnforce): true,
			string(BackendRedisShadow):  true,
		},
		"mode": {
			ModeConcurrency: true,
			ModeRPM:         true,
			ModeTPM:         true,
			ModeDisabled:    true,
		},
		"result": {
			"ready":         true,
			"saturated":     true,
			"invalid_token": true,
			"unknown":       true,
		},
		"state": {
			string(SnapshotStateReady):             true,
			string(SnapshotStateQueueFull):         true,
			string(SnapshotStateGovernorSaturated): true,
			string(SnapshotStateUnknown):           true,
		},
	}

	// Gather all metric families from the default registry.
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var governorMFs int
	for _, mf := range mfs {
		name := mf.GetName()
		if !strings.HasPrefix(name, "dispatch_governor_") {
			continue
		}
		governorMFs++
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				lname := lp.GetName()
				lval := lp.GetValue()
				if !allowedLabels[lname] {
					t.Errorf("dispatch_governor_* metric %q declared/emitted non-allowlist label %q (allowed: backend/mode/result/state)",
						name, lname)
					continue
				}
				if vset, ok := allowedValues[lname]; ok && !vset[lval] {
					t.Errorf("dispatch_governor_* metric %q emitted off-list value %s=%q", name, lname, lval)
				}
			}
		}
	}

	// Also walk Descs (works even if a Vec has never been WithLabelValues'd).
	descs := collectDescs(t)
	descSeen := 0
	for _, ds := range descs {
		fqName := parseFQNameLocal(ds)
		if !strings.HasPrefix(fqName, "dispatch_governor_") {
			continue
		}
		descSeen++
		for _, l := range parseVarLabelsLocal(ds) {
			if !allowedLabels[l] {
				t.Errorf("dispatch_governor_* Desc %q declares non-allowlist label %q (allowed: backend/mode/result/state)",
					fqName, l)
			}
		}
	}

	if governorMFs == 0 && descSeen == 0 {
		t.Fatal("no dispatch_governor_* metrics found at all; governor_metrics.go failed to register")
	}
}

// collectDescs parses *prometheus.Desc.String() output for every
// registered metric. Distinct from metrics/label_cardinality_guard_test.go's
// helper only because the latter is package-private to metrics/.
func collectDescs(t *testing.T) []string {
	t.Helper()
	ch := make(chan *prometheus.Desc, 256)
	go func() {
		// promauto registers on DefaultRegisterer; DefaultGatherer shares
		// it. The type assertion is safe because promauto always uses a
		// concrete *Registry internally.
		reg := prometheus.DefaultRegisterer
		if r, ok := reg.(*prometheus.Registry); ok {
			r.Describe(ch)
		}
		close(ch)
	}()
	var out []string
	for d := range ch {
		out = append(out, d.String())
	}
	return out
}

// parseFQName + parseVarLabels are duplicated from
// metrics/label_cardinality_guard_test.go. Keep them small so the
// dispatch package can enforce its own invariant without crossing the
// metrics dependency boundary (metrics does not import dispatch — it
// shouldn't; the other direction is fine).

func parseFQNameLocal(descStr string) string {
	i := strings.Index(descStr, `fqName: "`)
	if i < 0 {
		return "?"
	}
	rest := descStr[i+len(`fqName: "`):]
	if j := strings.Index(rest, `"`); j >= 0 {
		return rest[:j]
	}
	return "?"
}

func parseVarLabelsLocal(descStr string) []string {
	i := strings.Index(descStr, "variableLabels: {")
	if i < 0 {
		return nil
	}
	rest := descStr[i+len("variableLabels: {"):]
	j := strings.Index(rest, "}")
	if j < 0 {
		return nil
	}
	inner := strings.TrimSpace(rest[:j])
	if inner == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(inner, ",") {
		p = strings.TrimSpace(p)
		p = strings.TrimSuffix(strings.TrimPrefix(p, "c("), ")")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
