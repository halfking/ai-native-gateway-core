package telemetry

import (
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

func TestSanitizeRequestLogEntry_JSONBFieldsNotEscaped(t *testing.T) {
	// JSONB fields should keep their backslashes; text fields should escape them.
	jsonBody := `{"content":"a\nb"}`
	backslashField := `hello\u4e2d`
	mkPtr := func(s string) *string { return &s }
	entry := &RequestLogEntry{
		RequestBody:    mkPtr(jsonBody),
		ResponseBody:   mkPtr(jsonBody),
		ClientModel:    mkPtr(backslashField),
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
