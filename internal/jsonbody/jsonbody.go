// Package jsonbody — strict + bounded JSON body helpers.
//
// 2026-08-26 (Phase 0 P1-19 + P1-1 fix): endpoints that accept a JSON
// body — required or optional — must parse strictly:
//   - bounded input (http.MaxBytesReader),
//   - exactly one well-formed top-level JSON value,
//   - no trailing data.
//
// Before this fix, callers used `json.NewDecoder(r.Body).Decode(&dst)`
// against an unbounded body, and many sites silently coerced parse
// errors to the zero-value target. The helpers here split into two:
//
//   - ReadOptional: body MAY be empty; parse failure → 400.
//   - ReadRequired: body MUST be present; empty / parse failure → 400.
//
// Both enforce a hard cap (1 MiB default) so a hostile or buggy client
// cannot use any admin endpoint as an unbounded-DoS surface. Callers
// needing a different cap can pass it via ReadOptionalWithLimit /
// ReadRequiredWithLimit.
//
// Every admin endpoint that previously did
// `if err := json.NewDecoder(r.Body).Decode(&dst); err != nil { ... }`
// should migrate to one of these two helpers.
package jsonbody

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// ErrBodyTooLarge is returned when a body exceeds the cap.
var ErrBodyTooLarge = errors.New("jsonbody: body too large")

// ErrEmptyBody is returned by ReadRequired when no body was supplied.
var ErrEmptyBody = errors.New("jsonbody: body required but empty")

// IsBodyTooLarge reports whether err is (or wraps) ErrBodyTooLarge.
func IsBodyTooLarge(err error) bool {
	return errors.Is(err, ErrBodyTooLarge)
}

// IsEmptyBody reports whether err is (or wraps) ErrEmptyBody.
func IsEmptyBody(err error) bool {
	return errors.Is(err, ErrEmptyBody)
}

// MaxOptionalBody caps an optional-body endpoint at 1 MiB — generous
// for any plausible admin / replay payload, but bounded so an
// attacker cannot use an optional endpoint as an unbounded-DoS
// surface.
const MaxOptionalBody = 1 << 20

// MaxRequiredBody caps a required-body admin endpoint at 1 MiB. Real
// admin POSTs (create / update endpoints) are < 64 KiB in practice;
// 1 MiB is enough headroom for batch operations.
const MaxRequiredBody = 1 << 20

// ReadOptional decodes a JSON body that may be empty.
//
// Semantics:
//   - body is nil or empty                       → returns (true, nil)
//   - body is present and parses successfully    → returns (true, nil), dst populated
//   - body is present but malformed              → returns (false, error), writes 400
//   - body exceeds MaxOptionalBody               → returns (false, ErrBodyTooLarge)
//
// The first return value is "is the request shape acceptable" — true
// when the handler may proceed, false when it should write an error
// response and abort.
func ReadOptional(w http.ResponseWriter, r *http.Request, dst any) (bool, error) {
	return readWithLimit(w, r, dst, MaxOptionalBody, false)
}

// ReadRequired decodes a JSON body that must be present.
//
// Semantics:
//   - body is nil or empty                       → returns (false, ErrEmptyBody), writes 400
//   - body is present and parses successfully    → returns (true, nil), dst populated
//   - body is present but malformed              → returns (false, error), writes 400
//   - body exceeds MaxRequiredBody               → returns (false, ErrBodyTooLarge), writes 400
func ReadRequired(w http.ResponseWriter, r *http.Request, dst any) (bool, error) {
	return readWithLimit(w, r, dst, MaxRequiredBody, true)
}

// ReadRequiredWithLimit is ReadRequired with a custom cap. Used by
// batch endpoints that legitimately need more than 1 MiB.
func ReadRequiredWithLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) (bool, error) {
	return readWithLimit(w, r, dst, limit, true)
}

// readWithLimit is the shared implementation. requireEmpty controls
// whether an empty body is allowed (ReadOptional) or rejected with
// ErrEmptyBody (ReadRequired).
func readWithLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64, requireBody bool) (bool, error) {
	if r == nil || r.Body == nil {
		if requireBody {
			WriteErrorWithCode(w, http.StatusBadRequest,
				"request body is required", ErrEmptyBody)
			return false, ErrEmptyBody
		}
		return true, nil
	}
	limited := http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(limited)
	if err := dec.Decode(dst); err != nil {
		// EOF on an empty body.
		if isEOF(err) {
			if requireBody {
				WriteErrorWithCode(w, http.StatusBadRequest,
					"request body is required", ErrEmptyBody)
				return false, ErrEmptyBody
			}
			return true, nil
		}
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			slog.Warn("jsonbody: body exceeded cap",
				"path", r.URL.Path,
				"limit_bytes", limit)
			WriteErrorWithCode(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", limit), ErrBodyTooLarge)
			return false, ErrBodyTooLarge
		}
		slog.Warn("jsonbody: body failed to parse",
			"path", r.URL.Path,
			"method", r.Method,
			"error", err.Error())
		WriteErrorWithCode(w, http.StatusBadRequest,
			"request body is not valid JSON", err)
		return false, err
	}
	// Reject trailing garbage: a single well-formed JSON value is the
	// contract.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err == nil && len(trailing) > 0 {
		slog.Warn("jsonbody: trailing data after first JSON value",
			"path", r.URL.Path)
		WriteErrorWithCode(w, http.StatusBadRequest,
			"body must contain a single JSON value", nil)
		return false, fmt.Errorf("jsonbody: trailing data")
	}
	return true, nil
}

// WriteError writes a stable error response so every caller has the
// same shape. Code is the stable machine-readable error code.
func WriteError(w http.ResponseWriter, status int, msg string, parseErr error) {
	WriteErrorWithCode(w, status, msg, parseErr)
}

// WriteErrorWithCode is the lower-level variant that lets callers set
// a custom code (e.g. ErrEmptyBody → "jsonbody.empty_required_body").
func WriteErrorWithCode(w http.ResponseWriter, status int, msg string, parseErr error) {
	code := "jsonbody.invalid_body"
	switch {
	case errors.Is(parseErr, ErrEmptyBody):
		code = "jsonbody.empty_required_body"
	case errors.Is(parseErr, ErrBodyTooLarge):
		code = "jsonbody.body_too_large"
	case parseErr != nil:
		code = "jsonbody.invalid_body"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	payload := map[string]any{
		"error": msg,
		"code":  code,
	}
	if parseErr != nil && !errors.Is(parseErr, ErrEmptyBody) && !errors.Is(parseErr, ErrBodyTooLarge) {
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
