package upstream

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)


// R28-P-2: strict egress mode must fail a non-domestic request when no
// healthy proxy exists, while domestic hosts keep direct access and the
// default (non-strict) mode keeps the historical fail-open behavior.
func TestStrictProxyTransportBlocksOverseasWithoutProxy(t *testing.T) {
	// Precondition: strict mode with NO healthy proxy. NewProxyResolver reads
	// HTTPS_PROXY/HTTP_PROXY from the environment; on a developer machine with
	// a live local proxy the resolver would report proxyAvailable()=true and
	// the strict block would never trigger. Pin the precondition explicitly.
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("HTTP_PROXY", "")

	strict := NewProxyResolver()
	strict.strict.Store(true)

	blocked := strict.StrictBlocked("api.openai.com")
	if !blocked {
		t.Fatal("non-domestic host must be blocked in strict mode without a proxy")
	}
	if strict.StrictBlocked("api.deepseek.com") {
		t.Fatal("domestic host must never be strict-blocked")
	}

	rt := &strictProxyTransport{base: &errorRoundTripper{}, resolver: strict}
	req, _ := http.NewRequest(http.MethodGet, "https://api.openai.com/v1/x", nil)
	if _, err := rt.RoundTrip(req); err == nil ||
		!strings.Contains(err.Error(), "strict egress mode") {
		t.Fatalf("strict transport must reject overseas host: %v", err)
	}
	reqDomestic, _ := http.NewRequest(http.MethodGet, "https://api.deepseek.com/v1/x", nil)
	if _, err := rt.RoundTrip(reqDomestic); err == nil ||
		!strings.Contains(err.Error(), "errorRoundTripper") {
		t.Fatalf("domestic host must reach the base transport: %v", err)
	}

	// Default mode (strict off) keeps fail-open semantics.
	open := NewProxyResolver()
	if open.StrictBlocked("api.openai.com") {
		t.Fatal("non-strict resolver must not block")
	}
}

type errorRoundTripper struct{}

func (errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("errorRoundTripper reached")
}
