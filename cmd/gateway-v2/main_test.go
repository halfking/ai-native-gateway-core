// main_test.go — P1-23 gateway-v2 compat endpoint body contract.
//
// 2026-08-26 (P1-23 fix): the four v2 compat endpoints
// (/v1/chat/completions, /v1/messages, /v1/responses, /v1/completions)
// used json.NewDecoder(r.Body).Decode(&req) against an unbounded
// body, with no guard against multiple top-level JSON values. The
// helper decodeStrictJSON now wraps r.Body in http.MaxBytesReader and
// runs a second Decode into json.RawMessage to reject trailing data.
//
// We test the helper directly. The handler integration is covered by
// cmd/gateway-v2/e2e_test.go (separate file, not touched here).
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeStrictJSON_AcceptsValidBody(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}`))
	w := httptest.NewRecorder()

	var dst struct {
		A int `json:"a"`
	}
	ok, err := decodeStrictJSON(w, r, &dst, "openai")
	if !ok || err != nil {
		t.Fatalf("valid body must succeed: ok=%v err=%v", ok, err)
	}
	if dst.A != 1 {
		t.Fatalf("dst not populated: %+v", dst)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("happy path must not write a status: got %d", w.Code)
	}
}

func TestDecodeStrictJSON_RejectsOversize(t *testing.T) {
	// Build a body > 32 MiB. Cap is gatewayV2MaxBodyBytes.
	big := strings.Repeat("a", gatewayV2MaxBodyBytes+1024)
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"x":"`+big+`"}`))
	w := httptest.NewRecorder()

	var dst struct{ X string }
	ok, _ := decodeStrictJSON(w, r, &dst, "openai")
	if ok {
		t.Fatalf("oversize body must fail")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body must write 413, got %d", w.Code)
	}
}

func TestDecodeStrictJSON_RejectsMalformed(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	var dst struct{}
	ok, _ := decodeStrictJSON(w, r, &dst, "openai")
	if ok {
		t.Fatalf("malformed body must fail")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body must write 400, got %d", w.Code)
	}
}

func TestDecodeStrictJSON_RejectsMultipleValues(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}{"b":2}`))
	w := httptest.NewRecorder()

	var dst struct{ A int }
	ok, _ := decodeStrictJSON(w, r, &dst, "openai")
	if ok {
		t.Fatalf("trailing values must fail")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("trailing values must write 400, got %d", w.Code)
	}
}

func TestDecodeStrictJSON_AnthropicErrorShape(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()

	var dst struct{}
	_, _ = decodeStrictJSON(w, r, &dst, "anthropic")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("anthropic path must write 400, got %d", w.Code)
	}
	// Anthropic error envelope wraps the error under a "type":"error" key.
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("anthropic envelope must parse: %v", err)
	}
	if envelope.Type != "error" {
		t.Fatalf("anthropic envelope top-level type must be 'error', got %q", envelope.Type)
	}
}
