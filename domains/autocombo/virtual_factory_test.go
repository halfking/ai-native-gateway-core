package autocombo

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/freeresource"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func defaultWeightsJSON(t *testing.T) json.RawMessage {
	weights := ScoringWeights{
		HealthScore:    0.4,
		LatencyP95:     0.3,
		QuotaRemaining: 0.0,
		Cost:           0.1,
		TaskFit:        0.1,
		TierAffinity:   0.1,
	}
	raw, err := json.Marshal(weights)
	if err != nil {
		t.Fatalf("marshal weights: %v", err)
	}
	return raw
}

func baseSpec(t *testing.T) *AutoComboSpec {
	return &AutoComboSpec{
		ComboName:          "auto/free",
		Variant:            VariantCheap,
		ToSFilter:          []string{"ok", "caution"},
		ProviderAllowlist:  nil,
		ProviderDenylist:   nil,
		MaxCandidates:      0,
		ScoringWeightsJSON: defaultWeightsJSON(t),
		TenantID:           "default",
	}
}

func cand(credID int, code, model, billingMode string, latency int, price float64, success float64) provider.Candidate {
	c := provider.Candidate{
		CredentialID:     credID,
		ProviderID:       credID,
		CatalogCode:      code,
		RawModel:         model,
		StandardizedName: model,
		BillingMode:      billingMode,
		P95LatencyMs:     latency,
		SuccessRate:      success,
		BaseURL:          "https://example.invalid",
		Protocol:         "openai-completions",
	}
	if price > 0 {
		in := price
		out := price * 2
		c.PriceInPer1M = &in
		c.PriceOutPer1M = &out
	}
	return c
}

func catalogFixtures() []CatalogEntry {
	return []CatalogEntry{
		{ProviderCode: "openrouter", ModelID: "openai/gpt-3.5-turbo:free", FreeType: "recurring-daily", ToSVerdict: "ok"},
		{ProviderCode: "groq", ModelID: "llama-3.3-70b", FreeType: "recurring-monthly", ToSVerdict: "caution"},
		{ProviderCode: "blocked", ModelID: "blocked-model", FreeType: "recurring-daily", ToSVerdict: "avoid"},
	}
}

func TestVirtualFactory_FilterCandidates_AllowlistAndCatalog(t *testing.T) {
	vf := &VirtualFactory{}
	spec := baseSpec(t)
	spec.ProviderAllowlist = []string{"openrouter", "groq"}

	candidates := []provider.Candidate{
		cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9),
		cand(2, "groq", "llama-3.3-70b", "free", 100, 0, 0.95),
		cand(3, "blocked", "blocked-model", "free", 500, 0, 0.8),
		cand(4, "openrouter", "some-other-model", "free", 800, 0, 0.7),
	}
	filtered := vf.filterCandidates(candidates, catalogFixtures(), spec)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 candidates after filter, got %d", len(filtered))
	}
	seen := map[string]bool{}
	for _, c := range filtered {
		seen[c.CatalogCode+":"+c.StandardizedName] = true
	}
	if !seen["openrouter:openai/gpt-3.5-turbo:free"] {
		t.Errorf("expected openrouter gpt-3.5 in filtered candidates")
	}
	if !seen["groq:llama-3.3-70b"] {
		t.Errorf("expected groq llama-3.3 in filtered candidates")
	}
}

func TestVirtualFactory_FilterCandidates_DenylistAndToS(t *testing.T) {
	vf := &VirtualFactory{}
	spec := baseSpec(t)
	spec.ProviderDenylist = []string{"blocked"}

	candidates := []provider.Candidate{
		cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9),
		cand(2, "groq", "llama-3.3-70b", "free", 100, 0, 0.95),
		cand(3, "blocked", "blocked-model", "free", 500, 0, 0.8),
	}
	filtered := vf.filterCandidates(candidates, catalogFixtures(), spec)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(filtered))
	}
	for _, c := range filtered {
		if c.CatalogCode == "blocked" {
			t.Errorf("blocked provider should be filtered")
		}
	}
}

func TestVirtualFactory_FilterCandidates_VariantHint(t *testing.T) {
	vf := &VirtualFactory{}
	spec := baseSpec(t)
	spec.Variant = VariantCoding

	candidates := []provider.Candidate{
		cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9),
		cand(2, "groq", "llama-3.3-70b", "free", 100, 0, 0.95),
	}
	filtered := vf.filterCandidates(candidates, catalogFixtures(), spec)
	if len(filtered) != 0 {
		t.Fatalf("expected variant=coding to drop non-coding candidates, got %d", len(filtered))
	}

	spec.Variant = VariantCheap
	catalogWithCoder := append(catalogFixtures(), CatalogEntry{
		ProviderCode: "groq", ModelID: "starcoder", FreeType: "recurring-monthly", ToSVerdict: "ok",
	})
	coders := []provider.Candidate{
		cand(2, "groq", "starcoder", "free", 100, 0, 0.95),
	}
	got := vf.filterCandidates(coders, catalogWithCoder, spec)
	if len(got) != 1 {
		t.Fatalf("expected coder candidate to remain, got %d", len(got))
	}
}

