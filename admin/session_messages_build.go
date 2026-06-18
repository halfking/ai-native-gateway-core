package admin

import (
	"time"
)

func buildSessionMessageMap(m requestMessageRow, seq int) map[string]any {
	direction := "user"
	if m.WorkType != nil && (*m.WorkType == "agent" || *m.WorkType == "memora") {
		direction = "assistant"
	} else if m.RequestMode != nil {
		mode := lowerASCII(*m.RequestMode)
		if mode == "completion" || mode == "embedding" {
			direction = "assistant"
		}
	}

	msg := map[string]any{
		"ts":                m.Ts.UTC().Format(time.RFC3339),
		"request_id":        m.RequestID,
		"seq":               seq,
		"direction":         direction,
		"client_model":      nilStr(m.ClientModel),
		"outbound_model":    nilStr(m.OutboundModel),
		"prompt_preview":    nilStr(m.PromptPreview),
		"response_preview":  nilStr(m.ResponsePreview),
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"latency_ms":        0,
		"cost_usd":          0.0,
		"status":            nilStr(m.RequestStatus),
	}
	if m.PromptTokens != nil {
		msg["prompt_tokens"] = *m.PromptTokens
	}
	if m.CompletionTokens != nil {
		msg["completion_tokens"] = *m.CompletionTokens
	}
	if m.LatencyMs != nil {
		msg["latency_ms"] = *m.LatencyMs
	}
	if m.CostUSD != nil {
		msg["cost_usd"] = *m.CostUSD
	}
	if m.ErrorKind != nil && *m.ErrorKind != "" {
		msg["error_kind"] = *m.ErrorKind
	}
	enrichSessionMessageFields(msg, m.RequestBody, m.ResponseBody, m.PromptPreview, m.ResponsePreview)
	return msg
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
