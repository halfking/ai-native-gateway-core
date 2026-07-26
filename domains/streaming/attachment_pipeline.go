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

// attachmentsJSON serializes attachment metadata, returning (nil, nil) when
// there is nothing to serialize. The error is returned separately because the
// nil return value is shared with the legitimate empty case — without it the
// caller cannot tell "no attachments" from "attachment list failed to encode".
func attachmentsJSON(items []attachments.AttachmentMetadata) (json.RawMessage, error) {
	if len(items) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func attachmentsFromLogContext(logCtx *RequestLogContext) json.RawMessage {
	if logCtx == nil {
		return nil
	}
	b, err := attachmentsJSON(logCtx.Attachments)
	if err != nil {
		logCtx.recordMetadataLoss("attachments", err)
		return nil
	}
	return b
}