func TestVirtualFactory_FilterCandidates_Deduplicates(t *testing.T) {
	vf := &VirtualFactory{}
	spec := baseSpec(t)
	candidates := []provider.Candidate{
		cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9),
		cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9),
	}
	got := vf.filterCandidates(candidates, catalogFixtures(), spec)
	if len(got) != 1 {
		t.Fatalf("expected deduplication, got %d", len(got))
	}
}

func TestVirtualFactory_PreflightQuota_FilterDropsExhausted(t *testing.T) {
	tracker := &fakeQuotaTracker{
		results: map[string]bool{
			preflightKey(1, "openrouter", "openai/gpt-3.5-turbo:free"): true,
			preflightKey(2, "groq", "llama-3.3-70b"):                   false,
		},
	}
	vf := NewVirtualFactoryWith(nil, tracker)

	candidates := []provider.Candidate{
		cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9),
		cand(2, "groq", "llama-3.3-70b", "free", 100, 0, 0.95),
	}
	got, _ := vf.preflightQuota(context.Background(), candidates, "default")
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate after preflight, got %d", len(got))
	}
	if got[0].CatalogCode != "openrouter" {
		t.Errorf("expected openrouter to remain, got %s", got[0].CatalogCode)
	}
}

func TestVirtualFactory_PreflightQuota_NilPreflighterPassesThrough(t *testing.T) {
	vf := NewVirtualFactoryWith(nil, nil)
	candidates := []provider.Candidate{cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9)}
	got, _ := vf.preflightQuota(context.Background(), candidates, "default")
	if len(got) != 1 {
		t.Fatalf("expected nil prefighter to pass through, got %d", len(got))
	}
}

func TestVirtualFactory_PreflightQuota_SkipsNonFree(t *testing.T) {
	tracker := &fakeQuotaTracker{results: map[string]bool{}}
	vf := NewVirtualFactoryWith(nil, tracker)
	candidates := []provider.Candidate{
		cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "per_token", 300, 1, 0.9),
	}
	got, _ := vf.preflightQuota(context.Background(), candidates, "default")
	if len(got) != 1 {
		t.Fatalf("non-free billing should bypass quota gate, got %d", len(got))
	}
	if tracker.calls != 0 {
		t.Errorf("preflight should not be called for non-free billing, got %d calls", tracker.calls)
	}
}

func TestVirtualFactory_BuildFromCandidates_HappyPath(t *testing.T) {
	tracker := &fakeQuotaTracker{results: map[string]bool{
		preflightKey(1, "openrouter", "openai/gpt-3.5-turbo:free"): true,
		preflightKey(2, "groq", "llama-3.3-70b"):                   true,
	}}
	vf := NewVirtualFactoryWith(nil, tracker)
	vf.loadCatalogFn = func(ctx context.Context, tenantID string, spec *AutoComboSpec) ([]CatalogEntry, error) {
		return catalogFixtures(), nil
	}

	candidates := []provider.Candidate{
		cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9),
		cand(2, "groq", "llama-3.3-70b", "free", 100, 0, 0.95),
		cand(3, "blocked", "blocked-model", "free", 500, 0, 0.8),
	}
	got, err := vf.BuildFromCandidates(context.Background(), baseSpec(t), candidates, "default")
	if err != nil {
		t.Fatalf("BuildFromCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	if got[0].CatalogCode != "groq" {
		t.Errorf("expected groq first after sort, got %s", got[0].CatalogCode)
	}
}

func TestVirtualFactory_BuildFromCandidates_Empty(t *testing.T) {
	vf := NewVirtualFactoryWith(nil, &fakeQuotaTracker{})
	vf.loadCatalogFn = func(ctx context.Context, tenantID string, spec *AutoComboSpec) ([]CatalogEntry, error) {
		return catalogFixtures(), nil
	}
	got, err := vf.BuildFromCandidates(context.Background(), baseSpec(t), nil, "default")
	if err != nil {
		t.Fatalf("BuildFromCandidates: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result, got %v", got)
	}
}

func TestVirtualFactory_BuildFromCandidates_NilSpecRejected(t *testing.T) {
	vf := NewVirtualFactoryWith(nil, &fakeQuotaTracker{})
	if _, err := vf.BuildFromCandidates(context.Background(), nil, nil, "default"); err == nil {
		t.Errorf("expected error for nil spec")
	}
}

