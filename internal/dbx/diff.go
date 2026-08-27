package dbx

import (
	"fmt"
	"sort"
)

// DriftKind classifies a manifest-versus-live-schema difference.
type DriftKind string

const (
	DriftMissingTable     DriftKind = "missing_table"
	DriftMissingColumn    DriftKind = "missing_column"
	DriftUnexpectedColumn DriftKind = "unexpected_column"
	DriftTypeMismatch     DriftKind = "type_mismatch"
	DriftNullableMismatch DriftKind = "nullable_mismatch"
	DriftReadonlyMismatch DriftKind = "readonly_mismatch"
	DriftPKMismatch       DriftKind = "pk_mismatch"
	DriftStorageMismatch  DriftKind = "storage_mismatch"
	DriftRLSMismatch      DriftKind = "rls_mismatch"
	DriftForceRLSMismatch DriftKind = "force_rls_mismatch"
)

// Drift is a read-only report item. No method in this package applies a
// repair; callers must turn accepted changes into reviewed migrations.
type Drift struct {
	Table    string
	Column   string
	Kind     DriftKind
	Expected string
	Actual   string
}

// Relation kinds recognized by the gate. PostgreSQL relkind values: r = plain
// table, p = partition parent, v = view, m = materialized view, f = foreign
// table, S = sequence, I = index ... Only r/v/m can back a manifest.
const (
	relKindTable   = "r"
	relKindView    = "v"
	relKindMatView = "m"
)

// CompareManifest compares a validated manifest with catalog metadata. It
// checks only the manifest's declared contract and reports extra live columns
// as informational drift, preserving the tolerance between physical schema
// and a deliberately narrow API projection. It never generates DDL.
func CompareManifest(manifest *TableManifest, actual TableMetadata) []Drift {
	if manifest == nil {
		return []Drift{{Kind: DriftMissingTable, Expected: "manifest", Actual: "nil manifest"}}
	}
	drifts := make([]Drift, 0)
	// View columns are always reported nullable by PostgreSQL regardless of
	// the underlying columns, so nullability cannot be gated for views.
	nullableGate := actual.RelationKind != relKindView && actual.RelationKind != relKindMatView
	for _, expected := range manifest.Columns {
		got, ok := actual.Columns[expected.Name]
		if !ok {
			drifts = append(drifts, Drift{Table: manifest.Table, Column: expected.Name, Kind: DriftMissingColumn, Expected: expected.Kind.String(), Actual: "missing"})
			continue
		}
		if !compatibleDataType(expected.Kind, got.DataType) {
			drifts = append(drifts, Drift{Table: manifest.Table, Column: expected.Name, Kind: DriftTypeMismatch, Expected: expected.Kind.String(), Actual: got.DataType})
		}
		if nullableGate && got.Nullable != expected.Nullable {
			drifts = append(drifts, Drift{Table: manifest.Table, Column: expected.Name, Kind: DriftNullableMismatch, Expected: fmt.Sprintf("nullable=%t", expected.Nullable), Actual: fmt.Sprintf("nullable=%t", got.Nullable)})
		}
		// A column the manifest admits for writes must be writable in the
		// catalog: identity/generated columns reject INSERT/UPDATE values.
		if (expected.Writable || expected.Insertable) && (got.Identity || got.Generated) {
			drifts = append(drifts, Drift{Table: manifest.Table, Column: expected.Name, Kind: DriftReadonlyMismatch, Expected: "writable column", Actual: "identity/generated column"})
		}
	}
	// A manifest may intentionally expose only a projection. Extra columns
	// are therefore reported for visibility, but do not fail that projection.
	for name, got := range actual.Columns {
		if _, ok := manifest.Column(name); !ok {
			drifts = append(drifts, Drift{Table: manifest.Table, Column: name, Kind: DriftUnexpectedColumn, Expected: "not exposed", Actual: got.DataType})
		}
	}
	drifts = append(drifts, compareRelation(manifest, actual)...)
	sort.Slice(drifts, func(i, j int) bool {
		if drifts[i].Table != drifts[j].Table {
			return drifts[i].Table < drifts[j].Table
		}
		if drifts[i].Column != drifts[j].Column {
			return drifts[i].Column < drifts[j].Column
		}
		return drifts[i].Kind < drifts[j].Kind
	})
	return drifts
}

