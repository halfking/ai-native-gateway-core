package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// headerStub implements httpHeaderGetter for the task-type resolution tests.
type headerStub map[string]string

func (h headerStub) Header(key string) string { return h[key] }

// TestResolveTaskTypeForAlternatives_HeaderWins verifies tier 1 of the
// fallback chain: an explicit X-Gw-Task-Hint is authoritative, because the
// client knows its own intent better than any inference we can make.
func TestResolveTaskTypeForAlternatives_HeaderWins(t *testing.T) {
	got := resolveTaskTypeForAlternatives(
		context.Background(),
		headerStub{autoTaskHintHeader: "code"},
		nil,
		autoroute.ClassificationSignals{},
	)
	if got != "code" {
		t.Errorf("task type = %q, want %q (X-Gw-Task-Hint must win)", got, "code")
	}
}

// TestResolveTaskTypeForAlternatives_RejectsUnknownHint pins that a typo'd or
// hostile hint is ignored rather than trusted.
//
// This matters beyond input hygiene: tier 1 of the suggestion query matches
// task_default_routing on exact task_type equality, so an unrecognized value
// would silently return zero task-matched rows and the client would get a
// worse list than if no hint had been sent at all.
func TestResolveTaskTypeForAlternatives_RejectsUnknownHint(t *testing.T) {
	got := resolveTaskTypeForAlternatives(
		context.Background(),
		headerStub{autoTaskHintHeader: "not-a-real-task-type"},
		nil,
		// Empty signals so the inline heuristic has nothing to work with;
		// any non-empty result would have to come from the bad hint.
		autoroute.ClassificationSignals{},
	)
	if got == "not-a-real-task-type" {
		t.Error("unknown X-Gw-Task-Hint was trusted; it must be ignored so the " +
			"task_default_routing lookup does not silently match nothing")
	}
}

// TestResolveTaskTypeForAlternatives_SessionCacheSecond verifies tier 2: with
// no header, a task type already cached for this session is used. This is what
// makes suggestions task-aware for a session whose first request used
// model="auto" and was classified then.
func TestResolveTaskTypeForAlternatives_SessionCacheSecond(t *testing.T) {
	cache := autoroute.NewSessionIntentCache(10 * 60 * 1e9) // 10m
	cache.Put("sess-abc", autoroute.CachedIntent{TaskType: autoroute.TaskReasoning})

	got := resolveTaskTypeForAlternatives(
		context.Background(),
		headerStub{"X-Gw-Session-Id": "sess-abc"},
		cache,
		autoroute.ClassificationSignals{},
	)
	if got != string(autoroute.TaskReasoning) {
		t.Errorf("task type = %q, want %q (session intent cache is tier 2)",
			got, autoroute.TaskReasoning)
	}
}

// TestResolveTaskTypeForAlternatives_InlineHeuristicLast verifies tier 3, the
// tier that makes this feature work at all for ordinary requests.
//
// autoroute only classifies when model == "auto", so a request naming an
// explicit model has no task type on file and no session cache entry. Without
// inline classification, task-matched suggestions would never fire for exactly
// the requests that need them.
func TestResolveTaskTypeForAlternatives_InlineHeuristicLast(t *testing.T) {
	got := resolveTaskTypeForAlternatives(
		context.Background(),
		headerStub{}, // no hint, no session id
		nil,          // no cache
		autoroute.ClassificationSignals{
			LastUserPrompt: "refactor this function and fix the failing unit test",
			HasCodeBlock:    true,
		},
	)
	if got == "" {
		t.Error("inline heuristic produced no task type; tier 3 is what makes " +
			"suggestions task-aware for non-auto requests")
	}
}

// TestFindModelAlternatives_NilFinderDegradesCleanly pins the graceful-
// degradation contract. A deployment that has not wired the finder (or has no
// DB) must still get the historical 503 rather than a panic on an
// already-failing request.
func TestFindModelAlternatives_NilFinderDegradesCleanly(t *testing.T) {
	h := &ChatHandler{} // altFinder deliberately nil
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))

	got := h.findModelAlternatives(r, &chatRequestBody{}, []byte("{}"), "gpt-5.6-luna", nil)

	if got.RequestedModel != "gpt-5.6-luna" {
		t.Errorf("RequestedModel = %q, want %q", got.RequestedModel, "gpt-5.6-luna")
	}
	if got.Alternatives == nil {
		t.Error("Alternatives is nil; must be an empty slice so the JSON body " +
			"carries [] rather than null")
	}
	if len(got.Alternatives) != 0 {
		t.Errorf("got %d alternatives from a nil finder, want 0", len(got.Alternatives))
	}
}

