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
