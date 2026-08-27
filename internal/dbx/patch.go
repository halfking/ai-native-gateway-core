package dbx

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// PatchOp is one column assignment inside an update patch. Value nil means
// SQL NULL (allowed only for nullable columns).
type PatchOp struct {
	Column string
	Value  any
}

// Patch is a validated set of update ops. In lenient mode Dropped lists
// unknown input fields that were ignored; strict mode errors instead and
// never drops silently.
type Patch struct {
	Ops     []PatchOp
	Dropped []string
}

type patchOptions struct {
	lenient bool
}

// PatchOption customizes patch building.
type PatchOption func(*patchOptions)

// WithLenientUnknown drops unknown fields instead of failing. Dropped names
// are always reported back on the Patch; callers must surface them.
func WithLenientUnknown() PatchOption {
	return func(o *patchOptions) { o.lenient = true }
}

// PatchFromMap builds a patch from a JSON-object style map. Every present
// key is an explicit write; nil value means SQL NULL (nullable columns
// only). Missing keys are untouched - the map form has no "missing" tri-state.
func PatchFromMap(m *TableManifest, fields map[string]any, opts ...PatchOption) (*Patch, error) {
	var o patchOptions
	for _, opt := range opts {
		opt(&o)
	}
	p := &Patch{}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic op order
	for _, k := range keys {
		if err := appendOp(m, p, k, fields[k], o.lenient); err != nil {
			return nil, err
		}
	}
	return finalizePatch(m, p)
}

// PatchFromDTO builds a patch from a struct using `dbx:"column"` tags
// (`dbx:"-"` skips a field). Tri-state semantics:
//
//	pointer field, nil   → missing (column untouched)
//	pointer field, non-nil → write *ptr
//	non-pointer field    → always written, zero value is an explicit zero
//
// Explicit NULL is expressed with PatchFromMap (nil value) or a *pointer
// field pointing at a nil-able value is not supported; use maps for nulls.
func PatchFromDTO(m *TableManifest, dto any, opts ...PatchOption) (*Patch, error) {
	var o patchOptions
	for _, opt := range opts {
		opt(&o)
	}
	v := reflect.ValueOf(dto)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, fmt.Errorf("dbx: PatchFromDTO: nil DTO: %w", ErrInvalidInput)
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("dbx: PatchFromDTO: expected struct, got %s: %w", v.Kind(), ErrInvalidInput)
	}
	t := v.Type()
	p := &Patch{}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.PkgPath != "" {
			continue // unexported field: never mapped, never read via reflection
		}
		tag := sf.Tag.Get("dbx")
		if tag == "" || tag == "-" {
			continue
		}
		col := strings.TrimSpace(tag)
		fv := v.Field(i)
		if fv.Kind() == reflect.Pointer {
			if fv.IsNil() {
				continue // missing: untouched
			}
			fv = fv.Elem()
		}
		// nil maps/slices are "missing" in DTO form: writing JSON null for
		// an unset composite would be surprising. Map-form patches express
		// explicit nulls with a nil value instead.
		switch fv.Kind() {
		case reflect.Map, reflect.Slice:
			if fv.IsNil() {
				continue
			}
		}
		if fv.Kind() == reflect.Interface && !fv.IsNil() {
			fv = fv.Elem()
		}
		if err := appendOp(m, p, col, fv.Interface(), o.lenient); err != nil {
			return nil, err
		}
	}
	return finalizePatch(m, p)
}

func finalizePatch(m *TableManifest, p *Patch) (*Patch, error) {
	if len(p.Ops) == 0 {
		return nil, fmt.Errorf("dbx: patch for %q: %w", m.Table, ErrEmptyPatch)
	}
	sort.Strings(p.Dropped)
	return p, nil
}

// appendOp validates one input field against the manifest and appends the
// normalized op.
func appendOp(m *TableManifest, p *Patch, column string, value any, lenient bool) error {
	spec, ok := m.Column(column)
	if !ok {
		if lenient {
			p.Dropped = append(p.Dropped, column)
			return nil
		}
		return fieldError(ErrUnknownField, m.Table, column)
	}
	if !spec.Writable {
		// Protected even in lenient mode: attempts to touch primary key,
		// tenant, audit or framework-managed columns are never droppable.
		return fieldError(ErrProtectedField, m.Table, column)
	}
	normalized, err := coerceValue(m, spec, value)
	if err != nil {
		return err
	}
	p.Ops = append(p.Ops, PatchOp{Column: column, Value: normalized})
	return nil
}

// coerceValue validates the value against the column kind. Returns the
// binding-ready value (JSONB becomes canonical []byte).
func coerceValue(m *TableManifest, spec ColumnSpec, value any) (any, error) {
	if value == nil {
		if !spec.Nullable {
			return nil, fmt.Errorf("null for non-nullable column: %w", fieldError(ErrInvalidInput, m.Table, spec.Name))
		}
		return nil, nil
	}
	switch spec.Kind {
	case KindText:
		s, ok := value.(string)
		if !ok {
			return nil, kindError(m, spec, "string")
		}
		return s, nil
	case KindBool:
		b, ok := value.(bool)
		if !ok {
			return nil, kindError(m, spec, "bool")
		}
		return b, nil
	case KindInt:
		if !isIntValue(value) {
			return nil, kindError(m, spec, "integer")
		}
		return value, nil
	case KindFloat:
		if !isIntValue(value) && !isFloatValue(value) {
			return nil, kindError(m, spec, "float")
		}
		return value, nil
	case KindTimestamp:
		ts, ok := value.(time.Time)
		if !ok {
			return nil, kindError(m, spec, "time.Time")
		}
		return ts, nil
	case KindJSONB:
		raw, err := NormalizeJSONB(spec, value)
		if err != nil {
			return nil, err
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("unsupported column kind %s: %w", spec.Kind, fieldError(ErrInvalidInput, m.Table, spec.Name))
	}
}

func kindError(m *TableManifest, spec ColumnSpec, want string) error {
	return fmt.Errorf("want %s: %w", want, fieldError(ErrInvalidInput, m.Table, spec.Name))
}

func isIntValue(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

func isFloatValue(v any) bool {
	switch v.(type) {
	case float32, float64:
		return true
	}
	return false
}
