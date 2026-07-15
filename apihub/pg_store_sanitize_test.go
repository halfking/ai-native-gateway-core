// 2026-07-16 hardening tests for apihub marshal helpers.
//
// These exercise the JSONB safety net added after the apihub watcher
// logged 170k+ "invalid input syntax for type json" errors for three
// stuck ref_ids (1139198-200). The helpers must:
//
//   - Never feed PostgreSQL a payload that fails json.Valid.
//   - Strip NaN / +Inf / -Inf floats (json.Marshal rejects these).
//   - Strip NUL bytes from string leaves (PG SQLSTATE 22021).
//   - Recurse into nested maps / slices.
package apihub

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestMarshalAny_StripsNaNAndInf(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
	}{
		{"nan", map[string]any{"score": math.NaN()}},
		{"pinf", map[string]any{"score": math.Inf(1)}},
		{"ninf", map[string]any{"score": math.Inf(-1)}},
		{"nested_nan", map[string]any{
			"inner": map[string]any{"ratio": math.NaN()},
		}},
		{"slice_with_nan", map[string]any{
			"values": []any{1.0, math.NaN(), 2.0, math.Inf(1)},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := marshalAny(tc.in)
			if err != nil {
				t.Fatalf("marshalAny error: %v", err)
			}
			if !json.Valid(out) {
				t.Fatalf("output is not valid JSON: %s", string(out))
			}
			if strings.Contains(string(out), "NaN") || strings.Contains(string(out), "Inf") {
				t.Fatalf("non-finite value leaked through: %s", string(out))
			}
		})
	}
}

func TestMarshalStringMap_StripsNULAndInvalidUTF8(t *testing.T) {
	in := map[string]string{
		"good":  "ok",
		"nul":   "before\x00after",
		"bad":   "before\xc3\x28after", // invalid UTF-8
		"clean": "normal value",
	}
	out, err := marshalStringMap(in)
	if err != nil {
		t.Fatalf("marshalStringMap error: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("output is not valid JSON: %s", string(out))
	}
	if strings.Contains(string(out), "\x00") {
		t.Fatalf("NUL byte leaked through: %q", string(out))
	}
}

func TestMarshalAny_EmptyMap(t *testing.T) {
	out, err := marshalAny(map[string]any{})
	if err != nil {
		t.Fatalf("marshalAny error: %v", err)
	}
	if string(out) != "{}" {
		t.Fatalf("expected {}, got %s", string(out))
	}
}

func TestMarshalStringMap_EmptyMap(t *testing.T) {
	out, err := marshalStringMap(map[string]string{})
	if err != nil {
		t.Fatalf("marshalStringMap error: %v", err)
	}
	if string(out) != "{}" {
		t.Fatalf("expected {}, got %s", string(out))
	}
}

func TestScrubUTF8ForJSONB_KeepsTabNewlineCR(t *testing.T) {
	// PG JSONB accepts \t \n \r in strings; other C0 control bytes
	// must be stripped to avoid surprises.
	in := "a\tb\nc\rd\x01e\x1ff"
	out := scrubUTF8ForJSONB(in)
	if strings.ContainsAny(out, "\x01\x1f") {
		t.Fatalf("control byte leaked through: %q", out)
	}
	if !strings.Contains(out, "\t") || !strings.Contains(out, "\n") || !strings.Contains(out, "\r") {
		t.Fatalf("legitimate whitespace stripped: %q", out)
	}
}
