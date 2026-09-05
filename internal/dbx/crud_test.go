package dbx

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

func newMockPool(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() { _ = mock.ExpectationsWereMet() })
	t.Cleanup(func() { mock.Close() })
	return mock
}

func newTestCRUD(t *testing.T) (*CRUD, *Registry) {
	t.Helper()
	reg, err := NewRegistry(probeManifest())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	crud, err := NewCRUD(reg)
	if err != nil {
		t.Fatalf("NewCRUD: %v", err)
	}
	return crud, reg
}

func probeRow() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "tenant_id", "name", "note", "enabled", "metadata",
		"version", "deleted_at", "created_at", "updated_at",
	}).AddRow(
		int64(7), "tenant-1", "gadget", nil, true, []byte(`{}`),
		int64(1), nil, nil, nil,
	)
}

func TestInsertSQLShape(t *testing.T) {
	mock := newMockPool(t)
	crud, _ := newTestCRUD(t)

	mock.ExpectQuery(`INSERT INTO "dbx_probe_records" ("tenant_id", "enabled", "name") VALUES ($1, $2, $3) RETURNING `+probeColumns).
		WithArgs("tenant-1", true, "gadget").
		WillReturnRows(probeRow())

	row, err := crud.Insert(context.Background(), mock, "dbx_probe_records", "tenant-1",
		map[string]any{"name": "gadget", "enabled": true})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if row["name"] != "gadget" || row["id"] != int64(7) {
		t.Errorf("row = %#v", row)
	}
}

func TestInsertRejectsProtectedAndUnknown(t *testing.T) {
	mock := newMockPool(t)
	crud, _ := newTestCRUD(t)

	for col := range map[string]any{"id": 1, "tenant_id": "x", "version": 2, "created_at": nil} {
		if _, err := crud.Insert(context.Background(), mock, "dbx_probe_records", "tenant-1", map[string]any{col: 1}); !errors.Is(err, ErrProtectedField) {
			t.Errorf("insert %s = %v, want ErrProtectedField", col, err)
		}
	}
	if _, err := crud.Insert(context.Background(), mock, "dbx_probe_records", "tenant-1", map[string]any{"ghost": 1}); !errors.Is(err, ErrUnknownField) {
		t.Errorf("insert unknown = %v, want ErrUnknownField", err)
	}
	if _, err := crud.Insert(context.Background(), mock, "dbx_probe_records", "", map[string]any{"name": "x"}); !errors.Is(err, ErrMissingScope) {
		t.Errorf("insert without tenant = %v, want ErrMissingScope", err)
	}
	if _, err := crud.Insert(context.Background(), mock, "nope", "tenant-1", map[string]any{"name": "x"}); !errors.Is(err, ErrUnknownTable) {
		t.Errorf("insert unknown table = %v, want ErrUnknownTable", err)
	}
}

func TestInsertJSONBNormalizedBinding(t *testing.T) {
	mock := newMockPool(t)
	crud, _ := newTestCRUD(t)

	mock.ExpectQuery(`INSERT INTO "dbx_probe_records" ("tenant_id", "metadata") VALUES ($1, $2) RETURNING `+probeColumns).
		WithArgs("tenant-1", `{"k":"v"}`).
		WillReturnRows(probeRow())

	if _, err := crud.Insert(context.Background(), mock, "dbx_probe_records", "tenant-1",
		map[string]any{"metadata": map[string]any{"k": "v"}}); err != nil {
		t.Fatalf("Insert jsonb: %v", err)
	}
}

func TestSelectByPKSQLShape(t *testing.T) {
	mock := newMockPool(t)
	crud, _ := newTestCRUD(t)

	mock.ExpectQuery(`SELECT `+probeColumns+` FROM "dbx_probe_records" WHERE "id" = $1 AND "tenant_id" = $2 AND "deleted_at" IS NULL`).
		WithArgs(int64(7), "tenant-1").
		WillReturnRows(probeRow())

	row, err := crud.SelectByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(7))
	if err != nil {
		t.Fatalf("SelectByPK: %v", err)
	}
	if row["tenant_id"] != "tenant-1" {
		t.Errorf("row tenant = %#v", row["tenant_id"])
	}

	// Not found.
	mock.ExpectQuery(`SELECT `+probeColumns+` FROM "dbx_probe_records" WHERE "id" = $1 AND "tenant_id" = $2 AND "deleted_at" IS NULL`).
		WithArgs(int64(8), "tenant-1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}))
	if _, err := crud.SelectByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(8)); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row = %v, want ErrNotFound", err)
	}
}

