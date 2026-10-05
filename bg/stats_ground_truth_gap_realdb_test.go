package bg

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This check asks a question none of the other 14 ask: not "is the data right"
// but "would you be able to know". The reconciliation compares usage_facts
// (ground truth) against stats_usage_daily (projection); if the ground truth
// does not cover the window, every projection row reconciles as a phantom and
// the diff backlog fills with artifacts. A saturated signal cannot answer
// "did my recorded cost drift", which is the only question it exists for.
//
// The load-bearing assertion here is the NEGATIVE control: a check that fires
// unconditionally would look exactly right in production (there is always a
// gap) and would be worthless. So the fixture must be driven into a state
// where the gap is closed and the check must then say nothing.

func statsGapFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	tables := []string{"usage_facts", "stats_usage_daily", "stats_reconciliation_diffs"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// Registered BEFORE any CREATE: a half-built fixture must not make the next
	// run skip on the safety gate and report a pass that never happened.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `
			DROP TABLE IF EXISTS public.stats_reconciliation_diffs;
			DROP TABLE IF EXISTS public.stats_usage_daily;
			DROP TABLE IF EXISTS public.usage_facts;`)
	})

	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.usage_facts (
		    event_id text, occurred_at timestamptz NOT NULL, cost_amount numeric);
		CREATE TABLE public.stats_usage_daily (
		    day_utc date NOT NULL, dimension_type text NOT NULL, cost_usd numeric);
		CREATE TABLE public.stats_reconciliation_diffs (
		    id bigserial PRIMARY KEY, resolution text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());`); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// 量具自证: without this, "the fixture did not get built" masquerades as
	// "the assertion went red", and the reader starts debugging the wrong thing.
	for _, obj := range tables {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c
			JOIN pg_namespace ns ON ns.oid = c.relnamespace
			WHERE ns.nspname='public' AND c.relname = $1`, obj).Scan(&n); err != nil {
			t.Fatalf("self-check %s: %v", obj, err)
		}
		if n != 1 {
			t.Fatalf("fixture self-check: public.%s is absent — assertions would fail for the wrong reason", obj)
		}
	}
}

type gapFinding struct{ key, name, detail string }

func runStatsGapCheck(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []gapFinding {
	t.Helper()
	def := healthCheckDefByID(t, "stats_ground_truth_gap")
	if !def.Optional {
		t.Error("this check must be Optional: usage_facts arrives with the stats inbox " +
			"migrations, and a fresh environment that has not applied them must not abort the health round")
	}
	rows, err := pool.Query(ctx, def.Query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var out []gapFinding
	for rows.Next() {
		var f gapFinding
		if err := rows.Scan(&f.key, &f.name, &f.detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func TestStatsGroundTruthGap(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	statsGapFixture(t, ctx, pool)

	const reset = `DELETE FROM public.usage_facts;
		DELETE FROM public.stats_usage_daily;
		DELETE FROM public.stats_reconciliation_diffs;`

	// ── phase 1: the real shape. Ground truth starts 5 days after the
	//    projection, and there is a backlog of unresolved diffs.
	//    Dates are ::date so the gap is exact: today minus 5 days is 5 days,
	//    not "6" — a 6-day interval difference rounded by date truncation is a
	//    test that fails for a reason unrelated to the check.
	if _, err := pool.Exec(ctx, reset+`
		INSERT INTO public.usage_facts (event_id, occurred_at, cost_amount)
		VALUES ('e1', now(), 1.0);
		INSERT INTO public.stats_usage_daily (day_utc, dimension_type, cost_usd) VALUES
			((now() - interval '5 days')::date, 'provider_model', 10.0),
			(now()::date,                     'provider_model', 10.0);
		INSERT INTO public.stats_reconciliation_diffs (resolution) VALUES ('open'), ('phantom_open');`); err != nil {
		t.Fatalf("seed phase 1: %v", err)
	}
	got := runStatsGapCheck(t, ctx, pool)
	if len(got) != 1 {
		t.Fatalf("phase 1: want exactly 1 finding (a 5-day ground-truth gap), got %d: %+v", len(got), got)
	}
	// The gap in days and the backlog size are the two things this check is
	// for; a detail that omits either cannot be acted on.
	//
	// ★ The day count is asserted WITH ITS LEADING ", so " — and that is load
	//   bearing, not decoration. An earlier version asserted only "5 day",
	//   which happily matches "-5 day(s)" too: the production run then
	//   reported **-48 day(s)** for a 48-day gap, and the judge stayed green.
	//   A substring assertion that cannot see a sign is not a weak assertion,
	//   it is no assertion.
	for _, want := range []string{", so 5 day(s)", "2 row"} {
		if !strings.Contains(got[0].detail, want) {
			t.Errorf("detail must state %q, got %q", want, got[0].detail)
		}
	}
	// Belt and braces against the sign: the detail must not carry a negative
	// day count at all, whatever else it says.
	if strings.Contains(got[0].detail, "-5 day") {
		t.Errorf("detail reports a negative gap: %q — the projection starts EARLIER "+
			"than the ground truth, so the subtraction must be facts-oldest minus "+
			"projection-oldest", got[0].detail)
	}
	// One row per gap: the key must identify the two window starts, otherwise
	// the health table UPSERTs on a key that changes every day and accumulates
	// history instead of refreshing one row.
	if !strings.Contains(got[0].key, "usage_facts") && !strings.Contains(got[0].key, "facts_start_") {
		t.Errorf("key %q must name the two window starts so the row refreshes instead of accumulating", got[0].key)
	}

	// ── phase 2 (NEGATIVE CONTROL): gap closed. A check that always fires is
	//    indistinguishable from a correct one in production, because there is
	//    always a gap somewhere. This phase is what gives phase 1 meaning.
	if _, err := pool.Exec(ctx, reset+`
		INSERT INTO public.usage_facts (event_id, occurred_at, cost_amount)
		VALUES ('e2', now(), 1.0);
		INSERT INTO public.stats_usage_daily (day_utc, dimension_type, cost_usd)
		VALUES (now()::date, 'provider_model', 10.0);
		INSERT INTO public.stats_reconciliation_diffs (resolution) VALUES ('open');`); err != nil {
		t.Fatalf("seed phase 2: %v", err)
	}
	if gap := runStatsGapCheck(t, ctx, pool); len(gap) != 0 {
		t.Errorf("phase 2 (negative control): ground truth starts on the same day as the "+
			"projection, so there is no gap and the check must report nothing; got %+v", gap)
	}

	// ── phase 3: ground truth completely empty. NULL must not turn into a
	//    finding that reads like "0 day(s) of window have no ground truth".
	//
	//    HONEST SCOPE: this phase passes, but it is NOT independently pinned
	//    by a mutation. Two single-predicate mutations (an "OR facts.oldest IS
	//    NULL" branch, and stripping one WHERE predicate) both stayed green,
	//    because "a < NULL" is NULL and the gap comparison already suppresses
	//    the row. Treat this as a backstop against a future rewrite of the
	//    query, not as a proven guard. Phases 1 and 2 are the pinned ones.
	if _, err := pool.Exec(ctx, reset+`
		INSERT INTO public.stats_usage_daily (day_utc, dimension_type, cost_usd)
		VALUES ((now() - interval '3 days')::date, 'provider_model', 10.0);`); err != nil {
		t.Fatalf("seed phase 3: %v", err)
	}
	if gap := runStatsGapCheck(t, ctx, pool); len(gap) != 0 {
		t.Errorf("phase 3: with usage_facts empty the check has nothing to compare and must "+
			"stay silent rather than emit a NULL-concatenated finding; got %+v", gap)
	}
}
