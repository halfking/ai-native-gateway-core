package db

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repoint value-fidelity gate (audit §9.166).
//
// §9.165 graded the three `repoint-safe` readers (admin/swim_lane_init.go,
// bg/model_probe.go, bg/today_success_probe.go) by **column fill rate**: the
// columns they need are at v1 parity on the session side, so switching sources
// costs nothing. Fill rate answers "will the column be there", not "will the
// value be the same" — and those are different questions, because the view does
// not carry the v1 row for a row that has a session twin: it anti-joins it and
// emits the **session leg's** value instead (db/request_logs_view_schema.go, the
// `NOT EXISTS … session_turns_hot … session_turns` guard).
//
// So the real question is conditional on having a twin, and it has three parts:
//
//  1. coverage  — is every v1 row in the window present in the view at all?
//  2. agreement — where a twin exists, is the value the view exposes the value
//     v1 held?
//  3. cast hazard — the view normalises the session side's TEXT credential_id
//     with `CASE WHEN t.credential_id ~ '^[0-9]+$' THEN t.credential_id::bigint
//     END`, which silently NULLs any non-numeric id. A twin carrying one would
//     make a credential-filtered reader drop that row while still looking fine.
//
// Measured 2026-10-04 on the local database, 24h window: 616 v1 success rows,
// 255 with a session twin, 0 absent from the view, 0 value mismatches, 0 cast
// hazards. Rows without a twin ride the v1 leg verbatim, so they are covered by
// (1) rather than by (2).
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestRepointValueFidelity -count=1 -v

// repointFidelityColumns are the columns the three repoint-safe readers select
// or filter on, which is what this gate certifies. The list is pinned against
// the source files by TestRepointFidelityColumnsMatchTheReaders below, so it
// cannot quietly stop describing them.
//
// `success` is included because all three filter on it
// (`WHERE rl.success` / `rl.success = TRUE`): a twin whose `success` disagrees
// with v1's would move a row across the filter boundary, which is the single
// most consequential way a repoint can change a result set.
var repointFidelityColumns = []string{
	"success", "credential_id", "client_model", "outbound_model", "ts",
}

// repointSafeReaders are the files §9.165 graded `repoint-safe`. Named here so
// the column list above cannot drift away from the thing it certifies.
var repointSafeReaders = []string{
	"admin/swim_lane_init.go",
	"bg/model_probe.go",
	"bg/today_success_probe.go",
}

// TestRepointFidelityColumnsMatchTheReaders is the anti-drift check: every
// certified column must actually appear in at least one of the three readers.
//
// Without it, deleting a query from a reader would leave this gate still
// measuring a column nobody depends on — a green result that certifies nothing.
// Same failure shape as "a validator that no longer validates", and the fix is
// the same: make the vacuous state fail rather than pass quietly.
func TestRepointFidelityColumnsMatchTheReaders(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(file))

	var blob strings.Builder
	found := 0
	for _, rel := range repointSafeReaders {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		blob.Write(b)
		found++
	}
	if found != len(repointSafeReaders) {
		t.Fatalf("expected %d readers, read %d", len(repointSafeReaders), found)
	}
	src := blob.String()

	// `ts` is checked on the view's own column name too: these readers qualify it
	// as rl.ts, and the gate's window predicate uses the bare name.
	for _, col := range repointFidelityColumns {
		if !strings.Contains(src, col) {
			t.Errorf("repointFidelityColumns names %q, which appears in none of the %d "+
				"repoint-safe readers — either the reader changed or the certified column "+
				"set is stale; both mean this gate is measuring the wrong thing", col, found)
		}
	}
	t.Logf("certified %d columns against %d readers", len(repointFidelityColumns), found)
}

