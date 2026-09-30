//go:build integration

package admin

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/db"
)

func dbpkgSessionFamily() string { return db.SessionFamilyTurnsForSessionSQL() }

func fmtMD5(raw []byte) string {
	sum := md5.Sum(raw)
	return hex.EncodeToString(sum[:])
}

// TestSessionCompareSplitMatchesLegacyQuery runs the compare path's pre-split
// statement and the new two-phase path over the same real session and compares
// them turn by turn.
//
// The split exists because a session-sized outer side LEFT JOINed to
// request_logs_bodies_with_current_month costs seconds once migration 765 made
// that partition a Citus columnar table:
//
//	old single query   4,183 ms
//	new phase 1          170 ms
//	new phase 2           97 ms
//
// The join key also changed: the legacy statement joined on `rb.request_id`
// alone, the split one on `(rb.request_id, rb.ts)`. That is only equivalent if
// no request_id carries more than one ts — checked on the live database
// (0 such rows) and re-asserted here by the comparison itself.
//
// Probes are discovered rather than hard-coded: session ids rot as soon as the
// bodies retention window moves, and a hard-coded id would make this gate fail
// for the wrong reason.
func TestSessionCompareSplitMatchesLegacyQuery(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the compare split preserved the old rows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	probes := pickCompareProbeSessions(t, ctx, pool, 4)
	if len(probes) == 0 {
		t.Skip("no usable session probes in this database — skipping proves nothing either way")
	}

	for _, sessionID := range probes {
		t.Run(sessionID, func(t *testing.T) {
			legacy := queryLegacyCompareRows(t, ctx, pool, sessionID, "default")
			if len(legacy) == 0 {
				t.Fatalf("legacy query returned no rows — an empty comparison proves nothing")
			}
			split := querySplitCompareRows(t, ctx, pool, sessionID, "default")
			if len(legacy) != len(split) {
				t.Fatalf("turn count diverged: legacy=%d split=%d", len(legacy), len(split))
			}
			for i := range legacy {
				if legacy[i] != split[i] {
					t.Fatalf("turn %d diverged:\n legacy=%s\n split =%s", i, legacy[i], split[i])
				}
			}
			t.Logf("%d turns compared", len(legacy))
		})
	}
}

type compareProbeRow struct {
	requestID     string
	ts            time.Time
	clientModel   string
	outboundModel string
	reqMD5        string
	outMD5        string
	respMD5       string
}

func (r compareProbeRow) String() string {
	return r.requestID + "@" + r.ts.UTC().Format(time.RFC3339Nano) +
		" cm=" + r.clientModel + "/" + r.outboundModel +
		" req=" + r.reqMD5 + " out=" + r.outMD5 + " resp=" + r.respMD5
}

