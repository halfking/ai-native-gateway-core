package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── parseVendorModelsBody — covers all four recognised shapes ──────────────

func TestParseVendorModelsBody_OpenAIStandard(t *testing.T) {
	body := []byte(`{"object":"list","data":[
		{"id":"gpt-4o","object":"model","owned_by":"openai"},
		{"id":"gpt-4o-mini","object":"model","owned_by":"openai"}
	]}`)
	ids, err := parseVendorModelsBody(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 2 || ids[0] != "gpt-4o" || ids[1] != "gpt-4o-mini" {
		t.Fatalf("ids = %v, want [gpt-4o gpt-4o-mini]", ids)
	}
}

func TestParseVendorModelsBody_AltModelsKey(t *testing.T) {
	body := []byte(`{"models":[
		{"id":"claude-3-5-sonnet","name":"Claude 3.5"},
		{"id":"claude-3-haiku"}
	]}`)
	ids, err := parseVendorModelsBody(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 2 || ids[0] != "claude-3-5-sonnet" || ids[1] != "claude-3-haiku" {
		t.Fatalf("ids = %v, want [claude-3-5-sonnet claude-3-haiku]", ids)
	}
}

func TestParseVendorModelsBody_BareArray(t *testing.T) {
	body := []byte(`["mimo-v2-flash","mimo-v2-pro","mimo-v2.5"]`)
	ids, err := parseVendorModelsBody(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 3 || ids[0] != "mimo-v2-flash" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestParseVendorModelsBody_ObjArrayFallback(t *testing.T) {
	body := []byte(`[{"id":"a"},{"name":"b"},{"model":"c"},{"id":""}]`)
	ids, err := parseVendorModelsBody(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Empty id should be skipped; the rest fall through to name/model.
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Fatalf("ids = %v, want [a b c]", ids)
	}
}

func TestParseVendorModelsBody_UnrecognisedReturnsError(t *testing.T) {
	body := []byte(`{"weird":{"thing":[1,2,3]}}`)
	if _, err := parseVendorModelsBody(body); err == nil {
		t.Fatal("expected error for unrecognised shape, got nil")
	}
}

func TestParseVendorModelsBody_EmptyOpenAIShapedFallsThrough(t *testing.T) {
	// {"data":[]} matches the OpenAI shape but yields zero ids; the parser
	// must NOT short-circuit on an empty data array — it should keep trying
	// the alt shapes. We feed in an alt shape inside `models` to verify the
	// fallback path.
	body := []byte(`{"data":[],"models":[{"id":"fallback-1"},{"id":"fallback-2"}]}`)
	ids, err := parseVendorModelsBody(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 2 || ids[0] != "fallback-1" {
		t.Fatalf("ids = %v, want fallback path [fallback-1 fallback-2]", ids)
	}
}

// ── extractManifestModels — manifest JSON fallback ─────────────────────────

func TestExtractManifestModels_WrappedObject(t *testing.T) {
	raw := `{"models":[{"id":"m1"},{"id":"m2"}]}`
	ids, err := extractManifestModels(&raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestExtractManifestModels_OpenAIShape(t *testing.T) {
	raw := `{"data":[{"id":"x"},{"id":"y"},{"id":"z"}]}`
	ids, err := extractManifestModels(&raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestExtractManifestModels_BareArray(t *testing.T) {
	raw := `["a","b"]`
	ids, err := extractManifestModels(&raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestExtractManifestModels_NilAndEmpty(t *testing.T) {
	if ids, err := extractManifestModels(nil); err != nil || ids != nil {
		t.Fatalf("nil manifest should give (nil,nil); got (%v,%v)", ids, err)
	}
	empty := ""
	if ids, err := extractManifestModels(&empty); err != nil || ids != nil {
		t.Fatalf("empty manifest should give (nil,nil); got (%v,%v)", ids, err)
	}
}

func TestExtractManifestModels_UnknownShapeReturnsEmpty(t *testing.T) {
	// extractManifestModels must never error — it's a fallback, so a
	// completely unrecognised JSON shape should yield (nil, nil) rather
	// than propagate an error to the caller.
	raw := `{"weird":{"thing":[1,2]}}`
	ids, err := extractManifestModels(&raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v, want nil", ids)
	}
}

// ── provider refresh state machine (in-memory tracking) ───────────────────

func TestProviderRefreshState_LazyInit(t *testing.T) {
	h := &Handler{}
	if h.refreshState != nil {
		t.Fatal("refreshState must start nil (lazy init)")
	}
	st := h.getProviderRefreshState()
	if st == nil {
		t.Fatal("getProviderRefreshState must not return nil after first call")
	}
	if st.latest == nil {
		t.Fatal("latest map must be initialised")
	}
	// Subsequent calls must return the SAME instance (no map reset).
	st2 := h.getProviderRefreshState()
	if st != st2 {
		t.Fatal("getProviderRefreshState must return a stable instance")
	}
}

func TestProviderRefresh_RecordAndGetCopy(t *testing.T) {
	h := &Handler{}
	run := &providerRefreshRun{
		RunID:      "test-run-1",
		ProviderID: 42,
		Status:     providerRefreshRunning,
	}
	h.recordProviderRefresh(42, run)

	got := h.getProviderRefresh(42)
	if got == nil {
		t.Fatal("expected a run after recordProviderRefresh, got nil")
	}
	if got.RunID != "test-run-1" {
		t.Fatalf("RunID = %q, want test-run-1", got.RunID)
	}
	if got.ProviderID != 42 {
		t.Fatalf("ProviderID = %d, want 42", got.ProviderID)
	}
	if got.Status != providerRefreshRunning {
		t.Fatalf("Status = %q, want running", got.Status)
	}

	// Mutating the returned pointer must NOT leak into the stored state —
	// the helper must hand out a defensive copy.
	got.Status = providerRefreshFailed
	got2 := h.getProviderRefresh(42)
	if got2.Status != providerRefreshRunning {
		t.Fatalf("getProviderRefresh must return a copy; store now leaked status=%q", got2.Status)
	}
}

func TestProviderRefresh_UnknownProviderReturnsNil(t *testing.T) {
	h := &Handler{}
	// Force lazy init even when we never recorded anything for the id.
	_ = h.getProviderRefreshState()
	if got := h.getProviderRefresh(9999); got != nil {
		t.Fatalf("expected nil for unknown provider, got %+v", got)
	}
}

func TestProviderRefresh_OverwriteLatestForSameProvider(t *testing.T) {
	h := &Handler{}
	h.recordProviderRefresh(7, &providerRefreshRun{RunID: "first", ProviderID: 7, Status: providerRefreshRunning})
	h.recordProviderRefresh(7, &providerRefreshRun{RunID: "second", ProviderID: 7, Status: providerRefreshSucceed})

	got := h.getProviderRefresh(7)
	if got == nil || got.RunID != "second" {
		t.Fatalf("expected latest to be overwritten, got %+v", got)
	}
	if got.Status != providerRefreshSucceed {
		t.Fatalf("Status = %q, want succeeded", got.Status)
	}
}

func TestProviderRefresh_ProvidersAreIsolated(t *testing.T) {
	h := &Handler{}
	h.recordProviderRefresh(1, &providerRefreshRun{RunID: "r1", ProviderID: 1, Status: providerRefreshRunning})
	h.recordProviderRefresh(2, &providerRefreshRun{RunID: "r2", ProviderID: 2, Status: providerRefreshFailed})

	if got := h.getProviderRefresh(1); got == nil || got.RunID != "r1" {
		t.Fatalf("provider 1 got %+v", got)
	}
	if got := h.getProviderRefresh(2); got == nil || got.RunID != "r2" {
		t.Fatalf("provider 2 got %+v", got)
	}
	if got := h.getProviderRefresh(3); got != nil {
		t.Fatalf("provider 3 must be nil, got %+v", got)
	}
}

// ── startRefreshProviderModels — error paths that don't touch the DB ──────

func TestStartRefreshProviderModels_NoDatabase(t *testing.T) {
	h := &Handler{db: nil}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/providers/1/refresh-models", nil)
	h.startRefreshProviderModels(rec, req, 1)

	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503 when db nil", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "database not configured") {
		t.Fatalf("body = %q, want database-unavailable message", rec.Body.String())
	}
}

// ── fetchVendorModels — HTTP layer against a stub upstream ────────────────

func testOpenAICred() credentialRowLite {
	return credentialRowLite{protocol: "openai-completions", catalogCode: "test"}
}

func TestFetchVendorModels_HappyPath(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want Bearer test-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
	}))
	defer srv.Close()

	ids, err := h.fetchVendorModels(context.Background(), srv.URL+"/v1/models", testOpenAICred(), "test-key")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Fatalf("ids = %v, want [m1 m2]", ids)
	}
}

func TestFetchVendorModels_AuthRejected(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := h.fetchVendorModels(context.Background(), srv.URL+"/v1/models", testOpenAICred(), "bad-key")
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want it to mention 401", err)
	}
}

func TestFetchVendorModels_5xxBubblesUp(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := h.fetchVendorModels(context.Background(), srv.URL+"/v1/models", testOpenAICred(), "k")
	if err == nil {
		t.Fatal("expected error on 502, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want it to mention 502", err)
	}
}

func TestFetchVendorModelsFromURLs_FirstCandidateFailsSecondSucceeds(t *testing.T) {
	h := &Handler{}
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"candidate-model"}]}`))
	}))
	defer srv.Close()

	ids, err := h.fetchVendorModelsFromURLs(context.Background(), []string{
		srv.URL + "/bad/models",
		srv.URL + "/v1/models",
	}, testOpenAICred(), "test-key")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 1 || ids[0] != "candidate-model" {
		t.Fatalf("ids = %v, want [candidate-model]", ids)
	}
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 candidate attempts", hits)
	}
}

func strPtr(s string) *string { return &s }

// MiniMax catalog uses discovery_strategy=manifest with models_endpoint_template=/models.
// Manual refresh (forceAPI=true) must call the live API, not the stale manifest seed —
// but catalog manifest entries that the live list omits are merged in so known-but-
// unlisted models still surface. Here the live API answers with m2.7/m2.5 while the
// manifest holds two legacy seeds; the merge appends them, and source reports the
// composition.
func TestResolveModelsForCredential_ManifestStrategyForceAPIUsesLiveAPI(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"minimax-m2.7"},{"id":"minimax-m2.5"}]}`))
	}))
	defer srv.Close()

	tpl := "/v1/models"
	manifest := `{"models":[{"id":"MiniMax-Text-01"},{"id":"abab6.5s-chat"}]}`
	cred := credentialRowLite{
		baseURL:            srv.URL,
		protocol:           "openai-completions",
		discoveryStrategy:  "manifest",
		modelsEndpointTpl:  &tpl,
		modelsManifestJSON: &manifest,
	}

	ids, source, err := h.resolveModelsForCredential(context.Background(), cred, "test-key", true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if source != "api+manifest" {
		t.Fatalf("source = %q, want api+manifest", source)
	}
	if len(ids) != 4 || ids[0] != "minimax-m2.7" || ids[1] != "minimax-m2.5" {
		t.Fatalf("ids = %v, want live API models first then manifest seeds", ids)
	}
	if ids[2] != "MiniMax-Text-01" || ids[3] != "abab6.5s-chat" {
		t.Fatalf("merged manifest ids = %v, want legacy seeds appended", ids[2:])
	}
}

