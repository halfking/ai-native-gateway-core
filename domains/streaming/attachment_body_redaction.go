package streaming

import (
	"encoding/json"
	"os"
	"strings"
)

const attachmentBodyRedactionEnv = "LLM_GATEWAY_REDACT_ATTACHMENT_BODY"

func redactAttachmentBodyIfEnabled(body []byte) []byte {
	if strings.TrimSpace(os.Getenv(attachmentBodyRedactionEnv)) != "1" || len(body) == 0 {
		return body
	}
	return redactAttachmentBody(body)
}

func redactAttachmentBody(body []byte) []byte {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return body
	}
	redactAttachmentValue(&value)
	redacted, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return redacted
}

func redactAttachmentValue(value *any) {
	switch current := (*value).(type) {
	case string:
		if strings.HasPrefix(current, "data:") {
			*value = attachmentDataPlaceholder(current)
		}
	case []any:
		for i := range current {
			redactAttachmentValue(&current[i])
		}
	case map[string]any:
		for key := range current {
			item := current[key]
			redactAttachmentValue(&item)
			current[key] = item
		}
	}
}

func attachmentDataPlaceholder(dataURI string) string {
	header := dataURI
	if comma := strings.IndexByte(dataURI, ','); comma >= 0 {
		header = dataURI[:comma]
	}
	return header + ",[attachment data redacted]"
}
