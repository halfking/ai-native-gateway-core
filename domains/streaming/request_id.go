package streaming

import "github.com/kaixuan/llm-gateway-go/domains/hooks/audit" //nolint:depguard

func newAuditEvent(requestID string) *audit.EventBuilder {
	b := audit.NewEvent()
	if requestID != "" {
		b.RequestID(requestID)
	}
	return b
}
