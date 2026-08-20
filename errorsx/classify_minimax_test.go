package errorsx

import "testing"

func TestClassifyErrorWithBody_MiniMaxTokenPlanQuotaIsCredentialFatal(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"已达到 Token Plan 用量上限：请升级 Token Plan 套餐或购买积分补充用量。 (2056)","http_code":"429"}}`)

	got := ClassifyErrorWithBody(429, body)
	if got != KindQuotaPermanent {
		t.Fatalf("MiniMax Token Plan quota = %q, want %q", got, KindQuotaPermanent)
	}
	if !IsCredentialFatal(got) {
		t.Fatal("MiniMax Token Plan quota must be credential-fatal")
	}
}
