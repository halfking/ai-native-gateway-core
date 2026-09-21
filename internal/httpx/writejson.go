// Package httpx provides shared helpers for writing HTTP responses.
//
// It exists to converge the per-handler writeJSON-family helpers that
// were previously hand-copied across admin, cmd/gateway and domains
// packages (deadcode cleanup round 2, follow-up batch). Local helpers
// remain as thin wrappers so call sites are untouched; behavioural
// differences (extra envelope fields, marshal-error fallback bodies)
// stay at the call sites.
package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON marshals v as JSON and writes it to w with the given status
// code and Content-Type, followed by the trailing newline that every
// previous per-handler helper emitted (json.Encoder appends it
// implicitly; the marshal-first helpers wrote it explicitly).
//
// contentType is used verbatim; the two conventions in this repo are
// "application/json" and "application/json; charset=utf-8". Pass "" to
// leave the header unset and let net/http sniff the body.
//
// Only marshalling errors are returned, and when WriteJSON returns a
// non-nil error nothing has been written yet (no header, no status), so
// the caller may still emit its own error response. Write errors after
// the status is committed are ignored: the client connection is gone
// and, matching the previous per-handler helpers, the caller cannot
// recover.
func WriteJSON(w http.ResponseWriter, status int, contentType string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	//nolint:errcheck // HTTP write error non-recoverable
	w.Write(append(data, '\n'))
	return nil
}
