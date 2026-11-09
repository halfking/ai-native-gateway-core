package contextbudget

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingReader) Close() error             { return nil }

func TestGuardCancellationAndReadFailures(t *testing.T) {
	if _, err := CheckRequest(nil); err == nil {
		t.Fatal("nil request accepted")
	}
	if _, ok := SpecFromContext(nil); ok {
		t.Fatal("nil context had a bound spec")
	}
	ctx, cancel := context.WithCancel(WithSpec(context.Background(), Spec{ContextWindow: 128000}))
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://example.invalid", strings.NewReader(`{"input":"hi"}`))
	cancel()
	if _, err := CheckRequest(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attempt = %v", err)
	}
	// 2026-11-10 改判：空 spec（未知窗口）回落全局保守上限，受卫请求被放行
	// （仍受 MaxInputTokens/DefaultMaxJSONBytes 封顶；不再因数据缺口硬拒）。
	req, _ = http.NewRequestWithContext(WithSpec(context.Background(), Spec{}), "POST", "https://example.invalid", strings.NewReader(`{}`))
	if _, err := CheckRequest(req); err != nil {
		t.Fatalf("unknown-window guarded request must pass under global caps: %v", err)
	}
	req, _ = http.NewRequestWithContext(WithSpec(context.Background(), Spec{ContextWindow: 128000}), "POST", "https://example.invalid", strings.NewReader(`{}`))
	req.Body = nil
	if _, err := CheckRequest(req); err == nil {
		t.Fatal("missing guarded body was accepted")
	}
	req.Body = failingReader{}
	if _, err := CheckRequest(req); err == nil {
		t.Fatal("failed body read was accepted")
	}
	req.Body = failingReader{}
	req.ContentLength = DefaultMaxJSONBytes + 1
	if _, err := CheckRequest(req); err == nil {
		t.Fatal("known oversized body was accepted")
	}
}

func TestCounterErrorsAndTextDocuments(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"text","data":"words"}},{"type":"tool_use","input":{"file_id":"just an argument"}}]}]}`)
	if _, err := Check(body, Spec{ContextWindow: 128000}); err != nil {
		t.Fatalf("inline text/argument was treated as fetched media: %v", err)
	}
	wantErr := errors.New("counter unavailable")
	_, err := CheckWithCounter(body, Spec{ContextWindow: 128000}, CounterFunc(func([]byte) (int, error) { return 0, wantErr }))
	if !errors.Is(err, wantErr) {
		t.Fatalf("counter failure hidden: %v", err)
	}
	if _, err := UpperBound([]byte(`{`)); err == nil {
		t.Fatal("invalid JSON got a token upper bound")
	}
	_, err = Check([]byte(`{"input":"hi","max_tokens":120000}`), Spec{ContextWindow: 128000, SafetyMargin: 10000})
	var budgetErr *Error
	if !errors.As(err, &budgetErr) || !strings.Contains(err.Error(), TokensExceeded) {
		t.Fatalf("request reserve did not exhaust bucket context: %v", err)
	}
}
