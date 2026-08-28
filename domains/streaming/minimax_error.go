package streaming

import (
	"encoding/json"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// ParseMiniMaxBaseResp extracts MiniMax's HTTP 200-wrapped error signal
// (base_resp.status_code) from a response body. MiniMax encodes errors as
// {status_code: 0=success, non-0=failure, status_msg} rather than HTTP 4xx/5xx.
//
// Returns:
//   - statusCode: base_resp.status_code value
//   - statusMsg: base_resp.status_msg value
//   - isError: true if status_code != 0
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

// ClassifyMiniMaxStatusCode maps MiniMax base_resp.status_code to gateway ErrorKind.
// Based on official docs: https://platform.minimax.io/docs/api-reference/text-post
//
// Status codes:
//   0    - success
//   1000 - unknown error
//   1001 - timeout
//   1002 - rate limit (throttling)
//   1004 - authentication failure
//   1008 - insufficient balance
//   1013 - service error
//   1027 - invalid output (content filter)
//   1039 - token limit exceeded
//   2013 - invalid parameter
func ClassifyMiniMaxStatusCode(code int) errorsx.ErrorKind {
	switch code {
	case 0:
		return "" // success, not an error
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

// FormatMiniMaxError returns a user-facing error message for a MiniMax base_resp error.
func FormatMiniMaxError(statusCode int, statusMsg string) string {
	if statusMsg != "" {
		return fmt.Sprintf("MiniMax error %d: %s", statusCode, statusMsg)
	}
	return fmt.Sprintf("MiniMax error %d", statusCode)
}
