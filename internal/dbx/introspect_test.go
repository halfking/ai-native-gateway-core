package dbx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestExpectedDataTypes(t *testing.T) {
	for _, tc := range []struct {
		kind ColumnKind
		want string
	}{
		{KindText, "text"},
		{KindInt, "bigint"},
		{KindFloat, "numeric"},
		{KindBool, "boolean"},
		{KindTimestamp, "timestamp with time zone"},
		{KindJSONB, "jsonb"},
	} {
		found := false
		for _, got := range ExpectedDataTypes(tc.kind) {
			if got == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("ExpectedDataTypes(%s) missing %q", tc.kind, tc.want)
		}
	}
	if got := ExpectedDataTypes(ColumnKind(999)); got != nil {
		t.Errorf("unknown kind types = %v, want nil", got)
	}
}

// cleanProbeMetadata is catalog state that satisfies every gate rule for
// the probe manifest: plain heap table, RLS enabled and forced, id backed
// by a single-column primary key. A non-nil overlay replaces or adds
// individual columns on top of the full clean column set.
func cleanProbeMetadata(overlay map[string]ColumnMetadata) TableMetadata {
	t := TableMetadata{
		Schema:                 "public",
		Table:                  "dbx_probe_records",
		RelationKind:           "r",
		AccessMethod:           "heap",
		RLS:                    true,
		ForceRLS:               true,
		SingleColumnUniqueKeys: []string{"id"},
		Columns: map[string]ColumnMetadata{
			"id":         {Name: "id", DataType: "bigint", Nullable: false},
			"tenant_id":  {Name: "tenant_id", DataType: "text", Nullable: false},
			"name":       {Name: "name", DataType: "text", Nullable: false},
			"note":       {Name: "note", DataType: "text", Nullable: true},
			"enabled":    {Name: "enabled", DataType: "boolean", Nullable: false},
			"metadata":   {Name: "metadata", DataType: "jsonb", Nullable: false},
			"version":    {Name: "version", DataType: "bigint", Nullable: false},
			"deleted_at": {Name: "deleted_at", DataType: "timestamp with time zone", Nullable: true},
			"created_at": {Name: "created_at", DataType: "timestamp with time zone", Nullable: false},
			"updated_at": {Name: "updated_at", DataType: "timestamp with time zone", Nullable: false},
		},
	}
	for name, col := range overlay {
		t.Columns[name] = col
	}
	return t
}

