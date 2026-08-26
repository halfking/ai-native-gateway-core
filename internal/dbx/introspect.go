package dbx

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ColumnMetadata is the PostgreSQL catalog state for one exposed column.
// It is intentionally read-only: this package reports drift and never
// attempts DDL repair.
type ColumnMetadata struct {
	Name      string
	DataType  string
	Nullable  bool
	Default   *string
	Identity  bool
	Generated bool
}

// TableMetadata is the catalog state required to decide whether a manifest
// can safely use a table. Storage and partition facts are included because
// columnar and partition-parent tables must not be treated as mutable heaps.
type TableMetadata struct {
	Schema       string
	Table        string
	Columns      map[string]ColumnMetadata
	RelationKind string
	AccessMethod string
	RLS          bool
	ForceRLS     bool
	// IsPartition is true when the relation is itself a partition or
	// traditional-inheritance child (pg_inherits.inhrelid side).
	IsPartition bool
	// HasPartitions is true when the relation is a partition parent or
	// inheritance parent (pg_inherits.inhparent side). Neither a parent nor
	// a leaf partition may back a manifest.
	HasPartitions bool
	// SingleColumnUniqueKeys lists columns that fully constitute a valid,
	// non-partial, non-expression primary key or unique index. A manifest
	// PrimaryKey must appear here or UPDATE-by-PK could touch many rows.
	SingleColumnUniqueKeys []string
	CapturedAt             time.Time
}

// MetadataSnapshot is an immutable catalog observation over one registry.
// The RegistryVersion makes an explicit host-side invalidation observable.
// The snapshot is owned by the framework/cache: hosts must treat all maps
// as read-only and never mutate a stored snapshot.
type MetadataSnapshot struct {
	RegistryVersion uint64
	Tables          map[string]TableMetadata
	CapturedAt      time.Time
}

// MetadataReader performs only whitelisted, read-only catalog lookups. It
// never scans arbitrary tables or all database objects.
type MetadataReader struct {
	Schema string
}

// NewMetadataReader creates a reader for a PostgreSQL schema. An empty
// schema uses public, which is an explicit default for PostgreSQL hosts.
func NewMetadataReader(schema string) (*MetadataReader, error) {
	if schema == "" {
		schema = "public"
	}
	if err := ValidateIdentifier(schema); err != nil {
		return nil, fmt.Errorf("dbx: metadata schema %q: %w", schema, ErrInvalidIdentifier)
	}
	return &MetadataReader{Schema: schema}, nil
}

// ReadRegistry captures metadata only for tables registered in reg. The
// call fails closed: if any registered table cannot be read (missing,
// revoked, ...), no snapshot is produced and the host must keep the gate
// closed rather than partially trust the registry.
func (r *MetadataReader) ReadRegistry(ctx context.Context, dbtx DBTX, reg *Registry) (*MetadataSnapshot, error) {
	if r == nil || dbtx == nil || reg == nil {
		return nil, fmt.Errorf("dbx: ReadRegistry: %w", ErrInvalidInput)
	}
	snapshot := &MetadataSnapshot{
		RegistryVersion: reg.Version(),
		Tables:          make(map[string]TableMetadata, len(reg.Tables())),
		CapturedAt:      time.Now().UTC(),
	}
	for _, table := range reg.Tables() {
		m, err := r.ReadTable(ctx, dbtx, table)
		if err != nil {
			return nil, err
		}
		snapshot.Tables[table] = m
	}
	return snapshot, nil
}

