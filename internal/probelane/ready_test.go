package probelane

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubRow struct {
	name *string
	err  error
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("want 1 dest")
	}
	ptr, ok := dest[0].(**string)
	if !ok {
		return errors.New("dest type")
	}
	*ptr = r.name
	return nil
}

type stubDB struct {
	tables map[string]*string
	err    error
}

func (d stubDB) QueryRow(_ context.Context, _ string, args ...any) DBRow {
	if d.err != nil {
		return stubRow{err: d.err}
	}
	key, _ := args[0].(string)
	name := d.tables[key]
	return stubRow{name: name}
}

func TestAssertLaneTablesReadySkippedWhenOff(t *testing.T) {
	SetPhysicalEnabledForTest(false)
	defer ResetPhysicalOverrideForTest()
	if err := AssertLaneTablesReady(context.Background(), nil); err != nil {
		t.Fatalf("flag off must be no-op, got %v", err)
	}
}

func TestAssertLaneTablesReadyFailsWhenMissing(t *testing.T) {
	SetPhysicalEnabledForTest(true)
	defer ResetPhysicalOverrideForTest()
	present := "public.session_turns_probe_hot"
	db := stubDB{tables: map[string]*string{
		"public.session_turns_probe_hot": &present,
		// request_stats_probe_minute intentionally absent
	}}
	err := AssertLaneTablesReady(context.Background(), db)
	if err == nil {
		t.Fatal("want error when request_stats_probe_minute missing")
	}
	if !strings.Contains(err.Error(), "request_stats_probe_minute") {
		t.Fatalf("error must name missing table, got %v", err)
	}
}

func TestAssertLaneTablesReadyOK(t *testing.T) {
	SetPhysicalEnabledForTest(true)
	defer ResetPhysicalOverrideForTest()
	a := "public.session_turns_probe_hot"
	b := "public.request_stats_probe_minute"
	db := stubDB{tables: map[string]*string{
		"public.session_turns_probe_hot":     &a,
		"public.request_stats_probe_minute": &b,
	}}
	if err := AssertLaneTablesReady(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