func TestCompareManifest(t *testing.T) {
	m := normalizedProbe(t)

	actual := cleanProbeMetadata(map[string]ColumnMetadata{
		"extra": {Name: "extra", DataType: "text", Nullable: true},
	})
	if drifts := CompareManifest(&m, actual); len(drifts) != 1 || drifts[0].Kind != DriftUnexpectedColumn {
		t.Errorf("extra-column drifts = %+v, want one non-blocking drift", drifts)
	}
	if HasBlockingDrift(CompareManifest(&m, actual)) {
		t.Error("extra projection column should not be blocking")
	}

	// Writable manifest on a clean table: zero drift.
	if drifts := CompareManifest(&m, cleanProbeMetadata(nil)); len(drifts) != 0 {
		t.Errorf("clean table drifts = %+v, want none", drifts)
	}

	// Type + nullable mismatch block.
	dirty := cleanProbeMetadata(nil)
	dirty.Columns["name"] = ColumnMetadata{Name: "name", DataType: "integer", Nullable: true}
	if !HasBlockingDrift(CompareManifest(&m, dirty)) {
		t.Fatalf("type/nullable mismatch not blocking: %+v", CompareManifest(&m, dirty))
	}

	if _, err := NewRegistry(); err != nil {
		t.Fatalf("empty registry: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*TableMetadata)
		want   DriftKind
	}{
		{"rls disabled", func(a *TableMetadata) { a.RLS = false }, DriftRLSMismatch},
		{"force rls off", func(a *TableMetadata) { a.ForceRLS = false }, DriftForceRLSMismatch},
		{"pk not unique", func(a *TableMetadata) { a.SingleColumnUniqueKeys = nil }, DriftPKMismatch},
		{"partition parent", func(a *TableMetadata) { a.RelationKind = "p"; a.HasPartitions = true }, DriftStorageMismatch},
		{"partition child", func(a *TableMetadata) { a.IsPartition = true }, DriftStorageMismatch},
		{"inheritance parent", func(a *TableMetadata) { a.HasPartitions = true }, DriftStorageMismatch},
		{"writable on columnar", func(a *TableMetadata) { a.AccessMethod = "columnar" }, DriftStorageMismatch},
		{"writable on view", func(a *TableMetadata) { a.RelationKind = "v" }, DriftStorageMismatch},
		{"foreign table", func(a *TableMetadata) { a.RelationKind = "f" }, DriftStorageMismatch},
		{"identity column insertable", func(a *TableMetadata) {
			c := a.Columns["name"]
			c.Identity = true
			a.Columns["name"] = c
		}, DriftReadonlyMismatch},
		{"generated column writable", func(a *TableMetadata) {
			c := a.Columns["note"]
			c.Generated = true
			a.Columns["note"] = c
		}, DriftReadonlyMismatch},
	}
	for _, tc := range cases {
		a := cleanProbeMetadata(nil)
		tc.mutate(&a)
		drifts := CompareManifest(&m, a)
		if !HasBlockingDrift(drifts) {
			t.Errorf("%s: not blocking: %+v", tc.name, drifts)
			continue
		}
		found := false
		for _, d := range drifts {
			if d.Kind == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: drift kind %s not reported: %+v", tc.name, tc.want, drifts)
		}
	}

	// Readonly manifests relax storage and force-RLS rules: a readonly view
	// without RLS support passes, a readonly manifest on a heap table is a
	// host policy choice, and FORCE RLS is only required for writers.
	ro := m
	ro.Readonly = true
	viewMeta := cleanProbeMetadata(nil)
	viewMeta.RelationKind = "v"
	viewMeta.RLS = false
	viewMeta.ForceRLS = false
	if drifts := CompareManifest(&ro, viewMeta); HasBlockingDrift(drifts) {
		t.Errorf("readonly view drifts = %+v, want non-blocking", drifts)
	}
	heapRO := cleanProbeMetadata(nil)
	heapRO.ForceRLS = false
	if drifts := CompareManifest(&ro, heapRO); HasBlockingDrift(drifts) {
		t.Errorf("readonly heap without force drifts = %+v, want non-blocking", drifts)
	}
	// Readonly on a columnar relation stays legal (columnar history tables
	// are the intended readonly target).
	colRO := cleanProbeMetadata(nil)
	colRO.AccessMethod = "columnar"
	if drifts := CompareManifest(&ro, colRO); HasBlockingDrift(drifts) {
		t.Errorf("readonly columnar drifts = %+v, want non-blocking", drifts)
	}
}

func TestCompareSnapshot(t *testing.T) {
	m := normalizedProbe(t)
	reg, err := NewRegistry(m)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	clean := cleanProbeMetadata(nil)
	snap := &MetadataSnapshot{RegistryVersion: reg.Version(), Tables: map[string]TableMetadata{"dbx_probe_records": clean}}
	if drifts := CompareSnapshot(reg, snap); len(drifts) != 0 {
		t.Errorf("clean snapshot drifts = %+v, want none", drifts)
	}

	// Missing table in snapshot → blocking missing_table drift.
	delete(snap.Tables, "dbx_probe_records")
	drifts := CompareSnapshot(reg, snap)
	if len(drifts) != 1 || drifts[0].Kind != DriftMissingTable || drifts[0].Table != "dbx_probe_records" {
		t.Fatalf("missing-table drifts = %+v", drifts)
	}
	if !HasBlockingDrift(drifts) {
		t.Error("missing table must be blocking")
	}

	if drifts := CompareSnapshot(nil, snap); len(drifts) != 1 || drifts[0].Kind != DriftMissingTable {
		t.Errorf("nil registry drifts = %+v", drifts)
	}
	if drifts := CompareSnapshot(reg, nil); len(drifts) != 1 || drifts[0].Kind != DriftMissingTable {
		t.Errorf("nil snapshot drifts = %+v", drifts)
	}
}

func TestMetadataCache(t *testing.T) {
	var cache MetadataCache
	if got, epoch := cache.Snapshot(); got != nil || epoch != 0 {
		t.Errorf("empty Snapshot = (%v,%d)", got, epoch)
	}
	if err := cache.Store(nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("Store(nil) = %v, want ErrInvalidInput", err)
	}
	snap := &MetadataSnapshot{RegistryVersion: 7, Tables: map[string]TableMetadata{"t": {Table: "t"}}, CapturedAt: time.Now()}
	if err := cache.Store(snap); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, epoch := cache.Snapshot()
	if got != snap || epoch != 0 {
		t.Fatalf("Snapshot = (%p,%d), want (%p,0)", got, epoch, snap)
	}
	if epoch := cache.Invalidate(); epoch != 1 {
		t.Fatalf("Invalidate epoch = %d, want 1", epoch)
	}
	if got, epoch := cache.Snapshot(); got != nil || epoch != 1 {
		t.Errorf("after invalidate Snapshot = (%v,%d)", got, epoch)
	}
}

func TestMetadataReaderSQLAndScan(t *testing.T) {
	reader, err := NewMetadataReader("")
	if err != nil {
		t.Fatalf("NewMetadataReader: %v", err)
	}
	if reader.Schema != "public" {
		t.Errorf("default schema = %q", reader.Schema)
	}
	if _, err := NewMetadataReader("public;drop"); !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("invalid schema = %v", err)
	}
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	defer mock.Close()

	mock.ExpectQuery(`SELECT column_name, data_type, is_nullable, column_default`).
		WithArgs("public", "dbx_probe_records").
		WillReturnRows(pgxmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "is_identity", "is_generated"}).
			AddRow("id", "bigint", "NO", nil, "NO", "NEVER").
			AddRow("tenant_id", "text", "NO", nil, "NO", "NEVER"))
	mock.ExpectQuery(`SELECT c\.relkind::text,`).
		WithArgs("public", "dbx_probe_records").
		WillReturnRows(pgxmock.NewRows([]string{"relkind", "amname", "relrowsecurity", "relforcerowsecurity", "is_partition", "has_partitions"}).
			AddRow("r", "heap", true, true, false, false))
	mock.ExpectQuery(`SELECT a\.attname`).
		WithArgs("public", "dbx_probe_records").
		WillReturnRows(pgxmock.NewRows([]string{"attname"}).AddRow("id"))

	meta, err := reader.ReadTable(context.Background(), mock, "dbx_probe_records")
	if err != nil {
		t.Fatalf("ReadTable: %v", err)
	}
	if meta.Columns["tenant_id"].DataType != "text" || !meta.RLS || meta.AccessMethod != "heap" {
		t.Errorf("metadata = %+v", meta)
	}
	if meta.HasPartitions || meta.IsPartition {
		t.Errorf("partition flags = %+v", meta)
	}
	if len(meta.SingleColumnUniqueKeys) != 1 || meta.SingleColumnUniqueKeys[0] != "id" {
		t.Errorf("unique keys = %v, want [id]", meta.SingleColumnUniqueKeys)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}

	if _, err := reader.ReadTable(context.Background(), mock, "BadName"); !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("invalid table = %v, want ErrInvalidIdentifier", err)
	}
}

func TestMetadataReaderEmptyTable(t *testing.T) {
	reader, _ := NewMetadataReader("public")
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	defer mock.Close()
	mock.ExpectQuery(`SELECT column_name, data_type`).
		WithArgs("public", "missing").
		WillReturnRows(pgxmock.NewRows([]string{"column_name", "data_type", "is_nullable", "column_default", "is_identity", "is_generated"}))
	if _, err := reader.ReadTable(context.Background(), mock, "missing"); !errors.Is(err, ErrUnknownTable) {
		t.Errorf("empty metadata = %v, want ErrUnknownTable", err)
	}
}
