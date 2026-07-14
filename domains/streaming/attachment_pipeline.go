package streaming

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/attachments" //nolint:depguard // request pipeline owns attachment logging
)

func attachmentStrictMode() bool {
	return strings.TrimSpace(os.Getenv("LLM_GATEWAY_ATTACHMENT_STRICT")) != "0"
}

func applyAttachmentResult(logCtx *RequestLogContext, result *attachments.ExtractResult) bool {
	if logCtx == nil || result == nil {
		return false
	}
	logCtx.Attachments = result.Attachments
	return result.Failed > 0
}

func (c *RequestLogContext) markAttachmentsSent() {
	if c == nil {
		return
	}
	for i := range c.Attachments {
		if c.Attachments[i].Status == attachments.AttachmentStatusManifestReady {
			c.Attachments[i].Status = attachments.AttachmentStatusSent
		}
	}
}

func attachmentsJSON(items []attachments.AttachmentMetadata) json.RawMessage {
	if len(items) == 0 {
		return nil
	}
	b, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	return b
}

func attachmentsFromLogContext(logCtx *RequestLogContext) json.RawMessage {
	if logCtx == nil {
		return nil
	}
	return attachmentsJSON(logCtx.Attachments)
}
