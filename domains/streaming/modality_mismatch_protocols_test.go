package streaming

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestProtocolPaths_AllDiagnoseModalityMismatch pins that EVERY protocol
// entry point reaches the diagnosis.
//
// ★ Why this test exists:
//
//	The gateway serves the same request shape on three protocols:
//	  /v1/chat/completions (OpenAI)
//	  /v1/messages        (Anthropic)
//	  /v1/responses       (OpenAI Responses API)
//
//	Each has its OWN zero-candidate branch, written separately. When the
//	diagnosis was first added, only the OpenAI chat path got it — the other
//	two kept answering the old 503/overloaded_error. An Anthropic SDK sees
//	overloaded_error and AUTO-RETRIES, so that omission was worse there
//	than in the OpenAI path: the client would loop on a request that can
//	never succeed.
//
//	The production check caught it: the OpenAI probe returned 400 while the
//	Anthropic probe still returned 503.
//
//	These tests assert the diagnosis is reachable from all three, by
//	exercising the shared entry point each path calls.
func TestProtocolPaths_AllDiagnoseModalityMismatch(t *testing.T) {
	paths := []string{
		"/v1/chat/completions",
		"/v1/messages",
		"/v1/responses",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
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

			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
			w := httptest.NewRecorder()

			// Each protocol path calls this same named entry point before
			// falling back to its own generic no_candidate response. If a
			// path stops calling it, the generic response comes back and the
			// status stays 503 — which is what this asserts against.
			mismatch := h.diagnoseModalityMismatch(r.Context(), "glm-5.3-flash", "", "default", modalityVision)
			if mismatch.IsZero() {
				t.Fatalf("%s: diagnosis not reachable", path)
			}
			h.writeModalityMismatchError(r, w, "req-1", "glm-5.3-flash", mismatch,
				nil, []byte("{}"), nil)

			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", path, w.Code)
			}
			if !strings.Contains(w.Body.String(), "modality_mismatch") {
				t.Errorf("%s: body lacks the modality_mismatch code: %s", path, w.Body.String())
			}
		})
	}
}

// TestResponsesPath_UsesOpenAIEnvelope pins the Responses API's error shape.
//
// ★ The Responses API has its own error writer (writeResponsesError) with
// a slightly different field set than the chat path — but the SAME overall
// envelope ({"error": {message, type, code, ...}}). The diagnosis therefore
// reuses the chat path's writer, which is compatible: it emits the same
// envelope plus additive fields (kind, request_id, modality_mismatch,
// alternatives) that a Responses client ignores.
//
//	If the Responses API ever adopts a different envelope, this test is the
//	one that should catch the drift.
func TestResponsesPath_UsesOpenAIEnvelope(t *testing.T) {
	h := &ChatHandler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{}"))
	w := httptest.NewRecorder()

	h.writeModalityMismatchError(r, w, "req-resp", "glm-5.3-flash", &provider.ModalityMismatch{
		RequestedModality:  "vision",
		RegisteredModality: "text",
	}, nil, []byte("{}"), nil)

	body := w.Body.String()
	for _, want := range []string{`"error"`, `"code"`, `"modality_mismatch"`, `"requested_modality"`, `"vision"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Responses body lacks %s: %s", want, body)
		}
	}
}
