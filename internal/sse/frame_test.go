package sse

import (
	"bytes"
	"testing"
)

func TestFrameEndRecognizesLFCRLFCRAndMixedTerminators(t *testing.T) {
	for _, delimiter := range []string{"\n\n", "\r\n\r\n", "\r\r", "\n\r\n", "\r\n\n", "\n\r", "\r\n\r"} {
		t.Run(delimiter, func(t *testing.T) {
			frame := []byte("data: {\"ok\":true}" + delimiter + "next")
			end, ok := FrameEnd(frame)
			if !ok || string(frame[end:]) != "next" {
				t.Fatalf("FrameEnd(%q) = %d, %v", frame, end, ok)
			}
			if got := string(frame[:end]); got != "data: {\"ok\":true}"+delimiter {
				t.Fatalf("frame = %q, want original delimiter %q", got, delimiter)
			}
		})
	}
}

func TestFrameEndDefersTrailingCRUntilMoreDataOrEOF(t *testing.T) {
	frame := []byte("data: [DONE]\r\r")
	if end, ok := FrameEnd(frame); ok {
		t.Fatalf("FrameEnd prematurely accepted ambiguous trailing CR: %d", end)
	}
	if end, ok := FrameEndAtEOF(frame); !ok || end != len(frame) {
		t.Fatalf("FrameEndAtEOF = %d, %v; want end=%d", end, ok, len(frame))
	}
	if got, ok := DelimiterPrefixAtBoundary([]byte("data: [DONE]\r"), []byte("\rnext")); !ok || len(got) != 1 || got[0] != '\r' {
		t.Fatalf("DelimiterPrefixAtBoundary = %q, %v; want second CR", got, ok)
	}
}

func TestParseDataFrameJoinsFieldsAndPreservesCRLFFraming(t *testing.T) {
	frame := []byte("event: content\r\ndata:{\"text\":\r\ndata: \"a\"}\r\n\r\n")
	wantPayload := []byte("{\"text\":\n\"a\"}")
	payload, hasData, rewrite := ParseDataFrame(frame)
	if !hasData || !bytes.Equal(payload, wantPayload) {
		t.Fatalf("ParseDataFrame payload=%q hasData=%v, want %q", payload, hasData, wantPayload)
	}
	got := rewrite([]byte(`{"text":"masked"}`))
	want := []byte("event: content\r\ndata:{\"text\":\"masked\"}\r\n\r\n")
	if !bytes.Equal(got, want) {
		t.Fatalf("rewritten frame = %q, want %q", got, want)
	}
}
