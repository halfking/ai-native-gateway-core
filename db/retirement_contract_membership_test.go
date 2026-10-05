package db

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Contract-membership gate (audit §9.166).
//
// The gap this pins: `request_logs` carries 157 columns, the canonical view
// projects 118. A reader may name a column that is real on the table being
// retired and absent from the view meant to replace it. RetirementExposureClassify
// used to fall through every branch to `baseline` for such a name, so
// RetirementRepointVerdictFor reported **repoint-safe** about a column the view
// does not have — the "a drifted safe entry is worse than no entry" failure that
// retirement_column_exposure.go's own header warns about, committed by that same
// file.
//
// The fix is a `not-in-contract` arm ahead of every other branch. These two tests
// are the teeth: the offline one pins the shape, the real-database one pins the
// actual measured name set, and between them they fail if the arm is removed.
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run 'TestRetirement(ExposureRejects|V1ColumnsOutside)' -count=1 -v

// v1ColumnsOutsideContractSample is a hand-picked sample of names that exist on
// `request_logs` but not in the canonical view, measured on the local database
// 2026-10-04.
//
// It is a **sample, not the set** — the real-DB test enumerates the full set and
// compares it. The sample exists so the offline test still has teeth with no
// database: a checker that only works when a DB is attached is a checker that
// silently does not run in CI.
//
// Deliberately includes `id`-adjacent and error-adjacent names: those are the
// ones a reader is most likely to reach for and the ones a name-similarity
// shortcut would wave through.
var v1ColumnsOutsideContractSample = []string{
	"task_id",              // v1 per-request task handle; session side has gw_task_id
	"session_title",        // v1 denormalised title
	"trace_events",         // explicitly NOT projected (§9.160 note in canonicalV2Comment)
	"upstream_endpoint",    // no session-side source at all
	"cache_hit",            // rollup-side name; absent from the canonical contract
	"audio_tokens",         // modality token counters the projection omits
	"reasoning_tokens",     //
	"provider_tokens",      //
	"video_tokens",         //
	"image_tokens",         //
	"context_size_tokens",  //
	"is_terminal",          // session side has turn_kind instead
	"request_depth",        //
	"node_switch_count",    //
	"vendor_metadata",      //
	"content_safety_score", //
}

// TestRetirementExposureRejectsColumnsOutsideTheContract is the offline gate: a
// name outside the contract must never classify as baseline and must never come
// out repoint-safe.
func TestRetirementExposureRejectsColumnsOutsideTheContract(t *testing.T) {
	for _, col := range v1ColumnsOutsideContractSample {
		if retirementContractSet[col] {
			t.Errorf("sample column %q IS in the canonical contract — the sample is stale, "+
				"and a stale sample makes this test assert nothing about the arm it is here to pin", col)
			continue
		}
		if got := RetirementExposureClassify(col); got != "not-in-contract" {
			t.Errorf("RetirementExposureClassify(%q) = %q, want %q — a column the view does "+
				"not carry must not fall through to baseline", col, got, "not-in-contract")
		}
		if got := RetirementRepointVerdictFor([]string{col}); got != RepointNoSuchColumn {
			t.Errorf("RetirementRepointVerdictFor([%q]) = %q, want %q — reporting safe here "+
				"would send a reader to a column that does not exist", col, got, RepointNoSuchColumn)
		}
	}

	// The general property, not just the sample: **no** name outside the contract
	// may be reported safe, whatever it is. Enumerating the sample alone would
	// pass even if the arm were narrowed to a hardcoded list of these 16 names.
	probes := append([]string{}, v1ColumnsOutsideContractSample...)
	probes = append(probes, "definitely_not_a_column", "", "SUCCESS", "Ts", "id_extra")
	for _, col := range probes {
		if retirementContractSet[col] {
			continue
		}
		if got := RetirementRepointVerdictFor([]string{col}); got == RepointSafe {
			t.Errorf("RetirementRepointVerdictFor([%q]) = repoint-safe for a name outside the "+
				"contract — the membership arm is not general", col)
		}
	}

	// Ranking: no-such-column must outrank empty, because a column the view lacks
	// does not "return nothing", it fails to parse. A reader that returns nothing
	// is debuggable by looking at its output; one that does not run is only
	// debuggable by reading the migration.
	if RetirementRepointVerdictFor([]string{"id", "upstream_endpoint"}) != RepointNoSuchColumn {
		t.Error("a real blocker plus a missing column must resolve to repoint-no-such-column, " +
			"so the harder failure is never hidden behind a softer one")
	}
}

// TestRetirementV1ColumnsOutsideCanonicalContract measures the actual gap between
// the v1 table and the view, and asserts the exposure classifier refuses every
// name in it.
//
// The count is logged rather than asserted: 157 vs 118 today, but the exact
// number moves as migrations land, and pinning it would make this test a
// tripwire for unrelated schema work. What must hold, and is asserted, is that
// the gap is non-empty (otherwise this test is measuring nothing) and that not
// one of its members is classified as servable.
func TestRetirementV1ColumnsOutsideCanonicalContract(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var v1Total, viewTotal int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM pg_attribute
		         WHERE attrelid='public.request_logs'::regclass AND attnum>0 AND NOT attisdropped),
		       (SELECT count(*) FROM pg_attribute
		        WHERE attrelid='public.request_logs_with_current_month'::regclass
		          AND attnum>0 AND NOT attisdropped)`).Scan(&v1Total, &viewTotal); err != nil {
		t.Skipf("canonical view or v1 table not readable (%v)", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT a.attname
		FROM pg_attribute a
		WHERE a.attrelid='public.request_logs'::regclass AND a.attnum>0 AND NOT a.attisdropped
		  AND a.attname NOT IN (SELECT attname FROM pg_attribute
		        WHERE attrelid='public.request_logs_with_current_month'::regclass
		          AND attnum>0 AND NOT attisdropped)
		ORDER BY a.attname`)
	if err != nil {
		t.Fatalf("enumerate out-of-contract columns: %v", err)
	}
	defer rows.Close()
	var outside []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		outside = append(outside, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(outside) == 0 {
		t.Skipf("v1 table has %d columns and the view has %d — no out-of-contract names on this "+
			"database, so this gate has nothing to measure here", v1Total, viewTotal)
	}
	t.Logf("request_logs=%d columns, canonical view=%d, outside contract=%d",
		v1Total, viewTotal, len(outside))

	// A name the Go contract does not carry must not be silently servable even if
	// the live view happens to be a different build than canonicalColumnOrderV2.
	// The Go contract is what repointing will be written against.
	for _, col := range outside {
		if retirementContractSet[col] {
			continue // the Go contract is stricter than this database's view; nothing to say
		}
		if got := RetirementExposureClassify(col); got == "baseline" {
			t.Errorf("live column %q is outside canonicalColumnOrderV2 but classified %q — "+
				"the membership arm is not doing its job", col, got)
		}
		if got := RetirementRepointVerdictFor([]string{col}); got == RepointSafe {
			t.Errorf("live column %q is outside the contract but judged repoint-safe", col)
		}
	}

	// The sample in the offline gate must still be a subset of reality. If a
	// migration projects one of these, the sample is stale and the offline gate
	// above has been asserting against a fiction.
	real := map[string]bool{}
	for _, c := range outside {
		real[c] = true
	}
	var stale []string
	for _, c := range v1ColumnsOutsideContractSample {
		if !real[c] {
			stale = append(stale, c)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Logf("NOTE: sample names no longer outside the contract on this database (%s) — "+
			"they may have been projected by a newer view build; the offline gate re-checks "+
			"this itself and will fail if so", strings.Join(stale, " "))
	}
}
