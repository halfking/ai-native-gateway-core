package streaming

import (
	"context"
	"net/http"
)

// SetRuleSnapshotPin fixes the policy once at the common request entry, even
// when request sanitization is disabled. Protocol adapters preserve this context.
func (h *ChatHandler) SetRuleSnapshotPin(pin func(context.Context) context.Context) {
	h.pinRuleContext = pin
}

func (h *ChatHandler) pinRules(r *http.Request) *http.Request {
	if h != nil && h.pinRuleContext != nil {
		return r.WithContext(h.pinRuleContext(r.Context()))
	}
	return r
}