// ReadTable captures a table's declared columns and relation properties.
// table comes from a Registry caller in normal use, but is still validated
// before it can be passed into the parameterized catalog queries.
func (r *MetadataReader) ReadTable(ctx context.Context, dbtx DBTX, table string) (TableMetadata, error) {
	if r == nil || dbtx == nil {
		return TableMetadata{}, fmt.Errorf("dbx: ReadTable: %w", ErrInvalidInput)
	}
	if err := ValidateIdentifier(table); err != nil {
		return TableMetadata{}, fieldError(ErrInvalidIdentifier, table, "")
	}
	meta := TableMetadata{
		Schema:     r.Schema,
		Table:      table,
		Columns:    make(map[string]ColumnMetadata),
		CapturedAt: time.Now().UTC(),
	}

	columns, err := dbtx.Query(ctx, `
		SELECT column_name, data_type, is_nullable, column_default,
		       is_identity, is_generated
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position`, r.Schema, table)
	if err != nil {
		return TableMetadata{}, fmt.Errorf("dbx: introspect columns %s.%s: %w", r.Schema, table, err)
	}
	defer columns.Close()
	for columns.Next() {
		var col ColumnMetadata
		var nullable, identity, generated string
		if err := columns.Scan(&col.Name, &col.DataType, &nullable, &col.Default, &identity, &generated); err != nil {
			return TableMetadata{}, fmt.Errorf("dbx: scan column metadata %s.%s: %w", r.Schema, table, err)
		}
		col.Nullable = nullable == "YES"
		col.Identity = identity == "YES"
		col.Generated = generated != "NEVER"
		meta.Columns[col.Name] = col
	}
	if err := columns.Err(); err != nil {
		return TableMetadata{}, fmt.Errorf("dbx: read column metadata %s.%s: %w", r.Schema, table, err)
	}
	if len(meta.Columns) == 0 {
		return TableMetadata{}, fieldError(ErrUnknownTable, table, "")
	}

	row := dbtx.QueryRow(ctx, `
		SELECT c.relkind::text,
		       COALESCE(am.amname, ''),
		       c.relrowsecurity,
		       c.relforcerowsecurity,
		       EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid),
		       EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhparent = c.oid)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_am am ON am.oid = c.relam
		WHERE n.nspname = $1 AND c.relname = $2`, r.Schema, table)
	if err := row.Scan(&meta.RelationKind, &meta.AccessMethod, &meta.RLS, &meta.ForceRLS, &meta.IsPartition, &meta.HasPartitions); err != nil {
		return TableMetadata{}, fmt.Errorf("dbx: introspect relation %s.%s: %w", r.Schema, table, err)
	}

	// Single-column primary keys / unique indexes. Partial and expression
	// indexes are excluded: they cannot guarantee at-most-one-row semantics
	// for UPDATE ... WHERE pk = $1.
	uniq, err := dbtx.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = i.indkey[0]
		WHERE n.nspname = $1 AND c.relname = $2
		  AND i.indisvalid
		  AND (i.indisprimary OR i.indisunique)
		  AND i.indnkeyatts = 1
		  AND i.indpred IS NULL
		  AND i.indexprs IS NULL
		  AND NOT a.attisdropped
		ORDER BY a.attname`, r.Schema, table)
	if err != nil {
		return TableMetadata{}, fmt.Errorf("dbx: introspect unique keys %s.%s: %w", r.Schema, table, err)
	}
	defer uniq.Close()
	for uniq.Next() {
		var col string
		if err := uniq.Scan(&col); err != nil {
			return TableMetadata{}, fmt.Errorf("dbx: scan unique key %s.%s: %w", r.Schema, table, err)
		}
		meta.SingleColumnUniqueKeys = append(meta.SingleColumnUniqueKeys, col)
	}
	if err := uniq.Err(); err != nil {
		return TableMetadata{}, fmt.Errorf("dbx: read unique keys %s.%s: %w", r.Schema, table, err)
	}
	return meta, nil
}

// MetadataCache caches immutable snapshots. Hosts must call Invalidate after
// a reviewed migration successfully completes; it deliberately does not poll
// pg_catalog on request paths.
type MetadataCache struct {
	mu       sync.RWMutex
	snapshot *MetadataSnapshot
	epoch    uint64
}

// Snapshot returns the current immutable snapshot and its invalidation epoch.
func (c *MetadataCache) Snapshot() (*MetadataSnapshot, uint64) {
	if c == nil {
		return nil, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot, c.epoch
}

// Store replaces the snapshot after a successful reader refresh.
func (c *MetadataCache) Store(snapshot *MetadataSnapshot) error {
	if c == nil || snapshot == nil {
		return fmt.Errorf("dbx: MetadataCache.Store: %w", ErrInvalidInput)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = snapshot
	return nil
}

// Invalidate clears the cached snapshot and advances the epoch. It does not
// query the database or execute DDL.
func (c *MetadataCache) Invalidate() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = nil
	c.epoch++
	return c.epoch
}

// ExpectedDataTypes returns catalog data type names accepted for a coarse
// ColumnKind. It keeps comparison conservative: an unknown kind is reported
// as incompatible rather than guessed.
func ExpectedDataTypes(kind ColumnKind) []string {
	switch kind {
	case KindText:
		return []string{"text", "character varying", "character"}
	case KindInt:
		return []string{"smallint", "integer", "bigint"}
	case KindFloat:
		return []string{"real", "double precision", "numeric", "decimal"}
	case KindBool:
		return []string{"boolean"}
	case KindTimestamp:
		return []string{"timestamp with time zone", "timestamp without time zone"}
	case KindJSONB:
		return []string{"jsonb"}
	default:
		return nil
	}
}

func compatibleDataType(kind ColumnKind, actual string) bool {
	actual = strings.ToLower(actual)
	for _, expected := range ExpectedDataTypes(kind) {
		if actual == expected {
			return true
		}
	}
	return false
}
