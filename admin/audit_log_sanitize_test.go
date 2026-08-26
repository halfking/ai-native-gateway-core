package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeJSONBPayload_AlreadyValid(t *testing.T) {
	in := []byte(`{"ip":"1.2.3.4","ok":true}`)
	got := sanitizeJSONBPayload(in)
	if string(got) != string(in) {
		t.Fatalf("expected passthrough, got %s", string(got))
	}
}

func TestSanitizeJSONBPayload_NULBytes(t *testing.T) {
	// PG JSONB rejects \x00 with SQLSTATE 22021. Confirm sanitiser
	// removes them and re-validates.
	in := []byte(`{"v":"before` + "\x00" + `after"}`)
	got := sanitizeJSONBPayload(in)
	for _, b := range got {
		if b == 0 {
			t.Fatalf("NUL byte leaked: %q", string(got))
		}
	}
	if !json.Valid(got) {
		t.Fatalf("not valid JSON after sanitize: %q", string(got))
	}
}

func TestSanitizeJSONBPayload_InvalidUTF8InValue(t *testing.T) {
	in := []byte("{\"v\":\"\xc3\x28\"}") // invalid UTF-8 sequence
	got := sanitizeJSONBPayload(in)
	t.Logf("got hex: % x", got)
	t.Logf("got str: %s", string(got))
	if !json.Valid(got) {
		t.Fatalf("not valid JSON after sanitize: %q", string(got))
	}
	if strings.Contains(string(got), "\xc3\x28") {
		t.Fatalf("invalid UTF-8 leaked: %q", string(got))
	}
}

func TestSanitizeJSONBPayload_UnrecoverableReturnsNull(t *testing.T) {
	// Raw garbage that can't be salvaged.
	in := []byte("not json at all {[}")
	got := sanitizeJSONBPayload(in)
	if string(got) != "null" {
		t.Fatalf("expected null fallback, got %s", string(got))
	}
}

func TestAuditJSONBValidationFallbackEscapesPayload(t *testing.T) {
	fallback := auditJSONBValidationFallback([]byte(`{"raw":"quote\\slash"}`))
	if !json.Valid(fallback) {
		t.Fatalf("fallback is invalid JSON: %q", fallback)
	}
	var decoded map[string]string
	if err := json.Unmarshal(fallback, &decoded); err != nil {
		t.Fatalf("decode fallback: %v", err)
	}
	if decoded["raw"] != `{"raw":"quote\\slash"}` {
		t.Fatalf("fallback raw=%q", decoded["raw"])
	}
}

func TestIsJSONBValidationError(t *testing.T) {
	cases := map[error]bool{
		nil: false,
		&fakeErr{msg: "ERROR: invalid input syntax for type json (SQLSTATE 22P02)"}:                true,
		&fakeErr{msg: "ERROR: invalid byte sequence for encoding \"UTF8\": 0x00 (SQLSTATE 22021)"}: true,
		&fakeErr{msg: "ERROR: duplicate key value violates unique constraint (SQLSTATE 23505)"}:    false,
		&fakeErr{msg: "connection refused"}:                                                        false,
	}
	for err, want := range cases {
		if got := isJSONBValidationError(err); got != want {
			t.Fatalf("isJSONBValidationError(%v)=%v want %v", err, got, want)
		}
	}
}

type fakeErr struct{ msg string }

func (e *fakeErr) Error() string { return e.msg }
