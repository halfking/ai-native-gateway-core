// Command ursm-k2-preflight runs the read-only inventory phase of the URSM
// k2 key migration (doc 14 §4/§6). It scans the legacy node namespace,
// classifies every source key, prints the human-readable verdict plus the
// exact preflight checksum, and exits non-zero on any ambiguous/conflict
// verdict — the gateway must never be told to proceed under those
// conditions. By default it is dry-run: it never writes to Redis or PG
// and never records the migration in the durable ledger; pass --apply to
// open a migration run (still no Redis writes).
//
// Gating: URSM_V2_KEY_SCHEMA_MODE is intentionally NOT consulted here —
// preflight is read-only and is the input that decides which mode is even
// viable. It is safe to run before mode is committed; the operator's
// decision is recorded when copy/cleanup run later.
//
// Operator mapping: any source key whose classification is "ambiguous" or
// "conflict" must move out of that bucket via an out-of-band operator
// identity mapping recorded against the source key in the ledger; the CLI
// never guesses tenant/model from a key string.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/migration"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
)

const (
	defaultRedisURL  = "redis://localhost:6379/2"
	defaultKeyPrefix  = "ursm:v2:"
	defaultLedgerID  = "ursm-v2-k2-20260818-001"
	defaultOwner     = "halfking"
	defaultDeadline  = "2026-09-01T00:00:00Z"
)

func main() {
	var (
		apply          = flag.Bool("apply", false, "Record the migration run in PostgreSQL. Default is dry-run.")
		redisURL       = flag.String("redis", redisURLFromEnv(), "Redis URL")
		pgDSN          = flag.String("pg", envOr("LLM_GATEWAY_DATABASE_URL", envOr("DATABASE_URL", "")), "Postgres DSN")
		keyPrefix      = flag.String("key-prefix", defaultKeyPrefix, "URSM v2 Redis key prefix")
		owner          = flag.String("owner", defaultOwner, "Migration owner recorded in the ledger")
		ledgerID       = flag.String("ledger-id", defaultLedgerID, "Non-reusable migration ledger id")
		mode           = flag.String("mode", store.KeySchemaModeLegacy.String(), "Planned key schema mode (legacy/dual/canonical) — recorded only when --apply is set")
		rollbackDeadline = flag.String("rollback-deadline", defaultDeadline, "When the legacy shadow must be cleaned up")
		limit          = flag.Int("limit", 0, "Limit SCAN output (0 = no limit)")
	)
	flag.Parse()

	keySchemaMode, err := store.ParseKeySchemaMode(*mode)
	if err != nil {
		log.Fatalf("invalid --mode: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	redisOptions, err := redis.ParseURL(*redisURL)
	if err != nil {
		log.Fatalf("redis parse: %v", err)
	}
	rdb := redis.NewClient(redisOptions)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}

	report, err := migration.NewEntryPreflight(rdb, *keyPrefix).Scan(ctx)
	if err != nil {
		log.Fatalf("preflight scan: %v", err)
	}

	if *limit > 0 && len(report.Entries) > *limit {
		report.Entries = report.Entries[:*limit]
	}

	fmt.Printf("preflight ledger=%s owner=%s mode=%s\n", *ledgerID, *owner, keySchemaMode.String())
	fmt.Printf("checksum=%s\n", report.Checksum)
	fmt.Printf("totals: migratable=%d canonical_present=%d ambiguous=%d excluded=%d go=%v\n",
		report.Migratable, report.CanonicalPresent, report.Ambiguous, report.Excluded, report.Go())
	for _, e := range report.Entries {
		fmt.Printf("  %s class=%s schema=%s reason=%q ttl_ms=%d gen=%d\n",
			e.SourceKey, e.Class, e.Schema, e.Reason, e.PTTLMillis, e.Generation)
	}

	if !*apply {
		if !report.Go() {
			log.Printf("DRY-RUN: ambiguous or conflict verdicts present — ref --apply is forbidden until they are resolved")
		} else {
			fmt.Println("[DRY-RUN] no Redis or PG writes; pass --apply to open a migration run")
		}
		return
	}

	if *pgDSN == "" {
		log.Fatalf("--apply requires --pg or DATABASE_URL / LLM_GATEWAY_DATABASE_URL")
	}
	if !report.Go() {
		log.Fatalf("--apply refused: ambiguous/conflict verdicts must be resolved out-of-band first")
	}

	pool, err := pgxpool.New(ctx, *pgDSN)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("postgres ping: %v", err)
	}
	store_ := migration.NewPGStore(pool)
	if err := store_.OpenRun(ctx, migration.RunRecord{
		LedgerID:          *ledgerID,
		Owner:             *owner,
		KeySchemaMode:     keySchemaMode,
		PreflightChecksum: report.Checksum,
		Checkpoint:        migration.CheckpointPreflight,
		RollbackDeadline:  *rollbackDeadline,
		Total:             len(report.Entries),
		Migratable:        report.Migratable,
		CanonicalPresent:  report.CanonicalPresent,
		Ambiguous:         report.Ambiguous,
		Excluded:          report.Excluded,
	}); err != nil {
		log.Fatalf("open migration run: %v", err)
	}
	if err := store_.UpsertEntries(ctx, *ledgerID, report.Entries); err != nil {
		log.Fatalf("upsert ledger entries: %v", err)
	}
	fmt.Printf("migration run %s opened with %d entries\n", *ledgerID, len(report.Entries))
}

func envOr(key, def string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return def
}

func redisURLFromEnv() string {
	if value := strings.TrimSpace(os.Getenv("REDIS_URL")); value != "" {
		return value
	}
	addr := envOr("LLM_GATEWAY_REDIS_ADDR", "localhost:6379")
	db := 2
	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REDIS_DB")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			db = parsed
		}
	}
	endpoint := &url.URL{Scheme: "redis", Host: addr, Path: "/" + strconv.Itoa(db)}
	if password := os.Getenv("LLM_GATEWAY_REDIS_PASSWORD"); password != "" {
		endpoint.User = url.UserPassword("", password)
	}
	return endpoint.String()
}