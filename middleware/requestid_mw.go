package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

type requestIDContextKey struct{} //nolint:unused

type RequestIDMiddleware struct {
	BaseMiddleware
}

func NewRequestIDMiddleware() *RequestIDMiddleware {
	return &RequestIDMiddleware{
		BaseMiddleware: BaseMiddleware{name: "request_id"},
	}
}

// Wrap assigns a server-generated UUID to every inbound HTTP request and
// exposes it on the response as X-Request-Id.
//
// Behaviour:
//   - ALWAYS generates a fresh server-side request_id (so two retries
//     carrying the same X-Request-Id do NOT collide in request_logs).
//   - Preserves the client-supplied X-Request-Id on X-Client-Request-Id
//     (response header) and X-Gw-Client-Request-Id (request header for
//     downstream handlers) so it can be recorded in
//     request_logs.client_request_id for debug / cross-system tracing.
//   - gw_session_id (X-Gw-Session-Id) is the canonical correlation key
//     for "same client retry". Per-request ids are intentionally unique.
//
// Regression context (2026-06-26): previously this middleware reused the
// client X-Request-Id when present. A misbehaving client (ZCode agent
// reusing the same id across retries) caused 5 retries to produce 5
// request_logs rows sharing one request_id, all marked success=true
// because the last retry's UPDATE flowed through COALESCE. See the
// bug report for request_id 3875431e-9ba6-4e90-8b43-0d234f90d85d.
func (m *RequestIDMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always generate a new server-side id. Never reuse the
		// client's value — that would let one client retry poison
		// another retry's audit row.
		id := generateRequestID()

		// Forward the client value downstream so handlers can
		// persist it into request_logs.client_request_id.
		if clientID := strings.TrimSpace(r.Header.Get("X-Request-Id")); clientID != "" && clientID != id {
			r.Header.Set("X-Gw-Client-Request-Id", clientID)
			w.Header().Set("X-Client-Request-Id", clientID)
		}

		// Standard X-Request-Id round-trip (server value).
		r.Header.Set("X-Request-Id", id)
		w.Header().Set("X-Request-Id", id)

		next.ServeHTTP(w, r)
	})
}

func generateRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is exceedingly rare. Fall back to a
		// timestamp-based id so we still emit *something* unique.
		return "ts-" + hex.EncodeToString([]byte{byte(b[0])})
	}
	return hex.EncodeToString(b)
}
