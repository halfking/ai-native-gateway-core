package streaming

import (
	"context"
	"io"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

type historyGuardFactory func(context.Context) func(context.Context, func() io.Reader) (bool, error)

var defaultHistoryDetector = sanitize.NewPatternDetector()

// SetHistoryGuardFactory shares the pinned input policy with the file reducer.
// Configure before serving requests, together with the sanitizer middleware.
func (h *ChatHandler) SetHistoryGuardFactory(factory func(context.Context) func(context.Context, func() io.Reader) (bool, error)) {
	h.historyGuardFactory = factory
}

func (h *ChatHandler) prepareHistoryGuard(r *http.Request) *http.Request {
	r = h.pinRules(r)
	if sessioncontext.HasHistoryGuard(r.Context()) {
		return r
	}
	factory := h.historyGuardFactory
	if factory == nil {
		factory = defaultHistoryDetector.HistoryGuard
	}
	return r.WithContext(sessioncontext.WithHistoryGuard(r.Context(), factory(r.Context())))
}
