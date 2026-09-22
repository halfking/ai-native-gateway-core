package resolve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
)

// autoseedHarness wires a Resolver with injected ensure/lookup/resolve fakes
// and a non-nil (never dereferenced) pool so the miss path is reachable
// without a database.
type autoseedHarness struct {
	r *Resolver
	// fake provider_models lookup
	lookupModel  string
	lookupRaw    string
	lookupFound  bool
	lookupCalls  int
	ensureCalls  int
	ensureSource string
	ensureRaw    string
	ensureErr    error
	// fake resolveDB: returns missRes on the first call (the initial lookup)
	// and seededRes afterwards (the post-seed relookup).
	missCalls int
	missRes   *Resolution
	seededRes *Resolution
	seededErr error
}

func newAutoseedHarness(t *testing.T) *autoseedHarness {
	t.Helper()
	h := &autoseedHarness{
		r: NewResolver("", 60*time.Second),
	}
	h.r.dbPool = &pgxpool.Pool{}
	h.r.rawLookupFn = func(ctx context.Context, clientModel string) (string, bool, error) {
		h.lookupCalls++
		if h.lookupFound && clientModel != "" {
			return h.lookupRaw, true, nil
		}
		return "", false, nil
	}
	h.r.ensureFn = func(ctx context.Context, db modelcatalog.Querier, rawName, source string) (int, string, error) {
		h.ensureCalls++
		h.ensureRaw = rawName
		h.ensureSource = source
		if h.ensureErr != nil {
			return 0, "", h.ensureErr
		}
		return 42, "seeded-canonical", nil
	}
	h.r.resolveDBFn = func(ctx context.Context, clientModel, clientProfile string) (*Resolution, error) {
		h.missCalls++
		if h.missCalls == 1 {
			if h.missRes != nil {
				return h.missRes, nil
			}
			return nil, nil
		}
		if h.seededErr != nil {
			return nil, h.seededErr
		}
		return h.seededRes, nil
	}
	t.Cleanup(h.r.Stop)
	return h
}

func TestSameRawVariants_CasePrefixDateSuffix(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		mustHave []string
	}{
		{
			name:     "case variant keeps lowercase form",
			model:    "GPT-5-Mini",
			mustHave: []string{"gpt-5-mini"},
		},
		{
			name:     "vendor prefix stripped on client side",
			model:    "z-ai/glm-5.2",
			mustHave: []string{"glm-5.2"},
		},
		{
			name:     "date suffix keeps verbatim form alongside folded form",
			model:    "deepseek-v4-flash-260425",
			mustHave: []string{"deepseek-v4-flash-260425", "deepseek-v4-flash"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameRawVariants(tc.model)
			set := map[string]bool{}
			for _, v := range got {
				set[v] = true
			}
			for _, want := range tc.mustHave {
				if !set[want] {
					t.Fatalf("sameRawVariants(%q) = %v, missing %q", tc.model, got, want)
				}
			}
		})
	}
	if v := sameRawVariants("   "); len(v) != 0 {
		t.Fatalf("whitespace-only model should produce no variants, got %v", v)
	}
}

func TestResolve_MissWithSameRawAutoSeeds(t *testing.T) {
	h := newAutoseedHarness(t)
	h.lookupFound = true
	h.lookupRaw = "DeepSeek-V4-Flash-260425"
	h.seededRes = &Resolution{
		ClientModel:    "deepseek-v4-flash-260425",
		CanonicalName:  strPtr("deepseek-v4-flash"),
		RawModels:      []string{"deepseek-v4-flash-260425"},
		ResolutionPath: "alias",
	}

	res := h.r.Resolve(context.Background(), "deepseek-v4-flash-260425", "")
	if res.ResolutionPath != "alias" {
		t.Fatalf("expected seeded alias resolution, got %s", res.ResolutionPath)
	}
	if h.ensureCalls != 1 {
		t.Fatalf("expected exactly one ensure call, got %d", h.ensureCalls)
	}
	// The verbatim provider raw (not the client form) is what gets seeded.
	if h.ensureRaw != "DeepSeek-V4-Flash-260425" {
		t.Fatalf("expected verbatim provider raw seeded, got %q", h.ensureRaw)
	}
	if h.ensureSource != "resolve_autoseed" {
		t.Fatalf("expected resolve_autoseed source, got %q", h.ensureSource)
	}
	// Seeded resolutions are positive-cached: the repeat call must not
	// re-run the lookup or the seed.
	res2 := h.r.Resolve(context.Background(), "deepseek-v4-flash-260425", "")
	if res2.ResolutionPath != "alias" {
		t.Fatalf("expected cached seeded resolution, got %s", res2.ResolutionPath)
	}
	if h.ensureCalls != 1 || h.lookupCalls != 1 || h.missCalls != 2 {
		t.Fatalf("expected cache hit (ensure=%d lookup=%d dbCalls=%d)",
			h.ensureCalls, h.lookupCalls, h.missCalls)
	}
}

