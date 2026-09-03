// Package modelresponse parses vendor model-list responses into model IDs.
package modelresponse

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// 2026-09-02: structured Error so callers can distinguish an upstream that
// "returned non-JSON (likely an HTML error page)" from "returned JSON but
// we could not recognize the model-list shape". The two have very different
// operator-facing fixes:
//   - non_json_body       → base_url / reverse-proxy misconfiguration;
//     the upstream gateway/proxy intercepts and
//     returns an HTML login/error page on 200 OK.
//   - invalid_models_format → the body is JSON but uses an undocumented
//     vendor wrapper that collectModelIDs did not
//     recognize; needs code-side support.
//
// Surface this as a typed error so admins / UI can render a tailored hint
// instead of leaking "<html>…" into the admin health_error field.
const (
	ErrorKindNonJSONBody       = "non_json_body"
	ErrorKindInvalidModelsFmt  = "invalid_models_format"
	ErrorKindUnrecognizedShape = "unrecognized_models_response_shape"
)

// Error wraps a model-list parse failure with a Kind so callers can render
// a targeted message. Body holds the offending payload when non-empty so
// preview helpers can render a short, redacted snippet.
type Error struct {
	Kind string
	Err  error
	Body []byte
}

func (e *Error) Error() string {
	switch e.Kind {
	case ErrorKindNonJSONBody:
		return fmt.Sprintf("parse models response failed: body is not JSON (likely HTML/XML), underlying=%v, body_bytes=%d", e.Err, len(e.Body))
	case ErrorKindInvalidModelsFmt:
		return fmt.Sprintf("parse models response failed: JSON decoded but no model ids recognized, underlying=%v, body_bytes=%d", e.Err, len(e.Body))
	default:
		return fmt.Sprintf("parse models response failed: %v (context: body_bytes=%d)", e.Err, len(e.Body))
	}
}

func (e *Error) Unwrap() error { return e.Err }

// Is reports Kind membership without callers having to type-assert; lets
// `errors.Is(err, modelresponse.ErrNonJSONBody)` work too (we keep a
// package-level sentinel for ergonomic comparison).
var (
	ErrNonJSONBody       = errors.New("upstream models body is not JSON")
	ErrInvalidModelsFmt  = errors.New("upstream models body is JSON but unrecognized shape")
	ErrUnrecognizedShape = errors.New("unrecognized models response format")
)

var collectionKeys = []string{"data", "models", "items", "results", "result", "response"}
var modelIDKeys = []string{"id", "name", "model", "model_id", "model_name", "slug"}

// maxCollectDepth bounds collectModelIDs recursion. The parser only walks
// vendor model-list responses, where the realistic depth is 4-6 (wrapper →
// collection → model object). A cap of 64 is generous for any documented
// provider shape and still small enough to keep the goroutine stack safe
// against a hostile or pathological body that nests objects thousands deep
// (the previous implementation recursed without bound and would stack-overflow
// on such input, killing the worker goroutine).
const maxCollectDepth = 64

// bodyPreviewSize bounds the snippet attached to a non-JSON parse failure.
// 200 bytes is enough to spot "<html", "<!DOCTYPE", "<?xml", a Cloudflare
// error page banner, or an Nginx/Apache default 404 banner, without leaking
// the entire upstream payload into admin surfaces or audit logs.
const bodyPreviewSize = 200

// ParseModelIDs accepts common OpenAI-compatible and vendor-wrapped model lists.
func ParseModelIDs(data []byte) ([]string, error) {
	trimmed := bytes.TrimLeftFunc(data, unicode.IsSpace)
	if looksLikeNonJSON(trimmed) {
		return nil, &Error{
			Kind: ErrorKindNonJSONBody,
			Err:  ErrNonJSONBody,
			Body: data,
		}
	}

	var root any
	if err := json.NewDecoder(bytes.NewReader(trimmed)).Decode(&root); err != nil {
		return nil, &Error{
			Kind: ErrorKindInvalidModelsFmt,
			Err:  err,
			Body: data,
		}
	}

	ids := make([]string, 0)
	seen := make(map[string]struct{})
	collectModelIDs(root, false, 0, &ids, seen)
	if len(ids) == 0 {
		return nil, &Error{
			Kind: ErrorKindUnrecognizedShape,
			Err:  ErrUnrecognizedShape,
			Body: data,
		}
	}
	return ids, nil
}

