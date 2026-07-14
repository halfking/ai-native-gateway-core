package streaming

import (
	"strings"
	"testing"
)

func TestRedactAttachmentBodyIfEnabled(t *testing.T) {
	body := []byte(`{"messages":[{"content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,SECRET"}}]}]}`)

	t.Setenv(attachmentBodyRedactionEnv, "")
	if string(redactAttachmentBodyIfEnabled(body)) != string(body) {
		t.Fatal("redaction must be disabled by default")
	}

	t.Setenv(attachmentBodyRedactionEnv, "1")
	redacted := string(redactAttachmentBodyIfEnabled(body))
	if strings.Contains(redacted, "SECRET") {
		t.Fatalf("redacted body contains attachment data: %s", redacted)
	}
	if !strings.Contains(redacted, "data:image/png;base64,[attachment data redacted]") {
		t.Fatalf("redacted body lost MIME header: %s", redacted)
	}
}

func TestRedactAttachmentBody_InvalidJSONIsUnchanged(t *testing.T) {
	body := []byte(`{"data":"data:image/png;base64,SECRET"`)
	t.Setenv(attachmentBodyRedactionEnv, "1")
	if string(redactAttachmentBodyIfEnabled(body)) != string(body) {
		t.Fatal("invalid JSON must remain unchanged for diagnostics")
	}
}
