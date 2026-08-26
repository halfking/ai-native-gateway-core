package requestfact

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func validDurableInput() DurableProjectionInput {
	return DurableProjectionInput{
		Protocol:           "openai-completions",
		Endpoint:           "/v1/chat/completions",
		TenantID:           "tenant-1",
		ApplicationID:      7,
		APIKeyID:           42,
		SessionID:          "session-1",
		SessionSource:      "body",
		ClientModel:        "gpt-4o-mini",
		NormalizedBody:     []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"session_id":"session-1"}`),
		ClientIdentityHash: "identity-hash",
		ToolsRequested:     true,
		RequestID:          "request-1",
		ParentRequestID:    "parent-1",
		PolicyVersion:      "survival-policy-v1",
	}
}

func TestProjectDurableMapsEveryField(t *testing.T) {
	projection, err := ProjectDurable(validDurableInput())
	if err != nil {
		t.Fatalf("ProjectDurable() error = %v", err)
	}
	want := &DurableProjection{
		Version:            DurableProjectionInputVersionV1,
		Endpoint:           "/v1/chat/completions",
		ClientProtocol:     "openai-completions",
		ClientModel:        "gpt-4o-mini",
		NormalizedBody:     []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"session_id":"session-1"}`),
		APIKeyID:           42,
		TenantID:           "tenant-1",
		ApplicationID:      7,
		SessionID:          "session-1",
		SessionSource:      "body",
		ClientIdentityHash: "identity-hash",
		ToolsRequested:     true,
		PolicyVersion:      "survival-policy-v1",
		RequestID:          "request-1",
		TaskCorrelationID:  "request-1",
		ParentRequestID:    "parent-1",
	}
	want.RequestHash = mustProjectHash(t, want.NormalizedBody)
	if !reflect.DeepEqual(projection, want) {
		t.Fatalf("projection = %#v, want %#v", projection, want)
	}
	if len(projection.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none for fully populated input", projection.Warnings)
	}
}

func TestProjectDurableHashesExactBytes(t *testing.T) {
	base := []byte(`{"alpha":1,"beta":2}`)
	reordered := []byte(`{"beta":2,"alpha":1}`)
	spaced := []byte(`{ "alpha": 1, "beta": 2 }`)
	if BodySHA256(base) != BodySHA256(reordered) || BodySHA256(base) != BodySHA256(spaced) {
		t.Fatal("precondition: BodySHA256 canonicalizes formatting")
	}

	baseHash := mustProjectHash(t, base)
	if len(baseHash) != 64 || strings.ToLower(baseHash) != baseHash {
		t.Fatalf("hash = %q, want 64-char lowercase hex", baseHash)
	}
	if _, err := hex.DecodeString(baseHash); err != nil {
		t.Fatalf("hash not hex: %v", err)
	}
	if mustProjectHash(t, reordered) == baseHash {
		t.Fatal("exact-byte hash ignored member order")
	}
	if mustProjectHash(t, spaced) == baseHash {
		t.Fatal("exact-byte hash ignored whitespace")
	}
	if mustProjectHash(t, base) != baseHash {
		t.Fatal("hash not deterministic across identical inputs")
	}
}

func TestProjectDurableCopiesCallerBytes(t *testing.T) {
	input := validDurableInput()
	projection, err := ProjectDurable(input)
	if err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), input.NormalizedBody...)

	input.NormalizedBody[0] = 'X'
	if !bytes.Equal(projection.NormalizedBody, original) {
		t.Fatal("projection body aliases caller bytes")
	}

	projection.NormalizedBody[0] = 'X'
	if bytes.Equal(projection.NormalizedBody, original) {
		t.Fatal("mutating projection body would leak into original")
	}
}