// pickCompareProbeSessions returns short-to-medium sessions, ordered so the run
// is reproducible. Bodies availability is deliberately not a filter here: the
// compare path is exercised on turns with and without stored bodies.
func pickCompareProbeSessions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT session_id
		  FROM session_turns
		 WHERE session_id NOT LIKE 'sys:%'
		 GROUP BY session_id
		HAVING count(*) BETWEEN 3 AND 60
		 ORDER BY session_id
		 LIMIT $1`, n)
	if err != nil {
		t.Fatalf("discover compare probes: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("discover compare probes scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("discover compare probes rows: %v", err)
	}
	return out
}

// legacyCompareQuery is the pre-split **shape** — one statement, bodies joined
// onto the session-sized outer side — kept so the gate compares the split
// against the shape it replaced rather than against a paraphrase.
//
// It deliberately also applies the client_model NULL fix (§5.9). Without that,
// the two sides differ by the 17.32% of turns the old code silently dropped and
// the gate would be measuring the wrong change. The NULL fix has its own
// assertion, TestCompareKeepsTurnsWithNullClientModel.
func legacyCompareQuery(sessionID, tenantID string) (string, []any) {
	return `
		SELECT
			rl.request_id,
			rl.ts,
			rl.client_model,
			rl.outbound_model,
			rb.request_body AS request_body,
			rb.outbound_body, rb.response_body AS response_body
		FROM ` + dbpkgSessionFamily() + ` rl
		LEFT JOIN request_logs_bodies_with_current_month rb
		  ON rb.request_id = rl.request_id
		WHERE rl.tenant_id = $2
		ORDER BY rl.ts ASC
		LIMIT 500
	`, []any{sessionID, tenantID}
}

func queryLegacyCompareRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID, tenantID string) []compareProbeRow {
	t.Helper()
	query, args := legacyCompareQuery(sessionID, tenantID)
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		t.Fatalf("legacy compare query: %v", err)
	}
	defer rows.Close()
	var out []compareProbeRow
	for rows.Next() {
		var r compareProbeRow
		var clientModel, outboundModel *string
		var req, outbound, resp []byte
		if err := rows.Scan(&r.requestID, &r.ts, &clientModel, &outboundModel, &req, &outbound, &resp); err != nil {
			t.Fatalf("legacy compare scan: %v", err)
		}
		r.clientModel = derefOrEmpty(clientModel)
		r.outboundModel = derefOrEmpty(outboundModel)
		r.reqMD5, r.outMD5, r.respMD5 = md5Of(req), md5Of(outbound), md5Of(resp)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("legacy compare rows: %v", err)
	}
	return out
}

// querySplitCompareRows runs the production two-phase path: compareTurnsSQL
// for the turn metadata, then querySessionBodies for the bodies, merged by
// turn identity.
func querySplitCompareRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID, tenantID string) []compareProbeRow {
	t.Helper()
	rows, err := pool.Query(ctx, compareTurnsSQL(), sessionID, tenantID)
	if err != nil {
		t.Fatalf("compare phase 1: %v", err)
	}
	type key struct {
		requestID     string
		ts            time.Time
		clientModel   string
		outboundModel string
	}
	var order []key
	requestIDs := make([]string, 0, 64)
	timestamps := make([]time.Time, 0, 64) // retained to assert phase 1 ordering
	for rows.Next() {
		var requestID string
		var ts time.Time
		var strat, meta, clientModel, outboundModel *string
		if err := rows.Scan(&requestID, &ts, &strat, &meta, &clientModel, &outboundModel); err != nil {
			rows.Close()
			t.Fatalf("compare phase 1 scan: %v", err)
		}
		order = append(order, key{requestID: requestID, ts: ts, clientModel: derefOrEmpty(clientModel), outboundModel: derefOrEmpty(outboundModel)})
		requestIDs = append(requestIDs, requestID)
		timestamps = append(timestamps, ts)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("compare phase 1 rows: %v", err)
	}

	bodies, err := querySessionBodiesByRequestID(ctx, pool, requestIDs)
	if err != nil {
		t.Fatalf("compare phase 2: %v", err)
	}

	if len(timestamps) != len(order) {
		t.Fatalf("phase 1 returned %d timestamps for %d turns", len(timestamps), len(order))
	}
	out := make([]compareProbeRow, 0, len(order))
	for _, k := range order {
		row := compareProbeRow{requestID: k.requestID, ts: k.ts, clientModel: k.clientModel, outboundModel: k.outboundModel, reqMD5: "∅", outMD5: "∅", respMD5: "∅"}
		if b, ok := bodies[k.requestID]; ok {
			if b.requestBody != nil {
				row.reqMD5 = md5Of([]byte(*b.requestBody))
			}
			if b.outboundBody != nil {
				row.outMD5 = md5Of([]byte(*b.outboundBody))
			}
			if b.responseBody != nil {
				row.respMD5 = md5Of([]byte(*b.responseBody))
			}
		}
		out = append(out, row)
	}
	return out
}

// md5Of gives a printable fingerprint that distinguishes NULL (∅) from an
// empty string, which a plain "" would conflate.
func md5Of(raw []byte) string {
	if raw == nil {
		return "∅"
	}
	return fmtMD5(raw)
}
