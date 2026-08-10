package webcookie

// deepseek-web adapter (SKELETON — not yet functional).
//
// deepseek-web wraps chat.deepseek.com's browser chat. OmniRoute's
// open-sse/executors/deepseek-web.ts shows the protocol: SockJS over HTTPS,
// with a session token (x-vqd-4-style) fetched from a bootstrap endpoint,
// then chat messages POSTed as JSON.
//
// To make this functional, capture chat.deepseek.com's actual request flow
// (browser DevTools → Network during a chat), then implement:
//   - Login: GET the bootstrap endpoint to obtain a session token + cookies.
//   - Chat: POST to the chat-completions-shaped endpoint with the token.
//   - ParseStream: convert deepseek's SSE variant to OpenAI SSE chunks.
//
// This file provides the struct + method stubs so the framework compiles and
// the provider is routable (returning a clear "not implemented" error until
// the protocol is captured). Register via init() once implemented.

import (
	"context"
	"errors"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// ErrNotImplemented is returned by skeleton adapters whose site protocol
// has not yet been captured. The executor surfaces this as a 503 so the
// candidate is skipped and failover proceeds.
var ErrNotImplemented = errors.New("webcookie: adapter protocol not yet implemented; see docs/omnifree/web-cookie-providers.md")

// DeepSeekWebAdapter handles chat.deepseek.com (provider code "deepseek-web").
type DeepSeekWebAdapter struct{}

func (a *DeepSeekWebAdapter) ProviderCode() string { return "deepseek-web" }

func (a *DeepSeekWebAdapter) Login(_ context.Context, _ string, _ map[string]string) (*Session, error) {
	// TODO(capture): GET https://chat.deepseek.com/api/v0/chat/session to
	// bootstrap cookies + x-vqd-4 token. Populate Session.Cookies and ExpiresAt.
	return nil, ErrNotImplemented
}

func (a *DeepSeekWebAdapter) RefreshSession(_ context.Context, s *Session) (*Session, error) {
	return s, nil // no-op until Login is implemented
}

func (a *DeepSeekWebAdapter) Chat(_ context.Context, _ *Session, _ provider.Candidate, _ []byte, _ bool) (*http.Response, error) {
	// TODO(capture): POST https://chat.deepseek.com/api/v0/chat/completion
	// with session token + SockJS framing → return the streaming response.
	return nil, ErrNotImplemented
}

func (a *DeepSeekWebAdapter) ParseStream(_ http.ResponseWriter, _ *http.Response) error {
	// TODO(capture): deepseek streams as SSE data: lines; transform to OpenAI
	// chat.completion.chunk shape. Until then, ErrNotImplemented.
	return ErrNotImplemented
}

func init() {
	// Registered but returns ErrNotImplemented until the protocol is captured.
	// Keeping it registered lets the framework report "adapter exists, pending
	// capture" rather than "no adapter" in diagnostics.
	DefaultRegistry.Register(&DeepSeekWebAdapter{})
}
