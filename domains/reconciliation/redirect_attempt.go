package reconciliation

import (
	"net/http"
	"slices"
	"strings"
)

// AttributeRedirect keeps the gateway identity on a redirect hop and retains
// candidate dimensions only when the redirected request still supports them.
func AttributeRedirect(attempt Attempt, original, redirected *http.Request) Attempt {
	attempt.Source = "http_redirect:" + attempt.Source
	if !strings.EqualFold(original.URL.Scheme, redirected.URL.Scheme) ||
		!strings.EqualFold(original.URL.Host, redirected.URL.Host) ||
		!strings.EqualFold(original.Host, redirected.Host) {
		attempt.ProviderID = 0
		attempt.CredentialID = 0
		attempt.Model = ""
		attempt.RateSnapshot = nil
		return attempt
	}
	credentialChanged := original.URL.RawQuery != redirected.URL.RawQuery
	for _, header := range []string{"Authorization", "X-API-Key", "API-Key", "X-Goog-Api-Key"} {
		credentialChanged = credentialChanged || !slices.Equal(original.Header.Values(header), redirected.Header.Values(header))
	}
	if credentialChanged {
		attempt.CredentialID = 0
		attempt.RateSnapshot = nil
	}
	if original.Method != redirected.Method || original.URL.EscapedPath() != redirected.URL.EscapedPath() {
		attempt.Model = ""
		attempt.RateSnapshot = nil
	}
	return attempt
}

// ResponseEndpoint returns the observed HTTP endpoint for interpreting a
// provider's returned money. A Host override leaves the endpoint unverified.
func ResponseEndpoint(resp *http.Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	req := resp.Request
	if req.URL == nil || (req.Host != "" && !strings.EqualFold(req.Host, req.URL.Host)) {
		return ""
	}
	return req.URL.String()
}
