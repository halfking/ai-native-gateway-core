//go:build integration

package dbx

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestProbeMetadataSnapshotAndDrift(t *testing.T) {
	k := newProbeKit(t)
	reader, err := NewMetadataReader("public")
	if err != nil {
		t.Fatalf("NewMetadataReader: %v", err)
	}

	var snapshot *MetadataSnapshot
	err = k.runner.WithSuperAdminTx(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		var ierr error
		snapshot, ierr = reader.ReadRegistry(ctx, tx, k.reg)
		return ierr
	})
	if err != nil {
		t.Fatalf("ReadRegistry: %v", err)
	}
	meta, ok := snapshot.Tables["dbx_probe_records"]
	if !ok {
		t.Fatalf("snapshot tables = %#v", snapshot.Tables)
	}
	if meta.AccessMethod != "heap" || !meta.RLS || !meta.ForceRLS || meta.IsPartition || meta.HasPartitions {
		t.Errorf("table metadata = %+v", meta)
	}
	if len(meta.SingleColumnUniqueKeys) != 1 || meta.SingleColumnUniqueKeys[0] != "id" {
		t.Errorf("unique keys = %v, want [id]", meta.SingleColumnUniqueKeys)
	}
	if got := meta.Columns["metadata"]; got.DataType != "jsonb" || got.Nullable {
		t.Errorf("metadata column = %+v", got)
	}
	if got := meta.Columns["deleted_at"]; !got.Nullable {
		t.Errorf("deleted_at column = %+v, want nullable", got)
	}
	if got := meta.Columns["id"]; got.Identity || got.Default == nil {
		t.Errorf("id column = %+v, want sequence default (BIGSERIAL)", got)
	}
	manifest, _ := k.reg.Lookup("dbx_probe_records")
	if drifts := CompareManifest(manifest, meta); HasBlockingDrift(drifts) {
		t.Errorf("probe catalog has blocking drift: %+v", drifts)
	}

	var cache MetadataCache
	if err := cache.Store(snapshot); err != nil {
		t.Fatalf("cache store: %v", err)
	}
	cached, epoch := cache.Snapshot()
	if cached != snapshot || epoch != 0 {
		t.Errorf("cache snapshot = (%p,%d), want (%p,0)", cached, epoch, snapshot)
	}
	if drifts := CompareSnapshot(k.reg, snapshot); HasBlockingDrift(drifts) {
		t.Errorf("CompareSnapshot blocking drift: %+v", drifts)
	}
	if epoch := cache.Invalidate(); epoch != 1 {
		t.Errorf("cache epoch = %d, want 1", epoch)
	}
}

// readTableAsAdmin captures metadata through the privileged path.
func readTableAsAdmin(t *testing.T, k *probeKit, table string) TableMetadata {
	t.Helper()
	reader, err := NewMetadataReader("public")
	if err != nil {
		t.Fatalf("NewMetadataReader: %v", err)
	}
	var meta TableMetadata
	err = k.runner.WithSuperAdminTx(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		var ierr error
		meta, ierr = reader.ReadTable(ctx, tx, table)
		return ierr
	})
	if err != nil {
		t.Fatalf("ReadTable(%s): %v", table, err)
	}
	return meta
}

// driftKinds summarizes a drift list for assertions.
func driftKinds(drifts []Drift) map[DriftKind]int {
	kinds := map[DriftKind]int{}
	for _, d := range drifts {
		kinds[d.Kind]++
	}
	return kinds
}

