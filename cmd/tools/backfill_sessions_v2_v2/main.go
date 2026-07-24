// Command backfill_sessions_v2_v2 runs historical backfill from public.request_logs
// into gateway.session_turns for a single session, using the
// backfill_session_v2_turns(tenant, session, batch) SQL function.
//
// Usage:
//
//	psql "$DB" -f sql/scripts/backfill_sessions_v2_v2.sql   # one-time SQL install
//	go run ./cmd/tools/backfill_sessions_v2_v2 \
//	    --dsn="$DB" --tenant=tenant_xxx --session=gw_abc... --batch=100 --dry-run=true
//
// Exit codes:
//
//	0 — backfill function executed (inserted count printed regardless)
//	1 — argument / DB / function error
//
// The Go CLI does not perform the insert itself; it delegates to the SQL
// function so the same idempotency and ON CONFLICT guarantees apply.
package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := flag.String("dsn", "", "Postgres DSN (required)")
	tenant := flag.String("tenant", "", "tenant id (required)")
	session := flag.String("session", "", "gw_session_id (required)")
	batchSize := flag.Int("batch", 100, "batch size (>=1)")
	dryRun := flag.Bool("dry-run", true, "dry-run flag echoed in logs (SQL function always writes; dry-run is informational)")
	flag.Parse()

	if *dsn == "" || *tenant == "" || *session == "" {
		log.Fatal("--dsn, --tenant, --session are required")
	}
	if *batchSize < 1 {
		log.Fatalf("--batch must be >= 1, got %d", *batchSize)
	}

	pool, err := pgxpool.New(context.Background(), *dsn)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	start := time.Now()
	var inserted int
	err = pool.QueryRow(
		context.Background(),
		`SELECT * FROM backfill_session_v2_turns($1, $2, $3)`,
		*tenant, *session, *batchSize,
	).Scan(&inserted)
	if err != nil {
		log.Fatalf("backfill: %v", err)
	}

	log.Printf("backfill: tenant=%s session=%s batch=%d inserted=%d dryRun=%v elapsed=%s",
		*tenant, *session, *batchSize, inserted, *dryRun, time.Since(start))
}