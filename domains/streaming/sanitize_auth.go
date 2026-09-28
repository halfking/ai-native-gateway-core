package streaming

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // same handler authentication surface
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

type sanitizeVerifiedKeyContextKey struct{}

type sanitizeVerifiedKey struct {
	rawKey string
	info   *authentication.KeyInfo
	err    error
}

// prepareSanitizeRequest resolves tenant identity before allocating a
// tenant-scoped placeholder. The verified result is memoized for the handler's
// normal auth branch, so Verify's cache/touch side effects run only once.
func (h *ChatHandler) prepareSanitizeRequest(r *http.Request) (*http.Request, bool) {
	if h == nil || r == nil {
		return r, false
	}
	if h.keyVerifier == nil || !h.keyVerifier.Enabled() {
		// Chat's static-key fallback authenticates inside serveWithExecutor.
		// Reject before the sanitizer can persist attacker-controlled mappings.
		if h.staticDataPlaneKey != "" {
			rawKey := extractBearerToken(r)
			if rawKey == "" || subtle.ConstantTimeCompare([]byte(h.staticDataPlaneKey), []byte(rawKey)) != 1 {
				return r, false
			}
		}
		// Local no-verifier mode has one fixed tenant. Never use a caller header.
		return r.WithContext(sanitize.WithAuthenticatedTenant(r.Context(), "default")), true
	}
	rawKey := extractBearerToken(r)
	if rawKey == "" {
		// The existing handler emits its protocol-specific missing-key error.
		return r, false
	}
	info, err := h.keyVerifier.Verify(r.Context(), rawKey)
	if err == nil && info == nil {
		err = errors.New("key verifier returned nil key info")
	}
	ctx := context.WithValue(r.Context(), sanitizeVerifiedKeyContextKey{}, sanitizeVerifiedKey{
		rawKey: rawKey, info: info, err: err,
	})
	if err != nil {
		// The normal handler consumes the memoized failure and cannot dispatch.
		return r.WithContext(ctx), false
	}
	ctx = sanitize.WithAuthenticatedTenant(ctx, tenant(info))
	return r.WithContext(ctx), true
}

func verifyRequestKey(r *http.Request, verifier requestKeyVerifier, rawKey string) (*authentication.KeyInfo, error) {
	if r != nil {
		if cached, ok := r.Context().Value(sanitizeVerifiedKeyContextKey{}).(sanitizeVerifiedKey); ok && cached.rawKey == rawKey {
			return cached.info, cached.err
		}
	}
	return verifier.Verify(r.Context(), rawKey)
}
