package contextbudget

import (
	"encoding/json"
	"reflect"
	"time"
	"unicode/utf8"
)

// MetadataFits bounds JSON-shaped metadata without serializing it first. This
// is needed BEFORE cache deep copies: Marshal+length-check already allocates a
// giant string/map. Cycles, excessive structure and opaque marshalers fail closed.
func MetadataFits(value any, limit int) bool {
	if limit <= 0 {
		return false
	}
	return metadataFits(reflect.ValueOf(value), &limit, 0)
}

func metadataFits(value reflect.Value, remaining *int, depth int) bool {
	if !value.IsValid() {
		return true
	}
	*remaining -= 64 // Structural punctuation, field labels and scalar encodings.
	if *remaining < 0 || depth > 128 {
		return false
	}
	if value.Type() == reflect.TypeOf(time.Time{}) {
		return true
	}
	if value.Type().Implements(reflect.TypeOf((*json.Marshaler)(nil)).Elem()) {
		return false
	}
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		return value.IsNil() || metadataFits(value.Elem(), remaining, depth+1)
	case reflect.String:
		text := value.String()
		*remaining -= len(text)
		if *remaining < 0 {
			return false
		}
		for i := 0; i < len(text); {
			b := text[i]
			switch {
			case b < 0x20 || b == '<' || b == '>' || b == '&':
				*remaining -= 5
				i++
			case b == '\\' || b == '"':
				*remaining--
				i++
			case b < utf8.RuneSelf:
				i++
			default:
				r, n := utf8.DecodeRuneInString(text[i:])
				if r == utf8.RuneError && n == 1 {
					*remaining -= 5
				} else if r == '\u2028' || r == '\u2029' {
					*remaining -= 3
				}
				i += n
			}
			if *remaining < 0 {
				return false
			}
		}
	case reflect.Map:
		for key, item := range value.Seq2() {
			if !metadataFits(key, remaining, depth+1) || !metadataFits(item, remaining, depth+1) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if value.Len() > *remaining/64 {
			return false
		}
		for i := 0; i < value.Len(); i++ {
			if !metadataFits(value.Index(i), remaining, depth+1) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() && !metadataFits(value.Field(i), remaining, depth+1) {
				return false
			}
		}
	}
	return true
}