func TestProjectDurableRejectsInvalidInputs(t *testing.T) {
	mutations := map[string]func(*DurableProjectionInput){
		"missing protocol":       func(in *DurableProjectionInput) { in.Protocol = "" },
		"missing endpoint":       func(in *DurableProjectionInput) { in.Endpoint = "" },
		"missing tenant":         func(in *DurableProjectionInput) { in.TenantID = "" },
		"missing API key ID":     func(in *DurableProjectionInput) { in.APIKeyID = 0 },
		"negative API key ID":    func(in *DurableProjectionInput) { in.APIKeyID = -1 },
		"missing session":        func(in *DurableProjectionInput) { in.SessionID = "" },
		"missing model":          func(in *DurableProjectionInput) { in.ClientModel = "" },
		"missing identity hash":  func(in *DurableProjectionInput) { in.ClientIdentityHash = "" },
		"missing policy version": func(in *DurableProjectionInput) { in.PolicyVersion = "" },
		"missing request ID":     func(in *DurableProjectionInput) { in.RequestID = "" },
		"nil body":               func(in *DurableProjectionInput) { in.NormalizedBody = nil },
		"null body":              func(in *DurableProjectionInput) { in.NormalizedBody = []byte(" null ") },
		"empty body":             func(in *DurableProjectionInput) { in.NormalizedBody = []byte("   ") },
		"malformed body":         func(in *DurableProjectionInput) { in.NormalizedBody = []byte(`{"model":`) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			input := validDurableInput()
			mutate(&input)
			projection, err := ProjectDurable(input)
			if !errors.Is(err, ErrInvalidDurableProjectionInput) {
				t.Fatalf("error = %v, want ErrInvalidDurableProjectionInput", err)
			}
			if projection != nil {
				t.Fatalf("projection = %#v, want nil on failure", projection)
			}
		})
	}
}

func TestProjectDurableOptionalFieldsAndMediaMarkers(t *testing.T) {
	input := validDurableInput()
	input.ParentRequestID = ""
	input.SessionSource = ""
	input.NormalizedBody = []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/i.png"}}]}],"response_format":{"type":"json_object"}}`)
	projection, err := ProjectDurable(input)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.HasMultimodalContent {
		t.Fatal("image block did not mark multimodal")
	}
	if projection.ResponseFormat != "json_object" {
		t.Fatalf("response format = %q", projection.ResponseFormat)
	}
	fields := map[string]bool{}
	for _, warning := range projection.Warnings {
		if warning.Code != WarningOptionalFieldOmitted {
			t.Fatalf("warning code = %q", warning.Code)
		}
		fields[warning.Field] = true
	}
	if !fields["parent_request_id"] || !fields["session_source"] {
		t.Fatalf("warnings = %#v, want parent_request_id and session_source", projection.Warnings)
	}

	textOnly := validDurableInput()
	textOnly.NormalizedBody = []byte(`{"model":"m","messages":[{"role":"user","content":"plain"}]}`)
	projection, err = ProjectDurable(textOnly)
	if err != nil {
		t.Fatal(err)
	}
	if projection.HasMultimodalContent || projection.ResponseFormat != "" {
		t.Fatalf("text-only markers = multimodal:%v format:%q", projection.HasMultimodalContent, projection.ResponseFormat)
	}

	gemini := validDurableInput()
	gemini.NormalizedBody = []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"},{"inlineData":{"mimeType":"image/png","data":"aGk="}}]}]}`)
	projection, err = ProjectDurable(gemini)
	if err != nil {
		t.Fatal(err)
	}
	if projection.HasMultimodalContent {
		t.Fatal("Gemini inlineData misclassified as typed media block")
	}
}

func TestDurableProjectionExcludesSensitiveAndSchedulerFields(t *testing.T) {
	projection, err := ProjectDurable(validDurableInput())
	if err != nil {
		t.Fatal(err)
	}
	if projection.APIKeyID != 42 {
		t.Fatalf("API key ID = %d, want opaque numeric reference only", projection.APIKeyID)
	}
	// Structural exclusion: these concepts have no representation on the
	// projection type, so marshaling must not emit any such key.
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"authorization", "cookie", "api_key_value", "credential", "candidate",
		"provider", "lease", "fencing", "attempt", "status", "deadline",
		"retry", "ciphertext", "key_id", "created_at", "expires_at",
	} {
		if bytes.Contains(encoded, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("projection leaked forbidden key %q: %s", forbidden, encoded)
		}
	}
}

func mustProjectHash(t *testing.T, body []byte) string {
	t.Helper()
	input := validDurableInput()
	input.NormalizedBody = body
	projection, err := ProjectDurable(input)
	if err != nil {
		t.Fatal(err)
	}
	return projection.RequestHash
}
