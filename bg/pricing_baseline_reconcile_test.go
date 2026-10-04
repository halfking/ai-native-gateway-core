// bg/pricing_baseline_reconcile_test.go — 外部机读源的抓取与摊平
//
// 这组测试跑在真实抓取路径上（httptest 起一个 models.dev 形状的源），
// 只把出网那一步接管。目的是三件事：
//
//  1. 摊平后的键必须带 provider 段——同一模型在不同 provider 下价格不同，
//     只有原厂那一档才是基准价的对家。
//  2. 只有带 cost 的模型进观察表；没有 cost 的（免费/未定价）不能被当成
//     「0 元」参与对账。
//  3. ReconcileCatalog 只判 SSOT 里有的模型。
package bg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const mdevFixture = `{
  "openai": {
    "name": "OpenAI",
    "models": {
      "gpt-x": {
        "id": "gpt-x",
        "cost": {"input": 2.5, "output": 10, "cache_read": 0.25,
                 "tiers": [{"input": 5, "output": 22.5, "cache_read": 0.5,
                            "tier": {"type": "context", "size": 272000}}]},
        "modalities": {"input": ["text", "image"], "output": ["text"]}
      },
      "unpriced-model": {"id": "unpriced-model", "modalities": {"input": ["text"]}}
    }
  },
  "vapeur": {
    "name": "vapeur",
    "models": {
      "gpt-x": {"id": "gpt-x", "cost": {"input": 3.0, "output": 12}}
    }
  }
}`

func newObservationFixture(t *testing.T) (*httptest.Server, observedPrices, time.Time) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mdevFixture))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(machineReadablePricingURLEnv, srv.URL)

	observed, _, at, err := FetchMachineReadablePrices(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	return srv, observed, at
}

func TestFetchMachineReadablePrices(t *testing.T) {
	_, observed, at := newObservationFixture(t)

	if len(observed) != 2 {
		t.Fatalf("providers = %d want 2", len(observed))
	}
	got, ok := observed.LookupObservation("openai", "gpt-x")
	if !ok {
		t.Fatal("openai/gpt-x missing from observations")
	}
	if got.InputPer1M == nil || *got.InputPer1M != 2.5 {
		t.Errorf("input = %v want 2.5 (the BASE tier, not the 272k-context tier)", got.InputPer1M)
	}
	if got.OutputPer1M == nil || *got.OutputPer1M != 10 {
		t.Errorf("output = %v want 10", got.OutputPer1M)
	}
	if got.Currency != "USD" {
		t.Errorf("currency = %q want USD", got.Currency)
	}
	if got.Source != "machine_readable_catalog" {
		t.Errorf("source = %q", got.Source)
	}
	if got.ObservedAt.IsZero() || !got.ObservedAt.Equal(at) {
		t.Errorf("observedAt = %v want %v", got.ObservedAt, at)
	}

	// 没有 cost 的模型不得进观察表：把它当 0 元参与对账会造出 100% 假漂移。
	if _, ok := observed.LookupObservation("openai", "unpriced-model"); ok {
		t.Error("an unpriced model entered the observation table")
	}
	// 同一模型在中转下的价必须与原厂分开。
	relay, ok := observed.LookupObservation("vapeur", "gpt-x")
	if !ok {
		t.Fatal("vapeur/gpt-x missing — provider scoping is broken")
	}
	if relay.InputPer1M == nil || *relay.InputPer1M != 3.0 {
		t.Errorf("relay input = %v want 3.0", relay.InputPer1M)
	}
}

// 观察源缺该厂商时必须明确「没观察到」，而不是静默给 0。
func TestLookupObservationMisses(t *testing.T) {
	_, observed, _ := newObservationFixture(t)
	for _, tc := range []struct{ vendor, model string }{
		{"nope", "gpt-x"},
		{"openai", "not-a-model"},
		{"", "gpt-x"},
	} {
		if _, ok := observed.LookupObservation(tc.vendor, tc.model); ok {
			t.Errorf("LookupObservation(%q,%q) claimed a hit", tc.vendor, tc.model)
		}
	}
}

func TestReconcileCatalogOnlyTouchesSSOTModels(t *testing.T) {
	_, observed, at := newObservationFixture(t)

	now := at
	ssot := func(in, out float64) BaselinePrice {
		return BaselinePrice{
			InputPer1M: &in, OutputPer1M: &out,
			Currency: "USD", Vendor: "openai",
			Source: "vendor_pricing_page", SourceURL: "https://example.invalid/p",
			FetchedAt: now.Add(-time.Hour).Format(time.RFC3339),
		}
	}
	catalog := map[string]BaselinePrice{
		"gpt-x": ssot(2.50, 10.00), // 对得上
		"gpt-y": ssot(9.00, 10.00), // 观察源里没有 gpt-y ⇒ missing
	}

	var recorded []Reconciliation
	counts, err := ReconcileCatalog(context.Background(),
		func(r Reconciliation) error { recorded = append(recorded, r); return nil },
		catalog, observed, now)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(recorded) != 2 {
		t.Fatalf("recorded %d rows, want 2 (only SSOT models)", len(recorded))
	}
	if counts[PriceVerdictMatch] != 1 {
		t.Errorf("match count = %d want 1 (counts=%v)", counts[PriceVerdictMatch], counts)
	}
	if counts[PriceVerdictMissing] != 1 {
		t.Errorf("missing count = %d want 1 (counts=%v)", counts[PriceVerdictMissing], counts)
	}
	// 观察源里有、但 SSOT 没有的模型不得被凭空建出基准价。
	for _, r := range recorded {
		if r.Model != "gpt-x" && r.Model != "gpt-y" {
			t.Errorf("reconciled a model outside the SSOT: %q", r.Model)
		}
	}
}

// 外部源抓不到时不该产生任何判词——否则「源挂了」与「价格真的不对」
// 会混成同一类信号。
func TestFetchFailureYieldsNoVerdicts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	t.Setenv(machineReadablePricingURLEnv, srv.URL)

	if _, _, _, err := FetchMachineReadablePrices(context.Background(), srv.Client()); err == nil {
		t.Fatal("a 502 from the observation source must be an error, not an empty table")
	}
}