// glm-5.2 regression: zhipu publishes glm-5.2 (callable) but their /models
// endpoint still lists up to glm-5.1. The catalog manifest registers glm-5.2
// as "known but unlisted"; a manual refresh must surface it via the merge.
func TestResolveModelsForCredential_UnlistedGlm52MergedFromManifest(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-4.6"},{"id":"glm-5"},{"id":"glm-5.1"}]}`))
	}))
	defer srv.Close()

	tpl := "/models"
	// Manifest lists the full known GLM lineup including the unlisted glm-5.2.
	manifest := `{"models":[{"id":"glm-4.6"},{"id":"glm-5"},{"id":"glm-5.1"},{"id":"glm-5.2"}]}`
	cred := credentialRowLite{
		baseURL:            srv.URL,
		protocol:           "openai-completions",
		discoveryStrategy:  "manifest",
		modelsEndpointTpl:  &tpl,
		modelsManifestJSON: &manifest,
	}

	ids, source, err := h.resolveModelsForCredential(context.Background(), cred, "test-key", true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if source != "api+manifest" {
		t.Fatalf("source = %q, want api+manifest", source)
	}
	found := false
	for _, id := range ids {
		if id == "glm-5.2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ids = %v, want glm-5.2 merged in from manifest", ids)
	}
	// glm-4.6 / glm-5 / glm-5.1 already in live list must NOT be duplicated.
	seen := make(map[string]int)
	for _, id := range ids {
		seen[id]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("model %s appeared %d times, want dedup (ids=%v)", id, n, ids)
		}
	}
}