func TestProbeDriftGateOnRealCatalog(t *testing.T) {
	k := newProbeKit(t)
	manifest, _ := k.reg.Lookup("dbx_probe_records")

	// Partition parent + leaf: the contract forbids registering either.
	env := sharedProbeEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := env.admin.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS dbx_probe_parts (
		    id BIGINT, tenant_id TEXT NOT NULL, name TEXT
		) PARTITION BY RANGE (id);
		CREATE TABLE IF NOT EXISTS dbx_probe_parts_leaf
		    PARTITION OF dbx_probe_parts FOR VALUES FROM (0) TO (1000000);
		ALTER TABLE dbx_probe_parts ENABLE ROW LEVEL SECURITY;
		ALTER TABLE dbx_probe_parts FORCE ROW LEVEL SECURITY;
		CREATE OR REPLACE VIEW dbx_probe_v AS SELECT * FROM dbx_probe_records;
		GRANT SELECT ON dbx_probe_parts, dbx_probe_parts_leaf, dbx_probe_v TO dbx_app;`); err != nil {
		t.Fatalf("create partition probe: %v", err)
	}
	parent := readTableAsAdmin(t, k, "dbx_probe_parts")
	if parent.RelationKind != "p" || !parent.HasPartitions {
		t.Errorf("parent metadata = %+v", parent)
	}
	if drifts := CompareManifest(manifest, parent); !HasBlockingDrift(drifts) {
		t.Errorf("partition parent not blocking: %+v", drifts)
	}
	leaf := readTableAsAdmin(t, k, "dbx_probe_parts_leaf")
	if !leaf.IsPartition {
		t.Errorf("leaf metadata = %+v", leaf)
	}

	// Readonly view over the probe table: RLS-exempt, non-blocking for a
	// readonly manifest; blocking for a writable one. (The view and grants
	// are created together with the partition fixtures above.)
	viewMeta := readTableAsAdmin(t, k, "dbx_probe_v")
	ro := *manifest
	ro.Readonly = true
	ro.Table = "dbx_probe_v"
	if drifts := CompareManifest(&ro, viewMeta); HasBlockingDrift(drifts) {
		t.Errorf("readonly view blocking: %+v", drifts)
	}
	if drifts := CompareManifest(manifest, viewMeta); !HasBlockingDrift(drifts) {
		t.Error("writable manifest on view must be blocking")
	}

	// Dropping FORCE RLS then RLS must surface as blocking drift, and be
	// restored afterwards so other shared-container tests are unaffected.
	restore := func() {
		_, _ = env.admin.Exec(context.Background(), `
			ALTER TABLE dbx_probe_records FORCE ROW LEVEL SECURITY;
			ALTER TABLE dbx_probe_records ENABLE ROW LEVEL SECURITY;`)
	}
	if _, err := env.admin.Exec(ctx, `ALTER TABLE dbx_probe_records NO FORCE ROW LEVEL SECURITY`); err != nil {
		t.Fatalf("no force: %v", err)
	}
	if kinds := driftKinds(CompareManifest(manifest, readTableAsAdmin(t, k, "dbx_probe_records"))); kinds[DriftForceRLSMismatch] != 1 {
		t.Errorf("force drift kinds = %v", kinds)
	}
	restore()

	// Extra physical column stays non-blocking; a dropped declared column
	// is blocking.
	if _, err := env.admin.Exec(ctx, `
		ALTER TABLE dbx_probe_records ADD COLUMN extra_col TEXT;
		DROP VIEW dbx_probe_v`); err != nil {
		t.Fatalf("add column: %v", err)
	}
	drifts := CompareManifest(manifest, readTableAsAdmin(t, k, "dbx_probe_records"))
	if kinds := driftKinds(drifts); kinds[DriftUnexpectedColumn] != 1 || HasBlockingDrift(drifts) {
		t.Errorf("extra column kinds = %v blocking=%v", kinds, HasBlockingDrift(drifts))
	}
	if _, err := env.admin.Exec(ctx, `ALTER TABLE dbx_probe_records DROP COLUMN note`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	if !HasBlockingDrift(CompareManifest(manifest, readTableAsAdmin(t, k, "dbx_probe_records"))) {
		t.Error("missing declared column must be blocking")
	}
	if _, err := env.admin.Exec(ctx, `
		ALTER TABLE dbx_probe_records ADD COLUMN note TEXT;
		ALTER TABLE dbx_probe_records DROP COLUMN extra_col;
		CREATE OR REPLACE VIEW dbx_probe_v AS SELECT * FROM dbx_probe_records;
		GRANT SELECT ON dbx_probe_v TO dbx_app;`); err != nil {
		t.Fatalf("restore columns: %v", err)
	}
	if drifts := CompareManifest(manifest, readTableAsAdmin(t, k, "dbx_probe_records")); HasBlockingDrift(drifts) {
		t.Errorf("restored table still has drift: %+v", drifts)
	}
	restore()
}
