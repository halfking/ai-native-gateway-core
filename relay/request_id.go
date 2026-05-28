package relay

import "github.com/kaixuan/llm-gateway-go/audit"

func newAuditEvent(requestID string) *audit.EventBuilder {
	b := audit.NewEvent()
	if requestID != "" {
		b.RequestID(requestID)
	}
	return b
}