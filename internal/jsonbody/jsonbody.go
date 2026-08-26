// Package jsonbody — strict + bounded JSON body helpers.
//
// 2026-08-26 (Phase 0 P1-19 fix): endpoints that accept an "optional"
// body must NOT silently swallow JSON parse errors. Before this fix,
// several handlers did `_ = json.NewDecoder(r.Body).Decode(&dst)` and
// proceeded with the zero-value target — that hides malformed input
// (typos, truncated JSON, wrong Content-Type) and lets the rest of
// the handler run with stale defaults. The helper here distinguishes:
//
//   - body absent / empty           → optional, proceed with zero dst.
//   - body present but malformed    → 400 Bad Request with a stable
//                                     error code, log the parse error.
//
// Every caller also gets a hard cap on the body size so a malicious or
// buggy client can't push an unbounded chunked request through an
// optional-body endpoint.
package jsonbody

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// ErrBodyTooLarge is returned when an optional body exceeds the cap.
var ErrBodyTooLarge = errors.New("jsonbody: body too large")

// IsBodyTooLarge reports whether err is (or wraps) ErrBodyTooLarge.
// The MaxBytesReader check is folded into the same return path so
// callers can branch on one error type.
func IsBodyTooLarge(err error) bool {
	return errors.Is(err, ErrBodyTooLarge)
}

// MaxOptionalBody caps an optional-body endpoint at 1 MiB — generous
// for any plausible admin / replay payload, but bounded so an
// attacker cannot use an optional endpoint as an unbounded-DoS
// surface. Callers that need a different cap can wrap this constant
// with their own http.MaxBytesReader and rely on ReadOptional below.
const MaxOptionalBody = 1 << 20

// ReadOptional decodes a JSON body that may be empty.
//
// Semantics:
//   - body is nil or Content-Length == 0            → returns (true, nil)
//   - body is present and parses successfully       → returns (true, nil), dst populated
//   - body is present but malformed                 → returns (false, error)
//   - body exceeds MaxOptionalBody                  → returns (false, ErrBodyTooLarge)
//
// The first return value is "is the request shape acceptable" — true
// when the handler may proceed, false when it should write an error
// response and abort. Callers should do:
//
//	if ok, err := jsonbody.ReadOptional(w, r, &dst); !ok {
//	    // jsonbody.WriteError has already been called if err is set
//	    return
//	}
//
// The wrapper does NOT write a response on success; the caller keeps
// full control of the happy path. On failure it writes a 400 with a
// stable code so the frontend can switch on it.
func ReadOptional(w http.ResponseWriter, r *http.Request, dst any) (bool, error) {
	if r == nil || r.Body == nil {
		return true, nil
	}
	// Enforce a hard cap on the optional body. Without this, an
	// attacker who discovers an "optional body" endpoint can push
	// 4 GiB through it and starve the server's read buffers.
	limited := http.MaxBytesReader(w, r.Body, MaxOptionalBody)
	dec := json.NewDecoder(limited)
	if err := dec.Decode(dst); err != nil {
		// EOF on an empty body: optional path, proceed with zero dst.
		if errors.Is(err, http.ErrBodyReadAfterClose) || isEOF(err) {
			return true, nil
		}
		// http.MaxBytesReader returns *http.MaxBytesError when the
		// cap is exceeded. Surface it as ErrBodyTooLarge so callers
		// can branch cleanly.
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			slog.Warn("jsonbody: optional body exceeded cap",
				"path", r.URL.Path,
				"limit_bytes", MaxOptionalBody)
			WriteError(w, http.StatusBadRequest, "optional body exceeds limit", err)
			return false, ErrBodyTooLarge
		}
		// The body was present but did not parse. Surface 400.
		slog.Warn("jsonbody: optional body failed to parse",
			"path", r.URL.Path,
			"method", r.Method,
			"error", err.Error())
		WriteError(w, http.StatusBadRequest, "optional body is not valid JSON", err)
		return false, err
	}
	// Reject trailing garbage: a single well-formed JSON value is the
	// contract, not "JSON plus comments". json.Decoder.Decode stops
	// at the first valid value; the next token (if any) lives past
	// the stream. We poke it via a second Decode into json.RawMessage
	// — any non-trivial payload here means the caller sent multiple
	// top-level values, which our audit (P1-19 strict single-value)
	// prohibits.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err == nil && len(trailing) > 0 {
		slog.Warn("jsonbody: trailing data after first JSON value",
			"path", r.URL.Path)
		WriteError(w, http.StatusBadRequest, "body must contain a single JSON value", nil)
		return false, fmt.Errorf("jsonbody: trailing data")
	}
	return true, nil
}

// WriteError writes a stable error response so every caller has the
// same shape. Callers that already writeError(w, ...) directly can
// keep using their existing helper; this one is provided for the
// cases where the optional-body helper is shared.
func WriteError(w http.ResponseWriter, status int, msg string, parseErr error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	payload := map[string]any{
		"error":   msg,
		"code":    "jsonbody.invalid_optional_body",
	}
	if parseErr != nil {
		payload["parse_error"] = parseErr.Error()
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// isEOF reports whether err is the standard library's "no more input"
// sentinel. json.Decoder returns io.EOF when the body is empty; we
// tolerate that as the "truly optional" case.
func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}