func TestMissWithAutoSeed_DebouncesWithinWindow(t *testing.T) {
	h := newAutoseedHarness(t)
	h.lookupFound = true
	h.lookupRaw = "gpt-5-mini"
	// Seed "succeeds" but the follow-up lookup still misses, so the caller
	// falls back to the negative cache either way.
	h.seededRes = nil

	key := cacheKey("gpt-5-mini", "")
	ctx := context.Background()
	if res := h.r.missWithAutoSeed(ctx, "gpt-5-mini", "", key); res != nil {
		t.Fatalf("expected nil when relookup still misses, got %+v", res)
	}
	if h.ensureCalls != 1 {
		t.Fatalf("expected one seed attempt, got %d", h.ensureCalls)
	}
	// A second attempt inside the debounce window must skip the seed even
	// though the negative cache is bypassed by calling the method directly.
	if res := h.r.missWithAutoSeed(ctx, "gpt-5-mini", "", key); res != nil {
		t.Fatalf("expected nil on debounced attempt, got %+v", res)
	}
	if h.ensureCalls != 1 {
		t.Fatalf("debounce failed: expected still one seed attempt, got %d", h.ensureCalls)
	}
	if h.lookupCalls != 1 {
		t.Fatalf("debounced attempt must not re-query provider_models, got %d lookups", h.lookupCalls)
	}
}

func TestResolve_MissWithoutSameRawStaysNegative(t *testing.T) {
	h := newAutoseedHarness(t)
	h.lookupFound = false

	res := h.r.Resolve(context.Background(), "no-such-model", "")
	if res.ResolutionPath != "direct" {
		t.Fatalf("expected passthrough, got %s", res.ResolutionPath)
	}
	if h.ensureCalls != 0 {
		t.Fatalf("seed must not run without a same-named raw, got %d calls", h.ensureCalls)
	}
	// Negative cache: DB-lookup fakes are not consulted again inside the
	// negative TTL window.
	h.r.Resolve(context.Background(), "no-such-model", "")
	if h.lookupCalls != 1 {
		t.Fatalf("expected negative cache to absorb repeat miss, got %d lookups", h.lookupCalls)
	}
}

func TestResolve_SeedErrorFallsBackToNegativeCache(t *testing.T) {
	h := newAutoseedHarness(t)
	h.lookupFound = true
	h.lookupRaw = "gpt-5-mini"
	h.ensureErr = errors.New("upsert failed")

	res := h.r.Resolve(context.Background(), "gpt-5-mini", "")
	if res.ResolutionPath != "direct" {
		t.Fatalf("expected passthrough after seed error, got %s", res.ResolutionPath)
	}
	if h.ensureCalls != 1 {
		t.Fatalf("expected the failed seed to count as the attempt, got %d", h.ensureCalls)
	}
}

func TestResolve_DBFailurePathUnchanged(t *testing.T) {
	// Regression guard: with no pool, Resolve behaves exactly as before B3.
	r := NewResolver("", 0)
	defer r.Stop()
	res := r.Resolve(context.Background(), "gpt-4o", "")
	if res.ResolutionPath != "direct" {
		t.Fatalf("expected direct, got %s", res.ResolutionPath)
	}
}

func strPtr(s string) *string { return &s }
