package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Audit §9.160: the canonical view's session-leg `request_status` arm for
// `rate_limited` keyed off `status_code = 429`, and `session_turns` has **zero**
// such rows — real rate-limited turns carry `status_code = 500` plus
// `error_kind = 'rate_limit_exceeded'`. So the arm was dead code and 437,402
// turns were reported as plain `failure`.
//
// These tests pin the fix. The offline half pins the *shape* of the expression
// that ships; the real-DB half evaluates that **exact expression text** against
// a real PostgreSQL so the classification claims are measurements rather than
// readings of the SQL.
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestRequestStatusProjection -count=1 -v

// TestRequestStatusProjectionArmOrder pins the branch order. Order is not
// cosmetic: the NULL-success guard must stay first (it is the pre-existing
// contract — `success IS NULL` yields a NULL label, not a guess), and the
// error_kind arm must precede the status_code arm so a row carrying both is
// still classified as rate_limited.
func TestRequestStatusProjectionArmOrder(t *testing.T) {
	expr := sessionRequestStatusExpr
	idx := func(needle string) int {
		i := strings.Index(expr, needle)
		if i < 0 {
			t.Fatalf("expression is missing the %q arm:\n%s", needle, expr)
		}
		return i
	}
	nullGuard := idx("WHEN t.success IS NULL THEN NULL")
	successArm := idx("WHEN t.success THEN 'success'")
	ekArm := idx("WHEN t.error_kind IN ('rate_limit_exceeded', 'key_throttled') THEN 'rate_limited'")
	codeArm := idx("WHEN t.status_code = 429 THEN 'rate_limited'")
	failureArm := idx("ELSE 'failure'")

	if !(nullGuard < successArm && successArm < ekArm && ekArm < codeArm && codeArm < failureArm) {
		t.Errorf("arm order is wrong; want success IS NULL → success → error_kind → status_code → failure.\n"+
			"got offsets: null=%d success=%d error_kind=%d status_code=%d failure=%d\n%s",
			nullGuard, successArm, ekArm, codeArm, failureArm, expr)
	}
	// The error_kind arm must carry the exact literals the write side produces:
	// EmitRateLimited emits exactly these two error_kind values (R41 F1 — a
	// single-value arm files every post-823 'key_throttled' mirror row under
	// 'failure' forever). A typo here is invisible offline and silently
	// degrades to "rate_limited = 0" in production.
	for _, lit := range []string{"'rate_limit_exceeded'", "'key_throttled'"} {
		if !strings.Contains(expr, lit) {
			t.Errorf("error_kind arm must carry the write-side literal %s:\n%s", lit, expr)
		}
	}
}

