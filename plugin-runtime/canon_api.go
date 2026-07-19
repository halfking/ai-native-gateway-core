package pluginruntime

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"
)

// hmacEqualString is a constant-time comparison for two hex signature
// strings, wrapping subtle.ConstantTimeCompare. It does not short-circuit,
// limiting timing side-channels on signature verification.
func hmacEqualString(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type ctxKey int

const tenantCtxKey ctxKey = 0

// canonOpts holds optional configuration for VerifyPluginContext. The zero
// value reproduces the original behavior (HMAC + skew check, no replay cache).
type canonOpts struct {
	cache *NonceCache
}

// CanonOption configures VerifyPluginContext. Pass options after `next`; the
// middleware is backward-compatible when no option is supplied.
type CanonOption func(*canonOpts)

// WithCanonNonceCache enables replay protection on the canonical plugin path.
// After the HMAC signature passes, the token's (pluginID|tenantID|ts|nonce)
// key is recorded; a second call presenting the same key within the cache's
// TTL is rejected with 401. The cache TTL MUST exceed the ±5min skew window
// so a token cannot fall out of the cache before its freshness window closes.
func WithCanonNonceCache(c *NonceCache) CanonOption {
	return func(o *canonOpts) { o.cache = c }
}

// VerifyPluginContext is middleware that validates the X-Gateway-Context-*
// signed headers (the same HMAC contract the plugin process sends and the
// proxy already injects on forwarded requests). On success it stores the
// verified tenant in the request context for downstream retrieval via
// TenantFromVerified; on any failure it returns 401 without invoking the
// handler.
//
// This is the canonical auth path for plugin processes that run without a
// browser admin cookie: identity is not "trusted from headers" but derived
// from an HMAC signature over (pluginID|tenantID|timestamp|nonce) keyed by
// the shared gateway secret. A plugin cannot claim a tenant it was not
// signed for, because it cannot forge the signature.
//
// Without options the middleware is backward-compatible (HMAC + ±5min skew
// only). Pass WithCanonNonceCache to additionally reject replayed tokens for
// the cache's TTL — closing the 5-minute replay window that HMAC alone
// leaves open.
func VerifyPluginContext(secret []byte, next http.Handler, opts ...CanonOption) http.Handler {
	o := &canonOpts{}
	for _, opt := range opts {
		opt(o)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pluginID := r.Header.Get("X-Gateway-Plugin-ID")
		tenantID := r.Header.Get("X-Gateway-Tenant-ID")
		tsStr := r.Header.Get("X-Gateway-Context-Timestamp")
		nonce := r.Header.Get("X-Gateway-Context-Nonce")
		sig := r.Header.Get("X-Gateway-Context-Signature")
		if pluginID == "" || tenantID == "" || tsStr == "" || nonce == "" || sig == "" {
			http.Error(w, "missing plugin context", http.StatusUnauthorized)
			return
		}
		ts, err := strconv.ParseInt(tsStr, 10, 64)
		if err != nil {
			http.Error(w, "bad timestamp", http.StatusUnauthorized)
			return
		}
		// Replay window: reject timestamps skewed more than 5 minutes from now
		// (covers both stale and far-future signatures).
		if age := time.Since(time.Unix(ts, 0)); age > 5*time.Minute || age < -5*time.Minute {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		// Reuse the package's signContext (HMAC-SHA256 over
		// "%s|%s|%d|%s", hex) so the verifier is byte-identical to what the
		// plugin side and the proxy injector compute.
		want := signContext(secret, pluginID, tenantID, ts, nonce)
		if !hmacEqualString(sig, want) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		// Replay protection: AFTER the signature passes and BEFORE handing to
		// next, record the token key. SeenFirst returns true only for the
		// first caller within the TTL; a replayed (pluginID|tenantID|ts|nonce)
		// is rejected with 401. Skipped when no cache is configured, so the
		// middleware stays backward-compatible for existing callers.
		if o.cache != nil {
			key := pluginID + "|" + tenantID + "|" + tsStr + "|" + nonce
			if !o.cache.SeenFirst(key) {
				http.Error(w, "nonce replay", http.StatusUnauthorized)
				return
			}
		}
		r = r.WithContext(context.WithValue(r.Context(), tenantCtxKey, tenantID))
		next.ServeHTTP(w, r)
	})
}

// TenantFromVerified returns the tenant validated by VerifyPluginContext, or
// "" if the request did not pass verification.
func TenantFromVerified(r *http.Request) string {
	if v, ok := r.Context().Value(tenantCtxKey).(string); ok {
		return v
	}
	return ""
}
