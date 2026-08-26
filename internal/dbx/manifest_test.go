package dbx

import (
	"errors"
	"strings"
	"testing"
)

func TestManifestNormalizeDefaults(t *testing.T) {
	m := probeManifest()
	m.MaxUpdateRows = 0
	norm, err := m.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if norm.MaxUpdateRows != 1 {
		t.Errorf("MaxUpdateRows default = %d, want 1", norm.MaxUpdateRows)
	}
	spec, _ := norm.Column("metadata")
	if spec.JSONBMaxBytes != DefaultJSONBMaxBytes {
		t.Errorf("JSONBMaxBytes default = %d, want %d", spec.JSONBMaxBytes, DefaultJSONBMaxBytes)
	}
	if _, ok := norm.Column("id"); !ok {
		t.Error("normalized manifest lost column index")
	}
}

func TestManifestNormalizeRejections(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*TableManifest)
		want string
	}{
		{"bad table name", func(m *TableManifest) { m.Table = "1bad" }, "invalid identifier"},
		{"pk not declared", func(m *TableManifest) { m.PrimaryKey = "ghost" }, "primary key"},
		{"tenant not declared", func(m *TableManifest) { m.TenantColumn = "ghost" }, "tenant column"},
		{"no columns", func(m *TableManifest) { m.Columns = nil }, "no columns"},
		{"writable pk", func(m *TableManifest) {
			m.Columns = append([]ColumnSpec{{Name: "id", Kind: KindInt, Writable: true}}, m.Columns[1:]...)
		}, "must not be writable"},
		{"readonly with writable column", func(m *TableManifest) { m.Readonly = true }, "readonly table"},
		{"soft delete column writable", func(m *TableManifest) {
			sd := "name"
			m.SoftDelete = &sd
		}, "soft delete"},
		{"version column undeclared", func(m *TableManifest) {
			v := "ghost"
			m.VersionColumn = &v
		}, "version"},
		{"touch column undeclared", func(m *TableManifest) {
			tc := "ghost"
			m.TouchColumn = &tc
		}, "touch"},
		{"duplicate column", func(m *TableManifest) {
			m.Columns = append(m.Columns, ColumnSpec{Name: "name", Kind: KindText})
		}, "duplicate"},
	}
	for _, tc := range cases {
		m := probeManifest()
		tc.mut(&m)
		_, err := m.normalize()
		if err == nil {
			t.Errorf("%s: normalize accepted invalid manifest", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not contain %q", tc.name, err, tc.want)
		}
	}
}

func TestRegistryIsolatesCallerManifests(t *testing.T) {
	src := probeManifest()
	reg, err := NewRegistry(src)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Mutating the caller's manifest after construction must not leak into
	// the registry (deep-copied column slice).
	src.Columns[2].Writable = false // was: name writable
	src.Table = "mutated_after_registration"
	m, err := reg.Lookup("dbx_probe_records")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if spec, _ := m.Column("name"); !spec.Writable {
		t.Error("post-construction mutation leaked into registry columns")
	}

	// Mutating the manifest returned by Lookup must not affect subsequent
	// lookups (defensive copy).
	m.Readonly = true
	m2, err := reg.Lookup("dbx_probe_records")
	if err != nil {
		t.Fatalf("Lookup(2): %v", err)
	}
	if m2.Readonly {
		t.Error("mutating a Lookup result leaked back into the registry")
	}
}

func TestRegistry(t *testing.T) {
	reg, err := NewRegistry(probeManifest())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	m, err := reg.Lookup("dbx_probe_records")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if m.Table != "dbx_probe_records" {
		t.Errorf("Lookup returned %q", m.Table)
	}
	if _, err := reg.Lookup("nope"); !errors.Is(err, ErrUnknownTable) {
		t.Errorf("Lookup unknown = %v, want ErrUnknownTable", err)
	}
	if v := reg.Version(); v == 0 {
		t.Error("Version = 0, want monotonic epoch")
	}
	if got := reg.Tables(); len(got) != 1 || got[0] != "dbx_probe_records" {
		t.Errorf("Tables = %v", got)
	}

	reg2, err := NewRegistry(probeManifest())
	if err != nil {
		t.Fatalf("NewRegistry(2): %v", err)
	}
	if reg2.Version() <= reg.Version() {
		t.Error("registry version must be monotonic across constructions")
	}

	if _, err := NewRegistry(probeManifest(), probeManifest()); err == nil {
		t.Error("duplicate table registration accepted")
	}
	if _, err := NewRegistry(); err != nil {
		t.Errorf("empty registry: %v", err)
	}
}