// looksLikeNonJSON applies a cheap structural sniff: any payload that begins
// with '<' (HTML / XML / SGML / DOCTYPE / processing instruction) or is
// empty / whitespace-only is treated as non-JSON for parse-error purposes.
//
// We deliberately do NOT try to parse HTML or strip a leading BOM beyond the
// TrimLeftFunc above — being too clever here (e.g. handling the UTF-8 BOM)
// invites false positives on real JSON payloads that happen to start with a
// meta character.
func looksLikeNonJSON(payload []byte) bool {
	if len(payload) == 0 {
		return true
	}
	switch payload[0] {
	case '<':
		return true
	}
	return false
}

// Kind extracts the error kind from an error returned by ParseModelIDs. It
// walks the wrap chain via errors.As so nested fmt.Errorf("%w", …) callers
// get the right value. Returns "" if err is nil or not a modelresponse.Error.
func Kind(err error) string {
	if err == nil {
		return ""
	}
	var me *Error
	if errors.As(err, &me) {
		return me.Kind
	}
	return ""
}

// Is reports whether the underlying error chain carries the given kind.
// Returns false for nil.
func Is(err error, kind string) bool {
	return Kind(err) == kind
}

// NonJSONBody is shorthand for Is(err, ErrorKindNonJSONBody). Used in the
// admin probe path to suppress the verbose "<html>… body_bytes=1726" string
// from leaking into UI surfaces — instead callers can show a tailored
// "upstream returned non-JSON" message.
func NonJSONBody(err error) bool { return Is(err, ErrorKindNonJSONBody) }

// Preview returns a single-line, length-bounded snippet of the offending
// body. It strips control characters and non-printable bytes that would
// otherwise corrupt log lines (newlines, tabs, etc.) and trims to
// bodyPreviewSize bytes from the front. Returns "" if err is nil or not
// a modelresponse.Error.
func Preview(err error) string {
	if err == nil {
		return ""
	}
	var me *Error
	if !errors.As(err, &me) || len(me.Body) == 0 {
		return ""
	}
	limit := len(me.Body)
	if limit > bodyPreviewSize {
		limit = bodyPreviewSize
	}
	var b strings.Builder
	for i := 0; i < limit; i++ {
		c := me.Body[i]
		if c == '\n' || c == '\r' || c == '\t' || c < 0x20 {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(c)
	}
	return strings.TrimSpace(b.String())
}

func collectModelIDs(value any, inCollection bool, depth int, ids *[]string, seen map[string]struct{}) {
	if depth > maxCollectDepth {
		// Bound the recursion: the parser has walked deep enough that any
		// further model id is, by construction, unreachable through the
		// known collection keys. Stop descending instead of risking a
		// stack overflow on a hostile body.
		return
	}
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			collectModelIDs(item, true, depth+1, ids, seen)
		}
	case map[string]any:
		if inCollection {
			if id := modelIDFromObject(current); id != "" {
				appendModelID(id, ids, seen)
				return
			}
		}
		for _, key := range collectionKeys {
			if nested, ok := current[key]; ok {
				collectModelIDs(nested, key != "result" && key != "response", depth+1, ids, seen)
			}
		}
	case string:
		if inCollection {
			appendModelID(current, ids, seen)
		}
	}
}

func modelIDFromObject(model map[string]any) string {
	for _, key := range modelIDKeys {
		if value, ok := model[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func appendModelID(id string, ids *[]string, seen map[string]struct{}) {
	id = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(id), "models/"))
	if id == "" {
		return
	}
	key := strings.ToLower(id)
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*ids = append(*ids, id)
}
