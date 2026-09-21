package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
)

// R35 (2026-09-17 audit): the default flipped to lenient — the storage
// layer's declared contract is "store failure must not block forwarding",
// and the old any-value-except-"0" default made an attachments-dir outage a
// 503 for every image-bearing request. Strict is now an explicit "1" opt-in.
func TestAttachmentStrictMode(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ATTACHMENT_STRICT", "")
	if attachmentStrictMode() {
		t.Fatal("strict mode must default to disabled (lenient)")
	}
	t.Setenv("LLM_GATEWAY_ATTACHMENT_STRICT", "0")
	if attachmentStrictMode() {
		t.Fatal("strict mode must stay disabled on explicit zero")
	}
	t.Setenv("LLM_GATEWAY_ATTACHMENT_STRICT", "1")
	if !attachmentStrictMode() {
		t.Fatal("strict mode must be enabled by explicit one")
	}
}

func TestApplyAttachmentResultAndMarkSent(t *testing.T) {
	ctx := &RequestLogContext{}
	failed := applyAttachmentResult(ctx, &attachments.ExtractResult{Attachments: []attachments.AttachmentMetadata{
		{Status: attachments.AttachmentStatusManifestReady},
		{Status: attachments.AttachmentStatusStoreFailed},
	}, Failed: 1})
	if !failed || len(ctx.Attachments) != 2 {
		t.Fatalf("apply result = (%t, %d attachments), want (true, 2)", failed, len(ctx.Attachments))
	}
	ctx.markAttachmentsSent()
	if ctx.Attachments[0].Status != attachments.AttachmentStatusSent {
		t.Errorf("stored attachment status = %q, want sent", ctx.Attachments[0].Status)
	}
	if ctx.Attachments[1].Status != attachments.AttachmentStatusStoreFailed {
		t.Errorf("failed attachment status = %q, want store_failed", ctx.Attachments[1].Status)
	}
}
