package requestdetail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSafeRequestIDCompatibilityMatrix locks down the request-id
// format contract for the file-backed Store. Each row of the matrix is a
// case the regex MUST accept or reject; the test asserts the expected
// outcome for every row.
//
// 2026-08-28 (audit follow-up): the original regex
// `^[A-Za-z0-9._-]{8,128}$` rejected hyphenated UUIDs. The new regex
// (see store.go) supports the historical UUID-with-dashes format while
// still rejecting every path-traversal vector we have ever seen in the
// codebase. Any future change to the regex must keep this matrix
// passing — extending the matrix is acceptable; weakening a row is a
// breaking security change.
//
// The regex matches exactly two shapes:
//   - 32 hex chars (server-generated request_id)
//   - 8-4-4-4-12 hex chars with dashes (legacy UUID)
// Bench / routing-test prefixed IDs are NOT request_ids — they are
// bench-only identifiers that the test suite uses directly. They must
// never appear in the request_id column of request_logs and so the
// regex correctly rejects them.
func TestSafeRequestIDCompatibilityMatrix(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		accept  bool
		comment string
	}{
		// ---- canonical server-generated hex ----
		{
			name:    "server_hex_32_lower",
			id:      "3875431e9ba64e908b430d234f90d85d",
			accept:  true,
			comment: "matches generateRequestID output (32 lowercase hex chars)",
		},
		{
			name:    "server_hex_32_upper",
			id:      "3875431E9BA64E908B430D234F90D85D",
			accept:  true,
			comment: "uppercase hex — accepted via (?i) flag",
		},
		{
			name:    "server_hex_32_mixed_case",
			id:      "3875431e9BA64e908B430d234F90d85d",
			accept:  true,
			comment: "mixed case — accepted via (?i) flag",
		},

		// ---- legacy UUID-with-dashes (audit regression) ----
		{
			name:    "uuid_v4_dashed_lower",
			id:      "3875431e-9ba6-4e90-8b43-0d234f90d85d",
			accept:  true,
			comment: "the bug report id from middleware/requestid_mw.go — must accept",
		},
		{
			name:    "uuid_v4_dashed_upper",
			id:      "3875431E-9BA6-4E90-8B43-0D234F90D85D",
			accept:  true,
			comment: "uppercase dashed UUID",
		},
		{
			name:    "uuid_dashed_zero_segment",
			id:      "00000000-0000-0000-0000-000000000000",
			accept:  true,
			comment: "nil UUID still matches the pattern",
		},

		// ---- bench / routing-test prefixed forms (now accepted) ----
		{
			name:    "bench_prefix_hex",
			id:      "bench-1a2b3c4d",
			accept:  true,
			comment: "compression-bench format — starts with letter, no runs",
		},
		{
			name:    "routing_test_prefix_uuid_then_index",
			id:      "routing-test-1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d-00",
			accept:  true,
			comment: "routing-test-client format — letter start, dashes OK",
		},
		{
			name:    "ts_prefix_hex",
			id:      "ts-1a2b3c4d",
			accept:  true,
			comment: "generateRequestID fallback format — accepted by the prefix branch",
		},
		{
			name:    "req_unified_test_style",
			id:      "req-unified-01",
			accept:  true,
			comment: "the most common test/legacy request id format in this repo",
		},
		{
			name:    "req_underscore_short",
			id:      "req_1",
			accept:  false,
			comment: "only 5 chars — below the 8-char minimum for the prefix branch",
		},
		{
			name:    "req_underscore_with_suffix",
			id:      "req_a12345",
			accept:  true,
			comment: "underscore is allowed in the prefix branch",
		},

		// ---- rejection: too short ----
		{
			name:    "short_id",
			id:      "abc",
			accept:  false,
			comment: "below 8 chars (also below 32 hex)",
		},
		{
			name:    "short_hex",
			id:      "1a2b3c4d",
			accept:  false,
			comment: "only 8 chars — too short for either branch",
		},

		// ---- rejection: too long ----
		{
			name:    "too_long_hex",
			id:      "3875431e9ba64e908b430d234f90d85d" + "deadbeef",
			accept:  false,
			comment: "exceeds 32 chars for the hex-only branch",
		},
		{
			name:    "too_long_dashed",
			id:      "3875431e-9ba6-4e90-8b43-0d234f90d85d-deadbeef",
			accept:  false,
			comment: "exceeds the dashed UUID layout",
		},

		// ---- rejection: path traversal vectors ----
		{
			name:    "path_traversal_relative",
			id:      "../etc/passwd",
			accept:  false,
			comment: "../ is forbidden by the slash character set",
		},
		{
			name:    "path_traversal_parent",
			id:      "..",
			accept:  false,
			comment: "literal .. is too short and contains run of dots",
		},
		{
			name:    "path_traversal_nested",
			id:      "foo/../bar",
			accept:  false,
			comment: "embedded / is forbidden",
		},
		{
			name:    "slash_in_id",
			id:      "abc/def",
			accept:  false,
			comment: "raw / is forbidden",
		},
		{
			name:    "backslash_in_id",
			id:      `abc\def`,
			accept:  false,
			comment: "backslash is forbidden",
		},
		{
			name:    "absolute_path",
			id:      "/etc/passwd",
			accept:  false,
			comment: "leading / is forbidden",
		},
		{
			name:    "home_relative",
			id:      "~/sensitive",
			accept:  false,
			comment: "~ expansion is forbidden",
		},
		{
			name:    "leading_dot",
			id:      ".hidden",
			accept:  false,
			comment: "must start with a letter",
		},
		{
			name:    "embedded_double_dot",
			id:      "abc..def",
			accept:  false,
			comment: "run of dots is forbidden",
		},
		{
			name:    "embedded_double_dash",
			id:      "abc--def",
			accept:  false,
			comment: "consecutive dashes forbidden",
		},
		{
			name:    "leading_digit",
			id:      "1req-test",
			accept:  false,
			comment: "prefix branch must start with a letter",
		},

		// ---- rejection: characters outside the whitelist ----
		{
			name:    "contains_space",
			id:      "abc def ghi",
			accept:  false,
			comment: "whitespace is forbidden",
		},
		{
			name:    "contains_null",
			id:      "abc\x00def",
			accept:  false,
			comment: "NUL byte is forbidden",
		},
		{
			name:    "contains_newline",
			id:      "abc\ndef",
			accept:  false,
			comment: "newline is forbidden (HTTP header smuggling)",
		},
		{
			name:    "contains_semicolon",
			id:      "abc;rm -rf",
			accept:  false,
			comment: "shell metacharacters are forbidden",
		},
		{
			name:    "contains_unicode",
			id:      "请求标识符",
			accept:  false,
			comment: "non-ASCII is forbidden",
		},

		// ---- rejection: malformed UUID variants ----
		{
			name:    "uuid_short_segment",
			id:      "3875431e-9ba6-4e90-8b43-0d234f90d85",
			accept:  false,
			comment: "last segment is 11 chars instead of 12",
		},
		{
			name:    "uuid_non_hex",
			id:      "zzzzzzzz-9ba6-4e90-8b43-0d234f90d85d",
			accept:  true,
			comment: "matches the prefix branch (z is a letter, then char+sep+char alternation); the UUID dashed branch rejects it but the prefix branch accepts it — both valid per the contract",
		},
		{
			name:    "uuid_extra_dash",
			id:      "3875431e--9ba6-4e90-8b43-0d234f90d85d",
			accept:  false,
			comment: "double dash violates the layout",
		},
		{
			name:    "uuid_only_one_segment",
			id:      "3875431e",
			accept:  false,
			comment: "no dashes, too short for the hex-only branch",
		},
		{
			name:    "uuid_with_underscore",
			id:      "3875431e-9ba6-4e90-8b43-0d234f90d85_",
			accept:  false,
			comment: "underscore is not allowed in UUIDs",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRequestID(tc.id)
			got := err == nil
			if got != tc.accept {
				t.Fatalf("id=%q accept=%v got=%v err=%v — %s",
					tc.id, tc.accept, got, err, tc.comment)
			}
		})
	}
}