// mergeModelIDs — dedup + order preservation for the live/manifest merge.
func TestMergeModelIDs_AppendsMissing(t *testing.T) {
	live := []string{"glm-4.6", "glm-5", "glm-5.1"}
	manifest := []string{"glm-5", "glm-5.2"} // glm-5 dup, glm-5.2 new
	got := mergeModelIDs(live, manifest)
	want := []string{"glm-4.6", "glm-5", "glm-5.1", "glm-5.2"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMergeModelIDs_CaseInsensitiveDedup(t *testing.T) {
	live := []string{"GPT-4o"}
	manifest := []string{"gpt-4o", "gpt-4o-mini"}
	got := mergeModelIDs(live, manifest)
	if len(got) != 2 || got[0] != "GPT-4o" || got[1] != "gpt-4o-mini" {
		t.Fatalf("got %v, want [GPT-4o gpt-4o-mini]", got)
	}
}

func TestMergeModelIDs_EmptyManifestReturnsLiveUntouched(t *testing.T) {
	live := []string{"a", "b"}
	if got := mergeModelIDs(live, nil); len(got) != 2 {
		t.Fatalf("got %v, want live unchanged", got)
	}
}

func TestResolveModelsForCredential_ManifestStrategyScheduledUsesManifestOnly(t *testing.T) {
	h := &Handler{}
	manifest := `{"models":[{"id":"seed-a"},{"id":"seed-b"}]}`
	cred := credentialRowLite{
		discoveryStrategy:  "manifest_only",
		modelsManifestJSON: &manifest,
	}

	ids, source, err := h.resolveModelsForCredential(context.Background(), cred, "", false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if source != "manifest_only" {
		t.Fatalf("source = %q, want manifest_only", source)
	}
	if len(ids) != 2 || ids[0] != "seed-a" {
		t.Fatalf("ids = %v", ids)
	}
}
