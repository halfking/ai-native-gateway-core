package dbx

import (
	"errors"
	"testing"
	"time"
)

type probeDTO struct {
	Name    *string        `dbx:"name"`
	Note    *string        `dbx:"note"`
	Enabled *bool          `dbx:"enabled"`
	Meta    map[string]any `dbx:"metadata"`
	Skip    string         `dbx:"-"`
	NoTag   string
}

func strp(s string) *string { return &s }

func TestPatchFromMap(t *testing.T) {
	m := normalizedProbe(t)

	p, err := PatchFromMap(&m, map[string]any{
		"name": "renamed",
		"note": nil,
	})
	if err != nil {
		t.Fatalf("PatchFromMap: %v", err)
	}
	if len(p.Ops) != 2 {
		t.Fatalf("ops = %d, want 2", len(p.Ops))
	}
	// Sorted key order: name before note.
	if p.Ops[0].Column != "name" || p.Ops[0].Value != "renamed" {
		t.Errorf("op[0] = %+v", p.Ops[0])
	}
	if p.Ops[1].Column != "note" || p.Ops[1].Value != nil {
		t.Errorf("op[1] = %+v (explicit null)", p.Ops[1])
	}
	if len(p.Dropped) != 0 {
		t.Errorf("Dropped = %v, want empty", p.Dropped)
	}
}

func TestPatchFromMapNullOnNonNull(t *testing.T) {
	m := normalizedProbe(t)
	if _, err := PatchFromMap(&m, map[string]any{"name": nil}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("null on non-nullable = %v, want ErrInvalidInput", err)
	}
}

func TestPatchFromMapUnknown(t *testing.T) {
	m := normalizedProbe(t)
	_, err := PatchFromMap(&m, map[string]any{"ghost": 1})
	if !errors.Is(err, ErrUnknownField) {
		t.Fatalf("strict unknown field = %v, want ErrUnknownField", err)
	}

	p, err := PatchFromMap(&m,
		map[string]any{"ghost": 1, "another": 2, "name": "x"},
		WithLenientUnknown(),
	)
	if err != nil {
		t.Fatalf("lenient: %v", err)
	}
	if len(p.Dropped) != 2 || p.Dropped[0] != "another" || p.Dropped[1] != "ghost" {
		t.Errorf("Dropped = %v, want sorted [another ghost]", p.Dropped)
	}
}

func TestPatchProtectedFieldsAlwaysRejected(t *testing.T) {
	m := normalizedProbe(t)
	for _, col := range []string{"id", "tenant_id", "version", "deleted_at", "created_at", "updated_at"} {
		if _, err := PatchFromMap(&m, map[string]any{col: 1}); !errors.Is(err, ErrProtectedField) {
			t.Errorf("patch %s = %v, want ErrProtectedField", col, err)
		}
		// Protected columns are rejected even in lenient mode.
		if _, err := PatchFromMap(&m, map[string]any{col: 1}, WithLenientUnknown()); !errors.Is(err, ErrProtectedField) {
			t.Errorf("lenient patch %s = %v, want ErrProtectedField", col, err)
		}
	}
}

func TestPatchEmpty(t *testing.T) {
	m := normalizedProbe(t)
	if _, err := PatchFromMap(&m, map[string]any{}); !errors.Is(err, ErrEmptyPatch) {
		t.Errorf("empty map = %v, want ErrEmptyPatch", err)
	}
	if _, err := PatchFromMap(&m, map[string]any{"ghost": 1}, WithLenientUnknown()); !errors.Is(err, ErrEmptyPatch) {
		t.Errorf("all-dropped = %v, want ErrEmptyPatch", err)
	}
}

func TestPatchKindChecks(t *testing.T) {
	m := normalizedProbe(t)
	bad := []map[string]any{
		{"name": 42},                 // int for text
		{"enabled": "yes"},           // string for bool
		{"name": true},               // bool for text
		{"name": time.Now()},         // time for text
		{"metadata": make(chan int)}, // unmarshalable
	}
	for _, fields := range bad {
		if _, err := PatchFromMap(&m, fields); err == nil {
			t.Errorf("PatchFromMap accepted %#v", fields)
		}
	}
	if _, err := PatchFromMap(&m, map[string]any{"metadata": map[string]any{"k": "v"}}); err != nil {
		t.Errorf("valid jsonb map: %v", err)
	}
}

func TestPatchFromDTOTriState(t *testing.T) {
	m := normalizedProbe(t)
	on := true

	// All pointer fields nil → nothing to write → ErrEmptyPatch.
	empty := probeDTO{}
	if _, err := PatchFromDTO(&m, empty); !errors.Is(err, ErrEmptyPatch) {
		t.Errorf("empty DTO = %v, want ErrEmptyPatch", err)
	}

	// Non-pointer field (Meta) is always written even when nil map.
	p, err := PatchFromDTO(&m, probeDTO{Meta: map[string]any{"a": 1}, Name: strp("n1")})
	if err != nil {
		t.Fatalf("PatchFromDTO: %v", err)
	}
	byCol := map[string]any{}
	for _, op := range p.Ops {
		byCol[op.Column] = op.Value
	}
	if v, ok := byCol["name"].(string); !ok || v != "n1" {
		t.Errorf("name op = %#v", byCol["name"])
	}
	raw, ok := byCol["metadata"].(string)
	if !ok || raw != `{"a":1}` {
		t.Errorf("metadata op = %#v, want canonical string", byCol["metadata"])
	}
	if _, has := byCol["note"]; has {
		t.Error("nil pointer note must be missing (untouched)")
	}

	// Pointer present + zero value is an explicit zero write.
	p2, err := PatchFromDTO(&m, probeDTO{Enabled: &on, Name: strp("")})
	if err != nil {
		t.Fatalf("PatchFromDTO(2): %v", err)
	}
	if len(p2.Ops) != 2 {
		t.Errorf("ops = %d, want 2 (name=\"\" is explicit)", len(p2.Ops))
	}

	// Untagged and dbx:"-" fields are ignored entirely.
	if _, err := PatchFromDTO(&m, struct {
		Name string `dbx:"name"`
		Skip string `dbx:"-"`
		X    string
	}{Name: "ok", Skip: "s", X: "x"}); err != nil {
		t.Fatalf("tagged subset DTO: %v", err)
	}

	// Unknown column in tag is a manifest violation, not a drop.
	if _, err := PatchFromDTO(&m, struct {
		Ghost string `dbx:"ghost"`
	}{"x"}); !errors.Is(err, ErrUnknownField) {
		t.Errorf("unknown tag = %v, want ErrUnknownField", err)
	}

	if _, err := PatchFromDTO(&m, "not a struct"); err == nil {
		t.Error("non-struct DTO accepted")
	}
}

func TestPatchFromDTOSkipsUnexportedFields(t *testing.T) {
	m := normalizedProbe(t)
	// An unexported field carrying a dbx tag must be skipped, not panic
	// (reflect cannot Interface() unexported field values).
	type tagged struct {
		Name   string `dbx:"name"`
		hidden string `dbx:"note"`
	}
	if _, err := PatchFromDTO(&m, tagged{Name: "ok", hidden: "x"}); err != nil {
		t.Fatalf("PatchFromDTO with unexported tagged field: %v", err)
	}
}

func normalizedProbe(t *testing.T) TableManifest {
	t.Helper()
	m, err := probeManifest().normalize()
	if err != nil {
		t.Fatalf("normalize probe manifest: %v", err)
	}
	return m
}
