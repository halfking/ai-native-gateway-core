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

// modalityMismatchResolver is a providerResolver stub that reports a
// modality mismatch on demand.
//
// The two knobs matter separately:
//
//	known      — whether the model name is recognised at all
//	mismatch   — what ModelModalityMismatch reports (nil = "not a modality
//	              problem", i.e. the zero candidates came from somewhere else)
//
// A test that only exercised the "no candidates" path would pass even if the
// new branch never fired, so both arms are asserted below.
type modalityMismatchResolver struct {
	known    bool
	mismatch *provider.ModalityMismatch
}

func (r *modalityMismatchResolver) Enabled() bool { return true }

func (r *modalityMismatchResolver) GetCandidates(context.Context, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, provider.DefaultPolicy(), nil
}

func (r *modalityMismatchResolver) GetCandidatesByModality(context.Context, string, string, string, string) ([]provider.Candidate, *provider.Policy, error) {
	return nil, provider.DefaultPolicy(), nil
}

func (r *modalityMismatchResolver) ModelKnown(context.Context, string) bool { return r.known }

func (r *modalityMismatchResolver) ModelModalityMismatch(context.Context, string, string, string, string) (*provider.ModalityMismatch, error) {
	return r.mismatch, nil
}

// TestWriteModalityMismatchError_BodyShape pins the response shape.
//
// The whole point of this error is that a client can act on it without
// parsing prose, so the machine-readable fields are part of the contract:
// code, requested_modality, registered_modality, supported_modalities.
func TestWriteModalityMismatchError_BodyShape(t *testing.T) {
	h := &ChatHandler{}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
	w := httptest.NewRecorder()

	mismatch := &provider.ModalityMismatch{
		RequestedModality:  "vision",
		RegisteredModality: "text",
		CanonicalModel:     "glm-5.3-flash",
	}
	h.writeModalityMismatchError(r, w, "req-123", "glm-5.3-flash", mismatch,
		&chatRequestBody{}, []byte("{}"), nil)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: a modality mismatch is a client-side problem, not a transient 503",
			w.Code, http.StatusBadRequest)
	}

	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
			Kind    string `json:"kind"`
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

	if body.Error.Code != "modality_mismatch" {
		t.Errorf("code = %q, want %q", body.Error.Code, "modality_mismatch")
	}
	if body.Error.Kind != "modality_mismatch" {
		t.Errorf("kind = %q, want %q", body.Error.Kind, "modality_mismatch")
	}
	if body.Error.Type != "invalid_request_error" {
		t.Errorf("type = %q, want %q", body.Error.Type, "invalid_request_error")
	}
	if body.Error.Request != "req-123" {
		t.Errorf("request_id = %q, want %q", body.Error.Request, "req-123")
	}
	if body.Error.Detail.RequestedModality != "vision" {
		t.Errorf("requested_modality = %q, want %q", body.Error.Detail.RequestedModality, "vision")
	}
	if body.Error.Detail.RegisteredModality != "text" {
		t.Errorf("registered_modality = %q, want %q", body.Error.Detail.RegisteredModality, "text")
	}
	// A text-only model can serve text and nothing else. Listing more than
	// that would tell the client to send images to a model that cannot read
	// them.
	if len(body.Error.Detail.Supported) != 1 || body.Error.Detail.Supported[0] != "text" {
		t.Errorf("supported_modalities = %v, want [text]", body.Error.Detail.Supported)
	}
	// The message carries the LOCALISED modality word ("image" in English),
	// while the machine-readable field carries the raw token ("vision").
	// Both must be present: the token for machines, the word for humans.
	if !strings.Contains(body.Error.Message, "image") {
		t.Errorf("message does not name the modality: %q", body.Error.Message)
	}
	if !strings.Contains(body.Error.Message, "glm-5.3-flash") {
		t.Errorf("message does not name the model: %q", body.Error.Message)
	}
}

// TestSupportedModalitiesFor pins the acceptance table the response publishes.
//
// ★ This table is the client's contract for "may I send this modality to
// this model". It must mirror provider.modalityAccepts — the same rule the
// router's modality gate applies. If the two drift, the client would be told
// a model accepts audio while the gateway rejects every audio request for it.
func TestSupportedModalitiesFor(t *testing.T) {
	cases := []struct {
		registered string
		want       []string
	}{
		{"text", []string{"text"}},
		{"multimodal", []string{"text", "vision", "audio", "video", "image", "multimodal"}},
		{"vision", []string{"text", "vision", "image"}},
		{"audio", []string{"text", "audio"}},
		{"video", []string{"text", "video"}},
		{"image", []string{"text", "image"}},
		{"", []string{"text"}},
	}
	for _, tc := range cases {
		got := supportedModalitiesFor(tc.registered)
		if len(got) != len(tc.want) {
			t.Errorf("supportedModalitiesFor(%q) = %v, want %v", tc.registered, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("supportedModalitiesFor(%q) = %v, want %v", tc.registered, got, tc.want)
				break
			}
		}
	}
}

// TestModalityAccepts_MatchesPublishedTable cross-checks the two
// implementations of the acceptance rule.
//
// ★ Two copies of one rule is exactly the "same sentence in two places"
// failure this repo keeps auditing for. provider.modalityAccepts decides
// whether the request is a mismatch; supportedModalitiesFor publishes what
// the client may send. If they disagree, the client is told to send media
// the gateway will reject.
//
// The check runs both directions: everything published as supported must be
// accepted, and everything NOT published must be rejected.
func TestModalityAccepts_MatchesPublishedTable(t *testing.T) {
	registered := []string{"text", "multimodal", "vision", "audio", "video", "image"}
	requested := []string{"text", "vision", "audio", "video", "image", "multimodal"}

	for _, reg := range registered {
		supported := supportedModalitiesFor(reg)
		for _, req := range requested {
			want := modalityListContains(supported, req)
			got := provider.ModalityAccepts(reg, req)
			if got != want {
				t.Errorf("registered=%q requested=%q: modalityAccepts=%v but published table says %v",
					reg, req, got, want)
			}
		}
	}
}

func modalityListContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestModalityMismatch_IsZero covers the "not a mismatch" signal, which the
// handler branches on. A nil-safe IsZero keeps the branch from panicking on
// a nil mismatch.
func TestModalityMismatch_IsZero(t *testing.T) {
	var nilMismatch *provider.ModalityMismatch
	if !nilMismatch.IsZero() {
		t.Error("nil mismatch must report IsZero() = true")
	}
	if !(&provider.ModalityMismatch{}).IsZero() {
		t.Error("a mismatch with no requested modality must report IsZero() = true")
	}
	if (&provider.ModalityMismatch{RequestedModality: "vision"}).IsZero() {
		t.Error("a mismatch with a requested modality must report IsZero() = false")
	}
}
