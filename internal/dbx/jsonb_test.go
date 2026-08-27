package dbx

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func jsonbSpec(max int) ColumnSpec {
	spec := ColumnSpec{Name: "metadata", Kind: KindJSONB, Writable: true}
	if max > 0 {
		spec.JSONBMaxBytes = max
	}
	return spec
}

func TestNormalizeJSONBFromGoValue(t *testing.T) {
	raw, err := NormalizeJSONB(jsonbSpec(0), map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("NormalizeJSONB: %v", err)
	}
	if string(raw) != `{"k":"v"}` {
		t.Fatalf("canonical = %s", raw)
	}
}

func TestNormalizeJSONBRawForms(t *testing.T) {
	for _, in := range []any{
		[]byte(`{"a":1}`),
		`{"a":1}`,
		json.RawMessage(`{"a":1}`),
	} {
		if _, err := NormalizeJSONB(jsonbSpec(0), in); err != nil {
			t.Errorf("NormalizeJSONB(%T) = %v", in, err)
		}
	}
}

func TestNormalizeJSONBRejections(t *testing.T) {
	cases := []struct {
		name  string
		value any
		max   int
	}{
		{"nil", nil, 0},
		{"empty bytes", []byte{}, 0},
		{"invalid utf8", []byte{'"', 0xff, '"'}, 0},
		{"broken structure", []byte(`{"a":`), 0},
		{"NaN literal", []byte(`{"f":NaN}`), 0},
		{"Infinity literal", []byte(`[Infinity]`), 0},
		{"NaN go float", map[string]any{"f": math.NaN()}, 0},
		{"Inf nested", []any{map[string]any{"x": math.Inf(1)}}, 0},
		{"oversized", `"` + strings.Repeat("a", 64) + `"`, 16},
		{"wrong kind", "text", 0},
	}
	for _, tc := range cases {
		spec := jsonbSpec(tc.max)
		if tc.name == "wrong kind" {
			spec.Kind = KindText
		}
		if _, err := NormalizeJSONB(spec, tc.value); !errors.Is(err, ErrInvalidJSONB) {
			t.Errorf("%s: err = %v, want ErrInvalidJSONB", tc.name, err)
		}
	}
}

func TestNormalizeJSONBValidEdgeShapes(t *testing.T) {
	for _, in := range []any{
		[]byte(`{}`),
		[]byte(`[1,2,3]`),
		[]byte(`1e10`), // large but finite
		[]byte(`-0.5`),
		[]any{"a", 1, true, nil}, // nested null stays legal JSON
	} {
		if _, err := NormalizeJSONB(jsonbSpec(0), in); err != nil {
			t.Errorf("NormalizeJSONB(%v) = %v", in, err)
		}
	}
}

func TestNormalizeJSONBTopLevelNullRejected(t *testing.T) {
	for _, in := range []any{
		[]byte(`null`),
		[]byte(`  null  `),
		`null`,
	} {
		if _, err := NormalizeJSONB(jsonbSpec(0), in); !errors.Is(err, ErrInvalidJSONB) {
			t.Errorf("top-level null %q = %v, want ErrInvalidJSONB", in, err)
		}
	}
}

func TestNormalizeJSONBOutOfRangeNumberTyped(t *testing.T) {
	if _, err := NormalizeJSONB(jsonbSpec(0), []byte(`{"n":1e400}`)); !errors.Is(err, ErrInvalidJSONB) {
		t.Errorf("1e400 err = %v, want ErrInvalidJSONB", err)
	}
}

func TestNormalizeJSONBSizeGateSkipsParsing(t *testing.T) {
	// Oversized *and* structurally invalid: the size gate must reject it
	// without the payload ever reaching structural validation.
	big := make([]byte, 33)
	for i := range big {
		big[i] = '{'
	}
	if _, err := NormalizeJSONB(jsonbSpec(32), big); !errors.Is(err, ErrInvalidJSONB) {
		t.Errorf("oversized invalid err = %v, want ErrInvalidJSONB", err)
	}
}
