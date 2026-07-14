package streaming

import (
	"strings"
	"testing"
)

func TestRedactAttachmentBodyIfEnabled(t *testing.T) {
	body := []byte(`{"messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,SECRET"}}]}]}`)

	// Default (env unset): redaction is ON.
	redacted := string(redactAttachmentBodyIfEnabled(body))
	if strings.Contains(redacted, "SECRET") {
		t.Fatalf("default redaction must strip inline data: %s", redacted)
	}
	if !strings.Contains(redacted, "data:image/png;base64,[attachment data redacted]") {
		t.Fatalf("redacted body lost MIME header: %s", redacted)
	}

	// Explicit opt-out: LLM_GATEWAY_REDACT_ATTACHMENT_BODY=0
	t.Setenv(attachmentBodyRedactionEnv, "0")
	if string(redactAttachmentBodyIfEnabled(body)) != string(body) {
		t.Fatal("redaction must be disabled when env is explicitly 0")
	}
}

func TestRedactAttachmentBody_InvalidJSONIsUnchanged(t *testing.T) {
	body := []byte(`{"data":"data:image/png;base64,SECRET"`)
	t.Setenv(attachmentBodyRedactionEnv, "1")
	if string(redactAttachmentBodyIfEnabled(body)) != string(body) {
		t.Fatal("invalid JSON must remain unchanged for diagnostics")
	}
}
