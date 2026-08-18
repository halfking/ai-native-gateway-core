package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/tokenest"
)

func TestEstimateInputTokensUsesCanonicalEstimator(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`)
	if got, want := EstimateInputTokens(body), tokenest.FromChars(len(body)); got != want {
		t.Fatalf("EstimateInputTokens() = %d, want %d", got, want)
	}
}

func TestEstimateInputTokensEmptyBody(t *testing.T) {
	if got := EstimateInputTokens(nil); got != 0 {
		t.Fatalf("EstimateInputTokens(nil) = %d, want 0", got)
	}
}

func TestRequestLogContextRecordsReceiptEstimate(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`)
	ctx := &RequestLogContext{Body: body}
	ctx.recordReceiptTokenEstimate()
	if ctx.PromptTokensEstimate == nil {
		t.Fatal("PromptTokensEstimate is nil")
	}
	if got, want := *ctx.PromptTokensEstimate, EstimateInputTokens(body); got != want {
		t.Fatalf("PromptTokensEstimate = %d, want %d", got, want)
	}
}
