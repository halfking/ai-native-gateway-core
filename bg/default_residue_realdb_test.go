package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// D8-d (audit §9.152 / §9.153): the default-partition residue selector must
// report exactly one shape and nothing else.
//
//   - A parent WITH dedicated non-default children  → its `*_default` is a
//     misroute target: a row there means that month's partition can no longer
//     be created. MUST be selected.
//   - A parent whose ONLY partition is its default (stats_event_inbox,
//     system_probe_runs) → default is the design. MUST NOT be selected, or
//     every tick reports healthy data as residue.
//
// The second half matters as much as the first: §9.153.2 found both of those
// tables non-empty, so a selector that ignores the "has siblings" condition
// would emit a permanent false positive on production data.
func TestDefaultResidueTargets_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer pool.Close()

	const schema = "d8d_residue_probe"
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		bg, off := context.WithTimeout(context.Background(), 30*time.Second)
		defer off()
		_, _ = pool.Exec(bg, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	// (1) suspect: parent with a dedicated monthly sibling + a default.
	//     A row for an uncovered month lands in the default, silently.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE `+schema+`.suspect (id bigserial, ts timestamptz NOT NULL) PARTITION BY RANGE (ts);
		CREATE TABLE `+schema+`.suspect_2026_03 PARTITION OF `+schema+`.suspect
			FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
		CREATE TABLE `+schema+`.suspect_default PARTITION OF `+schema+`.suspect DEFAULT;
		INSERT INTO `+schema+`.suspect (ts) VALUES ('2026-05-15 10:00+08');
	`); err != nil {
		t.Fatalf("build suspect fixture: %v", err)
	}

	// (2) benign: default is the ONLY partition. Non-empty, by design.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE `+schema+`.benign (id bigserial, ts timestamptz NOT NULL) PARTITION BY RANGE (ts);
		CREATE TABLE `+schema+`.benign_default PARTITION OF `+schema+`.benign DEFAULT;
		INSERT INTO `+schema+`.benign (ts) VALUES ('2026-05-15 10:00+08');
	`); err != nil {
		t.Fatalf("build benign fixture: %v", err)
	}

	// (3) a clean sibling-bearing table: must be selected (it is a valid
	//     target) but must count zero — selection != residue.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE `+schema+`.clean (id bigserial, ts timestamptz NOT NULL) PARTITION BY RANGE (ts);
		CREATE TABLE `+schema+`.clean_2026_03 PARTITION OF `+schema+`.clean
			FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
		CREATE TABLE `+schema+`.clean_default PARTITION OF `+schema+`.clean DEFAULT;
	`); err != nil {
		t.Fatalf("build clean fixture: %v", err)
	}

	got, err := defaultResidueTargetsInSchema(ctx, pool, schema)
	if err != nil {
		t.Fatalf("defaultResidueTargetsInSchema: %v", err)
	}
	gotSet := map[string]bool{}
	for _, g := range got {
		gotSet[g] = true
	}

	if !gotSet["suspect_default"] {
		t.Errorf("selector missed suspect_default: a row there self-locks the "+
			"2026_05 partition (audit §9.152). got=%v", got)
	}
	if gotSet["benign_default"] {
		t.Errorf("selector reported benign_default: that table's default is its "+
			"ONLY partition, so a non-empty default is the design, not residue "+
			"(stats_event_inbox / system_probe_runs shape, audit §9.153.2). got=%v", got)
	}
	if !gotSet["clean_default"] {
		t.Errorf("selector missed clean_default: a sibling-bearing default is a "+
			"valid target even while empty — selection must not depend on rows. got=%v", got)
	}
	if len(got) != 2 {
		t.Errorf("selector returned %d tables, want exactly 2 (suspect_default, clean_default): %v", len(got), got)
	}

	// The row must actually be reachable by a plain count — otherwise the
	// check would report 0 forever and the gate above would be vacuous.
	var n int64
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM "+pgxIdent(schema, "suspect_default")).Scan(&n); err != nil {
		t.Fatalf("count suspect_default: %v", err)
	}
	if n != 1 {
		t.Errorf("suspect_default has %d rows, want 1 — the fixture no longer "+
			"reproduces the audit §9.152 shape, so this test proves nothing", n)
	}
}

// TestDefaultResidueTargets_ProductionIsClean pins the current baseline: on the
// audit database every selected `*_default` partition is empty (audit §9.153.1
// measured 16/16 by exact count). A non-empty result here means either the
// self-lock has started or something changed the schema — both need a human.
func TestDefaultResidueTargets_ProductionIsClean(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	defer pool.Close()

	targets, err := defaultResidueTargets(ctx, pool)
	if err != nil {
		t.Fatalf("defaultResidueTargets: %v", err)
	}
	if len(targets) == 0 {
		t.Fatal("no `*_default` partitions selected in public — the selector " +
			"silently stopped matching (a LIKE/relkind change would do this)")
	}
	for _, tbl := range targets {
		var n int64
		if err := pool.QueryRow(ctx,
			"SELECT count(*) FROM "+pgxIdent("public", tbl)).Scan(&n); err != nil {
			t.Errorf("count %s: %v", tbl, err)
			continue
		}
		if n > 0 {
			t.Errorf("public.%s holds %d rows — the monthly/daily partition for "+
				"those rows can no longer be created (audit §9.152). Baseline was "+
				"0/16; investigate before shipping.", tbl, n)
		}
	}
}

// TestPgxIdentQuotes covers the injection-safety contract of the identifier
// helper: names are interpolated, not parameterised, because pgx rejects a
// bind parameter in a FROM position.
//
// pgx.Identifier.Sanitize always emits the fully quoted form, even for names
// that need no quoting. That is intentional here — the helper is fed
// catalog-derived identifiers, and a uniform quoted form has no "is this name
// safe?" branch to get wrong.
func TestPgxIdentQuotes(t *testing.T) {
	cases := map[string]struct{ schema, table, want string }{
		"plain":     {"public", "session_turns_default", `"public"."session_turns_default"`},
		"uppercase": {"Public", "T", `"Public"."T"`},
		"injection": {"public", `x"; DROP TABLE y; --`, `"public"."x""; DROP TABLE y; --"`},
	}
	for name, c := range cases {
		if got := pgxIdent(c.schema, c.table); got != c.want {
			t.Errorf("%s: pgxIdent(%q,%q) = %q, want %q", name, c.schema, c.table, got, c.want)
		}
	}
}
