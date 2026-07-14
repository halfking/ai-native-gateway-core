package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/attachments"
)

func TestAttachmentStrictMode(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ATTACHMENT_STRICT", "")
	if !attachmentStrictMode() {
		t.Fatal("strict mode must default to enabled")
	}
	t.Setenv("LLM_GATEWAY_ATTACHMENT_STRICT", "0")
	if attachmentStrictMode() {
		t.Fatal("strict mode must be disabled by explicit zero")
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
