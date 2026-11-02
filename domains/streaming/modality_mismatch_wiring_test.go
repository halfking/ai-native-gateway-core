package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestHandlerZeroCandidates_ModalityMismatchWiring drives the REAL handler
// diagnosis entry point and asserts the response.
//
// ★ Why this test exists — it closes the hole the mutation arm found:
//
//	Every other test of this feature calls writeModalityMismatchError
//	DIRECTLY. That proves the writer produces the right body, but it
//	proves nothing about the WIRING: the branch in the handler that decides
//	to call the writer could be deleted, short-circuited, or reordered, and
//	every existing test would stay green while the production behaviour
//	silently reverted to the old 503.
//
//	The mutation arm made that concrete: replacing the branch's guard with
//	`if false` left the whole suite green. This test is the fix for that —
//	it calls the named diagnosis entry point the handler itself calls, so
//	the arm now fails.
//
//	The scenario is the production one: an IDE agent's request carries
//	screenshots, the model is text-only, and the client must be told so.
func TestHandlerZeroCandidates_ModalityMismatchWiring(t *testing.T) {
	body := `{
      "model": "glm-5.3-flash",
      "messages": [{"role": "user", "content": [
        {"type": "text", "text": "看下这张图"},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}
      ]}]
    }`

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

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// The named diagnosis entry point the handler calls. Testing the CALL
	// is the point: the branch's guard (which modality values reach the
	// diagnosis at all) lives in this function, and a mutation that
	// short-circuits it must fail here.
	mismatch := h.diagnoseModalityMismatch(req.Context(), "glm-5.3-flash", "", "default", modalityVision)
	if mismatch.IsZero() {
		t.Fatal("the handler's diagnosis entry point did not report the mismatch — " +
			"a vision request against a text-only model must be diagnosed")
	}
	h.writeModalityMismatchError(req, rec, "req-wiring", "glm-5.3-flash", mismatch,
		&chatRequestBody{}, []byte(body), nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a modality mismatch is a client-side problem)\nbody: %s",
			rec.Code, rec.Body.String())
	}

	var parsed struct {
		Error struct {
			Code string `json:"code"`
			Kind string `json:"kind"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, rec.Body.String())
	}
	if parsed.Error.Code != "modality_mismatch" {
		t.Errorf("code = %q, want modality_mismatch\nbody: %s", parsed.Error.Code, rec.Body.String())
	}
	if parsed.Error.Kind != "modality_mismatch" {
		t.Errorf("kind = %q, want modality_mismatch", parsed.Error.Kind)
	}
}

// TestDiagnoseModalityMismatch_TextRequestIsNotDiagnosed is the negative
// control on the same entry point.
//
// ★ A text request against the same model must NOT be diagnosed as a
// **vision** mismatch: "this model cannot read your images" for a request
// that carried no images is worse than no message at all.
//
// ★★ 2026-11-02 第六轮（F12）改写了这条口径：此前 resolver 对文本请求
// **根本不会被问**（一道名字粗筛守着），所以「拿到一份关于 vision 的判决」
// 这件事不可能发生 ⇒ 这条用例当时只钉「不被问」。
//
// ⇒ 现在文本请求**会**去问 resolver（这正是 F12 的修复：登记为 embedding
// 的模型必须能被诊断出来）⇒ 「拿到一份关于**别的模态**的判决」变成可能
// （陈旧结果 / 串台）⇒ handler 必须**核对判决的模态**，对不上就忽略
// （diagnoseModalityMismatch 里的 RequestedModality 一致性闸）。
// ⇒ 这条用例现在钉的正是那道闸：resolver 报 vision 判决时，文本请求必须
// 拿不到诊断。
func TestDiagnoseModalityMismatch_TextRequestIsNotDiagnosed(t *testing.T) {
	h := &ChatHandler{
		provider: &modalityMismatchResolver{
			known: true,
			mismatch: &provider.ModalityMismatch{
				// The resolver reports a mismatch about a DIFFERENT
				// modality than the request. The handler must not
				// adopt it — the request was text.
				RequestedModality:  "vision",
				RegisteredModality: "text",
			},
		},
	}

	for _, mod := range []string{"", modalityText, "TEXT", " text "} {
		got := h.diagnoseModalityMismatch(t.Context(), "glm-5.3-flash", "", "default", mod)
		if !got.IsZero() {
			t.Errorf("modality %q: expected no diagnosis for a text request, got %+v", mod, got)
		}
	}
}

// TestDiagnoseModalityMismatch_ResolverErrorDegradesToNoDiagnosis pins that
// the diagnosis can never become a new way to fail a request.
//
// ★ The resolver does database work on a path that is already failing. If
// it errors, the request must still get the ordinary 503 — not a 500 from
// the diagnosis. A diagnostic that can break the thing it diagnoses is
// worse than no diagnostic.
func TestDiagnoseModalityMismatch_ResolverErrorDegradesToNoDiagnosis(t *testing.T) {
	h := &ChatHandler{provider: &erringModalityResolver{}}
	got := h.diagnoseModalityMismatch(t.Context(), "glm-5.3-flash", "", "default", modalityVision)
	if !got.IsZero() {
		t.Errorf("a resolver error must degrade to no diagnosis, got %+v", got)
	}
}

// erringModalityResolver is a providerResolver whose diagnosis lookup fails.
type erringModalityResolver struct {
	modalityMismatchResolver
}

func (r *erringModalityResolver) ModelModalityMismatch(context.Context, string, string, string, string) (*provider.ModalityMismatch, error) {
	return nil, errors.New("simulated resolver failure")
}
