package streaming

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpstreamGoldenOrdinaryModeNeverWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "body.json")
	original := []byte("{ \"n\" : 9007199254740993 }\n")
	if err := upstreamBytesGolden(path, original, false); err == nil {
		t.Fatal("missing golden accepted")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("ordinary mode created directory: %v", err)
	}
	if err := upstreamBytesGolden(path, original, true); err != nil {
		t.Fatal(err)
	}
	if err := upstreamBytesGolden(path, bytes.TrimSpace(original), false); err == nil {
		t.Fatal("byte mismatch accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("ordinary mode changed golden: %q %v", got, err)
	}
	if err := upstreamBytesGolden(path, original, false); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamGoldenFixtureRejectsOnlyTopLevelWireFields(t *testing.T) {
	fake := &upstreamBytesFixture{protocol: "responses"}
	cases := []struct {
		body   string
		status int
	}{
		{`{"model":"m","input":"hi","unknown":{"stream_options":{},"messages":[]}}`, 200},
		{`{"model":"m","input":"hi","stream_options":{}}`, 400},
		{`{"model":"m","input":"hi","reasoning_effort":"high"}`, 400},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		fake.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.body)))
		if w.Code != tc.status || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("fixture status=%d body=%s", w.Code, w.Body.String())
		}
	}
	attempts := fake.captured()
	if len(attempts) != len(cases) {
		t.Fatalf("lost attempts: %d", len(attempts))
	}
	for i, attempt := range attempts {
		if string(attempt.Body) != cases[i].body {
			t.Fatalf("attempt %d reordered or normalized: %s", i, attempt.Body)
		}
	}
}

func TestUpstreamGoldenSemanticsRejectMissingOrRoundedPremises(t *testing.T) {
	if err := upstreamBytesSemantics([]byte(upstreamBytesChat), "chat"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(upstreamBytesChat, "9007199254740993", "9007199254740992", 1),
		strings.Replace(upstreamBytesChat, `"unknown_field"`, `"lost_unknown_field"`, 1),
		strings.Replace(upstreamBytesChat, `"tool_calls"`, `"lost_tool_calls"`, 1),
		strings.ReplaceAll(upstreamBytesChat, "system-prefix", "lost-prefix"),
		strings.ReplaceAll(upstreamBytesChat, "follow-up", "lost-followup"),
		strings.ReplaceAll(upstreamBytesChat, "tool-result", "lost-result"),
	} {
		if err := upstreamBytesSemantics([]byte(bad), "chat"); err == nil {
			t.Fatalf("semantic oracle accepted broken premise: %s", bad)
		}
	}
}

func TestUpstreamGoldenToolRoundRejectsWrongPairAndOrder(t *testing.T) {
	for _, bad := range []string{
		strings.Replace(upstreamBytesChat, `"tool_call_id":"call_1"`, `"tool_call_id":"call_other"`, 1),
		strings.Replace(upstreamBytesChat, `/a b/漢字`, `/wrong-path`, 1),
	} {
		if err := upstreamBytesToolRound([]byte(bad), "chat"); err == nil {
			t.Fatal("wrong tool mapping/arguments accepted")
		}
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(upstreamBytesChat), &fields)
	var messages []json.RawMessage
	_ = json.Unmarshal(fields["messages"], &messages)
	messages[3], messages[4] = messages[4], messages[3]
	fields["messages"], _ = json.Marshal(messages)
	reordered, _ := json.Marshal(fields)
	if err := upstreamBytesToolRound(reordered, "chat"); err == nil {
		t.Fatal("follow-up before tool result accepted")
	}
}
