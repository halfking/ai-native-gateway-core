package streaming

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests guard the "silent data drop" defect class: content discarded on a
// fallback path with no log and no anomaly record, so the loss is invisible in
// production. See docs/VIBECODING_GUIDELINES.md §3.

// extractFirstUserMessage must distinguish "body did not parse" from "parsed
// fine, no user message". Both return "" and both make the session-audit hook
// degrade to Pass, so without the flag an audit BYPASS looks like a clean pass.
func TestExtractFirstUserMessage_ReportsParseFailure(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantParsed bool
		wantText   string
	}{
		{"valid with user message", `{"messages":[{"role":"user","content":"hi"}]}`, true, "hi"},
		{"valid without user message", `{"messages":[{"role":"system","content":"x"}]}`, true, ""},
		{"valid but no messages key", `{"model":"gpt"}`, true, ""},
		{"unparseable body", `{"messages":[{"role":`, false, ""},
		{"not json at all", `garbage`, false, ""},
		{"empty", ``, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, parsed := extractFirstUserMessage([]byte(tc.body))
			if parsed != tc.wantParsed {
				t.Errorf("parsed = %v, want %v", parsed, tc.wantParsed)
			}
			if text != tc.wantText {
				t.Errorf("text = %q, want %q", text, tc.wantText)
			}
		})
	}
}

// mergeCompressionMetaV3 must never merge into an empty map after a decode
// failure: doing so returns a blob containing only the two new keys and
// silently deletes the pre-existing v7 compression fields.
func TestMergeCompressionMetaV3_PreservesExistingOnDecodeFailure(t *testing.T) {
	corrupt := json.RawMessage(`{"v7_field":`) // truncated, will not decode

	got, err := mergeCompressionMetaV3(corrupt, "window-a", "marker-b", "", nil, 0)
	if err == nil {
		t.Fatal("expected a decode error to be reported, got nil")
	}
	if string(got) != string(corrupt) {
		t.Fatalf("existing blob was not preserved:\n got=%s\nwant=%s", got, corrupt)
	}
	if strings.Contains(string(got), "window_triggered") {
		t.Error("new keys were merged over a corrupt blob, deleting the original fields")
	}
}

func TestMergeCompressionMetaV3_MergesWhenDecodable(t *testing.T) {
	existing := json.RawMessage(`{"v7_strategy":"mechanical"}`)

	got, err := mergeCompressionMetaV3(existing, "window-a", "marker-b", "", nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("result is not valid JSON: %s", got)
	}
	// The pre-existing v7 field must survive alongside the new keys.
	if m["v7_strategy"] != "mechanical" {
		t.Errorf("v7_strategy was lost: %s", got)
	}
	if m["window_triggered"] != "window-a" || m["summary_marker"] != "marker-b" {
		t.Errorf("new keys missing: %s", got)
	}
}

// attachmentsJSON must let the caller tell "no attachments" from "encoding
// failed" — both previously returned a bare nil.
func TestAttachmentsJSON_EmptyIsNotAnError(t *testing.T) {
	got, err := attachmentsJSON(nil)
	if err != nil {
		t.Fatalf("empty input must not be an error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty input, got %s", got)
	}
}