// TestFind_NilReceiverAndNilDB verifies the two other no-op paths return a
// well-formed empty result instead of panicking.
func TestFind_NilReceiverAndNilDB(t *testing.T) {
	var nilFinder *ModelAlternativesFinder
	got := nilFinder.Find(context.Background(), "m", "default", "code")
	if len(got.Alternatives) != 0 || got.RequestedModel != "m" {
		t.Errorf("nil receiver produced %+v", got)
	}

	empty := &ModelAlternativesFinder{} // db == nil
	got = empty.Find(context.Background(), "m", "default", "code")
	if len(got.Alternatives) != 0 {
		t.Errorf("nil db produced %d alternatives", len(got.Alternatives))
	}
}

// TestWriteNoCandidateWithAlternatives_OmitsEmptyList pins backward
// compatibility: with nothing to suggest, the response must be byte-identical
// in shape to the historical bare 503, with no "alternatives" key at all.
//
// An empty array would be defensible, but the absent field guarantees that a
// client which cannot use the field sees no change whatsoever.
func TestWriteNoCandidateWithAlternatives_OmitsEmptyList(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	writeNoCandidateWithAlternatives(context.Background(), w, r, "req-1", "gpt-5.6-luna",
		ModelAlternativesResult{RequestedModel: "gpt-5.6-luna", Alternatives: nil})

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if _, present := body.Error["alternatives"]; present {
		t.Error(`"alternatives" must be absent when there is nothing to offer, ` +
			`so the response stays identical for clients that ignore it`)
	}
	if body.Error["code"] != "no_candidate" {
		t.Errorf("code = %v, want no_candidate", body.Error["code"])
	}
}

// TestWriteNoCandidateWithAlternatives_OpenAIEnvelope verifies the populated
// OpenAI-protocol shape, including that alternatives ride INSIDE the error
// object (where clients already look) rather than at the top level.
func TestWriteNoCandidateWithAlternatives_OpenAIEnvelope(t *testing.T) {
	ctxWindow := 200000
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	writeNoCandidateWithAlternatives(context.Background(), w, r, "req-2", "gpt-5.6-luna",
		ModelAlternativesResult{
			RequestedModel: "gpt-5.6-luna",
			TaskType:       "code",
			Alternatives: []ModelAlternative{{
				Model:         "claude-sonnet-4-6",
				DisplayName:   "Claude Sonnet 4.6",
				Family:        "claude",
				ContextWindow: &ctxWindow,
				Featured:      true,
				Reason:        "task_match",
			}},
		})

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	var body struct {
		Error struct {
			Code         string                  `json:"code"`
			Kind         string                  `json:"kind"`
			Alternatives ModelAlternativesResult `json:"alternatives"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}

	if body.Error.Code != "no_candidate" || body.Error.Kind != "no_candidate" {
		t.Errorf("code/kind = %q/%q, want no_candidate/no_candidate",
			body.Error.Code, body.Error.Kind)
	}
	alts := body.Error.Alternatives
	if alts.TaskType != "code" {
		t.Errorf("task_type = %q, want code — the client needs it to explain "+
			"why these models were offered", alts.TaskType)
	}
	if len(alts.Alternatives) != 1 {
		t.Fatalf("got %d alternatives, want 1", len(alts.Alternatives))
	}
	first := alts.Alternatives[0]
	if first.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q", first.Model)
	}
	if first.Reason != "task_match" {
		t.Errorf("reason = %q, want task_match", first.Reason)
	}
	if first.ContextWindow == nil || *first.ContextWindow != 200000 {
		t.Errorf("context_window = %v, want 200000", first.ContextWindow)
	}
	if !first.Featured {
		t.Error("featured = false, want true")
	}
}

// TestWriteNoCandidateWithAlternatives_AnthropicEnvelope pins the
// protocol-aware shape. Before this change the zero-candidate exit sent the
// OpenAI envelope to /v1/messages callers, so an Anthropic SDK saw a body it
// does not model. Anthropic requires {"type":"error","error":{...}} with
// error.type drawn from a closed set.
func TestWriteNoCandidateWithAlternatives_AnthropicEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	writeNoCandidateWithAlternatives(context.Background(), w, r, "req-3", "claude-opus-4-8",
		ModelAlternativesResult{
			RequestedModel: "claude-opus-4-8",
			Alternatives:   []ModelAlternative{{Model: "claude-sonnet-4-6", Reason: "featured"}},
		})

	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type         string                  `json:"type"`
			Message      string                  `json:"message"`
			Alternatives ModelAlternativesResult `json:"alternatives"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, w.Body.String())
	}

	if body.Type != "error" {
		t.Errorf(`top-level type = %q, want "error" (Anthropic envelope)`, body.Type)
	}
	if body.Error.Type == "" {
		t.Error("error.type empty; Anthropic SDKs type-check this field")
	}
	if len(body.Error.Alternatives.Alternatives) != 1 {
		t.Errorf("alternatives not carried into the Anthropic envelope: %+v",
			body.Error.Alternatives)
	}
}

