package dbx

import (
	"errors"
	"fmt"
)

// Sentinel errors. Callers must use errors.Is; wrappers add table/field
// context via FieldError.
var (
	// ErrUnknownTable is returned when a table is not in the Registry.
	ErrUnknownTable = errors.New("dbx: unknown table")
	// ErrUnknownField is returned for fields absent from the manifest
	// (strict mode).
	ErrUnknownField = errors.New("dbx: unknown field")
	// ErrProtectedField is returned when a patch/insert attempts to write a
	// non-writable column (primary key, tenant, audit, framework-managed).
	ErrProtectedField = errors.New("dbx: protected field")
	// ErrMissingScope is returned when a tenant scope is absent. The
	// framework fails closed; it never falls back to a default tenant.
	ErrMissingScope = errors.New("dbx: missing tenant scope")
	// ErrConflict is returned when an optimistic-lock (version CAS) update
	// affects zero rows.
	ErrConflict = errors.New("dbx: optimistic lock conflict")
	// ErrNotFound is returned when a row matched by primary key + tenant
	// does not exist.
	ErrNotFound = errors.New("dbx: not found")
	// ErrTooManyRows is returned when a statement expected to touch at most
	// one row returns more (multi-row RETURNING last line of defense; all
	// current write paths are single-row by primary key).
	ErrTooManyRows = errors.New("dbx: affected rows exceed limit")
	// ErrUniqueViolation wraps PostgreSQL SQLSTATE 23505 so hosts can map
	// 409 responses without digging into pgconn.PgError; the original error
	// remains reachable via errors.As.
	ErrUniqueViolation = errors.New("dbx: unique constraint violation")
	// ErrEmptyPatch is returned when a patch contains no applicable column
	// changes.
	ErrEmptyPatch = errors.New("dbx: empty patch")
	// ErrUnsupported is returned for operations the manifest does not allow
	// (delete without soft-delete column, writes to readonly tables).
	ErrUnsupported = errors.New("dbx: unsupported operation")
	// ErrInvalidIdentifier is returned when a table/column/order identifier
	// is not a whitelisted identifier.
	ErrInvalidIdentifier = errors.New("dbx: invalid identifier")
	// ErrInvalidJSONB is returned when a JSONB payload fails validation
	// (UTF-8, size, structure, NaN/Inf).
	ErrInvalidJSONB = errors.New("dbx: invalid jsonb payload")
	// ErrInvalidInput is a generic guard for malformed call parameters.
	ErrInvalidInput = errors.New("dbx: invalid input")
)

// FieldError attaches table/field context to a sentinel error.
type FieldError struct {
	Err   error
	Table string
	Field string
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return fmt.Sprintf("%v (table %q)", e.Err, e.Table)
	}
	return fmt.Sprintf("%v (table %q, field %q)", e.Err, e.Table, e.Field)
}

func (e *FieldError) Unwrap() error { return e.Err }

func fieldError(err error, table, field string) error {
	return &FieldError{Err: err, Table: table, Field: field}
}
