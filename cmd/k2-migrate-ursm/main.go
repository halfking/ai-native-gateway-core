// Command k2-migrate-ursm is the operator entry point for the L3 k2
// migration machinery (docs/03-design/02-feature-design/会话优化v4/{14,15,16}*.md,
// slice 11-13). The command is intentionally dry-run only: --apply writes
// are stubbed until G1/G3/G4 evidence is complete. M5-0/T0 = BLOCKED /
// NO-GO remains in force; this binary must not be used to touch production
// Redis without the full exit-criteria chain satisfied.
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

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/migration"
	"github.com/redis/go-redis/v9"
)

const (
	defaultRedisKeyPrefix = "ursm:v2:"
	defaultRedisURL       = "redis://localhost:6379/15"
	defaultLedgerPath     = "/var/tmp/k2-ledger.ndjson"
	defaultOwner          = "halfking"
	defaultLedgerID       = "ursm-v2-k2-20260818-001"
	defaultMode           = string(migration.ModeDual)
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: k2-migrate-ursm <preflight|copy|cleanup|status> [flags]")
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	// T0 fail-closed gate: refuse --apply before flag parsing or any
	// Redis/PG client construction. The check runs ahead of flag.Parse
	// so a misconfigured --redis cannot trigger a network round trip
	// while T0 is BLOCKED / NO-GO. See handoff §3 / §2 for the contract
	// and docs/03-design/.../14-URSM-...md for the durable apply path.
	for _, arg := range args {
		if arg == "--apply" || arg == "-apply" || strings.HasPrefix(arg, "--apply=") || strings.HasPrefix(arg, "-apply=") {
			log.Fatal("k2 migration apply is disabled while T0 is BLOCKED / NO-GO; use the owner-reviewed durable library APIs only")
		}
	}

	common := flag.NewFlagSet(cmd, flag.ExitOnError)
	redisURL := common.String("redis", redisURLFromEnv(), "Redis URL")
	keyPrefix := common.String("key-prefix", defaultRedisKeyPrefix, "URSM v2 Redis prefix")
	ledgerPath := common.String("ledger", defaultLedgerPath, "Ledger NDJSON file path")
	owner := common.String("owner", defaultOwner, "Migration owner (doc 14 §0)")
	ledgerID := common.String("ledger-id", defaultLedgerID, "Migration ledger_id (one-shot, doc 14 §0)")
	modeStr := common.String("mode", defaultMode, "Schema mode: legacy|dual|canonical")
	apply := common.Bool("apply", false, "Write ledger/metadata. Default is dry-run.")
	rate := common.Int("rate", 0, "Cleanup rate limit (keys/sec). 0 disables.")
	pgDSN := common.String("pg", envOr("LLM_GATEWAY_DATABASE_URL", envOr("DATABASE_URL", "")), "Postgres DSN (required for --apply cleanup)")
	rollbackDeadline := common.String("rollback-deadline", "", "Rollback deadline (RFC3339Nano) for preflight metadata")
	noFsync := common.Bool("ledger-no-fsync", false, "Disable per-record fsync on Append (faster, accepts crash-window loss)")
	if err := common.Parse(args); err != nil {
		log.Fatalf("flag parse: %v", err)
	}

	if !*apply {
		fmt.Println("[DRY-RUN] no writes; pass --apply to enable writes")
	}

	if *apply && cmd != "status" {
		log.Fatal("k2 migration apply is disabled while T0 is BLOCKED / NO-GO; use the owner-reviewed durable library APIs only")
	}

	mode := migration.Mode(*modeStr)
	if !mode.Valid() {
		log.Fatalf("invalid mode %q", *modeStr)
	}

	parsedURL, err := redis.ParseURL(*redisURL)
	if err != nil {
		log.Fatalf("redis parse: %v", err)
	}
	rdb := redis.NewClient(parsedURL)
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}

	ledger := migration.NewLedgerWithSync(*ledgerPath, !*noFsync)
	mdStore := &migration.MetadataHash{Prefix: *keyPrefix, RDB: rdb}

	switch cmd {
	case "status":
		handleStatus(ctx, mdStore)
	case "copy":
		if !*apply {
			fmt.Println("[DRY-RUN] copy phase: no writes")
			return
		}
		handleCopy(ctx, ledger, rdb, *keyPrefix)
	case "cleanup":
		handleCleanup(ctx, ledger, rdb, *keyPrefix, *pgDSN, mode, *rate, *apply)
	case "preflight":
		if !*apply {
			fmt.Println("[DRY-RUN] preflight phase: would classify node:*/win:*/idx:model:* keys")
			return
		}
		handlePreflight(ctx, ledger, rdb, *keyPrefix, *owner, *ledgerID, mode, *rollbackDeadline)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(2)
	}
}

func handleStatus(ctx context.Context, store *migration.MetadataHash) {
	m, err := store.Read(ctx)
	if err != nil {
		log.Fatalf("metadata read: %v", err)
	}
	if m.LedgerID == "" {
		fmt.Println("metadata HASH is empty; no k2 migration in progress")
		return
	}
	fmt.Printf("ledger_id=%s\nowner=%s\nmode=%s\ncheckpoint=%s\nstarted_at=%s\nupdated_at=%s\npreflight_checksum=%s\n",
		m.LedgerID, m.Owner, m.Mode, m.Checkpoint, m.StartedAt.Format(time.RFC3339Nano), m.UpdatedAt.Format(time.RFC3339Nano), m.PreflightChecksum)
}