// TestIsKnownTaskType_MatchesAutorouteRegistry keeps hint validation in sync
// with autoroute rather than a hardcoded copy, so a task type added there is
// accepted here without a code change.
func TestIsKnownTaskType_MatchesAutorouteRegistry(t *testing.T) {
	if len(autoroute.AllTaskTypes) == 0 {
		t.Fatal("autoroute.AllTaskTypes is empty; the registry moved")
	}
	for _, tt := range autoroute.AllTaskTypes {
		if !isKnownTaskType(string(tt)) {
			t.Errorf("isKnownTaskType(%q) = false, but it is in AllTaskTypes", tt)
		}
	}
	for _, bad := range []string{"", "CODE", "code ", "reasoningx", "'; DROP TABLE"} {
		if isKnownTaskType(bad) {
			t.Errorf("isKnownTaskType(%q) = true, want false", bad)
		}
	}
}

// TestAlternativesSQL_UsesRoutableViewNotStaticCatalog is the correctness
// invariant of the whole feature, asserted against the query text because the
// alternative — suggesting an unreachable model — is invisible in unit tests
// but immediately wrong in production.
//
// The admin available-models catalog gates only on mo.available / c.status /
// p.enabled and never consults v_routable_credential_models. A model whose
// only credentials are quota-exhausted, cooling, or in probe backoff still
// looks "available" there. Offering such a model would walk the client
// straight into a second failure, which is the exact situation this feature
// exists to avoid.
func TestAlternativesSQL_UsesRoutableViewNotStaticCatalog(t *testing.T) {
	if !strings.Contains(alternativesSQL, "v_routable_credential_models") {
		t.Error("alternatives query does not consult v_routable_credential_models; " +
			"it would offer models that have no reachable node right now")
	}
	if !strings.Contains(alternativesSQL, "is_routable = TRUE") {
		t.Error("alternatives query does not filter on is_routable = TRUE")
	}

	// The requested model must be excluded: re-offering the model that just
	// failed is worse than offering nothing.
	if !strings.Contains(alternativesSQL, "mc.canonical_name <> $1") {
		t.Error("alternatives query does not exclude the requested model")
	}

	// Tenant scoping, so one tenant cannot enumerate another's private models.
	if !strings.Contains(alternativesSQL, "v.tenant_id = $2") {
		t.Error("alternatives query is not tenant-scoped")
	}

	// request_logs stores the standardized name in canonical_model (migration
	// 458). canonical_name does not exist there, and a silent typo would make
	// the popularity tier match nothing.
	if strings.Contains(alternativesSQL, "u.canonical_name") {
		t.Error("popularity CTE joins on canonical_name; request_logs uses canonical_model")
	}
}

// TestAlternativesSQL_NoGoComments guards the specific defect that took routing
// down on 2026-08-08: a Go // comment inside a SQL literal. The repo-wide guard
// in internal/sqlguard covers this too; this local assertion keeps the failure
// adjacent to the query for whoever edits it next.
func TestAlternativesSQL_NoGoComments(t *testing.T) {
	for i, line := range strings.Split(alternativesSQL, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			t.Errorf("line %d is a Go // comment; PostgreSQL cannot parse it "+
				"(use -- or /* */): %s", i+1, strings.TrimSpace(line))
		}
	}
}
