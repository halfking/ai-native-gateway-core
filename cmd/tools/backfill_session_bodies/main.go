// Command backfill_session_bodies derives and writes per-turn message deltas into
// public.session_bodies for one session, sourced from the accumulated
// request_logs bodies (the matryoshka). It is part of the "full switch to
// Sessions V2" preparation: historical sessions that predate the V2 dual-write
// have no session_bodies, so this tool backfills them so V2 can serve the detail
// page and be validated against request_logs.
//
// The request_logs → session_turns metadata backfill (backfill_session_v2_turns
// SQL function, see cmd/tools/backfill_sessions_v2_v2) should be run first so the
// session_turns rows exist; session_bodies is written independently here.
//
// Usage:
//
//	go run ./cmd/tools/backfill_session_bodies \
//	    --dsn="$DB" --tenant=tenant_xxx --session=gw_abc... --dry-run=true
//
// Exit codes:
//	0 — completed (rows written or dry-run reported)
//	1 — argument / DB / write error
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	v2 "github.com/kaixuan/llm-gateway-go/domains/session/v2"
)

type turnRow struct {
	turnNo    int
	requestID string
	tenantID  string
	ts        time.Time
	reqBody   []byte
	respBody  []byte
}

func main() {
	dsn := flag.String("dsn", "", "Postgres DSN (required)")
	tenant := flag.String("tenant", "", "tenant id (optional; empty = any)")
	session := flag.String("session", "", "gw_session_id (required)")
	dryRun := flag.Bool("dry-run", true, "when true, derive and report but do not write session_bodies")
	flag.Parse()

	if *dsn == "" || *session == "" {
		log.Fatal("--dsn and --session are required")
	}

	pool, err := pgxpool.New(context.Background(), *dsn)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	// Optimized query: no window function, just ORDER BY.
	// turn_no is computed in Go to avoid ROW_NUMBER() triggering slow scans.
	rows, err := pool.Query(context.Background(), `
		SELECT r.request_id, r.tenant_id, r.ts,
		       b.request_body, b.response_body
		FROM request_logs r
		LEFT JOIN request_logs_bodies b ON b.request_id = r.request_id
		WHERE r.gw_session_id = $1 AND ($2 = '' OR r.tenant_id = $2)
		ORDER BY r.ts ASC, r.request_id ASC`, *session, *tenant)
	if err != nil {
		log.Fatalf("query turns: %v", err)
	}

	var turnRows []turnRow
	var fullMsgs, respMsgs [][]Msg
	turnNo := 1
	for rows.Next() {
		var r turnRow
		var reqBody, respBody []byte
		if err := rows.Scan(&r.requestID, &r.tenantID, &r.ts, &reqBody, &respBody); err != nil {
			log.Fatalf("scan turn: %v", err)
		}
		r.turnNo = turnNo
		turnNo++
		r.reqBody = reqBody
		r.respBody = respBody
		full, err := ParseRequestMessages(reqBody)
		if err != nil {
			log.Fatalf("turn %d parse request: %v", r.turnNo, err)
		}
		resp, err := ParseResponseMessages(respBody)
		if err != nil {
			log.Fatalf("turn %d parse response: %v", r.turnNo, err)
		}
		turnRows = append(turnRows, r)
		fullMsgs = append(fullMsgs, full)
		respMsgs = append(respMsgs, resp)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate turns: %v", err)
	}
	rows.Close()

	reqDeltas, respDeltas := DeriveTurnDeltas(fullMsgs, respMsgs)

	writer := v2.NewSessionBodiesWriter(pool)
	written := 0
	for i, r := range turnRows {
		rec := v2.BodiesRecord{
			SessionID:     *session,
			TurnNo:        r.turnNo,
			TenantID:      r.tenantID,
			RequestID:     r.requestID,
			Ts:            r.ts,
			RequestDelta:  toV2Messages(reqDeltas[i]),
			ResponseDelta: toV2Messages(respDeltas[i]),
		}
		if len(rec.RequestDelta) == 0 && len(rec.ResponseDelta) == 0 {
			continue
		}
		if *dryRun {
			log.Printf("[dry-run] turn %d request_id=%s req_delta=%d resp_delta=%d",
				r.turnNo, r.requestID, len(rec.RequestDelta), len(rec.ResponseDelta))
			written++
			continue
		}
		if err := writer.WriteBodies(context.Background(), rec); err != nil {
			log.Fatalf("write bodies turn %d: %v", r.turnNo, err)
		}
		written++
	}

	log.Printf("backfill session_bodies: session=%s tenant=%s turns=%d bodies=%d dryRun=%v",
		*session, *tenant, len(turnRows), written, *dryRun)
}

func toV2Messages(in []Msg) []v2.Message {
	out := make([]v2.Message, 0, len(in))
	for _, m := range in {
		out = append(out, v2.Message{Role: m.Role, Content: m.Content})
	}
	return out
}