func TestVirtualFactory_BuildFromCandidates_NoDB_NoCatalog(t *testing.T) {
	// 工厂没有 DB 且测试钩子也没设置时, 返回 nil 而不是 panic.
	vf := NewVirtualFactoryWith(nil, &fakeQuotaTracker{})
	candidates := []provider.Candidate{cand(1, "openrouter", "openai/gpt-3.5-turbo:free", "free", 300, 0, 0.9)}
	got, err := vf.BuildFromCandidates(context.Background(), baseSpec(t), candidates, "default")
	if err != nil {
		t.Fatalf("BuildFromCandidates: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil candidates without DB, got %d", len(got))
	}
}

func TestEngine_SortProviderCandidates_TieBreakDeterministic(t *testing.T) {
	weights := ScoringWeights{
		HealthScore:  0.4,
		LatencyP95:   0.3,
		Cost:         0.1,
		TierAffinity: 0.2,
	}
	raw, _ := json.Marshal(weights)
	engine, err := NewEngine(raw)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	candidates := []provider.Candidate{
		cand(20, "openrouter", "model-a", "free", 200, 0, 0.9),
		cand(10, "openrouter", "model-b", "free", 200, 0, 0.9),
		cand(10, "openrouter", "model-c", "free", 200, 0, 0.9),
	}
	sorted := engine.sortCandidates(candidates, nil)
	if len(sorted) != 3 {
		t.Fatalf("expected 3 sorted candidates, got %d", len(sorted))
	}
	if sorted[0].ProviderID != 10 || sorted[0].RawModel != "model-b" {
		t.Errorf("unexpected sort order: %+v", sorted[0])
	}
}

func TestEngine_SortProviderCandidates_PrefersLowLatency(t *testing.T) {
	weights := ScoringWeights{
		HealthScore: 0.0,
		LatencyP95:  1.0,
	}
	raw, _ := json.Marshal(weights)
	engine, err := NewEngine(raw)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	candidates := []provider.Candidate{
		cand(1, "openrouter", "slow", "free", 1000, 0, 0.9),
		cand(2, "openrouter", "fast", "free", 50, 0, 0.9),
	}
	sorted := engine.sortCandidates(candidates, nil)
	if sorted[0].RawModel != "fast" {
		t.Errorf("expected fast first, got %s", sorted[0].RawModel)
	}
}

// TestEngine_SortCandidates_QuotaInfluencesOrdering (P3) verifies the previously-
// dead QuotaRemaining dimension now drives ordering: with equal health/latency/
// cost, a near-full credential (PercentUsed=0.1) must outrank a near-exhausted
// one (PercentUsed=0.9). Before P3 the formula had no quota term, so this
// weight (0.25, the free-tier default) multiplied into nothing.
func TestEngine_SortCandidates_QuotaInfluencesOrdering(t *testing.T) {
	weights := ScoringWeights{
		HealthScore:    0.25,
		LatencyP95:     0.2,
		QuotaRemaining: 0.25, // the dimension under test
		Cost:           0.1,
		TaskFit:        0.1,
		TierAffinity:   0.1,
	}
	raw, _ := json.Marshal(weights)
	engine, err := NewEngine(raw)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Two identical candidates except credential ID; quota snapshot makes
	// credID=1 (10% used) outrank credID=2 (90% used).
	candidates := []provider.Candidate{
		cand(1, "openrouter", "model-a", "free", 200, 0, 0.9),
		cand(2, "openrouter", "model-b", "free", 200, 0, 0.9),
	}
	quotaSnap := map[int]fetchedQuota{
		1: {PercentUsed: 0.1}, // near full
		2: {PercentUsed: 0.9}, // near exhausted
	}
	sorted := engine.sortCandidates(candidates, quotaSnap)
	if sorted[0].CredentialID != 1 {
		t.Fatalf("expected near-full credential (id=1) first, got id=%d", sorted[0].CredentialID)
	}
	if sorted[1].CredentialID != 2 {
		t.Fatalf("expected near-exhausted credential (id=2) second, got id=%d", sorted[1].CredentialID)
	}
}