// TestRepointValueFidelity measures the three properties above against a real
// database.
//
// It asserts zero mismatches rather than a rate, because a mismatch here is not
// noise: one row whose twin disagrees is one row a repointed reader silently
// loses. The paired log line is what makes the run interpretable — a run with
// twins = 0 has measured nothing, and is reported as a skip rather than a pass.
func TestRepointValueFidelity(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var v1Total int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.request_logs_hot`).Scan(&v1Total); err != nil {
		t.Skipf("request_logs_hot not readable (%v) — v1 already retired here", err)
	}
	if v1Total == 0 {
		t.Skip("request_logs_hot is empty — nothing to certify")
	}

	const window = "24 hours"

	// (1) coverage: every v1 success row in the window must be in the view.
	// `request_id` is the join key the view's own anti-join uses, so this is the
	// same identity the view reasons about — not an approximation of it.
	var v1Rows, present, absent int64
	if err := pool.QueryRow(ctx, `
		WITH v1 AS (
		    SELECT request_id FROM public.request_logs_hot
		    WHERE ts >= now() - interval '`+window+`' AND success = TRUE
		)
		SELECT count(*),
		       (SELECT count(*) FROM v1 JOIN public.request_logs_with_current_month v
		                          USING (request_id) WHERE v.ts >= now() - interval '`+window+`'),
		       (SELECT count(*) FROM v1 WHERE NOT EXISTS (
		            SELECT 1 FROM public.request_logs_with_current_month v
		            WHERE v.request_id = v1.request_id
		              AND v.ts >= now() - interval '`+window+`'))
		FROM v1`).Scan(&v1Rows, &present, &absent); err != nil {
		t.Fatalf("coverage: %v", err)
	}

	// (2)+(3) twin agreement. The LATERAL reproduces the view's own twin lookup
	// — hot first, then the partitioned parent — and the credential cast
	// reproduces the view's `~ '^[0-9]+$' THEN ::bigint` normalisation. Copying
	// a query locally does not degrade gracefully: an earlier version of this
	// area dropped the anti-join and reported 745,385 losses instead of 6
	// (audit §9.163), so each fragment here is here for a stated reason.
	var twins, castHazard, credMismatch, clientMismatch, outboundMismatch, successMismatch int64
	if err := pool.QueryRow(ctx, `
		WITH v1 AS (
		    SELECT request_id, success, credential_id, client_model, outbound_model
		    FROM public.request_logs_hot
		    WHERE ts >= now() - interval '`+window+`' AND success = TRUE
		), tw AS (
		    SELECT v1.request_id, v1.success AS v1_success, v1.credential_id AS v1_cred,
		           v1.client_model AS v1_client, v1.outbound_model AS v1_outbound,
		           t.credential_id AS raw_cred, t.client_model, t.outbound_model, t.success
		    FROM v1 JOIN LATERAL (
		        SELECT credential_id, client_model, outbound_model, success
		        FROM public.session_turns_hot WHERE request_id = v1.request_id
		        UNION ALL
		        SELECT credential_id, client_model, outbound_model, success
		        FROM public.session_turns     WHERE request_id = v1.request_id
		    ) t ON TRUE
		)
		SELECT count(*),
		       count(*) FILTER (WHERE raw_cred IS NOT NULL AND raw_cred !~ '^[0-9]+$'),
		       count(*) FILTER (WHERE v1_cred IS DISTINCT FROM
		                       CASE WHEN raw_cred ~ '^[0-9]+$' THEN raw_cred::bigint END),
		       count(*) FILTER (WHERE v1_client IS DISTINCT FROM client_model),
		       count(*) FILTER (WHERE v1_outbound IS DISTINCT FROM outbound_model),
		       count(*) FILTER (WHERE v1_success IS DISTINCT FROM success)
		FROM tw`).Scan(&twins, &castHazard, &credMismatch, &clientMismatch,
		&outboundMismatch, &successMismatch); err != nil {
		t.Fatalf("twin agreement: %v", err)
	}

	t.Logf("window=%s v1_success_rows=%d present_in_view=%d absent=%d | twins=%d "+
		"cast_hazard=%d cred_mismatch=%d client_model_mismatch=%d outbound_model_mismatch=%d "+
		"success_mismatch=%d",
		window, v1Rows, present, absent, twins, castHazard, credMismatch,
		clientMismatch, outboundMismatch, successMismatch)

	// Cross-check the twin **count** over a different code path, before believing
	// any of the agreement numbers.
	//
	// The agreement assertions below all read `twins`, and every one of them is
	// satisfied by an empty set. So if the LATERAL silently loses an arm — drop
	// the hot face and it still finds the parent rows, drop the anti-join and it
	// over-counts — the mismatch counts stay 0 and the gate stays green while
	// having measured less than it claims. That is precisely the failure this
	// area already made once: §9.163's first version dropped two NOT EXISTS
	// anti-joins and reported 745,385 losses where the truth was 6, with two
	// neighbouring buckets unchanged.
	//
	// EXISTS is a different formulation of the same question, so agreement between
	// the two is evidence and disagreement is a red test, not a mystery.
	var expectedTwins int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs_hot v
		WHERE v.ts >= now() - interval '`+window+`' AND v.success = TRUE
		  AND (EXISTS (SELECT 1 FROM public.session_turns_hot t WHERE t.request_id = v.request_id)
		    OR EXISTS (SELECT 1 FROM public.session_turns     t WHERE t.request_id = v.request_id))`,
	).Scan(&expectedTwins); err != nil {
		t.Fatalf("twin count cross-check: %v", err)
	}
	if twins != expectedTwins {
		t.Errorf("LATERAL found %d twins but EXISTS counts %d — the twin lookup in this gate "+
			"does not cover the same rows as an independent formulation of the same question, "+
			"so its agreement numbers describe a smaller set than the log line claims",
			twins, expectedTwins)
	}

	if v1Rows == 0 {
		t.Skipf("no v1 success rows in the last %s — measured nothing", window)
	}
	if absent != 0 {
		t.Errorf("%d of %d v1 success rows in the last %s are absent from the canonical view; "+
			"repointing any recent-window reader would drop them", absent, v1Rows, window)
	}

	if twins == 0 {
		// Reported as a skip, not a pass. A green run that measured zero twins
		// is indistinguishable from a green run that found agreement, and only
		// one of those is evidence.
		t.Skipf("0 of %d v1 success rows in the last %s have a session twin — the value-agreement "+
			"half of this gate measured nothing (the coverage half above did run)", v1Rows, window)
	}
	if castHazard != 0 {
		t.Errorf("%d session twins carry a non-numeric credential_id; the view's "+
			"'^[0-9]+$' THEN ::bigint normalisation NULLs those, so a credential-filtered "+
			"reader would drop rows it currently returns", castHazard)
	}
	for _, m := range []struct {
		name string
		n    int64
	}{
		{"credential_id", credMismatch},
		{"client_model", clientMismatch},
		{"outbound_model", outboundMismatch},
		{"success", successMismatch},
	} {
		if m.n != 0 {
			t.Errorf("%d of %d session twins disagree with v1 on %s — repointing makes the "+
				"view emit the session value, so these rows change meaning for the reader",
				m.n, twins, m.name)
		}
	}
}
