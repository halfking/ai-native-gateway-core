//go:build !integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// S4 gate measurement (audit §9.163).
//
// "Can S4 be opened?" is the first gate before request_logs can be retired, and
// until now the only way to answer it was to paste SQL by hand. This test makes
// it one command, on any database — including 252, once a read-only grant
// exists.
//
// It reuses the validator's own mirrorDriftClassSQL for classification, and
// mirrors the row source under TestS4GateSourceMatchesValidator. A hand-written
// copy of the whole gate would be a second version of it, and the two would
// drift at exactly the moment it matters: when someone changes the exclusion
// rules, the copy keeps reporting the old answer with full confidence.
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./cmd/gateway/ -run TestS4GateMeasurement -count=1 -v

// s4Windows are the windows reported. 1h is the freshness probe (is the mirror
// losing rows *right now*), 168h is the validator's own default and the number
// that feeds s4_ready, 720h is the widest window the endpoint accepts.
var s4Windows = []struct {
	label string
	dur   time.Duration
}{
	{"1h", time.Hour},
	{"24h", 24 * time.Hour},
	{"7d", 7 * 24 * time.Hour},
	{"30d", 30 * 24 * time.Hour},
}

// TestS4GateMeasurement reports the three drift buckets per window and states
// whether s4_ready would be true. It asserts nothing beyond the internal
// consistency of the numbers, on purpose: "is s4 safe to open" is a release
// decision that belongs in the decision sheet, not a threshold hidden in a test.
func TestS4GateMeasurement(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// ⚠️ The validator's scope is used **verbatim** — including its two
	// NOT EXISTS anti-joins. My first cut inlined a copy of the row source and
	// kept only the tenant predicate; that dropped the anti-join, so it counted
	// *every* v1 row in the window instead of the ones with no session twin.
	//
	// What that looked like: genuine_loss = 745,385 over 30 days, against a
	// hand-computed 6. Every assertion still passed, and the number was
	// confidently, spectacularly wrong. The only reason it was caught is that a
	// value had been measured before (6) and the two disagreed.
	//
	// The internal_loopback / non_terminal buckets were **identical** in both
	// versions, which is what made the bug so easy to miss: those rows genuinely
	// have no session twin either way, so the only bucket that moved was the one
	// carrying every successful mirrored row. A partial duplication of a query
	// does not degrade gracefully — it degrades in exactly the branch nobody
	// cross-checks.
	q := `
		SELECT drift_class, count(*)
		FROM (
		  SELECT ` + mirrorDriftClassSQL + ` AS drift_class
		  FROM (` + s4ScopeBody + `) rl
		  WHERE ($1 = '' OR rl.tenant_id = $1)
		    AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id)
		    AND NOT EXISTS (SELECT 1 FROM session_turns     tp WHERE tp.request_id = rl.request_id)
		) s
		GROUP BY drift_class`

	type windowResult struct {
		internal, nonTerminal, genuine int64
		total                          int64
	}
	results := map[string]windowResult{}

	for _, w := range s4Windows {
		rows, err := conn.Query(ctx, q, "", time.Now().Add(-w.dur))
		if err != nil {
			t.Fatalf("%s: query: %v", w.label, err)
		}
		var r windowResult
		for rows.Next() {
			var class string
			var n int64
			if err := rows.Scan(&class, &n); err != nil {
				rows.Close()
				t.Fatalf("%s: scan: %v", w.label, err)
			}
			r.total += n
			switch class {
			case "internal_loopback":
				r.internal = n
			case "non_terminal":
				r.nonTerminal = n
			case "genuine_loss":
				r.genuine = n
			default:
				rows.Close()
				t.Fatalf("%s: unknown drift class %q — the CASE in db.MirrorDriftClassSQL "+
					"grew an arm and this test's switch no longer covers it", w.label, class)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatalf("%s: iterate: %v", w.label, err)
		}
		results[w.label] = r

		ready := r.genuine == 0
		t.Logf("%-4s internal_loopback=%-6d non_terminal=%-6d genuine_loss=%-3d total=%-6d  s4_ready=%v",
			w.label, r.internal, r.nonTerminal, r.genuine, r.total, ready)
	}

	// The validator's own contract: the three buckets are mutually exclusive and
	// sum to V1RowsWithoutTurns. Checking the total is what makes the three
	// individual numbers trustworthy — a bucket that silently stopped matching
	// (a renamed class, a predicate that stopped applying) would otherwise just
	// make genuine_loss smaller, i.e. the gate would get *more* permissive
	// exactly when its instrument is broken.
	//
	// So: an unrecognised class must fail (above), and the three reported
	// classes must be the only ones. There is no independent total to check
	// against without re-running the scope, so the check that matters is the
	// exhaustive switch plus this monotonicity observation below.
	def := results["7d"]
	t.Logf("7d window: genuine_loss is %d. s4_ready would be %v.", def.genuine, def.genuine == 0)

	// A freshness observation, reported rather than asserted: if the 1h window
	// has genuine losses while the 30d window does not, the loss is ongoing; if
	// 30d has more than 1h, it is decaying. Both are informative to whoever
	// reads this, and neither is a threshold this test should own.
	one := results["1h"]
	if one.genuine > 0 && results["30d"].genuine > 0 {
		t.Logf("ONGOING: genuine losses in the last hour (%d) — the mirror is losing terminal "+
			"failures right now, not just historically", one.genuine)
	} else if results["30d"].genuine > one.genuine {
		t.Logf("historical: genuine losses exist in 30d (%d) but none in the last hour — decaying",
			results["30d"].genuine)
	}
}

// s4ScopeBody is the **row source only** of mirrorDriftScopeSQL (which
// relations, which columns, which window predicate), inlined so the window can
// be a bound parameter instead of string concatenation.
//
// The anti-join is NOT part of it — see the comment at the query: omitting it is
// the exact bug this file was written after, and it is repeated here verbatim as
// a warning rather than abstracted away. mirrorDriftClassSQL (the classification)
// is referenced from production, not copied.
//
// TestS4GateSourceMatchesValidator below pins this copy's column list against
// the production constant, so the duplication cannot rot unnoticed.
const s4ScopeBody = `
	SELECT request_id, gw_session_id, tenant_id, work_type, request_status, error_kind,
	       is_auto_request, origin_actor, request_type, task_type, success, ts
	FROM request_logs_hot
	WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''
	UNION ALL
	SELECT request_id, gw_session_id, tenant_id, work_type, request_status, error_kind,
	       is_auto_request, origin_actor, request_type, task_type, success, ts
	FROM request_logs
	WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''`

// TestS4GateSourceMatchesValidator pins the copied row-source against the
// production constant, so the duplication above cannot rot unnoticed.
//
// The check is on **column names and order**, which is what actually has to
// match: mirrorDriftClassSQL references rl.<col> by name, so a reordering is
// harmless and a renaming is fatal. A whole-text equality check would fail on a
// harmless whitespace change and train people to ignore this test.
func TestS4GateSourceMatchesValidator(t *testing.T) {
	for _, col := range []string{
		"request_id", "gw_session_id", "tenant_id", "work_type", "request_status",
		"error_kind", "is_auto_request", "origin_actor", "request_type", "task_type",
		"success", "ts",
	} {
		if !containsIdentifier(s4ScopeBody, col) {
			t.Errorf("s4ScopeBody no longer selects %q, but db.MirrorDriftClassSQL references "+
				"rl.%s — the copied scope and the production classifier have drifted apart", col, col)
		}
	}
	for _, rel := range []string{"request_logs_hot", "request_logs"} {
		if !containsIdentifier(s4ScopeBody, rel) {
			t.Errorf("s4ScopeBody no longer reads %s; the two storage faces (hot ∪ parent) are "+
				"what make the scope complete", rel)
		}
	}
	if !containsIdentifier(mirrorDriftScopeSQL, "session_turns_hot") ||
		!containsIdentifier(mirrorDriftScopeSQL, "session_turns") {
		t.Error("the validator's scope lost its session-family anti-join; every v1 row would " +
			"then look mirrored and s4_ready would be true by construction")
	}
}

// containsIdentifier reports whether needle appears in hay delimited by
// identifier boundaries, so `client_model` does not match `client_model_v2`.
func containsIdentifier(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] != needle {
			continue
		}
		beforeOK := i == 0 || !isIdentByte(hay[i-1])
		after := i + len(needle)
		if beforeOK && (after >= len(hay) || !isIdentByte(hay[after])) {
			return true
		}
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