func handlePreflight(ctx context.Context, ledger *migration.Ledger, rdb *redis.Client, prefix, owner, ledgerID string, mode migration.Mode, rollbackDeadline string) {
	if owner == "" || ledgerID == "" {
		log.Fatal("--owner and --ledger-id are required")
	}
	deadline, err := time.Parse(time.RFC3339Nano, rollbackDeadline)
	if err != nil || deadline.IsZero() {
		log.Fatal("--rollback-deadline is required and must be RFC3339Nano")
	}
	pre := &migration.PreflightRunner{
		Prefix:  prefix,
		Ledger:  ledger,
		Scanner: &migration.RedisScanner{RDB: rdb},
		RunID:   fmt.Sprintf("preflight-%d", time.Now().UTC().UnixNano()),
		Now:     func() time.Time { return time.Now().UTC() },
	}
	summary, err := pre.Preflight(ctx)
	if err != nil {
		log.Fatalf("preflight: %v", err)
	}
	md := migration.Metadata{
		Owner:             owner,
		LedgerID:          ledgerID,
		Mode:              mode,
		CutoverEpoch:      0,
		StartedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
		PreflightChecksum: summary.Checksum,
		RollbackDeadline:  deadline,
		Checkpoint:        migration.CheckpointPreflight,
	}
	if err := (&migration.MetadataHash{Prefix: prefix, RDB: rdb}).Write(ctx, md); err != nil {
		log.Fatalf("metadata write: %v", err)
	}
	fmt.Printf("preflight done: total=%d migratable=%d canonical_present=%d ambiguous=%d excluded=%d conflicts=%d checksum=%s\n",
		summary.TotalKeys, summary.Migratable, summary.CanonicalPresent, summary.Ambiguous, summary.Excluded, summary.Conflicts, summary.Checksum)
}

func handleCopy(ctx context.Context, ledger *migration.Ledger, rdb *redis.Client, prefix string) {
	cp := &migration.Copy{Prefix: prefix, Ledger: ledger, RDB: rdb}
	results, err := cp.Copy(ctx)
	if err != nil {
		log.Fatalf("copy: %v", err)
	}
	counts := map[migration.CopyStatus]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	fmt.Printf("copy done: copied=%d skipped=%d refused=%d failed=%d unchanged=%d total=%d\n",
		counts[migration.CopyStatusCopied], counts[migration.CopyStatusSkipped],
		counts[migration.CopyStatusRefused], counts[migration.CopyStatusFailed],
		counts[migration.CopyStatusUnchanged], len(results))
}

func handleCleanup(ctx context.Context, ledger *migration.Ledger, rdb *redis.Client, prefix, pgDSN string, mode migration.Mode, rate int, apply bool) {
	if !apply {
		fmt.Println("[DRY-RUN] cleanup phase: no writes")
		return
	}
	if strings.TrimSpace(pgDSN) == "" {
		log.Fatal("--apply cleanup requires --pg or DATABASE_URL / LLM_GATEWAY_DATABASE_URL")
	}
	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("postgres ping: %v", err)
	}
	pgStore := migration.NewPGStore(pool)
	authorization, err := migration.LoadCleanupAuthorization(ctx, &migration.MetadataHash{Prefix: prefix, RDB: rdb}, pgStore)
	if err != nil {
		log.Fatalf("cleanup authorization: %v", err)
	}
	cl := &migration.Cleanup{
		Prefix: prefix,
		Ledger: ledger,
		RDB:    rdb,
		Opts: migration.CleanupOptions{
			Mode:            authorization.Run.Mode,
			RateLimitPerSec: rate,
			Authorization:   authorization,
			Claim: func(item migration.Item) error {
				return pgStore.ClaimEntryForCleanup(ctx, authorization.Run.LedgerID, item.SourceKey, item.FieldChecksum)
			},
			ReleaseClaim: func(item migration.Item) error {
				return pgStore.ReleaseEntryCleanupClaim(ctx, authorization.Run.LedgerID, item.SourceKey, item.FieldChecksum)
			},
			MarkCleaned: func(item migration.Item) error {
				return pgStore.MarkEntryCleaned(ctx, authorization.Run.LedgerID, item.SourceKey, item.FieldChecksum)
			},
			Now: func() time.Time { return time.Now().UTC() },
		},
	}
	results, err := cl.Cleanup(ctx)
	if err != nil {
		log.Fatalf("cleanup: %v", err)
	}
	counts := map[migration.CleanupStatus]int{}
	for _, r := range results {
		counts[r.Status]++
	}
	fmt.Printf("cleanup done: deleted=%d preserved=%d skipped=%d refused=%d total=%d\n",
		counts[migration.CleanupStatusDeleted], counts[migration.CleanupStatusPreserved],
		counts[migration.CleanupStatusSkipped], counts[migration.CleanupStatusRefused], len(results))
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func redisURLFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("REDIS_URL")); v != "" {
		return v
	}
	addr := envOr("LLM_GATEWAY_REDIS_ADDR", "localhost:6379")
	db := 15
	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REDIS_DB")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			db = parsed
		}
	}
	endpoint := &url.URL{Scheme: "redis", Host: addr, Path: "/" + strconv.Itoa(db)}
	if pw := os.Getenv("LLM_GATEWAY_REDIS_PASSWORD"); pw != "" {
		endpoint.User = url.UserPassword("", pw)
	}
	return endpoint.String()
}