// TestEngine_SortCandidates_QuotaSnapNilIsNeutral (P3) verifies that when no
// quota snapshot is available (legacy deployments / nil quotaFetcher), the
// quota term degrades to neutral 0.5 and does NOT distort ordering — i.e. the
// change is backwards-compatible.
func TestEngine_SortCandidates_QuotaSnapNilIsNeutral(t *testing.T) {
	weights := ScoringWeights{
		HealthScore:    0.3,
		QuotaRemaining: 0.25,
		Cost:           0.15,
		TaskFit:        0.15,
		TierAffinity:   0.15,
	}
	raw, _ := json.Marshal(weights)
	engine, err := NewEngine(raw)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// credID=2 has worse latency; without quota distortion it should rank
	// behind credID=1 (faster). A non-neutral quota term could flip this.
	candidates := []provider.Candidate{
		cand(1, "openrouter", "fast", "free", 50, 0, 0.9),
		cand(2, "openrouter", "slow", "free", 1000, 0, 0.9),
	}
	sorted := engine.sortCandidates(candidates, nil)
	if sorted[0].RawModel != "fast" {
		t.Fatalf("nil quotaSnap should keep latency-driven order (fast first), got %s", sorted[0].RawModel)
	}
}

// TestComputeResetWindowAffinity (P3) verifies the reset-window scoring formula
// mirrors OmniRoute combo/quotaScoring.ts:304-311.
func TestComputeResetWindowAffinity(t *testing.T) {
	now := time.Now()

	// Already reset (past) → full runway.
	past := now.Add(-1 * time.Hour)
	if got := computeResetWindowAffinity(&past, resetWindowHorizon); got != 1.0 {
		t.Fatalf("past reset → want 1.0, got %v", got)
	}

	// Unknown (nil) → neutral 0.5.
	if got := computeResetWindowAffinity(nil, resetWindowHorizon); got != 0.5 {
		t.Fatalf("nil reset → want 0.5, got %v", got)
	}

	// Resets soon → high score (just under 1).
	soon := now.Add(1 * time.Hour) // 1h / 168h horizon ≈ 0.006
	if got := computeResetWindowAffinity(&soon, resetWindowHorizon); got < 0.98 {
		t.Fatalf("resets in 1h → want ≥0.98, got %v", got)
	}

	// Resets far in the future → near 0.
	far := now.Add(30 * 24 * time.Hour) // 30d >> 7d horizon
	if got := computeResetWindowAffinity(&far, resetWindowHorizon); got != 0 {
		t.Fatalf("resets in 30d → want 0, got %v", got)
	}
}

func TestIsFreeBilling(t *testing.T) {
	cases := map[string]bool{
		// 已知 free 模式 — 走配额门.
		"free":               true,
		"FREE":               true,
		"keyless":            true,
		"token_plan":         true,
		"code_plan":          true,
		"tier1":              true,
		"recurring-daily":    true,
		"recurring-monthly":  true,
		"recurring-credit":   true,
		"recurring-uncapped": true,
		"one-time-initial":   true,
		// 已知 paid 模式 — 跳过配额门.
		"per_token": false,
		"paid":      false,
		"premium":   false,
		"pro":       false,
		// round 3 H6: 空 / 未知模式不再默认 free (避免 misconfig 漏到 free 池).
		"":      false,
		"junk":  false,
		"OTHER": false,
	}
	for mode, want := range cases {
		if got := isFreeBilling(mode); got != want {
			t.Errorf("isFreeBilling(%q) = %v, want %v", mode, got, want)
		}
	}
}

// ----- test doubles -----

type fakeQuotaTracker struct {
	results map[string]bool
	calls   int
	err     error
}

func (f *fakeQuotaTracker) Preflight(ctx context.Context, req freeresource.PreflightRequest) (bool, error) {
	f.calls++
	if f.err != nil {
		return false, f.err
	}
	if f.results == nil {
		return true, nil
	}
	ok, found := f.results[preflightKeyInt64(req.CredentialID, req.ProviderCode, req.ModelID)]
	if !found {
		return true, nil
	}
	return ok, nil
}

func preflightKeyInt64(credID int64, code, model string) string {
	return code + "|" + model + "|" + itoa64(credID)
}

func preflightKey(credID int, code, model string) string {
	return preflightKeyInt64(int64(credID), code, model)
}

func itoa64(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = digits[n%10]
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func TestFakeQuotaTracker_PassesThroughWhenUnknown(t *testing.T) {
	tracker := &fakeQuotaTracker{results: nil}
	ok, err := tracker.Preflight(context.Background(), freeresource.PreflightRequest{
		CredentialID: 1,
		ProviderCode: "openrouter",
		ModelID:      "m",
	})
	if err != nil || !ok {
		t.Errorf("expected pass-through, got ok=%v err=%v", ok, err)
	}
}

func TestFakeQuotaTracker_PropagatesErrors(t *testing.T) {
	tracker := &fakeQuotaTracker{err: errors.New("boom")}
	ok, err := tracker.Preflight(context.Background(), freeresource.PreflightRequest{CredentialID: 1})
	if err == nil {
		t.Errorf("expected error")
	}
	if ok {
		t.Errorf("expected ok=false on error")
	}
}
