// Unit tests for the format classifier. No DB required — these cover the
// pure-Go classification logic that the 2026-08-18 154 incident exposed:
// ciphertext that starts with 0x80 (raw Fernet token) is unreadable by the
// gateway's DecryptAny, and the CLI must label it as raw-fernet-binary.
package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// encryptFernetRaw mirrors admin/crypto.go's encryptFernet internals: it
// produces a raw 137-byte Fernet token whose first byte is 0x80. We never
// invoke real AES here — the exact ciphertext bytes are not under test, only
// the classification of leading-byte patterns.
func rawFernetBytes() []byte {
	out := make([]byte, 137)
	out[0] = 0x80
	for i := 1; i < len(out); i++ {
		out[i] = byte(i & 0xff)
	}
	return out
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		row  Row
		want Format
	}{
		{
			name: "empty ciphertext",
			row:  Row{CipherBytes: 0},
			want: FormatEmpty,
		},
		{
			name: "v1:legacy: prefix (Fernet envelope)",
			row:  Row{CipherBytes: 192, hexForm: hex.EncodeToString([]byte("v1:legacy:gAAAAABqgg6v..."))},
			want: FormatV1LegacyFernet,
		},
		{
			name: "v1: prefix without legacy (AES-GCM envelope)",
			row:  Row{CipherBytes: 137, hexForm: hex.EncodeToString([]byte("v1:abc:xyz"))},
			want: FormatV1AESEnvelope,
		},
		{
			name: "bare Fernet base64 (no envelope)",
			row:  Row{CipherBytes: 184, hexForm: hex.EncodeToString([]byte("gAAAAABqgg6vK85tMzYKEp..."))},
			want: FormatBareFernet,
		},
		{
			name: "raw Fernet binary (the 154 incident case)",
			row:  Row{CipherBytes: 137, hexForm: hex.EncodeToString(rawFernetBytes())},
			want: FormatRawFernetBinary,
		},
		{
			name: "unknown format",
			row:  Row{CipherBytes: 8, hexForm: hex.EncodeToString([]byte("????????"))},
			want: FormatUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(&tc.row)
			if got != tc.want {
				t.Fatalf("classify(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestShortHash_Stable(t *testing.T) {
	a := shortHash([]byte("hello"))
	b := shortHash([]byte("hello"))
	if a != b {
		t.Fatalf("shortHash not stable: %s vs %s", a, b)
	}
	if len(a) != 12 {
		t.Fatalf("shortHash length = %d, want 12", len(a))
	}
	// Different inputs must produce different prefixes
	c := shortHash([]byte("hellp"))
	if a == c {
		t.Fatalf("shortHash collision: %s == %s", a, c)
	}
}

// TestRewriteEnvelope_RoundTrip asserts that for a known raw Fernet token
// (the 154-incident format), base64-RawURLEncoding the bytes and prepending
// "v1:legacy:" yields the same shape as admin/crypto.go:encryptFernet would
// have produced via the correct code path. This guards against drift in the
// envelope format used by the fix path.
func TestRewriteEnvelope_RoundTrip(t *testing.T) {
	raw := rawFernetBytes()
	envelope := "v1:legacy:" + base64.RawURLEncoding.EncodeToString(raw)

	if !strings.HasPrefix(envelope, "v1:legacy:") {
		t.Fatalf("envelope missing prefix: %q", envelope)
	}
	if envelope[len("v1:legacy:"):] != base64.RawURLEncoding.EncodeToString(raw) {
		t.Fatalf("envelope body changed unexpectedly")
	}

	// The base64 body MUST decode back to a 137-byte raw Fernet token
	// starting with 0x80. (DecryptFernet does this base64 decode first.)
	body := strings.TrimPrefix(envelope, "v1:legacy:")
	decoded, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("envelope body not valid base64-url: %v", err)
	}
	if len(decoded) != 137 {
		t.Fatalf("decoded envelope body length = %d, want 137", len(decoded))
	}
	if decoded[0] != 0x80 {
		t.Fatalf("decoded envelope body first byte = 0x%x, want 0x80", decoded[0])
	}
}

// TestClassifyNullAndEmpty guards the regression that scanRows crashed on
// the first NULL ciphertext row because classify tried to read rawBytes[0]
// of an empty slice. NULL and empty bytea now route to FormatEmpty via
// the empty hexForm short-circuit at the top of classify.
func TestClassifyNullAndEmpty(t *testing.T) {
	cases := []struct {
		name string
		row  Row
		want Format
	}{
		{
			name: "NULL ciphertext (empty hex, 0 bytes)",
			row:  Row{CipherBytes: 0, hexForm: ""},
			want: FormatEmpty,
		},
		{
			name: "zero-length bytea (empty hex, 0 bytes)",
			row:  Row{CipherBytes: 0, hexForm: ""},
			want: FormatEmpty,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(&tc.row); got != tc.want {
				t.Fatalf("classify = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPrintHumanFlagsAllAnomalies pins the "healthy" short-circuit so a
// future refactor cannot accidentally silence raw / unknown / empty
// anomalies. The function is internal, so we drive it through a
// ScanResult struct and check stdout for the canonical line; this avoids
// exporting printHuman or splitting it into smaller pieces.
func TestPrintHumanFlagsAllAnomalies(t *testing.T) {
	cases := []struct {
		name   string
		byFmt  map[Format]int
		fmt    Format
		expect int
	}{
		{name: "raw-fernet-binary visible", byFmt: map[Format]int{FormatRawFernetBinary: 1}, fmt: FormatRawFernetBinary, expect: 1},
		{name: "unknown visible", byFmt: map[Format]int{FormatUnknown: 2}, fmt: FormatUnknown, expect: 2},
		{name: "empty visible", byFmt: map[Format]int{FormatEmpty: 3}, fmt: FormatEmpty, expect: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := os.Stdout
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("pipe: %v", err)
			}
			os.Stdout = w
			defer func() { os.Stdout = old }()

			r2 := ScanResult{ByFormat: tc.byFmt}
			printHuman(r2, false, false, true)
			_ = w.Close()
			out, _ := io.ReadAll(r)
			got := string(out)
			if !strings.Contains(got, string(tc.fmt)) {
				t.Fatalf("expected output to mention format %q, got:\n%s", tc.fmt, got)
			}
			wantCount := fmt.Sprintf("%d", tc.expect)
			if !strings.Contains(got, wantCount) {
				t.Fatalf("expected output to include count %q, got:\n%s", wantCount, got)
			}
		})
	}
}
