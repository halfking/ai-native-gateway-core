// Package jsonbody — strict + bounded JSON body helpers.
//
// 2026-08-26 (Phase 0 P1-19 + P1-1 fix): endpoints that accept a JSON
// body — required or optional — must parse strictly:
//   - bounded input (http.MaxBytesReader),
//   - exactly one well-formed top-level JSON value,
//   - no trailing data.
//
// 2026-08-27 (audit fix — 兼容性): real-world senders turn out to emit
// bodies that are valid JSON-with-quirks rather than attacks:
//   - a UTF-8 BOM prefix (`\xEF\xBB\xBF`) from Windows tooling and
//     several SDKs — encoding/json rejects it outright;
//   - a literal `null` body where the endpoint requires an object —
//     Decode "succeeds" but leaves dst zero-valued, which downstream
//     code then mistakes for a real (empty) payload;
//   - CRLF / stray whitespace around the payload.
// The helpers now strip a single leading BOM and treat a bare `null`
// as "no body" so those senders keep working. Trailing garbage and
// multiple concatenated documents are still rejected.
package jsonbody

import (
	"bytes"
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

// utf8BOM is stripped from the head of a body before decoding. Go's
// encoding/json treats it as a syntax error, but a large population of
// Windows tooling and some vendor SDKs prepend it unconditionally.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// isJSONNull reports whether body (after BOM strip) consists solely of
// the literal top-level value `null` plus optional whitespace.
func isJSONNull(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) == 4 && bytes.Equal(trimmed, []byte("null"))
}

// DecodeBody parses an already-read JSON body into dst with the same
// compatibility rules as ReadRequired/ReadOptional: one leading UTF-8
// BOM is stripped, a bare `null` is reported via ErrEmptyBody (the
// caller decides whether that is acceptable), and trailing data after
// the first value is rejected.
//
// Callers that must hash the raw bytes (webhook HMAC verification)
// should hash the ORIGINAL body and only pass it here for parsing.
func DecodeBody(body []byte, dst any) error {
	if bytes.HasPrefix(body, utf8BOM) {
		body = body[len(utf8BOM):]
	}
	if isJSONNull(body) {
		return ErrEmptyBody
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(dst); err != nil {
		if isEOF(err) {
			return ErrEmptyBody
		}
		return err
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err == nil && len(trailing) > 0 {
		return errTrailingData
	}
	return nil
}

// readWithLimit is the shared implementation. requireBody controls
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
	// Read the whole (capped) body first so BOM stripping and the
	// null-literal check can run on bytes; the trailing-value check
	// then works on the same buffer DecodeBody uses.
	raw, err := io.ReadAll(limited)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			slog.Warn("jsonbody: body exceeded cap",
				"path", r.URL.Path,
				"limit_bytes", limit)
			WriteErrorWithCode(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", limit), ErrBodyTooLarge)
			return false, ErrBodyTooLarge
		}
		slog.Warn("jsonbody: body read failed",
			"path", r.URL.Path,
			"method", r.Method,
			"error", err.Error())
		WriteErrorWithCode(w, http.StatusBadRequest,
			"failed to read request body", err)
		return false, err
	}
	if err := DecodeBody(raw, dst); err != nil {
		if errors.Is(err, ErrEmptyBody) {
			if requireBody {
				WriteErrorWithCode(w, http.StatusBadRequest,
					"request body is required", ErrEmptyBody)
				return false, ErrEmptyBody
			}
			// Optional endpoint, empty (or null / BOM-only) body — proceed.
			return true, nil
		}
		if errors.Is(err, errTrailingData) {
			slog.Warn("jsonbody: trailing data after first JSON value",
				"path", r.URL.Path)
			WriteErrorWithCode(w, http.StatusBadRequest,
				"body must contain a single JSON value", nil)
			return false, err
		}
		slog.Warn("jsonbody: body failed to parse",
			"path", r.URL.Path,
			"method", r.Method,
			"error", err.Error())
		WriteErrorWithCode(w, http.StatusBadRequest,
			"request body is not valid JSON", err)
		return false, err
	}
	return true, nil
}

// errTrailingData is the sentinel DecodeBody wraps its trailing-data
// rejection in, so readWithLimit can distinguish it from a parse error.
var errTrailingData = errors.New("jsonbody: trailing data after first JSON value")

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