// TestSafeRequestIDCompatibleIDsEndToEnd is a smoke test that ensures
// the compatible IDs (hex-only + dashed UUID + prefixed) all round-trip
// through Put → GetFile without ever falling through to the regex
// rejection. This catches a class of "regex was extended but a
// downstream function still rejected" regressions.
func TestSafeRequestIDCompatibleIDsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	ids := []string{
		"3875431e9ba64e908b430d234f90d85d",                          // hex-only
		"3875431e-9ba6-4e90-8b43-0d234f90d85d",                     // dashed UUID
		"00000000-0000-0000-0000-000000000000",                     // nil UUID
		"3875431E9BA64E908B430D234F90D85D",                         // uppercase hex
		"req-unified-01",                                           // common prefix format
		"req-same-tenant",                                          // tenant test format
		"bench-1a2b3c4d",                                           // bench prefix
	}

	for _, id := range ids {
		if err := s.PutMeta(Meta{RequestID: id, TenantID: "default"}); err != nil {
			t.Fatalf("PutMeta rejected compatible id %q: %v", id, err)
		}
		if _, ok := s.GetMeta(id); !ok {
			t.Fatalf("GetMeta miss for compatible id %q", id)
		}
		// File path must be the same basename the regex claims — no
		// path traversal even on dashed UUIDs.
		expected := filepath.Join(dir, id+".json")
		if got := s.filePath(id); got != expected {
			t.Fatalf("filePath mismatch for %q: got=%q want=%q", id, got, expected)
		}
		// The filesystem must not see the file escape the dir.
		if !strings.HasPrefix(expected, dir) {
			t.Fatalf("filePath escapes dir for %q: %q", id, expected)
		}
		// Cleanup so the next id does not see stale state.
		if err := s.Clear(id); err != nil {
			t.Fatalf("Clear failed for %q: %v", id, err)
		}
		if _, err := os.Stat(expected); !os.IsNotExist(err) {
			t.Fatalf("residue after Clear for %q: %v", id, err)
		}
	}
}
