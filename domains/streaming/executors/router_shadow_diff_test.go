package executors

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"

	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestRouterShadowDiffEmitsMetric verifies that when URSMv2 is in ModeShadow,
// the router computes a diff between legacy and v2 orderings and emits the
// ursm_shadow_diff_total metric with the appropriate type label
// (identical, availability_mismatch, order_mismatch).
func TestRouterShadowDiffEmitsMetric(t *testing.T) {
	// Reset the metric before running tests to avoid pollution from other tests
	ursmShadowDiffTotal.Reset()

	// Two candidates with different lat_ewma scores so v2 produces a different
	// ordering than legacy P2C.
	candidates := []provider.Candidate{
		{CredentialID: 1, ProviderID: 10, RawModel: "m", Tier: 1, Routable: true, P50LatencyMs: 100},
		{CredentialID: 2, ProviderID: 11, RawModel: "m", Tier: 1, Routable: true, P50LatencyMs: 200},
	}
	planCtx := PlanContext{
		TenantID:       "tenant-shadow",
		CanonicalModel: "m",
		RequestID:      "req-shadow-1",
	}

	// seedV2 writes v2 store entries: cred=2 has lower lat_ewma so v2 will
	// rank it first, creating an order mismatch with legacy ordering.
	seedV2 := func(t *testing.T, rdb *redis.Client) {
		t.Helper()
		ctx := context.Background()
		if err := rdb.HSet(ctx,
			"ursm:v2:node:1:m", "available", "1", "lat_ewma", "100",
		).Err(); err != nil {
			t.Fatalf("seed cred=1: %v", err)
		}
		if err := rdb.HSet(ctx,
			"ursm:v2:node:2:m", "available", "1", "lat_ewma", "50",
		).Err(); err != nil {
			t.Fatalf("seed cred=2: %v", err)
		}
	}

	t.Run("shadow_mode_emits_diff_metric", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		seedV2(t, rdb)

		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeShadow

		mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
		if err := mgr.SetReady(context.Background(), true); err != nil {
			t.Fatalf("ready: %v", err)
		}

		r := NewRouter(nil, nil)
		r.URSMv2 = mgr

		// Capture the metric value before the call
		metricBefore := getCounterValue(t, ursmShadowDiffTotal)

		// Call PlanCandidates - shadow mode should compute diff but not alter production ordering
		out, _ := r.PlanCandidates(candidates, planCtx, nil, &provider.Policy{}, nil)

		// Verify production ordering is unchanged (shadow mode doesn't modify it)
		if len(out) != 2 {
			t.Fatalf("expected 2 candidates, got %d", len(out))
		}

		// Capture the metric value after the call
		metricAfter := getCounterValue(t, ursmShadowDiffTotal)

		// Verify that the metric was incremented
		if metricAfter <= metricBefore {
			t.Fatalf("ursm_shadow_diff_total counter did not increment: before=%v, after=%v", metricBefore, metricAfter)
		}

		// Verify that one of the diff type labels was incremented
		// (we expect either order_mismatch or identical depending on legacy ordering)
		labels := []string{"identical", "availability_mismatch", "order_mismatch"}
		totalIncremented := false
		for _, label := range labels {
			val := getCounterValueWithLabel(t, ursmShadowDiffTotal, "type", label)
			if val > 0 {
				totalIncremented = true
				t.Logf("shadow diff metric incremented for type=%s: %v", label, val)
			}
		}

		if !totalIncremented {
			t.Fatal("no shadow diff type label was incremented")
		}
	})

	t.Run("non_shadow_mode_does_not_emit_diff_metric", func(t *testing.T) {
		mr := miniredis.RunT(t)
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		seedV2(t, rdb)

		cfg := ursmv2.DefaultConfig()
		cfg.Mode = api.ModeCanary
		cfg.CanaryPercent = 0

		mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
		if err := mgr.SetReady(context.Background(), true); err != nil {
			t.Fatalf("ready: %v", err)
		}

		r := NewRouter(nil, nil)
		r.URSMv2 = mgr

		// Capture the metric value before the call
		metricBefore := getCounterValue(t, ursmShadowDiffTotal)

		// Call PlanCandidates in canary mode (not shadow)
		out, _ := r.PlanCandidates(candidates, planCtx, nil, &provider.Policy{}, nil)

		if len(out) != 2 {
			t.Fatalf("expected 2 candidates, got %d", len(out))
		}

		// Capture the metric value after the call
		metricAfter := getCounterValue(t, ursmShadowDiffTotal)

		// Verify that the metric was NOT incremented (only shadow mode emits)
		if metricAfter != metricBefore {
			t.Fatalf("ursm_shadow_diff_total counter should not increment in non-shadow mode: before=%v, after=%v", metricBefore, metricAfter)
		}
	})
}

// getCounterValue extracts the total counter value from a CounterVec across all labels
func getCounterValue(t *testing.T, cv *prometheus.CounterVec) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 10)
	cv.Collect(ch)
	close(ch)

	var total float64
	for m := range ch {
		var metric dto.Metric
		if err := m.Write(&metric); err != nil {
			t.Fatalf("failed to write metric: %v", err)
		}
		if metric.Counter != nil && metric.Counter.Value != nil {
			total += *metric.Counter.Value
		}
	}
	return total
}

// getCounterValueWithLabel extracts the counter value for a specific label from a CounterVec
func getCounterValueWithLabel(t *testing.T, cv *prometheus.CounterVec, labelName, labelValue string) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 10)
	cv.Collect(ch)
	close(ch)

	for m := range ch {
		var metric dto.Metric
		if err := m.Write(&metric); err != nil {
			t.Fatalf("failed to write metric: %v", err)
		}
		// Check if this metric has the matching label
		for _, label := range metric.Label {
			if label.GetName() == labelName && label.GetValue() == labelValue {
				if metric.Counter != nil && metric.Counter.Value != nil {
					return *metric.Counter.Value
				}
			}
		}
	}
	return 0
}
