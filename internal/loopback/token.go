package loopback

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
)

// TokenHeader carries this process's per-boot loopback trust token. The
// internal HTTP loopbacks (auto-title / auto-summary generators) attach it
// to their self-calls; middleware strips the gateway correlation headers
// from any request that does not present the current token, so external
// clients cannot forge them (R35-R1, 2026-09-17).
//
// The goal audit shadow rounds need no token: they call h.ServeHTTP
// in-process (response_interceptor_helpers.go defaultDispatchFollowUp) and
// never traverse the middleware chain.
const TokenHeader = "X-Gw-Loopback-Token"

// CorrelationHeaders are the forgeable gateway correlation headers that only
// this process's own HTTP loopbacks may set. X-Gw-Is-Auto drives
// logCtx.IsAutoRequest (session_turns mirror exclusion, auto-title
// self-trigger suppression, executors' continuation-trim exemption);
// X-Gw-Parent-Request-Id / X-Gw-Source-Actor persist into
// request_logs_hot.parent_request_id / origin_actor.
var CorrelationHeaders = []string{
	"X-Gw-Is-Auto",
	"X-Gw-Parent-Request-Id",
	"X-Gw-Source-Actor",
}

var (
	tokenOnce sync.Once
	tokenVal  string
)

// Token returns this process's per-boot random loopback token. The value
// lives only in process memory — the documented deployment contract is that
// LLM_GATEWAY_ENDPOINT (the loopback URL override) always points at THIS
// process; a topology that loops back through an LB or a peer instance gets
// its correlation headers stripped (observability regression only, logged)
// and must move to a deployment-injected shared token instead.
func Token() string {
	tokenOnce.Do(func() {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			// A broken crypto/rand makes the token guessable, which defeats
			// the whole trust mechanism — fail loudly instead of serving with
			// a forgeable trust credential.
			panic("loopback: crypto/rand unavailable for loopback token: " + err.Error())
		}
		tokenVal = hex.EncodeToString(b)
	})
	return tokenVal
}

// StripUntrustedCorrelationHeaders deletes the correlation headers from a
// request that does not carry the current loopback token, and always removes
// the token header itself so it never leaks into logs or upstream calls.
// It returns the names of the correlation headers that were actually
// present and stripped (empty when the request carried none — the common
// case — so callers can skip logging).
func StripUntrustedCorrelationHeaders(r *http.Request) []string {
	present := tokenFrom(r)
	if subtle.ConstantTimeCompare([]byte(present), []byte(Token())) == 1 {
		r.Header.Del(TokenHeader)
		return nil
	}
	stripped := make([]string, 0, len(CorrelationHeaders))
	for _, h := range CorrelationHeaders {
		if r.Header.Get(h) != "" {
			stripped = append(stripped, h)
		}
		r.Header.Del(h)
	}
	r.Header.Del(TokenHeader)
	if len(stripped) == 0 {
		return nil
	}
	return stripped
}

func tokenFrom(r *http.Request) string {
	return r.Header.Get(TokenHeader)
}