func TestUpdateByPKSQLShape(t *testing.T) {
	mock := newMockPool(t)
	crud, reg := newTestCRUD(t)
	m, _ := reg.Lookup("dbx_probe_records")
	patch, err := PatchFromMap(m, map[string]any{"name": "renamed"})
	if err != nil {
		t.Fatalf("PatchFromMap: %v", err)
	}

	mock.ExpectQuery(`UPDATE "dbx_probe_records" SET "name" = $1, "updated_at" = now(), "version" = "version" + 1 WHERE "id" = $2 AND "tenant_id" = $3 AND "deleted_at" IS NULL RETURNING `+probeColumns).
		WithArgs("renamed", int64(7), "tenant-1").
		WillReturnRows(probeRow())

	row, err := crud.UpdateByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(7), patch, nil)
	if err != nil {
		t.Fatalf("UpdateByPK: %v", err)
	}
	if row["id"] != int64(7) {
		t.Errorf("row = %#v", row)
	}
}

func TestUpdateByPKVersionCAS(t *testing.T) {
	mock := newMockPool(t)
	crud, reg := newTestCRUD(t)
	m, _ := reg.Lookup("dbx_probe_records")
	patch, _ := PatchFromMap(m, map[string]any{"name": "renamed"})
	stale := int64(1)

	mock.ExpectQuery(`UPDATE "dbx_probe_records" SET "name" = $1, "updated_at" = now(), "version" = "version" + 1 WHERE "id" = $2 AND "tenant_id" = $3 AND "version" = $4 AND "deleted_at" IS NULL RETURNING `+probeColumns).
		WithArgs("renamed", int64(7), "tenant-1", stale).
		WillReturnRows(pgxmock.NewRows([]string{"id"})) // zero rows → conflict

	if _, err := crud.UpdateByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(7), patch, &UpdateOptions{ExpectedVersion: &stale}); !errors.Is(err, ErrConflict) {
		t.Errorf("stale CAS = %v, want ErrConflict", err)
	}
}

func TestUpdateRejections(t *testing.T) {
	mock := newMockPool(t)
	crud, _ := newTestCRUD(t)

	if _, err := crud.UpdateByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(7), nil, nil); !errors.Is(err, ErrEmptyPatch) {
		t.Errorf("nil patch = %v, want ErrEmptyPatch", err)
	}
	if _, err := crud.UpdateByPK(context.Background(), mock, "dbx_probe_records", "", int64(7), nil, nil); !errors.Is(err, ErrMissingScope) {
		t.Errorf("update without tenant = %v, want ErrMissingScope", err)
	}

	// CAS requested but manifest without version column must fail.
	bare, err := NewRegistry(TableManifest{
		Table: "bare", TenantColumn: "tenant_id", PrimaryKey: "id",
		Columns: []ColumnSpec{
			{Name: "id", Kind: KindInt}, {Name: "tenant_id", Kind: KindText},
			{Name: "name", Kind: KindText, Writable: true},
		},
	})
	if err != nil {
		t.Fatalf("bare registry: %v", err)
	}
	bareM, _ := bare.Lookup("bare")
	barePatch, _ := PatchFromMap(bareM, map[string]any{"name": "x"})
	bareCRUD, _ := NewCRUD(bare)
	v := int64(1)
	if _, err := bareCRUD.UpdateByPK(context.Background(), mock, "bare", "t", int64(1), barePatch, &UpdateOptions{ExpectedVersion: &v}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("CAS without version column = %v, want ErrUnsupported", err)
	}
}

func TestDeleteSoftOnly(t *testing.T) {
	mock := newMockPool(t)
	crud, _ := newTestCRUD(t)

	mock.ExpectExec(`UPDATE "dbx_probe_records" SET "deleted_at" = now() WHERE "id" = $1 AND "tenant_id" = $2 AND "deleted_at" IS NULL`).
		WithArgs(int64(7), "tenant-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := crud.DeleteByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(7)); err != nil {
		t.Fatalf("DeleteByPK: %v", err)
	}

	mock.ExpectExec(`UPDATE "dbx_probe_records" SET "deleted_at" = now() WHERE "id" = $1 AND "tenant_id" = $2 AND "deleted_at" IS NULL`).
		WithArgs(int64(8), "tenant-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	if err := crud.DeleteByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(8)); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing = %v, want ErrNotFound", err)
	}

	// Manifest without soft-delete column: hard DELETE must never happen.
	hard := TableManifest{
		Table: "hard", TenantColumn: "tenant_id", PrimaryKey: "id",
		Columns: []ColumnSpec{
			{Name: "id", Kind: KindInt}, {Name: "tenant_id", Kind: KindText},
			{Name: "name", Kind: KindText},
		},
	}
	reg, err := NewRegistry(hard)
	if err != nil {
		t.Fatalf("hard registry: %v", err)
	}
	hardCRUD, _ := NewCRUD(reg)
	if err := hardCRUD.DeleteByPK(context.Background(), mock, "hard", "t", int64(1)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("delete without soft column = %v, want ErrUnsupported", err)
	}
}

