package contextbudget

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestByteFallbackReferenceCalibration(t *testing.T) {
	// A tokenizer that emits one token per UTF-8 byte is a deliberately worse
	// textual reference than byte/subword tokenizers that merge byte pieces.
	// Provider framing is separately charged rather than hidden in /3.5.
	for _, input := range []string{"hello world", "你好，世界", "👩🏽‍💻🚀", "a\x00b\n\"\\", "e\u0301", strings.Repeat("9", 10000)} {
		value := map[string]any{
			"messages": []any{map[string]any{"role": "user", "content": input}},
			"tools":    []any{map[string]any{"type": "function", "function": map[string]any{"name": "read", "parameters": map[string]any{"type": "object"}}}},
		}
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := UpperBound(body)
		if err != nil {
			t.Fatal(err)
		}
		// Include textual scaffolding in the reference, not just user content.
		reference := len(input) + len("user") + len("read") + len("object") + 8 + 64
		if got < reference {
			t.Fatalf("bound=%d below byte-fallback reference=%d", got, reference)
		}
	}
}

func TestCounterCanAdmitAnExactCountWithoutChangingHardCaps(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("long repeated text ", 10000) + `"}]}`)
	if _, err := Check(body, Spec{ContextWindow: 10000}); err == nil {
		t.Fatal("fallback should not pretend that compressible bytes are few tokens")
	}
	got, err := CheckWithCounter(body, Spec{ContextWindow: 10000}, CounterFunc(func([]byte) (int, error) { return 1000, nil }))
	if err != nil || got.Tokens != 1000 || got.Counter != "bucket_counter" {
		t.Fatalf("exact counter result=%+v err=%v", got, err)
	}
	_, err = CheckWithCounter(body, Spec{ContextWindow: 2000000}, CounterFunc(func([]byte) (int, error) { return 500001, nil }))
	var budgetErr *Error
	if !errors.As(err, &budgetErr) || budgetErr.Kind != TokensExceeded {
		t.Fatalf("exact counter bypassed global cap: %v", err)
	}
	_, err = CheckWithCounter(body, Spec{ContextWindow: 10000}, CounterFunc(func([]byte) (int, error) { return -1, nil }))
	if !errors.As(err, &budgetErr) || budgetErr.Kind != Uncountable {
		t.Fatalf("negative counter result accepted: %v", err)
	}
}

func TestSafeByteTargetAndUnknownState(t *testing.T) {
	spec := Spec{ContextWindow: 128000}
	limit := SafeBodyBytes(spec)
	if limit <= 0 || limit > 64000 {
		t.Fatalf("invalid safe byte target %d", limit)
	}
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", limit-100) + `"}]}`)
	if _, err := Check(body, spec); err != nil {
		t.Fatalf("safe target was not admitted: %v", err)
	}
	if SafeBodyBytes(Spec{}) != 0 {
		t.Fatal("unknown context produced a safe byte target")
	}
	if _, err := UpperBound([]byte(`{"input":"x","previous_response_id":"r"}`)); err == nil {
		t.Fatal("UpperBound claimed to count opaque history")
	}
}

func TestInspectionCapacityDoesNotPanic(t *testing.T) {
	for _, body := range []string{
		`{"input":` + strings.Repeat("[", 200) + `0` + strings.Repeat("]", 200) + `}`,
		`{"input":[` + strings.Repeat("0,", 100001) + `0]}`,
		"{\"input\":\"\xff\"}",
	} {
		if _, err := Check([]byte(body), Spec{ContextWindow: 1000000}); err == nil {
			t.Fatal("unbounded/invalid structure was accepted")
		}
	}
}

func FuzzCheckBudgetInvariants(f *testing.F) {
	for _, seed := range []string{`{"messages":[{"role":"user","content":"hello"}]}`, `{"input":"你好"}`, `{"input":[],"cachedContent":"x"}`, `{"a":1,"a":2}`, `null`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			return
		}
		receipt, err := Check(body, Spec{ContextWindow: 128000})
		if err == nil && (receipt.Tokens > 64000 || receipt.Tokens < len(body) || receipt.JSONBytes != len(body) || receipt.BodySHA256 == "") {
			t.Fatalf("invalid receipt: %+v", receipt)
		}
	})
}

func BenchmarkConservativeCheck(b *testing.B) {
	body := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("hello 你好 ", 20000) + `"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, err := Check(body, Spec{ContextWindow: 1000000}); err != nil {
			b.Fatal(err)
		}
	}
}
