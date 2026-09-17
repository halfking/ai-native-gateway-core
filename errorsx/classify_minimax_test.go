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

// 2026-09-18 audit (245 credential-flap follow-up): MiniMax rejects
// client-sent reasoning params with 400 "invalid params, invalid
// thinking.type ... (2013)". This is client request shape, NOT credential
// health: the previous fall-through to KindTransient made UpdateOnFailure
// cool the credential while the node probe (well-formed ping) kept
// succeeding — minimax-prod-v2 flapped cooling↔ready every few minutes in
// prod. It must classify as a client bug so the credential state writer
// skips it and failover does not enqueue a meaningless probe.
func TestClassifyErrorWithBody_MiniMaxInvalidThinkingTypeIsClientBug(t *testing.T) {
	body := []byte(`{"type":"error","error":{"type":"bad_request_error","message":"invalid params, invalid thinking.type: \"enabled\" (allowed: adaptive, disabled) (2013)","http_code":"400"}}`)

	got := ClassifyErrorWithBody(400, body)
	if got != KindClientBug {
		t.Fatalf("MiniMax invalid thinking.type 400 = %q, want %q", got, KindClientBug)
	}
	if !IsClientBug(got) {
		t.Fatal("MiniMax invalid thinking.type must be a client bug (UpdateOnFailure must skip it)")
	}
	// A non-MiniMax 400 with a different invalid-params shape must NOT be
	// dragged into the client-bug class by the new patterns.
	other := ClassifyErrorWithBody(400, []byte(`{"error":{"message":"invalid params"}}`))
	if other == KindClientBug {
		t.Fatalf("bare 'invalid params' without the MiniMax thinking.type shape must not be KindClientBug, got %q", other)
	}
}

// R42 pin (2026-09-18 audit): the MiniMax-native base_resp envelope carries
// the 2013 code as a bare number, which the toolCallIdMismatchRe
// `(?:code|error_code|status)...2013` branch matches BEFORE the
// invalidRequestFormatRe thinking.type pattern — so the classification lands
// on KindToolCallIdMismatch, not KindClientBug. Both kinds are IsClientBug
// (UpdateOnFailure skip, no probe, terminal action), so downstream behavior
// is identical; this test only documents the envelope shape so a classifier
// reorder cannot silently flip it into the transient family.
func TestClassifyErrorWithBody_MiniMaxBaseRespEnvelope400StaysClientBugFamily(t *testing.T) {
	body := []byte(`{"base_resp":{"status_code":2013,"msg":"invalid thinking.type: \"enabled\" (allowed: adaptive, disabled)"}}`)

	got := ClassifyErrorWithBody(400, body)
	if !IsClientBug(got) {
		t.Fatalf("MiniMax base_resp 2013 envelope = %q, want a KindClientBug-family kind (tool_call_id_mismatch or client_bug)", got)
	}
}
