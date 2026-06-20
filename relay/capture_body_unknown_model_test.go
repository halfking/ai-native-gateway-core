package relay

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCaptureAttemptBody_NoModelField exercises the 2026-06-20 audit
// fix v2: when the body is captured but the JSON has no "model" field
// (e.g. /v1/messages client forgot the model), captureAttemptBody must
// set client_model to "<unknown>" — NOT leave it blank. Without this,
// the operator sees a non-empty body with a blank client_model and
// cannot tell whether the body was empty or whether the model field
// was simply absent.
func TestCaptureAttemptBody_NoModelFieldV2(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantModel     string
		wantBodyBytes int
	}{
		{
			name:          "messages body without model field",
			body:          `{"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`,
			wantModel:     "<unknown>",
			wantBodyBytes: len(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`),
		},
		{
			name:          "chat body without model field",
			body:          `{"messages":[{"role":"user","content":"hi"}]}`,
			wantModel:     "<unknown>",
			wantBodyBytes: len(`{"messages":[{"role":"user","content":"hi"}]}`),
		},
		{
			name:          "empty json object",
			body:          `{}`,
			wantModel:     "<unknown>",
			wantBodyBytes: 2,
		},
		{
			name:          "json array (unusual but valid body)",
			body:          `[{"role":"user","content":"hi"}]`,
			wantModel:     "<unknown>",
			wantBodyBytes: len(`[{"role":"user","content":"hi"}]`),
		},
		{
			name:          "body with valid model field (regression check)",
			body:          `{"model":"claude-opus-4-8","messages":[]}`,
			wantModel:     "claude-opus-4-8",
			wantBodyBytes: len(`{"model":"claude-opus-4-8","messages":[]}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			var model string
			r := httptest.NewRequest(http.MethodPost, "/v1/messages",
				bytes.NewReader([]byte(tc.body)))
			captureAttemptBody(r, &body, &model)
			if len(body) != tc.wantBodyBytes {
				t.Errorf("body length: got %d, want %d", len(body), tc.wantBodyBytes)
			}
			if model != tc.wantModel {
				t.Errorf("model: got %q, want %q", model, tc.wantModel)
			}
		})
	}
}

// TestEnsureRequestBodyBuffered_NoModelField is the v2 fix companion
// test: ensureRequestBodyBuffered (used by /v1/messages handler at the
// top of ServeHTTP) must also set "<unknown>" when model is absent.
// The two helpers together guarantee that request_logs.client_model
// is never blank whenever request_logs.request_body is non-empty.
func TestEnsureRequestBodyBuffered_NoModelFieldV2(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantModel string
	}{
		{
			name:      "messages without model",
			body:      `{"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`,
			wantModel: "<unknown>",
		},
		{
			name:      "chat without model",
			body:      `{"messages":[{"role":"user","content":"hi"}]}`,
			wantModel: "<unknown>",
		},
		{
			name:      "empty json",
			body:      `{}`,
			wantModel: "<unknown>",
		},
		{
			name:      "with valid model",
			body:      `{"model":"claude-opus-4-8"}`,
			wantModel: "claude-opus-4-8",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body []byte
			var model string
			r := httptest.NewRequest(http.MethodPost, "/v1/messages",
				strings.NewReader(tc.body))
			err := ensureRequestBodyBuffered(r, &body, &model)
			if err != nil {
				t.Fatalf("ensureRequestBodyBuffered returned error: %v", err)
			}
			if model != tc.wantModel {
				t.Errorf("model: got %q, want %q", model, tc.wantModel)
			}
			if len(body) == 0 {
				t.Errorf("body should have been captured, got empty")
			}
		})
	}
}

// TestMessagesHandler_BodyWithoutModel_RecordsUnknownModel is the
// E2E verification of the v2 fix for the specific case the user
// reported (id 37740: /v1/messages body has no "model" field).
// In this case the request reaches /v1/messages without a model
// field. The handler returns "model is required" (400). The
// captured request_logs row must have client_model set to
// "<unknown>" (not blank).
func TestMessagesHandler_BodyWithoutModel_RecordsUnknownModel(t *testing.T) {
	col := newRequestLogCollector()
	ch := &ChatHandler{}
	ch.SetRequestLogHook(col.Hook)
	h := NewMessagesHandler(ch)

	// Body has messages and max_tokens but NO model field —
	// exactly the case the user reported (request id 37740).
	body := `{"messages":[{"role":"user","content":"hi"}],"max_tokens":10}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The handler will reject with 400 "model is required"
	// (the body lacks the required model field).
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	rows := col.Snapshot()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// v2 fix invariant: client_model must NEVER be blank when
	// the row was emitted. It should be "<unknown>" for this
	// request because the body had no model field.
	if rows[0].clientModel == "" {
		t.Errorf("client_model is blank — should be %q for body without model field", "<unknown>")
	}
	if rows[0].clientModel != "<unknown>" {
		t.Errorf("client_model: got %q, want %q", rows[0].clientModel, "<unknown>")
	}
}