// compareRelation applies the relation-level rules: storage shape, primary
// key uniqueness, and RLS enforcement. Views are exempt from RLS and index
// checks because PostgreSQL does not support RLS on them; isolation is the
// underlying table's contract.
func compareRelation(manifest *TableManifest, actual TableMetadata) []Drift {
	drifts := make([]Drift, 0)
	isView := actual.RelationKind == relKindView || actual.RelationKind == relKindMatView

	switch actual.RelationKind {
	case relKindTable:
		if actual.IsPartition {
			drifts = append(drifts, Drift{Table: manifest.Table, Kind: DriftStorageMismatch, Expected: "plain heap table", Actual: "partition/inheritance child"})
		}
		if actual.HasPartitions {
			drifts = append(drifts, Drift{Table: manifest.Table, Kind: DriftStorageMismatch, Expected: "plain heap table", Actual: "partition/inheritance parent"})
		}
		// Columnar or other non-heap access methods do not support the
		// framework's write path (UPDATE ... RETURNING semantics).
		if !manifest.Readonly && actual.AccessMethod != "" && actual.AccessMethod != "heap" {
			drifts = append(drifts, Drift{Table: manifest.Table, Kind: DriftStorageMismatch, Expected: "heap access method", Actual: actual.AccessMethod})
		}
	case relKindView, relKindMatView:
		if !manifest.Readonly {
			drifts = append(drifts, Drift{Table: manifest.Table, Kind: DriftStorageMismatch, Expected: "readonly manifest on a view", Actual: "writable manifest"})
		}
	default:
		drifts = append(drifts, Drift{Table: manifest.Table, Kind: DriftStorageMismatch, Expected: "table, view or materialized view", Actual: fmt.Sprintf("relkind %q", actual.RelationKind)})
	}

	if !isView {
		if !actual.RLS {
			drifts = append(drifts, Drift{Table: manifest.Table, Kind: DriftRLSMismatch, Expected: "enabled", Actual: "disabled"})
		}
		// FORCE RLS closes the table-owner bypass. It is required whenever
		// the framework can write: without it an owner-role connection
		// silently skips row security. Readonly manifests keep read
		// isolation from plain RLS.
		if !manifest.Readonly && !actual.ForceRLS {
			drifts = append(drifts, Drift{Table: manifest.Table, Kind: DriftForceRLSMismatch, Expected: "forced", Actual: "not forced"})
		}
		if actual.RelationKind == relKindTable && !columnIn(manifest.PrimaryKey, actual.SingleColumnUniqueKeys) {
			drifts = append(drifts, Drift{Table: manifest.Table, Column: manifest.PrimaryKey, Kind: DriftPKMismatch, Expected: "single-column primary/unique key", Actual: "not a single-column unique key"})
		}
	}
	return drifts
}

func columnIn(name string, cols []string) bool {
	for _, c := range cols {
		if c == name {
			return true
		}
	}
	return false
}

// CompareSnapshot compares a whole registry against a snapshot, producing a
// DriftMissingTable report for registered tables absent from the snapshot.
// It is the gate entry point for hosts that cache snapshots across
// registry rebuilds.
func CompareSnapshot(reg *Registry, snap *MetadataSnapshot) []Drift {
	if reg == nil || snap == nil {
		return []Drift{{Kind: DriftMissingTable, Expected: "registry and snapshot", Actual: "nil argument"}}
	}
	drifts := make([]Drift, 0)
	for _, table := range reg.Tables() {
		meta, ok := snap.Tables[table]
		if !ok {
			drifts = append(drifts, Drift{Table: table, Kind: DriftMissingTable, Expected: "registered table", Actual: "absent from snapshot"})
			continue
		}
		m, err := reg.Lookup(table)
		if err != nil {
			drifts = append(drifts, Drift{Table: table, Kind: DriftMissingTable, Expected: "registered table", Actual: err.Error()})
			continue
		}
		drifts = append(drifts, CompareManifest(m, meta)...)
	}
	sort.Slice(drifts, func(i, j int) bool {
		if drifts[i].Table != drifts[j].Table {
			return drifts[i].Table < drifts[j].Table
		}
		if drifts[i].Column != drifts[j].Column {
			return drifts[i].Column < drifts[j].Column
		}
		return drifts[i].Kind < drifts[j].Kind
	})
	return drifts
}

// HasBlockingDrift returns true for differences that make the registered
// CRUD contract unsafe. Extra physical columns are non-blocking because the
// manifest is an explicit projection.
func HasBlockingDrift(drifts []Drift) bool {
	for _, d := range drifts {
		switch d.Kind {
		case DriftMissingTable, DriftMissingColumn, DriftTypeMismatch,
			DriftNullableMismatch, DriftReadonlyMismatch, DriftPKMismatch,
			DriftStorageMismatch, DriftRLSMismatch, DriftForceRLSMismatch:
			return true
		}
	}
	return false
}
