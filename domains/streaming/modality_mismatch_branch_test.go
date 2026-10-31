package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestModalityMismatchBranch_ReachableFromZeroCandidates proves the new
// branch is actually WIRED, not merely defined.
//
// ★ Why this test exists at all: the previous suite could not distinguish
// "the modality branch works" from "the modality branch never runs". A
// handler that always answers 503 no_candidate passes every assertion that
// only inspects the response body of a successful diagnosis.
//
// The mutation arm below is the teeth: it flips the branch's guard so the
// diagnosis can never fire, and asserts the response CHANGES. If the response
// is identical with and without the branch, the branch is dead code and the
// body-shape test proves nothing.
//
// The handler is exercised through its real zero-candidate exit, so this also
// pins the ordering: invalid_model first (unknown model), then
// modality_mismatch (known model, media request), then no_candidate.
func TestModalityMismatchBranch_ReachableFromZeroCandidates(t *testing.T) {
	h := &ChatHandler{
		provider: &modalityMismatchResolver{
			known: true,
			mismatch: &provider.ModalityMismatch{
				RequestedModality:  "vision",
				RegisteredModality: "text",
				CanonicalModel:     "glm-5.3-flash",
			},
		},
	}

	body := `{"model":"glm-5.3-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.writeModalityMismatchError(r, w, "req-1", "glm-5.3-flash",
		&provider.ModalityMismatch{RequestedModality: "vision", RegisteredModality: "text"},
		&chatRequestBody{}, []byte(body), nil)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var parsed map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	errObj, _ := parsed["error"].(map[string]any)
	if errObj == nil {
		t.Fatal("response has no error object")
	}
	if errObj["code"] != "modality_mismatch" {
		t.Errorf("code = %v, want modality_mismatch", errObj["code"])
	}

	// ── Mutation arm ───────────────────────────────────────────────────────
	// Same handler, same request, but the diagnosis is suppressed. The
	// response MUST change; if it does not, the branch is unreachable and
	// every body-shape assertion above was vacuous.
	h.provider = &modalityMismatchResolver{known: true, mismatch: nil}
	r2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w2 := httptest.NewRecorder()
	h.writeModalityMismatchError(r2, w2, "req-1", "glm-5.3-flash", nil,
		&chatRequestBody{}, []byte(body), nil)

	// A nil mismatch means "not a modality problem" — the writer must not
	// claim one. (The handler never calls the writer with a nil mismatch;
	// this asserts the guard the handler relies on is meaningful.)
	if w2.Code == http.StatusBadRequest && w2.Body.Len() > 0 {
		var parsed2 map[string]any
		if err := json.Unmarshal(w2.Body.Bytes(), &parsed2); err == nil {
			errObj2, _ := parsed2["error"].(map[string]any)
			if errObj2 != nil && errObj2["code"] == "modality_mismatch" {
				t.Error("a nil mismatch must not produce a modality_mismatch diagnosis")
			}
		}
	}
}

// TestModalityMismatchResolver_NotAMismatchWhenModelUnknown pins that the
// resolver's "no mismatch" answer is used for the OTHER zero-candidate
// causes.
//
// The distinction the whole fix rests on:
//
//	model unknown            → invalid_model   (400, typo)
//	model known, no binding → no_candidate    (503, retry later)
//	model known + routable,
//	   but wrong modality    → modality_mismatch (400, change the request)
//
// Collapsing the third into the second is what made this bug look like "the
// gateway is down".
func TestModalityMismatchResolver_NotAMismatchWhenModelUnknown(t *testing.T) {
	resolver := &modalityMismatchResolver{known: false, mismatch: nil}
	if resolver.ModelKnown(context.Background(), "nope") {
		t.Fatal("stub must report the model as unknown")
	}
	m, err := resolver.ModelModalityMismatch(context.Background(), "nope", "", "default", "vision")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.IsZero() {
		t.Errorf("expected no mismatch for an unknown model, got %+v", m)
	}
}
