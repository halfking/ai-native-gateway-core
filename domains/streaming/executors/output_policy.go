package executors

import (
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// The legacy redaction callback returns only bytes. Recognize its structured
// policy envelope at the write boundary so routing receives a typed terminal
// instead of committing that envelope as an HTTP 200 success.
func outputPolicyError(body []byte) error {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Code == "output_policy_blocked" {
		return errorsx.ErrOutputPolicyBlocked
	}
	return nil
}
