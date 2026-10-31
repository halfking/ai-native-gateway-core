package streaming

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// TestWriteModalityMismatchError_AnthropicEnvelope pins the Anthropic
// protocol's error shape.
//
// ★ Why the Anthropic arm needs its own assertions:
//
//	Anthropic SDKs type-check error.type against a closed set, so the type
//	has to be one of the SDK's own types. The obvious choice from the
//	neighbouring code path is "overloaded_error" — it is what the generic
//	no_candidate exit uses — and that would be actively harmful here:
//
//		overloaded_error  ⇒ "the server is busy, retry"
//		invalid_request_error ⇒ "change the request"
//
//	A modality mismatch is the latter: the request is fine, the model
//	choice is not, and retrying the identical request fails forever. An
//	Anthropic SDK that auto-retries on overloaded_error would loop on a
//	request that can never succeed.
//
//	The machine-readable detail block must be present here too: an SDK that
//	only reads error.type learns "invalid request", but an SDK that wants
//	to know WHICH modality was rejected has to read the detail.
func TestWriteModalityMismatchError_AnthropicEnvelope(t *testing.T) {
	h := &ChatHandler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	w := httptest.NewRecorder()

	mismatch := &provider.ModalityMismatch{
		RequestedModality:  "vision",
		RegisteredModality: "text",
		CanonicalModel:     "glm-5.3-flash",
	}
	h.writeModalityMismatchError(r, w, "req-anthropic", "glm-5.3-flash", mismatch,
		nil, []byte("{}"), nil)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
			Request string `json:"request_id"`
			Detail  struct {
				RequestedModality  string   `json:"requested_modality"`
				RegisteredModality string   `json:"registered_modality"`
				Supported          []string `json:"supported_modalities"`
			} `json:"modality_mismatch"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, w.Body.String())
	}

	if body.Type != "error" {
		t.Errorf("envelope type = %q, want %q", body.Type, "error")
	}
	if body.Error.Type != "invalid_request_error" {
		t.Errorf("error.type = %q, want invalid_request_error (NOT overloaded_error: "+
			"that tells an auto-retrying SDK to retry a request that can never succeed)",
			body.Error.Type)
	}
	if body.Error.Code != "modality_mismatch" {
		t.Errorf("error.code = %q, want modality_mismatch", body.Error.Code)
	}
	if body.Error.Request != "req-anthropic" {
		t.Errorf("request_id = %q, want req-anthropic", body.Error.Request)
	}
	if body.Error.Detail.RequestedModality != "vision" {
		t.Errorf("requested_modality = %q, want vision", body.Error.Detail.RequestedModality)
	}
	if body.Error.Detail.RegisteredModality != "text" {
		t.Errorf("registered_modality = %q, want text", body.Error.Detail.RegisteredModality)
	}
	if len(body.Error.Detail.Supported) != 1 || body.Error.Detail.Supported[0] != "text" {
		t.Errorf("supported_modalities = %v, want [text]", body.Error.Detail.Supported)
	}
}

// TestAnthropicErrorType_ModalityMismatch pins the type mapping itself.
//
// The mapping table is shared by every Anthropic error path; a future edit
// that adds "modality_mismatch" to the wrong branch would silently tell
// clients to retry.
func TestAnthropicErrorType_ModalityMismatch(t *testing.T) {
	if got := anthropicErrorType("server_error", "modality_mismatch"); got != "invalid_request_error" {
		t.Errorf("anthropicErrorType(modality_mismatch) = %q, want invalid_request_error", got)
	}
	// The generic no_candidate arm must keep its historical type — the
	// modality change must not alter what unrelated callers receive.
	if got := anthropicErrorType("server_error", "no_candidate"); got != "overloaded_error" {
		t.Errorf("anthropicErrorType(no_candidate) = %q, want overloaded_error", got)
	}
}

// TestExtractSignalsForAuto_NilRequestBody guards the nil-safety fix.
//
// ★ The Anthropic path calls the alternatives finder without a parsed
// chatRequestBody. Reading reqBody.Messages on a nil pointer panics — on a
// request that has ALREADY failed, which is the worst possible moment for a
// panic.
func TestExtractSignalsForAuto_NilRequestBody(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("extractSignalsForAuto panicked on a nil body: %v", r)
		}
	}()
	sigs := extractSignalsForAuto(nil, []byte(`{"model":"glm-5.3-flash"}`))
	if sigs.HasImages {
		t.Error("HasImages must be false for an empty body")
	}
}
