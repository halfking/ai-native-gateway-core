package dbx

import (
	"fmt"
	"strings"
)

// ColumnKind is the coarse value category the framework validates against.
// It is not a full type system; exotic types stay in hand-written SQL.
type ColumnKind int

const (
	KindText ColumnKind = iota
	KindInt
	KindFloat
	KindBool
	KindTimestamp
	KindJSONB
)

// String implements fmt.Stringer for diagnostics.
func (k ColumnKind) String() string {
	switch k {
	case KindText:
		return "text"
	case KindInt:
		return "int"
	case KindFloat:
		return "float"
	case KindBool:
		return "bool"
	case KindTimestamp:
		return "timestamp"
	case KindJSONB:
		return "jsonb"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// DefaultJSONBMaxBytes is the upper bound for a serialized JSONB payload
// unless the manifest declares otherwise.
const DefaultJSONBMaxBytes = 256 * 1024

// ColumnSpec declares one column of a table manifest.
type ColumnSpec struct {
	Name string
	Kind ColumnKind
	// Writable means the column may appear in an update patch.
	Writable bool
	// Insertable means the column may appear in an insert. Primary key
	// (DB-generated), tenant (scope-injected), and DB-defaulted audit
	// columns are not insertable.
	Insertable bool
	// Nullable means explicit NULL is accepted in patches.
	Nullable bool
	// JSONBMaxBytes overrides DefaultJSONBMaxBytes for KindJSONB columns.
	JSONBMaxBytes int
}

// TableManifest is the explicit, reviewed contract for one table. Tables
// without a manifest are unreachable through the CRUD layer.
type TableManifest struct {
	// Table is the physical table name. For hot/partitioned tables this is
	// the hot/default heap table, never the partition parent.
	Table string
	// TenantColumn is the RLS tenant column, injected from the scope, never
	// from caller input.
	TenantColumn string
	// PrimaryKey is the single-column primary key used in WHERE clauses.
	PrimaryKey string
	// Columns lists every column the framework reads back via RETURNING /
	// SELECT. Columns not listed here cannot be referenced at all.
	Columns []ColumnSpec
	// SoftDelete names the soft-delete column (e.g. deleted_at). When nil,
	// Delete is rejected outright - the framework never silently degrades
	// soft delete into a hard DELETE.
	SoftDelete *string
	// VersionColumn names the optimistic-lock column (e.g. version). The
	// framework increments it on update and uses it for CAS when the caller
	// passes an expected version.
	VersionColumn *string
	// TouchColumn names a last-modified column (e.g. updated_at) that the
	// framework sets to now() on every update.
	TouchColumn *string
	// Readonly marks tables that must not be written through this framework
	// (columnar history, views).
	Readonly bool
	// MaxUpdateRows is the declared ceiling for rows a single write may
	// affect. All current write paths are single-row by primary key; the
	// runtime last line of defense is the multi-row RETURNING check in
	// queryOne. Batch APIs, when introduced, will enforce this limit in SQL.
	MaxUpdateRows int64

	// byName is the immutable lookup index built by normalize.
	byName map[string]ColumnSpec
}

// Column returns the normalized spec for a column name.
func (m *TableManifest) Column(name string) (ColumnSpec, bool) {
	spec, ok := m.byName[name]
	return spec, ok
}

// ColumnNames returns declared column names in declaration order.
func (m *TableManifest) ColumnNames() []string {
	names := make([]string, len(m.Columns))
	for i, c := range m.Columns {
		names[i] = c.Name
	}
	return names
}

// normalize validates the manifest, applies defaults and builds the lookup
// index. It returns a copy; the input manifest is never mutated.
func (m TableManifest) normalize() (TableManifest, error) {
	if err := ValidateIdentifier(m.Table); err != nil {
		return m, fmt.Errorf("dbx: manifest table %q: %w", m.Table, ErrInvalidIdentifier)
	}
	if err := ValidateIdentifier(m.PrimaryKey); err != nil {
		return m, fmt.Errorf("dbx: manifest %q primary key: %w", m.Table, ErrInvalidIdentifier)
	}
	if err := ValidateIdentifier(m.TenantColumn); err != nil {
		return m, fmt.Errorf("dbx: manifest %q tenant column: %w", m.Table, ErrInvalidIdentifier)
	}
	if len(m.Columns) == 0 {
		return m, fmt.Errorf("dbx: manifest %q: no columns declared", m.Table)
	}
	if m.MaxUpdateRows <= 0 {
		m.MaxUpdateRows = 1
	}
	// Deep-copy the caller's slice: the registry must not share backing
	// arrays with manifests the host may still mutate after construction.
	cols := make([]ColumnSpec, len(m.Columns))
	copy(cols, m.Columns)
	m.Columns = cols
	m.byName = make(map[string]ColumnSpec, len(m.Columns))
	for _, c := range m.Columns {
		if err := ValidateIdentifier(c.Name); err != nil {
			return m, fmt.Errorf("dbx: manifest %q column %q: %w", m.Table, c.Name, ErrInvalidIdentifier)
		}
		if _, dup := m.byName[c.Name]; dup {
			return m, fmt.Errorf("dbx: manifest %q: duplicate column %q", m.Table, c.Name)
		}
		if c.Kind == KindJSONB && c.JSONBMaxBytes <= 0 {
			c.JSONBMaxBytes = DefaultJSONBMaxBytes
		}
		// Guard columns referenced by name must exist.
		for label, name := range map[string]string{
			"primary key":   m.PrimaryKey,
			"tenant column": m.TenantColumn,
		} {
			if c.Name == name {
				if c.Writable || c.Insertable {
					return m, fmt.Errorf("dbx: manifest %q: %s column %q must not be writable/insertable", m.Table, label, name)
				}
			}
		}
		m.byName[c.Name] = c
	}
	if _, ok := m.byName[m.PrimaryKey]; !ok {
		return m, fmt.Errorf("dbx: manifest %q: primary key %q not declared in columns", m.Table, m.PrimaryKey)
	}
	if _, ok := m.byName[m.TenantColumn]; !ok {
		return m, fmt.Errorf("dbx: manifest %q: tenant column %q not declared in columns", m.Table, m.TenantColumn)
	}
	for label, p := range map[string]*string{
		"soft delete":  m.SoftDelete,
		"version":      m.VersionColumn,
		"touch column": m.TouchColumn,
	} {
		if p == nil {
			continue
		}
		spec, ok := m.byName[*p]
		if !ok {
			return m, fmt.Errorf("dbx: manifest %q: %s column %q not declared in columns", m.Table, label, *p)
		}
		if spec.Writable || spec.Insertable {
			return m, fmt.Errorf("dbx: manifest %q: %s column %q is framework-managed and must not be writable/insertable", m.Table, label, *p)
		}
	}
	if m.Readonly {
		for _, c := range m.Columns {
			if c.Writable || c.Insertable {
				return m, fmt.Errorf("dbx: manifest %q: readonly table declares writable/insertable column %q", m.Table, c.Name)
			}
		}
	}
	return m, nil
}

// columnList renders a comma-separated quoted column list for
// RETURNING/SELECT clauses.
func (m *TableManifest) columnList() (string, error) {
	parts := make([]string, len(m.Columns))
	for i, c := range m.Columns {
		q, err := QuoteIdentifier(c.Name)
		if err != nil {
			return "", err
		}
		parts[i] = q
	}
	return strings.Join(parts, ", "), nil
}