// TestRequestStatusProjection_RealDB evaluates the shipped expression text on a
// real PostgreSQL against the five row shapes that matter, and asserts the
// resulting labels. It deliberately does NOT build the full 118-column view
// chain (that is covered by TestRequestLogsViewV2EnsureMatchesMigration); what
// it isolates is the classification itself, which is the thing §9.160 got wrong.
func TestRequestStatusProjection_RealDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set — offline mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Fixture lives in its own schema on a dedicated pool. It is NOT dropped via
	// t.Cleanup on the main pool: this test never opens one, but the pool that
	// owns the schema is closed by a plain defer **before** any cleanup would
	// run — the 2026-10-04 lesson (a t.Cleanup reusing an already-closed pool
	// fails silently and leaves the schema behind in the real database).
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const schema = "rs160"
	if _, err := pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		t.Fatalf("drop stale fixture schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	// Fail-closed cleanup on an independent connection, then re-verify absence.
	// "Cleanup ran" and "cleanup actually removed the schema" are different
	// claims and only the second one matters.
	cleanup := func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		cconn, err := pgx.Connect(cctx, dsn)
		if err != nil {
			t.Errorf("cleanup: connect on independent connection: %v", err)
			return
		}
		defer cconn.Close(cctx)
		if _, err := cconn.Exec(cctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Errorf("cleanup: drop fixture schema: %v", err)
			return
		}
		var stillThere bool
		if err := cconn.QueryRow(cctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`, schema,
		).Scan(&stillThere); err != nil {
			t.Errorf("cleanup: re-verify: %v", err)
			return
		}
		if stillThere {
			t.Errorf("cleanup: schema %q still exists after DROP SCHEMA CASCADE", schema)
		}
	}
	t.Cleanup(cleanup)

	// A stand-in with exactly the three columns the expression touches. Using a
	// real table (not a VALUES list) keeps the planner's type inference for the
	// CASE identical to the view's.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE `+schema+`.t (
			id            int PRIMARY KEY,
			success       boolean,   -- nullable, like session_turns
			error_kind    text,
			status_code   integer
		)`); err != nil {
		t.Fatalf("create fixture table: %v", err)
	}

	// shapes: name → (success, error_kind, status_code, want label)
	// `want` is a *string because the NULL-success contract produces a NULL label.
	shapes := []struct {
		name      string
		success   any
		errorKind any
		code      any
		want      any
		why       string
	}{
		{"rate_limited", false, "rate_limit_exceeded", 500, "rate_limited",
			"the shape §9.160 is about: session side records 500, not 429"},
		{"rate_limited_no_twin", false, "rate_limit_exceeded", 200, "rate_limited",
			"42,788 turns carry this error_kind with no v1 twin; they are still rate-limited"},
		{"key_throttled", false, "key_throttled", 500, "rate_limited",
			"handler.go:2376 emits this error_kind with the rate_limited label; a single-value arm files it under failure (R41 F1)"},
		{"upstream_failure", false, "provider_error", 502, "failure",
			"a genuine upstream failure must NOT be swept into rate_limited"},
		{"probe_429", false, nil, 429, "rate_limited",
			"the pre-existing status_code arm is kept — it is dead today but not wrong"},
		{"success", true, nil, 200, "success",
			"success must stay success even though it has a non-429 code"},
		{"in_flight", nil, nil, nil, nil,
			"success IS NULL must yield a NULL label, not a guess (pre-existing contract)"},
	}
	for i, s := range shapes {
		if _, err := pool.Exec(ctx,
			`INSERT INTO `+schema+`.t (id, success, error_kind, status_code) VALUES ($1,$2,$3,$4)`,
			i+1, s.success, s.errorKind, s.code); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}

	// Evaluate the shipped expression itself — not a re-typed copy. A copy
	// would let the test pass while the shipping constant regressed.
	q := `SELECT id, success, error_kind, status_code, ` + sessionRequestStatusExpr + ` AS label
	        FROM ` + schema + `.t ORDER BY id`
	rows, err := pool.Query(ctx, q)
	if err != nil {
		t.Fatalf("evaluate shipped expression: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var (
			id                  int
			success             *bool
			errorKind, code, lb *string
		)
		if err := rows.Scan(&id, &success, &errorKind, &code, &lb); err != nil {
			t.Fatalf("scan: %v", err)
		}
		s := shapes[id-1]
		seen++
		if lb == nil {
			if s.want != nil {
				t.Errorf("%s: got NULL label, want %q (%s)", s.name, s.want, s.why)
			}
			continue
		}
		if s.want == nil {
			t.Errorf("%s: got label %q, want NULL (%s)", s.name, *lb, s.why)
			continue
		}
		if *lb != s.want.(string) {
			t.Errorf("%s: got %q, want %q (%s)", s.name, *lb, s.want, s.why)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if seen != len(shapes) {
		t.Fatalf("evaluated %d rows, want %d — the fixture itself is wrong, so a green result here would be meaningless", seen, len(shapes))
	}
}

// TestRequestStatusProjectionMatchesShippedMigration pins that migration 824
// and the Go mirror carry the *same* expression. Without this, one side can be
// fixed while the other keeps the dead arm — and which one a given database
// gets depends on whether it was installed by the installer or self-healed at
// startup, which is invisible at runtime.
func TestRequestStatusProjectionMatchesShippedMigration(t *testing.T) {
	raw, err := os.ReadFile("../sql/migrations/startup/824_request_status_rate_limited_projection.sql")
	if err != nil {
		t.Fatalf("read migration 824: %v", err)
	}
	mig := string(raw)
	if !strings.Contains(mig, sessionRequestStatusExpr) {
		t.Errorf("migration 824 does not carry the Go expression verbatim.\n  go: %s\n"+
			"Either the migration or the mirror was edited alone; the two must ship together "+
			"(installer-applied vs startup-self-healed databases would otherwise diverge).",
			sessionRequestStatusExpr)
	}
	// …and the old dead arm must be gone from 824's **projection block**. It is
	// still legal in the down file, and it is deliberately *quoted in the header
	// comment* to document the defect — so this check must be scoped to the
	// `$proj$` block, not the whole file. Scoping to the file is the classic
	// "the document refutes its own guard" failure: the prose explaining the
	// bug reads exactly like the bug.
	projStart := strings.Index(mig, "proj := $proj$")
	projEnd := strings.Index(mig, "$proj$;")
	if projStart < 0 || projEnd <= projStart {
		t.Fatal("migration 824 no longer carries a $proj$ block; update this test")
	}
	proj := mig[projStart:projEnd]
	// The whole frozen expression must be absent — **not** its tail.
	//
	// The naive form of this check ("WHEN t.status_code = 429 THEN
	// 'rate_limited' ELSE 'failure' END" is gone) can never fire: 824's
	// expression *ends with exactly that text*, because the new arm is inserted
	// before the retained status_code arm. A superseding expression contains the
	// superseded one as a suffix, so a substring guard on the tail is
	// structurally unsatisfiable. Compare the full expression instead — with
	// the error_kind arm in the middle, the old string is not a substring of the
	// new one.
	ov, ok := registeredExpressionOverrides["request_status"]
	if !ok {
		t.Fatal("registeredExpressionOverrides has no request_status entry; the frozen expression must " +
			"come from that registry, not be re-typed here — a second copy is a second thing to drift")
	}
	if strings.Contains(proj, ov.frozen) {
		t.Errorf("migration 824's $proj$ block still contains the pre-824 (dead-arm) projection shape:\n%s", ov.frozen)
	}
	if !strings.Contains(proj, "t.error_kind IN ('rate_limit_exceeded', 'key_throttled')") {
		t.Error("migration 824's $proj$ block is missing the error_kind IN-set arm (write side emits both literals; R41 F1)")
	}
}
