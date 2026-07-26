package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeUTF8_ReplacesInvalidBytes(t *testing.T) {
	// Construct an invalid UTF-8 sequence: the first 2 bytes of '弊'
	// (0xE5 0xBC) followed by '…' first byte (0xE2) — exactly the
	// 0xE5 0xBC 0xE2 sequence seen in the 2026-06-11 incident.
	invalid := string([]byte{0xE5, 0xBC, 0xE2, 0x80, 0xA6})
	out := sanitizeUTF8(invalid)
	if !utf8.ValidString(out) {
		t.Fatalf("sanitizeUTF8 still invalid: %q (bytes %x)", out, []byte(out))
	}
	if !strings.Contains(out, "\uFFFD") {
		t.Fatalf("expected U+FFFD replacement in %q", out)
	}
}

func TestSanitizeUTF8_PassthroughValid(t *testing.T) {
	in := "hello 世界 🎉"
	out := sanitizeUTF8(in)
	if out != in {
		t.Fatalf("expected unchanged %q, got %q", in, out)
	}
}

func TestSanitizeUTF8_EscapesBackslashes(t *testing.T) {
	// Backslashes must be escaped to prevent SQLSTATE 22P05
	// "unsupported Unicode escape sequence" when PostgreSQL
	// misinterprets \uXXXX-like sequences.
	in := `hello\u4e2d\u世界的\d`
	out := sanitizeUTF8(in)
	// Every backslash must be doubled
	if !strings.Contains(out, "\\\\u") {
		t.Errorf("expected escaped backslash in %q", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("result is invalid UTF-8: %q", out)
	}
}

func TestSanitizeUTF8_InvalidBytesPlusBackslash(t *testing.T) {
	// Invalid UTF-8 bytes followed by a backslash: both the invalid
	// bytes and the backslash must be handled correctly.
	invalid := string([]byte{0xe5, 0xbc, 0xe2}) + `\u`
	out := sanitizeUTF8(invalid)
	if !utf8.ValidString(out) {
		t.Errorf("result is invalid UTF-8: %q (bytes %x)", out, []byte(out))
	}
	if !strings.Contains(out, "\\\\") {
		t.Errorf("expected escaped backslash in %q", out)
	}
}

func TestEscapeInvalidEscape(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "valid hex backslash-u escape is preserved",
			in:   "hello\u4e2d\u4e16",
			want: "hello\u4e2d\u4e16", // backslash + u + 4 hex chars, leave alone
		},
		{
			name: "invalid backslash-uZZZ gets backslash doubled",
			in:   `x\uZZZZy`,
			want: `x\\uZZZZy`, // 4 hex digits but Z is not hex -> double backslash
		},
		{
			name: "trailing backslash-u without 4 chars gets backslash doubled",
			in:   `x\uabcy`,
			want: `x\\uabcy`, // only 3 chars (abc) after \u -> double backslash
		},
		{
			name: "lone backslash is left alone",
			in:   `x\\y`,
			want: `x\\y`,
		},
		{
			name: "empty input returns empty",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeInvalidEscape(tc.in)
			if got != tc.want {
				t.Errorf("escapeInvalidEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeUTF8JSON_PreservesBackslashesInJSON(t *testing.T) {
	// JSONB fields must NOT have backslashes escaped — that would corrupt the JSON.
	json := `{"content":"hello\nworld"}`
	out := sanitizeUTF8JSON(json)
	if out != json {
		t.Errorf("JSON backslash was escaped: got %q", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("result is invalid UTF-8: %q", out)
	}
}

func TestSanitizeUTF8JSON_TruncatesToLastValidBoundary(t *testing.T) {
	// Simulates stream capture appending garbage after a complete JSON object.
	truncated := `{"a":1}{"garbage`
	want := `{"a":1}`
	out := sanitizeUTF8JSON(truncated)
	if out != want {
		t.Fatalf("expected %q, got %q", want, out)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("repaired JSON still invalid: %q", out)
	}
}

func TestSanitizeUTF8JSON_InvalidJSONReturnsEmpty(t *testing.T) {
	out := sanitizeUTF8JSON(`not json at all`)
	if out != "" {
		t.Fatalf("expected empty fallback, got %q", out)
	}
}

func TestSanitizeJSONField_NilOnUnrecoverableJSON(t *testing.T) {
	body := `{broken`
	field := &body
	sanitizeJSONField("request_body", &field)
	if field != nil {
		t.Fatalf("expected nil body, got %q", *field)
	}
}

func TestSanitizeJSONField_PreservesValidJSON(t *testing.T) {
	body := `{"ok":true}`
	field := &body
	sanitizeJSONField("request_body", &field)
	if field == nil || *field != body {
		t.Fatalf("expected unchanged JSON, got %v", field)
	}
}

func TestSanitizeRequestLogEntry_JSONBFieldsNotEscaped(t *testing.T) {
	// JSONB fields should keep their backslashes; text fields should escape them.
	jsonBody := `{"content":"a\nb"}`
	backslashField := `hello\u4e2d`
	mkPtr := func(s string) *string { return &s }
	entry := &RequestLogEntry{
		RequestBody:  mkPtr(jsonBody),
		ResponseBody: mkPtr(jsonBody),
		ClientModel:  mkPtr(backslashField),
	}
	sanitizeRequestLogEntry(entry)
	if *entry.RequestBody != jsonBody {
		t.Errorf("RequestBody backslash was escaped: got %q", *entry.RequestBody)
	}
	if *entry.ResponseBody != jsonBody {
		t.Errorf("ResponseBody backslash was escaped: got %q", *entry.ResponseBody)
	}
	if !strings.Contains(*entry.ClientModel, "\\\\") {
		t.Errorf("ClientModel backslash was NOT escaped: got %q", *entry.ClientModel)
	}
}

func TestSanitizeRequestLogEntry_DiscardsInvalidJSONBFields(t *testing.T) {
	valid := json.RawMessage(`{"valid":true}`)
	invalid := json.RawMessage(`{"broken"`)
	invalidDecision := `{"broken"`
	entry := &RequestLogEntry{
		AutoDecision:      &invalidDecision,
		CompressionMeta:   invalid,
		OutboundBody:      invalid,
		OutboundMsgHashes: invalid,
		QualityFixActions: invalid,
		ToolCalls:         invalid,
		Attachments:       invalid,
		RoutingAttempts:   invalid,
	}

	sanitizeRequestLogEntry(entry)

	if entry.AutoDecision != nil {
		t.Fatalf("AutoDecision = %q, want nil", *entry.AutoDecision)
	}
	for name, raw := range map[string]json.RawMessage{
		"CompressionMeta":   entry.CompressionMeta,
		"OutboundBody":      entry.OutboundBody,
		"OutboundMsgHashes": entry.OutboundMsgHashes,
		"QualityFixActions": entry.QualityFixActions,
		"ToolCalls":         entry.ToolCalls,
		"Attachments":       entry.Attachments,
		"RoutingAttempts":   entry.RoutingAttempts,
	} {
		if raw != nil {
			t.Errorf("%s = %q, want nil", name, raw)
		}
	}

	entry.CompressionMeta = valid
	sanitizeRequestLogEntry(entry)
	if string(entry.CompressionMeta) != string(valid) {
		t.Errorf("CompressionMeta = %q, want %q", entry.CompressionMeta, valid)
	}
}

func TestSanitizeRequestLogEntry_ScrubsAllStringFields(t *testing.T) {
	invalid := string([]byte{0xE5, 0xBC, 0xE2, 0x80, 0xA6})
	mkPtr := func(s string) *string { return &s }
	entry := &RequestLogEntry{
		RequestID:        invalid,
		TenantID:         invalid,
		EndUserID:        mkPtr(invalid),
		ClientModel:      mkPtr(invalid),
		OutboundModel:    mkPtr(invalid),
		ResponseBody:     mkPtr(invalid),
		RequestPreview:   mkPtr(invalid),
		ResponsePreview:  mkPtr(invalid),
		TransformSummary: mkPtr(invalid),
	}
	sanitizeRequestLogEntry(entry)

	for name, s := range map[string]string{
		"RequestID":        entry.RequestID,
		"TenantID":         entry.TenantID,
		"EndUserID":        derefStr(entry.EndUserID),
		"ClientModel":      derefStr(entry.ClientModel),
		"OutboundModel":    derefStr(entry.OutboundModel),
		"ResponseBody":     derefStr(entry.ResponseBody),
		"RequestPreview":   derefStr(entry.RequestPreview),
		"ResponsePreview":  derefStr(entry.ResponsePreview),
		"TransformSummary": derefStr(entry.TransformSummary),
	} {
		if !utf8.ValidString(s) {
			t.Errorf("%s remained invalid: %q", name, s)
		}
	}
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
