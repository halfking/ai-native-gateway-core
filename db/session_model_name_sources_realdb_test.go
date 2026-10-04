package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Session model-name sources (audit §9.169).
//
// §9.167 measured that the view's session leg renders `outbound_model` with a
// different value than v1 held on ~31% of twin rows, and concluded that the
// session family "normalises the model name". That conclusion was half right,
// and the half that was wrong is the half that would have broken production.
//
// What is actually true, measured on the local database 2026-10-04:
//
//   - `session_turns.model` is **100% populated on both storage faces**
//     (hot 669/669, parent 1,688,629/1,688,629). It is never NULL.
//   - `session_turns.raw_model_name` is **100% on the hot face and 0.44% on the
//     parent** (7,411 of 1,688,629), and every one of those 7,411 rows has
//     `ts >= 2026-10-01 07:25`. The column did not exist before then.
//   - Where `raw_model_name` exists it matches v1's `outbound_model` **100%**
//     (294/294 on the hot face). Where it does not, `t.model` is what the view
//     has always projected — and matches v1 on ~60% of recent parent rows.
//
// So the view's documented mapping `outbound_model ← model` (migration 710's
// header calls it 派生映射) was **correct when written** and became insufficient
// when the writer started emitting a better column a month later. It is not a
// typo, and it is not a data-quality defect.
//
// The consequence for the fix is the whole point of this file:
//
//	COALESCE(t.raw_model_name, t.model)   ← correct immediately on new rows,
//	                                        unchanged on rows that predate it
//	t.raw_model_name                      ← would NULL 1,688,218 parent rows
//
// I was one step from recommending the second form. A 24-hour sample said
// `raw_model_name` was never NULL — because every row in that sample came from
// the **hot face**, which holds 669 rows in total. The parent face holds
// 1.69 million and the column is essentially absent there. **A window that
// touches one storage face cannot speak for the other**, and §9.166.4 had
// already been bitten by a measurement that was too tight for the same reason.
//
// Hence the assertions below. The first one is the one that would have caught it:
// the fallback column must be populated everywhere, or COALESCE is not free.
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./db/ -run TestSessionModelNameSources -count=1 -v

func TestSessionModelNameSources(t *testing.T) {
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

	for _, face := range []string{"session_turns_hot", "session_turns"} {
		var total, modelNN, rawNN int
		if err := pool.QueryRow(ctx, `
			SELECT count(*), count(model), count(raw_model_name)
			FROM public.`+face).Scan(&total, &modelNN, &rawNN); err != nil {
			t.Skipf("%s not readable (%v)", face, err)
		}
		if total == 0 {
			t.Logf("%s: empty", face)
			continue
		}
		t.Logf("%-18s rows=%-9d model非空=%-9d raw_model_name非空=%-7d (%.2f%%)",
			face, total, modelNN, rawNN, 100*float64(rawNN)/float64(total))

		// The load-bearing assertion. `model` is the COALESCE fallback, so a NULL
		// anywhere in it would turn a one-expression fix into a data loss on the
		// rows that predate `raw_model_name`. This is the check that would have
		// caught the wrong recommendation.
		if modelNN != total {
			t.Errorf("%s: model is NULL on %d of %d rows — the session leg's outbound_model "+
				"falls back to this column, so it cannot be a blanket COALESCE fallback "+
				"any more; the fix needs a real source, not a wider one",
				face, total-modelNN, total)
		}

		// Reported, not asserted: this number is the thing a backfill would move,
		// and pinning it would make the gate a tripwire on a data job's progress
		// rather than a statement about the schema.
		if rawNN < total {
			var minTS *time.Time
			if err := pool.QueryRow(ctx,
				`SELECT min(ts) FROM public.`+face+` WHERE raw_model_name IS NOT NULL`).
				Scan(&minTS); err == nil && minTS != nil {
				t.Logf("%s: raw_model_name first appears at %s — rows before it have no "+
					"faithful source for outbound_model, only the model's own value",
					face, minTS.Format(time.RFC3339))
			}
		}
	}

	// The view must still be projecting the column §9.169 measured, or every
	// number above is describing a view that no longer exists.
	var proj string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_viewdef('public.request_logs_with_current_month'::regclass, true)`).
		Scan(&proj); err != nil {
		t.Skipf("canonical view not readable (%v)", err)
	}
	const want = "t.model AS outbound_model"
	if !containsFold(proj, want) {
		t.Errorf("canonical view no longer contains %q — §9.169's divergence measurement and the "+
			"COALESCE recommendation describe a projection that has since changed; re-measure "+
			"before acting on either", want)
	}
}

// containsFold is a case-insensitive substring check; the viewdef renders
// keywords in lower case but identifier quoting varies by PG version.
func containsFold(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexFold(haystack, needle) >= 0
}

func indexFold(h, n string) int {
	hl, nl := []rune(h), []rune(n)
	lower := func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	for i := 0; i+len(nl) <= len(hl); i++ {
		ok := true
		for j := range nl {
			if lower(hl[i+j]) != lower(nl[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}
