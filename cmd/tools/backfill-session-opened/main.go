// Command backfill-session-opened generates retroactive session.opened.v1 outbox
// events for sessions that were never given an opener (e.g. historical rows
// predating the terminal-path补发 fix, or rows whose opener was lost).
//
// Default mode is DRY-RUN: it only prints the candidate count and a sample.
// Pass --execute to perform the inserts. Inserts are idempotent (ON CONFLICT
// DO NOTHING on event_id), so re-running is safe.
//
// Design (2026-10-31, audit §8.2):
//   - Source of truth for "which sessions exist" = public.session_turns_hot
//     (and its parent public.session_turns), grouped by (tenant_id, session_id).
//   - A session is a candidate iff no row in outbox_events has
//     event_type='session.opened.v1' and aggregate_id=session_id.
//   - user_id is derived like the live path (sessionOpenedUserID): end_user_id
//     when present, else api_key_id, else "unknown".
//   - occurred_at is taken from the earliest turn's ts (not now()), so ASM
//     ordering stays chronological.
//
// This tool does NOT dispatch events. After backfill, run outbox-dlq-replay
// (or wait for the dispatcher) to deliver them.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/outbox"
	_ "github.com/lib/pq"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN (default: $DATABASE_URL)")
	fromRaw := flag.String("from", "", "only sessions whose earliest turn ts >= this RFC3339 UTC time")
	toRaw := flag.String("to", "", "only sessions whose earliest turn ts < this RFC3339 UTC time (exclusive)")
	limit := flag.Int("limit", 1000, "maximum sessions to process (max 10000)")
	execute := flag.Bool("execute", false, "perform inserts; default is dry-run")
	verbose := flag.Bool("verbose", false, "print every candidate")
	flag.Parse()

	if strings.TrimSpace(*dsn) == "" {
		log.Fatal("--dsn required (or set DATABASE_URL)")
	}
	if *limit < 1 || *limit > 10000 {
		log.Fatal("--limit must be in [1,10000]")
	}

	db, err := sql.Open("postgres", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping db: %v", err)
	}

	// Build the candidate query. We group by (tenant_id, session_id) and pick
	// the earliest turn's ts + user derivation. NOT EXISTS against outbox_events
	// keeps already-opened sessions out.
	q := `
		SELECT s.tenant_id, s.session_id, s.user_id, s.occurred_at
		FROM (
			SELECT DISTINCT ON (tenant_id, session_id)
				tenant_id, session_id,
				COALESCE(NULLIF(end_user_id, ''), NULLIF(CAST(api_key_id AS text), ''), 'unknown') AS user_id,
				ts AS occurred_at
			FROM session_turns_hot
			WHERE ($1::timestamptz IS NULL OR ts >= $1)
			  AND ($2::timestamptz IS NULL OR ts < $2)
			ORDER BY tenant_id, session_id, ts ASC
		) s
		WHERE NOT EXISTS (
			SELECT 1 FROM outbox_events o
			WHERE o.event_type = 'session.opened.v1'
			  AND o.aggregate_id = s.session_id
			  AND o.tenant_id = s.tenant_id
		)
		ORDER BY s.occurred_at ASC
		LIMIT $3
	`
	var fromPtr, toPtr interface{}
	if strings.TrimSpace(*fromRaw) != "" {
		t, err := time.Parse(time.RFC3339, *fromRaw)
		if err != nil {
			log.Fatalf("--from must be RFC3339 UTC: %v", err)
		}
		fromPtr = t.UTC()
	}
	if strings.TrimSpace(*toRaw) != "" {
		t, err := time.Parse(time.RFC3339, *toRaw)
		if err != nil {
			log.Fatalf("--to must be RFC3339 UTC: %v", err)
		}
		toPtr = t.UTC()
	}

	rows, err := db.QueryContext(ctx, q, fromPtr, toPtr, *limit)
	if err != nil {
		log.Fatalf("candidate query: %v", err)
	}
	defer rows.Close()

	type cand struct {
		tenantID   string
		sessionID  string
		userID     string
		occurredAt time.Time
	}
	var collected []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.tenantID, &c.sessionID, &c.userID, &c.occurredAt); err != nil {
			log.Fatalf("scan candidate: %v", err)
		}
		collected = append(collected, c)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	fmt.Printf("candidates=%d (limit=%d, from=%s, to=%s)\n", len(collected), *limit, *fromRaw, *toRaw)
	if len(collected) == 0 {
		fmt.Println("nothing to do")
		return
	}
	for i, c := range collected {
		if i < 5 || *verbose {
			fmt.Printf("  sample tenant=%s session=%s user=%s occurred=%s\n",
				c.tenantID, c.sessionID, c.userID, c.occurredAt.UTC().Format(time.RFC3339))
		}
	}

	if !*execute {
		fmt.Println("DRY-RUN: no inserts performed. Re-run with --execute to apply.")
		return
	}

	inserted, skipped := 0, 0
	for _, c := range collected {
		env, err := outbox.BuildSessionOpenedEventV1(c.tenantID, c.sessionID, c.userID)
		if err != nil {
			log.Printf("build event failed tenant=%s session=%s: %v", c.tenantID, c.sessionID, err)
			continue
		}
		// Override occurred_at with the earliest turn's ts (chronological).
		env.OccurredAt = c.occurredAt.UTC()

		payloadJSON, err := marshalPayload(env)
		if err != nil {
			log.Printf("marshal payload tenant=%s session=%s: %v", c.tenantID, c.sessionID, err)
			continue
		}
		tag, err := db.ExecContext(ctx, `
			INSERT INTO outbox_events (
				event_id, event_type, schema_version, tenant_id,
				aggregate_id, aggregate_version, occurred_at, payload, status
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
			ON CONFLICT (event_id) DO NOTHING
		`,
			env.EventID, env.EventType, env.SchemaVersion, env.TenantID,
			env.AggregateID, env.AggregateVersion, env.OccurredAt,
			string(payloadJSON),
		)
		if err != nil {
			log.Printf("insert failed tenant=%s session=%s: %v", c.tenantID, c.sessionID, err)
			continue
		}
		rowsAffected, _ := tag.RowsAffected()
		if rowsAffected == 0 {
			skipped++
		} else {
			inserted++
		}
	}
	fmt.Printf("done: inserted=%d skipped_already_present=%d\n", inserted, skipped)
}

func marshalPayload(env outbox.EventEnvelope) ([]byte, error) {
	// BuildSessionOpenedEventV1 already populated env.Payload; marshal it.
	b, err := json.Marshal(env.Payload)
	if err != nil {
		return nil, err
	}
	return b, nil
}
