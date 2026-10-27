package v2

import (
	"strings"
	"testing"
)

func TestBoundBodiesRecord_TruncatesAndMarks(t *testing.T) {
	// Use printable prose — pure "x"*N is redacted as a credential shape
	// by errorsx.RedactCredentialShapes before the size bound runs.
	huge := strings.Repeat("word ", MaxTurnBodyBytes/5)
	rec := BodiesRecord{
		RequestDelta:  []Message{{Role: "user", Content: huge}},
		ResponseDelta: []Message{{Role: "assistant", Content: "ok"}},
	}
	if !boundBodiesRecord(&rec) {
		t.Fatal("expected truncation")
	}
	markTruncation(&rec)
	if len(rec.RequestDelta) == 0 {
		t.Fatal("request delta emptied unexpectedly")
	}
	if messageWireSize(rec.RequestDelta[0]) > MaxTurnBodyBytes/2+64 {
		t.Fatalf("truncated message still too large: %d", messageWireSize(rec.RequestDelta[0]))
	}
	found := false
	for _, m := range rec.ResponseDelta {
		if strings.Contains(m.Content, "truncated") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing truncation marker")
	}
}

func TestScrubMessage_RedactsBearer(t *testing.T) {
	m := scrubMessage(Message{Role: "user", Content: "Authorization: Bearer sk-secret-token-value-here"})
	if strings.Contains(m.Content, "sk-secret") {
		t.Fatalf("secret not scrubbed: %q", m.Content)
	}
}

func TestRecordingConsentAllowsWrite_Hook(t *testing.T) {
	enableRecordingConsentForTest(t)
	if !RecordingConsentAllowsWrite("t1", 0) {
		t.Fatal("hook should allow")
	}
}

func TestRecordingConsentAllowsWrite_DefaultOff(t *testing.T) {
	prev := allowBodiesWriteHook
	allowBodiesWriteHook = nil
	t.Cleanup(func() { allowBodiesWriteHook = prev })
	if RecordingConsentAllowsWrite("no-such-tenant", 0) {
		t.Fatal("default must be off (fail closed)")
	}
}
