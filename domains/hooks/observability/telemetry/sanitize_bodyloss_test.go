package telemetry

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
)

// The invariant these tests protect:
//
//	Valid JSON in  ==>  valid, non-empty JSON out.
//
// Losing a request body is silent — a NULL request_body is indistinguishable
// from a request that never carried one. That is how the escapeInvalidEscape
// regression (36998a96) survived in production: it rewrote legitimately escaped
// backslashes in VALID JSON, truncateToValidJSON then found no valid prefix,
// and complete request bodies were persisted as SQL NULL.
//
// Any future change to the sanitize pipeline must keep this invariant.

// realWorldBodies are shapes actually seen in gateway traffic. Each is valid
// JSON and each MUST survive sanitization intact.
var realWorldBodies = []struct {
	name string
	body string
}{
	{
		name: "windows path (literal backslash before u)",
		body: `{"messages":[{"role":"user","content":"open C:\\users\\me\\notes.txt"}]}`,
	},
	{
		name: "latex macro",
		body: `{"messages":[{"role":"user","content":"\\usepackage{amsmath}"}]}`,
	},
	{
		name: "regex with backslash-u text",
		body: `{"messages":[{"role":"user","content":"match \\uabc then stop"}]}`,
	},
	{
		name: "genuine unicode escape",
		body: `{"messages":[{"role":"user","content":"\u4e2d\u6587"}]}`,
	},
	{
		name: "escaped quote and newline",
		body: `{"messages":[{"role":"user","content":"say \"hi\"\nthen bye"}]}`,
	},
	{
		name: "nested json-in-string tool result",
		body: `{"messages":[{"role":"tool","content":"{\"ok\":true,\"path\":\"C:\\\\tmp\"}"}]}`,
	},
	{
		name: "trailing backslash inside string",
		body: `{"messages":[{"role":"user","content":"dir name\\"}]}`,
	},
	{
		name: "consecutive escaped backslashes",
		body: `{"messages":[{"role":"user","content":"\\\\server\\share"}]}`,
	},
}

func TestSanitizeUTF8JSON_ValidJSONAlwaysSurvives(t *testing.T) {
	for _, tc := range realWorldBodies {
		t.Run(tc.name, func(t *testing.T) {
			if !json.Valid([]byte(tc.body)) {
				t.Fatalf("test fixture is not valid JSON: %s", tc.body)
			}
			got := sanitizeUTF8JSON(tc.body)
			if got == "" {
				t.Fatalf("valid JSON was discarded entirely (would persist as NULL): %s", tc.body)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("sanitizer produced invalid JSON:\n in: %s\nout: %s", tc.body, got)
			}
			if got != tc.body {
				t.Errorf("valid JSON was modified:\n in: %s\nout: %s", tc.body, got)
			}
		})
	}
}

// The user-visible symptom: request_body persisted as NULL while the request
// itself succeeded.
func TestSanitizeRequestLogEntry_RequestBodyNotDroppedForValidJSON(t *testing.T) {
	for _, tc := range realWorldBodies {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			e := &RequestLogEntry{RequestID: "req-1", TenantID: "t1", RequestBody: &body}
			sanitizeRequestLogEntry(e)
			if e.RequestBody == nil {
				t.Fatalf("RequestBody dropped to nil for valid JSON: %s", tc.body)
			}
			if strPtrToJSON(e.RequestBody) == "null" {
				t.Fatalf("RequestBody would bind as SQL NULL: %s", tc.body)
			}
		})
	}
}

// \u0000 is the one escape that is valid JSON but rejected by PostgreSQL's
// jsonb cast (SQLSTATE 22P05). It must be rewritten, not dropped.
func TestSanitizeUTF8JSON_NullEscapeNeutralizedNotDropped(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"before\u0000after"}]}`
	if !json.Valid([]byte(body)) {
		t.Fatalf("fixture not valid JSON")
	}
	got := sanitizeUTF8JSON(body)
	if got == "" {
		t.Fatal("body containing \\u0000 was discarded instead of repaired")
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("result is not valid JSON: %s", got)
	}
	if strings.Contains(got, `\u0000`) {
		t.Errorf("\\u0000 survived; PostgreSQL jsonb cast will fail with 22P05: %s", got)
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding content was lost: %s", got)
	}
}

// A literal backslash followed by the text u0000 is NOT a null escape and must
// not be rewritten.
func TestSanitizeUTF8JSON_LiteralBackslashU0000Preserved(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"literal \\u0000 text"}]}`
	got := sanitizeUTF8JSON(body)
	if got != body {
		t.Errorf("literal backslash sequence was rewritten:\n in: %s\nout: %s", body, got)
	}
}

// Genuinely broken input should still be repaired or dropped rather than
// producing something PostgreSQL will reject.
func TestSanitizeUTF8JSON_InvalidInputStillHandled(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"garbage after valid object", `{"a":1}{"garb`},
		{"bad hex escape", `{"a":"x\uZZZZy"}`},
		{"not json", `not json at all`},
		{"truncated mid-string", `{"messages":[{"role":"user","content":"half`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeUTF8JSON(tc.in)
			if got != "" && !json.Valid([]byte(got)) {
				t.Fatalf("sanitizer returned non-empty invalid JSON: %q", got)
			}
		})
	}
}

