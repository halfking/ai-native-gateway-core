package dbx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"
)

// NormalizeJSONB validates and canonicalizes a JSONB column value.
// Accepted inputs: []byte / string (raw JSON), json.RawMessage, and any
// Go value marshalable to JSON (maps, slices, structs, scalars).
// It returns the serialized JSON as a string ready for pgx binding: under
// QueryExecModeSimpleProtocol a []byte argument is encoded as bytea, which
// PostgreSQL cannot cast to jsonb, so string is the portable binding form.
//
// Rejections: non-UTF-8 raw input, structurally invalid raw JSON, NaN/Inf
// anywhere in the value, payloads over the column size limit.
func NormalizeJSONB(spec ColumnSpec, v any) (string, error) {
	if spec.Kind != KindJSONB {
		return "", fieldError(ErrInvalidJSONB, "", spec.Name)
	}
	max := spec.JSONBMaxBytes
	if max <= 0 {
		max = DefaultJSONBMaxBytes
	}

	var raw []byte
	switch t := v.(type) {
	case nil:
		// Explicit NULL is only meaningful for nullable columns; the patch
		// layer decides nullability, here nil is treated as an empty object
		// binding error to avoid silent surprises.
		return "", fieldError(ErrInvalidJSONB, "", spec.Name)
	case []byte:
		raw = t
	case string:
		raw = []byte(t)
	case json.RawMessage:
		raw = t
	default:
		if err := checkNoNaNInf(reflect.ValueOf(v)); err != nil {
			return "", fmt.Errorf("dbx: jsonb %q: %w", spec.Name, err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("dbx: jsonb %q: marshal: %w", spec.Name, err)
		}
		raw = b
	}

	if len(raw) == 0 {
		return "", fieldError(ErrInvalidJSONB, "", spec.Name)
	}
	// Size gate first: an oversized payload must be rejected before any
	// full-parse work, otherwise a large body is CPU-amplified through two
	// or three complete parsing passes.
	if len(raw) > max {
		return "", fmt.Errorf("dbx: jsonb %q: %d bytes exceeds limit %d: %w", spec.Name, len(raw), max, ErrInvalidJSONB)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("dbx: jsonb %q: invalid UTF-8: %w", spec.Name, ErrInvalidJSONB)
	}
	if !json.Valid(raw) {
		return "", fmt.Errorf("dbx: jsonb %q: invalid JSON structure: %w", spec.Name, ErrInvalidJSONB)
	}
	// A top-level literal null passes json.Valid but violates the NOT NULL
	// intent of most JSONB columns (jsonb null != SQL NULL, so PostgreSQL
	// would happily store it). Reject it explicitly.
	if isTopLevelNull(raw) {
		return "", fmt.Errorf("dbx: jsonb %q: top-level null is not a valid document: %w", spec.Name, ErrInvalidJSONB)
	}
	if err := scanNoNaNInf(raw); err != nil {
		return "", fmt.Errorf("dbx: jsonb %q: %w", spec.Name, err)
	}
	return string(raw), nil
}

// isTopLevelNull reports whether raw is the JSON literal null (ignoring
// surrounding whitespace).
func isTopLevelNull(raw []byte) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// scanNoNaNInf rejects NaN/Infinity tokens anywhere in raw JSON text
// (encoding/json refuses to produce them, but raw input may contain them).
func scanNoNaNInf(raw []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case json.Number:
			f, ferr := t.Float64()
			if ferr != nil {
				// e.g. 1e400: json.Number keeps the token but it is out of
				// float range; report it with the typed sentinel like every
				// other rejection.
				return fmt.Errorf("number out of range %s: %w", t.String(), ErrInvalidJSONB)
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return fmt.Errorf("non-finite number %s: %w", t.String(), ErrInvalidJSONB)
			}
		case json.Delim:
			if t == '{' || t == '[' {
				for dec.More() {
					if err := walk(); err != nil {
						return err
					}
				}
				if _, err := dec.Token(); err != nil { // closing delim
					return err
				}
			}
		}
		return nil
	}
	if err := walk(); err != nil {
		return fmt.Errorf("dbx: jsonb scan: %w", err)
	}
	return nil
}

// checkNoNaNInf rejects float NaN/Inf inside Go values before marshaling
// (json.Marshal would fail anyway; this yields a typed error instead).
func checkNoNaNInf(v reflect.Value) error {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("non-finite float: %w", ErrInvalidJSONB)
		}
	case reflect.Interface, reflect.Pointer:
		return checkNoNaNInf(v.Elem())
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := checkNoNaNInf(v.Index(i)); err != nil {
				return err
			}
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			if err := checkNoNaNInf(k); err != nil {
				return err
			}
			if err := checkNoNaNInf(v.MapIndex(k)); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if err := checkNoNaNInf(v.Field(i)); err != nil {
				return err
			}
		}
	}
	return nil
}
