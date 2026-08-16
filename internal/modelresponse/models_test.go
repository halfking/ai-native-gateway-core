package modelresponse

import (
	"strings"
	"testing"
)

func TestParseModelIDs(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string
		wantErr bool
	}{
		{name: "openai objects", body: `{"data":[{"id":"glm-5.2"}]}`, want: []string{"glm-5.2"}},
		{name: "string collection", body: `{"data":["glm-5.2","minimax-m3"]}`, want: []string{"glm-5.2", "minimax-m3"}},
		{name: "nested wrappers", body: `{"result":{"data":{"items":[{"model_id":"glm-5.2"},{"model_name":"minimax-m3"}]}}}`, want: []string{"glm-5.2", "minimax-m3"}},
		{name: "bare mixed array", body: `[{"model":"glm-5.2"},"minimax-m3"]`, want: []string{"glm-5.2", "minimax-m3"}},
		{name: "gemini prefix and dedup", body: `{"models":[{"name":"models/gemini-2.5-pro"},{"id":"GEMINI-2.5-PRO"}]}`, want: []string{"gemini-2.5-pro"}},
		{name: "error envelope", body: `{"error":{"message":"model glm-5.2 unavailable"}}`, wantErr: true},
		{name: "invalid json", body: `{`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseModelIDs([]byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseModelIDs() = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseModelIDs() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("ParseModelIDs() = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("ParseModelIDs() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestParseModelIDs_NumericIDIgnored pins down the post-UseNumber-removal
// behaviour: model IDs are always extracted as strings, so a numeric id
// field (rare, but observed on some Anthropic-style wraps) must NOT be
// coerced into the result list. Removing decoder.UseNumber() means numbers
// decode as float64, which is dropped by the (string, ok) type assertion
// in modelIDFromObject — exactly the right behaviour, but worth a regression
// test so a future "be permissive about numeric ids" change is intentional.
//
// With UseNumber() removed the decoder produces float64 for `42`. The
// collection walker descends into the `data` array, sees a model object
// whose `id` is a number, and the (string, ok) check fails. The walker
// then tries the next collection key (`models`, etc.), finds none, and
// returns zero IDs → the caller sees the "unrecognized models response
// format" error. That IS the correct outcome for an all-numeric-id
// payload: the upstream's response shape does not match any model-list
// contract we recognize.
func TestParseModelIDs_NumericIDIgnored(t *testing.T) {
	got, err := ParseModelIDs([]byte(`{"data":[{"id":42}]}`))
	if err == nil {
		t.Fatalf("ParseModelIDs() = %v, want unrecognized-format error — numeric-only ids should not be silently coerced", got)
	}
	if !strings.Contains(err.Error(), "unrecognized") {
		t.Fatalf("ParseModelIDs() error = %v, want unrecognized-format", err)
	}
	if len(got) != 0 {
		t.Fatalf("ParseModelIDs() = %v alongside error, want empty", got)
	}
}

// TestParseModelIDs_NumericIDAlongsideStringID confirms the numeric-id
// ignore policy does NOT cause string IDs in the same collection to be
// dropped — the parser still extracts strings, just skips numbers. This
// is the case a future "be permissive about numeric ids" change would
// regress.
func TestParseModelIDs_NumericIDAlongsideStringID(t *testing.T) {
	got, err := ParseModelIDs([]byte(`{"data":[{"id":42},{"id":"glm-5.2"}]}`))
	if err != nil {
		t.Fatalf("ParseModelIDs() unexpected error = %v", err)
	}
	if len(got) != 1 || got[0] != "glm-5.2" {
		t.Fatalf("ParseModelIDs() = %v, want [glm-5.2]", got)
	}
}

// TestParseModelIDs_DeepNestingBounded ensures pathological input (e.g. a
// hostile or misconfigured upstream returning a body nested far beyond any
// realistic model-list shape) does not crash the worker goroutine via stack
// overflow. The parser must bound recursion at maxCollectDepth and either
// extract no IDs (returning the unrecognized-format error) or extract the
// IDs found before the depth was exceeded — whichever, but it must NOT
// panic.
//
// We build the body by hand because strings.Repeat on already-built strings
// is O(n^2) and quickly becomes impractical at the depths we want to test.
func TestParseModelIDs_DeepNestingBounded(t *testing.T) {
	// Build a body whose collection-key chain `data` is nested 5000 deep
	// — well past maxCollectDepth (64). The leaf holds a single model id
	// that, if the parser walked the full chain, would be returned.
	const totalDepth = 5000
	var b strings.Builder
	for i := 0; i < totalDepth; i++ {
		b.WriteString(`{"data":`)
	}
	b.WriteString(`[{"id":"deep-model"}]`)
	for i := 0; i < totalDepth; i++ {
		b.WriteString(`}`)
	}
	body := b.String()

	// Must return without panicking. The expected outcome is either:
	//   1. The "unrecognized models response format" error (parser walked
	//      the chain, hit maxCollectDepth before reaching the leaf, found
	//      no IDs in the truncated subtree).
	//   2. The id "deep-model" extracted from inside the bound.
	// Anything else (panic, panic-recovered-as-error containing "stack
	// overflow", etc.) means the recursion cap is broken.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseModelIDs panicked on deep nesting: %v", r)
		}
	}()
	got, err := ParseModelIDs([]byte(body))
	if err == nil && len(got) == 0 {
		t.Fatal("ParseModelIDs returned no error and no IDs — boundary case, should be one or the other")
	}
	// Either error or a single id is acceptable; anything else is wrong.
	if err == nil && len(got) > 1 {
		t.Fatalf("ParseModelIDs returned unexpected %d ids: %v", len(got), got)
	}
	if err != nil && !strings.Contains(err.Error(), "unrecognized") {
		t.Fatalf("ParseModelIDs error must be the unrecognized-format message; got %v", err)
	}
}

// TestParseModelIDs_DeepNestingWithShallowID confirms that legitimate
// shallow model IDs in an otherwise-deep body are still extracted. The
// depth cap must NOT blind the parser to ids it can see without recursing
// past the bound.
func TestParseModelIDs_DeepNestingWithShallowID(t *testing.T) {
	// Build a wrapper `data` chain 100 deep (well past maxCollectDepth),
	// but put a sibling `models` key at the root with a real id. The parser
	// should find `models` at depth 1 and extract "shallow-model" without
	// recursing into the deep `data` chain.
	const totalDepth = 100
	var b strings.Builder
	b.WriteString(`{"models":[{"id":"shallow-model"}],`)
	b.WriteString(`"data":`)
	for i := 0; i < totalDepth; i++ {
		b.WriteString(`{"data":`)
	}
	b.WriteString(`[{"id":"deep-model"}]`)
	for i := 0; i < totalDepth; i++ {
		b.WriteString(`}`)
	}
	b.WriteString(`}`)

	got, err := ParseModelIDs([]byte(b.String()))
	if err != nil {
		t.Fatalf("ParseModelIDs unexpected error = %v", err)
	}
	if len(got) != 1 || got[0] != "shallow-model" {
		t.Fatalf("ParseModelIDs = %v, want [shallow-model]", got)
	}
}
