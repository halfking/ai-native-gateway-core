package startup

import (
	"os"
	"strings"
	"testing"
)

// TestMigration562HeapConversionContract pins the up contract for migration
// 562: the request_logs_bodies_2026_08 partition must be converted from
// columnar to heap ONLY when the partition is empty, and the
// ensure_request_logs_bodies_partition() function must be reinstalled so it
// stops creating columnar partitions for future months.
//
// Non-empty partitions must be left columnar (a DETACH+DROP would lose user
// bodies); the load-bearing 2026_07 partition must never be touched because
// analytics scans it via UNION ALL with the hot table.
func TestMigration562HeapConversionContract(t *testing.T) {
	up := string(migrationFile(t, "562_fix_request_logs_bodies_partitions_heap.sql"))

	for _, want := range []string{
		"BEGIN;",
		"COMMIT;",
		// ensure function replaced inline for environments that only run
		// startup migrations.
		"CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition",
		// Conversion sequence must detach before drop, then recreate as heap
		// partition of the same parent for the canonical Aug-2026 +08 range.
		"DETACH PARTITION public.request_logs_bodies_2026_08",
		"DROP TABLE public.request_logs_bodies_2026_08 CASCADE",
		"PARTITION OF public.request_logs_bodies",
		"FOR VALUES FROM ('2026-08-01 00:00:00+08') TO ('2026-09-01 00:00:00+08')",
		// Primary key on the recreated partition must be restored.
		"ADD CONSTRAINT request_logs_bodies_2026_08_pkey PRIMARY KEY (request_id, ts)",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 562 up missing %q", want)
		}
	}

	// Data-loss guard: conversion must be refused when the partition holds
	// rows. The migration chooses to remain columnar (safe no-op) rather than
	// DETACH+DROP user data.
	for _, want := range []string{
		"SELECT COUNT(*) INTO row_count FROM request_logs_bodies_2026_08",
		"IF row_count > 0 THEN",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 562 up missing row-count guard %q", want)
		}
	}

	// Detached-partition guard: if the partition is not attached to the
	// parent, abort loudly instead of converting metadata in isolation.
	if !strings.Contains(up, "RAISE EXCEPTION '562: ABORT — partition is detached") {
		t.Errorf("migration 562 up must abort when partition is detached from parent")
	}

	// Post-condition verification must exist and must hard-fail when the
	// partition is missing or detached after the conversion.
	for _, want := range []string{
		"RAISE EXCEPTION '562: VERIFY FAIL — request_logs_bodies_2026_08 missing'",
		"RAISE EXCEPTION '562: VERIFY FAIL — partition not attached to request_logs_bodies'",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration 562 up missing verification %q", want)
		}
	}

	// The 2026_07 partition (load-bearing July bodies) must be documented as
	// out of scope and must never appear in DDL statements targeting it.
	if !strings.Contains(up, "2026_07 keeps its columnar storage") &&
		!strings.Contains(up, "2026_07 is intentionally skipped") {
		t.Errorf("migration 562 up must document that request_logs_bodies_2026_07 is excluded")
	}
	for _, forbidden := range []string{
		"DETACH PARTITION public.request_logs_bodies_2026_07",
		"DROP TABLE public.request_logs_bodies_2026_07",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("migration 562 up must not contain %q (2026_07 is load-bearing)", forbidden)
		}
	}

	// The re-created partition must NOT be columnar: the new
	// ensure_request_logs_bodies_partition body must not contain
	// "USING columnar" (that is what made the promote path fail).
	fnStart := strings.Index(up, "CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition")
	if fnStart < 0 {
		t.Fatalf("migration 562 up missing ensure function replacement")
	}
	fnEnd := strings.Index(up[fnStart:], "$$;")
	if fnEnd < 0 {
		t.Fatalf("migration 562 up: ensure function body not terminated")
	}
	fnBody := up[fnStart : fnStart+fnEnd]
	if strings.Contains(fnBody, "USING columnar") {
		t.Errorf("migration 562 up: ensure_request_logs_bodies_partition must not keep USING columnar")
	}
}

// TestMigration562DownContract pins the down contract: the rollback restores
// the columnar ensure function and converts 2026_08 back to columnar, but
// must refuse to run when the partition holds rows (data-loss guard mirrors
// the up path).
func TestMigration562DownContract(t *testing.T) {
	down := string(migrationFile(t, "562_fix_request_logs_bodies_partitions_heap.down.sql"))

	for _, want := range []string{
		"BEGIN;",
		"COMMIT;",
		"CREATE OR REPLACE FUNCTION public.ensure_request_logs_bodies_partition",
		// Down restores the pre-562 columnar behaviour in the ensure function.
		"USING columnar",
		// Same canonical bound literal as the up path so the round-trip is
		// timezone-stable.
		"FOR VALUES FROM ('2026-08-01 00:00:00+08') TO ('2026-09-01 00:00:00+08')",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("migration 562 down missing %q", want)
		}
	}

	// Data-loss guard on the way down: if rows landed in the heap partition
	// after the up ran, the rollback must not drop them.
	norm := strings.Join(strings.Fields(down), " ")
	if !strings.Contains(norm, "row_count > 0") {
		t.Errorf("migration 562 down must guard on non-empty partition before rollback")
	}

	// The load-bearing 2026_07 partition stays out of scope on the way down.
	for _, forbidden := range []string{
		"DETACH PARTITION public.request_logs_bodies_2026_07",
		"DROP TABLE public.request_logs_bodies_2026_07",
	} {
		if strings.Contains(down, forbidden) {
			t.Errorf("migration 562 down must not contain %q (2026_07 is load-bearing)", forbidden)
		}
	}
}

// TestTitlestoreTablesStayHeap pins the W2-10 audit conclusion: the
// titlestore tables (session_titles, session_title_states) are plain heap
// tables — they are not RANGE partitioned and are not registered with the
// PartitionManager promote pipeline. A future change that partitions them
// must update this test deliberately.
func TestTitlestoreTablesStayHeap(t *testing.T) {
	for _, name := range []string{
		"../../objects/tables/session_titles.sql",
		"../../objects/tables/session_title_states.sql",
	} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(data)
		upper := strings.ToUpper(body)
		if strings.Contains(upper, "PARTITION BY") {
			t.Errorf("%s: titlestore table must stay a plain heap table, found PARTITION BY", name)
		}
		if !strings.Contains(body, "CREATE TABLE") {
			t.Errorf("%s: expected a CREATE TABLE statement", name)
		}
	}
}
