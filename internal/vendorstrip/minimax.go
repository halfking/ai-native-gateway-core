package strip

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

var miniMaxPrivateFields = []string{
	"nvext",
	"audio_content",
	"name",
	"output_sensitive_int",
	"base_resp",
	"request_id",
	"workflow_run_id",
	"created_by",
	"usage_extra",
}

type miniMaxStripper struct{}

func (miniMaxStripper) DetectError(body []byte) (Signal, bool) {
	code, message, isError := ParseMiniMaxBaseResp(body)
	if !isError {
		return Signal{}, false
	}
	return Signal{Code: code, Message: message, Kind: ClassifyMiniMaxStatusCode(code)}, true
}

func (miniMaxStripper) StripFields(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	stripped := false
	for _, field := range miniMaxPrivateFields {
		if _, ok := raw[field]; ok {
			delete(raw, field)
			stripped = true
		}
	}
	if choicesRaw, ok := raw["choices"]; ok {
		var choices []map[string]json.RawMessage
		if json.Unmarshal(choicesRaw, &choices) == nil {
			for i, choice := range choices {
				for _, field := range []string{"message", "delta"} {
					messageRaw, ok := choice[field]
					if !ok {
						continue
					}
					var message map[string]json.RawMessage
					if json.Unmarshal(messageRaw, &message) != nil {
						continue
					}
					if cleanMinimaxLeakFields(message) {
						choice[field], _ = json.Marshal(message)
						stripped = true
					}
				}
				choices[i] = choice
			}
			if stripped {
				raw["choices"], _ = json.Marshal(choices)
			}
		}
	}
	if !stripped {
		return body
	}
	out, err := json.Marshal(raw)
	if err != nil {
		slog.Warn("strip_minimax: marshal failed, returning original body", "error", err)
		return body
	}
	return out
}

// ParseMiniMaxBaseResp extracts MiniMax's HTTP 200-wrapped error signal.
func ParseMiniMaxBaseResp(body []byte) (statusCode int, statusMsg string, isError bool) {
	if len(body) == 0 {
		return 0, "", false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0, "", false
	}
	baseRespRaw, ok := raw["base_resp"]
	if !ok {
		return 0, "", false
	}
	var baseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	}
	if err := json.Unmarshal(baseRespRaw, &baseResp); err != nil {
		return 0, "", false
	}
	return baseResp.StatusCode, baseResp.StatusMsg, baseResp.StatusCode != 0
}

// ClassifyMiniMaxStatusCode preserves the established MiniMax-to-gateway map.
func ClassifyMiniMaxStatusCode(code int) errorsx.ErrorKind {
	switch code {
	case 0:
		return ""
	case 1002:
		return errorsx.KindRateLimit
	case 1004:
		return errorsx.KindAuth
	case 1008:
		return errorsx.KindQuota
	case 1027:
		return errorsx.KindContentFilter
	case 1039:
		return errorsx.KindContextLength
	case 1001:
		return errorsx.KindTimeout
	case 2013:
		return errorsx.KindClientBug
	case 1000, 1013:
		return errorsx.KindUpstreamDown
	default:
		return errorsx.KindUpstreamDown
	}
}

// FormatMiniMaxError returns the established user-facing error message.
func FormatMiniMaxError(statusCode int, statusMsg string) string {
	if statusMsg != "" {
		return fmt.Sprintf("MiniMax error %d: %s", statusCode, statusMsg)
	}
	return fmt.Sprintf("MiniMax error %d", statusCode)
}

func cleanMinimaxLeakFields(message map[string]json.RawMessage) bool {
	changed := false
	for _, field := range []string{"reasoning_content", "content"} {
		raw, ok := message[field]
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(raw, &text) != nil || !strings.Contains(text, "<function_calls>") {
			continue
		}
		start := strings.Index(text, "<function_calls>")
		end := strings.Index(text[start:], "</tool_call>")
		if end < 0 {
			continue
		}
		before := strings.TrimSpace(text[:start])
		after := strings.TrimSpace(text[start+end+len("</tool_call>"):])
		cleaned := before
		switch {
		case before == "":
			cleaned = after
		case after != "":
			cleaned = before + " " + after
		}
		message[field], _ = json.Marshal(cleaned)
		changed = true
	}
	return changed
}