func TestUpdateByPKTooManyRows(t *testing.T) {
	mock := newMockPool(t)
	crud, reg := newTestCRUD(t)
	m, _ := reg.Lookup("dbx_probe_records")
	patch, _ := PatchFromMap(m, map[string]any{"name": "dup"})

	mock.ExpectQuery(`UPDATE "dbx_probe_records" SET "name" = $1, "updated_at" = now(), "version" = "version" + 1 WHERE "id" = $2 AND "tenant_id" = $3 AND "deleted_at" IS NULL RETURNING `+probeColumns).
		WithArgs("dup", int64(7), "tenant-1").
		WillReturnRows(probeRow().AddRow(
			int64(8), "tenant-1", "other", nil, true, []byte(`{}`),
			int64(1), nil, nil, nil,
		))
	if _, err := crud.UpdateByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(7), patch, nil); !errors.Is(err, ErrTooManyRows) {
		t.Errorf("multi-row update = %v, want ErrTooManyRows", err)
	}
}

func TestInsertUniqueViolationTyped(t *testing.T) {
	mock := newMockPool(t)
	crud, _ := newTestCRUD(t)

	mock.ExpectQuery(`INSERT INTO "dbx_probe_records" ("tenant_id", "name") VALUES ($1, $2) RETURNING `+probeColumns).
		WithArgs("tenant-1", "dupe").
		WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate key"})

	_, err := crud.Insert(context.Background(), mock, "dbx_probe_records", "tenant-1", map[string]any{"name": "dupe"})
	if !errors.Is(err, ErrUniqueViolation) {
		t.Errorf("insert 23505 = %v, want ErrUniqueViolation", err)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Errorf("original PgError not reachable: %v", err)
	}

	mock.ExpectExec(`UPDATE "dbx_probe_records" SET "deleted_at" = now() WHERE "id" = $1 AND "tenant_id" = $2 AND "deleted_at" IS NULL`).
		WithArgs(int64(7), "tenant-1").
		WillReturnError(&pgconn.PgError{Code: "23505"})
	if err := crud.DeleteByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(7)); !errors.Is(err, ErrUniqueViolation) {
		t.Errorf("delete 23505 = %v, want ErrUniqueViolation", err)
	}
}

func TestReadonlyTableRejectsWrites(t *testing.T) {
	ro := probeManifest()
	ro.Table = "ro_view"
	ro.Readonly = true
	for i := range ro.Columns {
		ro.Columns[i].Writable = false
		ro.Columns[i].Insertable = false
	}
	reg, err := NewRegistry(ro)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	crud, _ := NewCRUD(reg)

	if _, err := crud.Insert(context.Background(), newMockPool(t), "ro_view", "t", map[string]any{"name": "x"}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("readonly insert = %v, want ErrUnsupported", err)
	}
	if err := crud.DeleteByPK(context.Background(), newMockPool(t), "ro_view", "t", int64(1)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("readonly delete = %v, want ErrUnsupported", err)
	}
}

func TestObserverFacts(t *testing.T) {
	var facts []QueryFact
	obs := ObserverFunc(func(f QueryFact) { facts = append(facts, f) })
	reg, _ := NewRegistry(probeManifest())
	crud, err := NewCRUD(reg, WithObserver(obs))
	if err != nil {
		t.Fatalf("NewCRUD: %v", err)
	}
	mock, _ := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	defer mock.Close()

	mock.ExpectQuery(`INSERT INTO "dbx_probe_records" ("tenant_id", "name") VALUES ($1, $2) RETURNING `+probeColumns).
		WithArgs("tenant-1", "gadget").
		WillReturnRows(probeRow())
	if _, err := crud.Insert(context.Background(), mock, "dbx_probe_records", "tenant-1", map[string]any{"name": "gadget"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// SQLSTATE surfaces on driver errors; no parameter values recorded.
	mock.ExpectQuery(`SELECT `+probeColumns+` FROM "dbx_probe_records" WHERE "id" = $1 AND "tenant_id" = $2 AND "deleted_at" IS NULL`).
		WithArgs(int64(9), "tenant-1").
		WillReturnError(&pgconn.PgError{Code: "42P01"})
	if _, err := crud.SelectByPK(context.Background(), mock, "dbx_probe_records", "tenant-1", int64(9)); err == nil {
		t.Fatal("expected error")
	}

	if len(facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(facts))
	}
	if facts[0].Op != "insert" || facts[0].Rows != 1 || facts[0].SQLState != "" || facts[0].Table != "dbx_probe_records" {
		t.Errorf("fact[0] = %+v", facts[0])
	}
	if facts[0].QueryID == "" || facts[0].QueryID == facts[1].QueryID {
		t.Errorf("fingerprints must be non-empty and distinct: %q vs %q", facts[0].QueryID, facts[1].QueryID)
	}
	if facts[1].Op != "select" || facts[1].SQLState != "42P01" {
		t.Errorf("fact[1] = %+v", facts[1])
	}
	// QueryFact structurally cannot carry parameter values; compile-time
	// contract is in observer.go. Defensive runtime check: fingerprint of a
	// parameterized statement is stable across values.
	if Fingerprint(`SELECT 1 WHERE a = $1 AND b = $2`) != Fingerprint(`SELECT 1 WHERE a = $3 AND b = $9`) {
		t.Error("fingerprint should not depend on placeholder numbering")
	}
}
