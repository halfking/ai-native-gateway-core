package dbx

// Shared fixtures for unit tests. The probe table mirrors the
// Testcontainers DDL in probe_integration_test.go; it exists only inside
// tests, never in production schemas.

func probeManifest() TableManifest {
	soft := "deleted_at"
	ver := "version"
	touch := "updated_at"
	return TableManifest{
		Table:        "dbx_probe_records",
		TenantColumn: "tenant_id",
		PrimaryKey:   "id",
		Columns: []ColumnSpec{
			{Name: "id", Kind: KindInt},
			{Name: "tenant_id", Kind: KindText},
			{Name: "name", Kind: KindText, Writable: true, Insertable: true},
			{Name: "note", Kind: KindText, Writable: true, Insertable: true, Nullable: true},
			{Name: "enabled", Kind: KindBool, Writable: true, Insertable: true},
			{Name: "metadata", Kind: KindJSONB, Writable: true, Insertable: true},
			{Name: "version", Kind: KindInt},
			{Name: "deleted_at", Kind: KindTimestamp, Nullable: true},
			{Name: "created_at", Kind: KindTimestamp},
			{Name: "updated_at", Kind: KindTimestamp},
		},
		SoftDelete:    &soft,
		VersionColumn: &ver,
		TouchColumn:   &touch,
		MaxUpdateRows: 1,
	}
}

// probeColumns is the exact RETURNING/SELECT column list for probe_records.
const probeColumns = `"id", "tenant_id", "name", "note", "enabled", "metadata", "version", "deleted_at", "created_at", "updated_at"`

// testScope mirrors the host's GUC conventions (values are injected, not
// hard-coded in the framework).
var testScope = ScopeConfig{
	TenantGUC:       "app.current_tenant",
	RoleGUC:         "app.current_role",
	BypassGUC:       "app.bypass_rls",
	SuperAdminValue: "super_admin",
	BypassValue:     "true",
}