// escapeInvalidEscape must treat a backslash as consuming the next character,
// so an escaped backslash pair is never re-read as an escape introducer.
func TestEscapeInvalidEscape_DoesNotCorruptEscapedBackslash(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"escaped backslash then u", `"C:\\users"`, `"C:\\users"`},
		{"escaped backslash then non-u", `"a\\b"`, `"a\\b"`},
		{"four backslashes then u", `"\\\\users"`, `"\\\\users"`},
		{"real escape preserved", `"\u4e2d"`, `"\u4e2d"`},
		{"bad hex still doubled", `x\uZZZZy`, `x\\uZZZZy`},
		{"short hex still doubled", `x\uabcy`, `x\\uabcy`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeInvalidEscape(tc.in); got != tc.want {
				t.Errorf("escapeInvalidEscape(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Property test: for ANY string content, marshaling it into a request body
// produces valid JSON, and that JSON must survive sanitization. This catches
// the whole class of "sanitizer corrupts valid JSON" bugs rather than only the
// specific characters known to have broken it before.
func TestSanitizeUTF8JSON_PropertyValidJSONNeverDropped(t *testing.T) {
	fragments := []string{
		`\`, `\\`, `\\\`, `\u`, `\u0041`, `\uZZZZ`, `u0000`,
		`"`, `'`, "\n", "\t", `{`, `}`, `[`, `]`,
		`C:\users`, `\usepackage`, `中文`, `emoji 🙂`,
		`</script>`, `%s`, `${x}`, "\x00", "\ufffd",
	}
	for i, a := range fragments {
		for j, b := range fragments {
			content := a + "mid" + b
			// Marshal through encoding/json: output is valid JSON by construction.
			raw, err := json.Marshal(map[string]any{
				"messages": []any{map[string]any{"role": "user", "content": content}},
			})
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}
			body := string(raw)
			if !json.Valid([]byte(body)) {
				t.Fatalf("encoding/json produced invalid JSON, impossible")
			}
			got := sanitizeUTF8JSON(body)
			if got == "" {
				t.Fatalf("case (%d,%d): valid JSON dropped entirely (persists as NULL)\ncontent=%q\nbody=%s", i, j, content, body)
			}
			if !json.Valid([]byte(got)) {
				t.Fatalf("case (%d,%d): sanitizer emitted invalid JSON\nin =%s\nout=%s", i, j, body, got)
			}
			if strings.Contains(got, `\u0000`) {
				t.Fatalf("case (%d,%d): \\u0000 survived, jsonb cast will fail 22P05\nout=%s", i, j, got)
			}
		}
	}
}

// The sanitizers are hand-written byte scanners with index arithmetic, so the
// bounds must hold for arbitrary input — including truncated escapes at the end
// of the string. Two output invariants are asserted: never panic, and never
// emit non-empty invalid JSON (which would fail the jsonb cast at write time).
func TestSanitize_NoPanicAndNeverEmitsInvalidJSON(t *testing.T) {
	alphabet := []string{`\`, `u`, `0`, `"`, `{`, `}`, `a`, `Z`, "\x00", "中", `[`, `]`, `:`, `,`}
	r := rand.New(rand.NewSource(42)) // fixed seed keeps failures reproducible
	for i := 0; i < 200000; i++ {
		var sb strings.Builder
		for j := 0; j < r.Intn(12); j++ {
			sb.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		s := sb.String()
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Fatalf("panic on input %q: %v", s, rec)
				}
			}()
			_ = escapeInvalidEscape(s)
			_ = neutralizeNullUnicodeEscape(s)
			if out := sanitizeUTF8JSON(s); out != "" && !json.Valid([]byte(out)) {
				t.Fatalf("emitted non-empty invalid JSON for %q: %q", s, out)
			}
		}()
	}
}

// The repair path that the original fix (36998a96) was written for must keep
// working: a \uXXXX-shaped sequence with non-hex digits is invalid JSON, and
// doubling the backslash makes it castable to jsonb as harmless text.
func TestSanitizeUTF8JSON_BadHexEscapeStillRepaired(t *testing.T) {
	in := `{"a":"x\uZZZZy"}`
	if json.Valid([]byte(in)) {
		t.Fatal("fixture should be invalid JSON")
	}
	got := sanitizeUTF8JSON(in)
	if got == "" {
		t.Fatal("repairable input was dropped instead of repaired")
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("repair produced invalid JSON: %s", got)
	}
	if !strings.Contains(got, `\\u`) {
		t.Errorf("expected doubled backslash, got %s", got)
	}
}

// sanitizeJSONField and sanitizeRawJSONField are the *string and
// json.RawMessage halves of the same operation. When they drift apart, one
// half loses data in a way the other reports — which is how the truncation
// gap in sanitizeRawJSONField survived the first pass of this very fix.
func TestSanitize_StringAndRawFieldsAgree(t *testing.T) {
	inputs := []string{
		`{"messages":[{"role":"user","content":"open C:\\users\\me"}]}`,
		`{"messages":[{"role":"user","content":"\\usepackage{x}"}]}`,
		`{"a":"x\u0000y"}`,
		`{"a":1}{"garbage`,
		`not json at all`,
		`{"a":"x\uZZZZy"}`,
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			s := in
			p := &s
			sanitizeJSONField("request_body", &p)

			raw := json.RawMessage(in)
			sanitizeRawJSONField("outbound_body", &raw)

			strDropped := p == nil
			rawDropped := len(raw) == 0
			if strDropped != rawDropped {
				t.Fatalf("drop decision differs: string dropped=%v, raw dropped=%v", strDropped, rawDropped)
			}
			if !strDropped && *p != string(raw) {
				t.Fatalf("outputs differ:\nstring=%s\nraw   =%s", *p, string(raw))
			}
		})
	}
}
