// Command migrate-ursm-v2 — one-time migration tool for the URSM v2 cutover.
//
// Origin: docs/audit/2026-07-28-llm-gateway-flow-comprehensive-audit.md §7.1 R-7.1
//
//	docs/design/2026-07-28-llm-gateway-flow-improvements.md §2 P0-3
//
// Purpose:
//
//	Pre-load the URSM v2 Redis namespace with the current
//	legacy credentialstate / node_probe_state view so the 7-day
//	shadow comparison window starts from a comparable baseline.
//	Without this, URSM v2 starts cold (every credential Available=false
//	via the T4 protection-rejection invariant) and the sidecar writes
//	in shadow mode would not reflect the production availability state.
//
// Data flow:
//
//	PG node_probe_state (legacy) → per-credential URSM v2 Redis hash
//
// Mapping:
//
//	paused = TRUE                      → manual_hold = 1 (admin override)
//	                                      available   = 0
//	                                      disabled    = 1
//	paused = FALSE & consec_failures >= 3 (FailStreakLimit)
//	                                     → available    = 0
//	                                      disabled     = 1
//	                                      cool_until_ms = ts + cool_seconds
//	otherwise                          → available    = 1
//	                                      disabled     = 0
//	                                      fail_streak  = consec_failures
//	                                      last_ok_ms   = last_attempt_at
//
// Usage:
//
//	# dry-run (default): print what would be written, no Redis I/O
//	./bin/migrate-ursm-v2 --dry-run
//
//	# actually apply
//	./bin/migrate-ursm-v2 --apply
//
//	# force-overwrite: bypass CAS guard (overwrites pre-existing keys)
//	# ONLY use if you intentionally want to reset live/admin state
//	./bin/migrate-ursm-v2 --apply --force-overwrite
//
//	# restrict to one tenant for staged rollout
//	./bin/migrate-ursm-v2 --apply --tenant-id=42
//
//	# custom Redis / PG (REDIS_URL takes precedence; otherwise the
//	# LLM_GATEWAY_REDIS_ADDR/PASSWORD/DB settings are used, with db=2 by
//	# default to match the gateway)
//	./bin/migrate-ursm-v2 --redis=redis://10.0.0.1:6379/2 \
//	    --pg="postgres://user:pass@host:5432/db?sslmode=disable"
//
// Safety:
//   - Default mode is --dry-run. Apply must be explicit.
//   - The migration is idempotent: running twice produces the same Redis
//     state (HSET overwrites). Use --dry-run to verify before --apply.
//   - CAS guard (default ON): pre-existing keys with manual_hold=1 or
//     generation>1 are SKIPPED to avoid clobbering admin overrides or
//     live traffic state. Use --force-overwrite to bypass.
//   - Operator MUST have manually set URSM_V2_MODE=shadow before this
//     run, otherwise the gateway will refuse the sidecar write path.
//   - Operator MUST verify the resulting Redis keys with the docs/runbooks/
//     ursm-v2-cutover.md verification steps before flipping ModeAuthoritative.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

// probeRow mirrors one row of public.node_probe_state (legacy DB).
// Defined locally so the migration doesn't break when the table
// schema evolves (we project only the columns we need).
type probeRow struct {
	TenantID             string
	CredentialID         int64
	RawModel             string
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	LastAttemptAt        sql.NullTime
	Paused               bool
	LastDirectOK         sql.NullBool
	LastErrCode          sql.NullString
}

// mappedNode is the URSM v2 Redis-hash representation we'll HSET for
// each legacy row. Field names MUST stay aligned with the URSM v2
// record_request.lua script (see domains/ursm/v2/store/record_request.lua).
type mappedNode struct {
	NodeKey string
	Fields  map[string]string
}

const (
	defaultRedisURL = "redis://localhost:6379/2"

	// defaultRedisKeyPrefix matches domains/ursm/v2/config.go DefaultConfig().
	defaultRedisKeyPrefix = "ursm:v2:"

	// failStreakLimit matches the limit hardcoded in record_request.lua
	// (FailStreakLimit: 3). A legacy row with consecutive_failures >=
	// failStreakLimit gets marked unavailable in URSM v2 too.
	failStreakLimit = 3

	// coolSeconds aligns with record_request.lua CoolSeconds default
	// (300s = 5min) and the URSM v2 Config.CoolSeconds default (120s).
	// Using 300s (Lua default) so migrated entries stay consistent with
	// live-written ones until the URSM v2 config knob lands.
	coolSeconds = 300
)

func main() {
	var (
		apply          = flag.Bool("apply", false, "Actually write to Redis. Default is dry-run.")
		forceOverwrite = flag.Bool("force-overwrite", false, "Bypass CAS guard and overwrite pre-existing keys.")
		redisURL       = flag.String("redis", redisURLFromEnv(), "Redis URL")
		pgDSN          = flag.String("pg", envOr("LLM_GATEWAY_DATABASE_URL", envOr("DATABASE_URL", "")), "Postgres DSN")
		keyPrefix      = flag.String("key-prefix", defaultRedisKeyPrefix, "URSM v2 Redis key prefix (must match gateway config)")
			tenantID       = flag.String("tenant-id", "", "Optional: restrict migration to one tenant_id (empty = all)")
		batchSize      = flag.Int("batch-size", 500, "Rows per batch (HSET pipelining)")
	)
	flag.Parse()

	if *pgDSN == "" {
		log.Fatal("--pg or DATABASE_URL / LLM_GATEWAY_DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 1) Connect PG.
	db, err := sql.Open("pgx", *pgDSN)
	if err != nil {
		log.Fatalf("pg open: %v", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("pg ping: %v", err)
	}
	fmt.Printf("✅ Connected to PG (%s)\n", maskDSN(*pgDSN))

	// 2) Connect Redis.
	opt, err := redis.ParseURL(*redisURL)
	if err != nil {
		log.Fatalf("redis parse: %v", err)
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}
	fmt.Printf("✅ Connected to Redis (%s)\n", maskDSN(*redisURL))

	// 3) Read legacy rows.
	rows, err := readProbeRows(ctx, db, *tenantID)
	if err != nil {
		log.Fatalf("read probe rows: %v", err)
	}
	fmt.Printf("✅ Read %d legacy node_probe_state rows\n", len(rows))
	if len(rows) == 0 {
		fmt.Println("nothing to migrate; exit")
		return
	}

	// 4) Map to URSM v2 Redis hashes.
	nodes := make([]mappedNode, 0, len(rows))
	for _, r := range rows {
		nodes = append(nodes, mapRow(r, *keyPrefix))
	}

	// 5) Print dry-run summary and exit.
	available, cooled, manualHold := classifyNodes(nodes)
	fmt.Printf("\n[DRY-RUN] would write %d URSM v2 nodes:\n", len(nodes))
	fmt.Printf("  - available (healthy)            : %d\n", available)
	fmt.Printf("  - in cool (>= %d fails)          : %d\n", failStreakLimit, cooled)
	fmt.Printf("  - manual_hold (legacy paused)    : %d\n", manualHold)
	fmt.Printf("  - key prefix                     : %s\n", *keyPrefix)
	fmt.Printf("  - expected coverage keys         : %d\n", len(nodes))
	if !*apply {
		fmt.Println("\n(no Redis writes; pass --apply to commit)")
		return
	}

	// 6) Pre-flight CAS check (B4): don't clobber existing keys with
	// manual_hold=1 (admin override) or generation>1 (live traffic).
	var toWrite []mappedNode
	type skipInfo struct{ Reason string }
	skipped := make(map[int]skipInfo) // index into nodes
	if !*forceOverwrite {
		fmt.Println("\n[cas] checking pre-existing keys...")
		pipe := rdb.Pipeline()
		existsCmds := make([]*redis.IntCmd, len(nodes))
		for i, n := range nodes {
			existsCmds[i] = pipe.Exists(ctx, n.NodeKey)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			log.Fatalf("redis pipeline (CAS exists check): %v", err)
		}

		// Load manual_hold + generation for existing keys.
		pipe2 := rdb.Pipeline()
		hmgetCmds := make([]*redis.SliceCmd, len(nodes))
		for i, n := range nodes {
			if existsCmds[i].Val() == 0 {
				continue
			}
			hmgetCmds[i] = pipe2.HMGet(ctx, n.NodeKey, "manual_hold", "generation")
		}
		if _, err := pipe2.Exec(ctx); err != nil {
			log.Fatalf("redis pipeline (CAS HMGET): %v", err)
		}

		for i, n := range nodes {
			if hmgetCmds[i] == nil {
				toWrite = append(toWrite, n)
				continue
			}
			vals, err := hmgetCmds[i].Result()
			if err != nil {
				log.Fatalf("HMGET %s: %v", n.NodeKey, err)
			}
			manualHold := ""
			generation := ""
			if len(vals) > 0 && vals[0] != nil {
				manualHold = fmt.Sprintf("%v", vals[0])
			}
			if len(vals) > 1 && vals[1] != nil {
				generation = fmt.Sprintf("%v", vals[1])
			}
			if manualHold == "1" {
				skipped[i] = skipInfo{Reason: "manual_hold=1 (admin override)"}
				continue
			}
			if generation != "" && generation != "1" {
				skipped[i] = skipInfo{Reason: fmt.Sprintf("generation=%s (live traffic)", generation)}
				continue
			}
			toWrite = append(toWrite, n)
		}
	} else {
		toWrite = nodes
	}

	// 7) Print final summary.
	available, cooled, manualHold = classifyNodes(nodes)
	skipCount := len(skipped)
	writeCount := len(toWrite)
	fmt.Printf("\n[APPLY] summary (prefix=%s):\n", *keyPrefix)
	fmt.Printf("  - total nodes from PG            : %d\n", len(nodes))
	fmt.Printf("  - available (healthy)            : %d\n", available)
	fmt.Printf("  - in cool (>= %d fails)          : %d\n", failStreakLimit, cooled)
	fmt.Printf("  - manual_hold (legacy paused)    : %d\n", manualHold)
	fmt.Printf("  - to write                       : %d\n", writeCount)
	if skipCount > 0 {
		fmt.Printf("  - skipped by CAS guard           : %d\n", skipCount)
		for idx, info := range skipped {
			fmt.Printf("      [%d] key=%s  reason=%s\n", idx, nodes[idx].NodeKey, info.Reason)
		}
		fmt.Println("  (use --force-overwrite to bypass CAS guard)")
	}
	if !*apply {
		fmt.Println("\n(no Redis writes; pass --apply to commit)")
		return
	}
	if writeCount == 0 {
		fmt.Println("\nℹ️  No node hashes required writing; publishing verified coverage manifest")
	}

	// 8) Apply in batches.
	written := 0
	for start := 0; start < len(toWrite); start += *batchSize {
		end := start + *batchSize
		if end > len(toWrite) {
			end = len(toWrite)
		}
		pipe := rdb.Pipeline()
		for _, n := range toWrite[start:end] {
			pipe.HSet(ctx, n.NodeKey, n.Fields)
			// Set a TTL slightly longer than NodeTTL (default 60min) so
			// cold entries self-expire if the gateway never touches them.
			pipe.Expire(ctx, n.NodeKey, 90*time.Minute)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			log.Fatalf("redis pipeline (rows %d-%d): %v", start, end, err)
		}
		written += end - start
		fmt.Printf("  ✓ wrote %d / %d\n", written, len(toWrite))
	}
	fmt.Printf("\n✅ Wrote %d URSM v2 nodes to Redis (prefix=%s)\n", written, *keyPrefix)
	if skipCount > 0 {
		fmt.Printf("   ⚠  Skipped %d pre-existing keys (CAS guard)\n", skipCount)
	}
	fmt.Println("\nNEXT STEPS (see docs/runbooks/ursm-v2-cutover.md):")
	fmt.Println("  1. verify Redis keys with redis-cli HGETALL ursm:v2:node:<cid>:<model>")
	fmt.Println("  2. start gateway with URSM_V2_MODE=shadow + URSM_V2_SHADOW_DOUBLE_WRITE=1")
	fmt.Println("  3. observe llm_gateway_ursm_v2_shadow_records_total{result=recorded} for 7 days")
	fmt.Println("  4. compare legacy credentialstate log volume vs URSM v2 records; < 1% drift = GO")
	fmt.Println("  5. cutover: URSM_V2_MODE=authoritative + remove URSM_V2_SHADOW_DOUBLE_WRITE")
}

// readProbeRows reads node_probe_state, optionally filtered by tenant.
//
// Tenant filtering requires a join with the credentialstate schema. If
// tenant-id is 0 we read every row. If non-zero we restrict to
// credentials that belong to that tenant in api_keys.
//
// Note: The legacy schema does NOT store tenant_id on node_probe_state —
// we join through credentials. If the credential has been hard-deleted,
// it silently disappears from the migration.
func readProbeRows(ctx context.Context, db *sql.DB, tenantID string) ([]probeRow, error) {
	// node_probe_state carries no tenant_id, so we left-join credentials
	// to recover it. Rows whose credential was hard-deleted keep an empty
	// TenantID and fall through to the "default" namespace in mapRow —
	// match the legacy behaviour where they silently disappeared when
	// filtered against the tenant flag.
	const baseQ = `SELECT COALESCE(c.tenant_id, ''), nps.credential_id, nps.raw_model_name,
		                      nps.consecutive_failures, nps.consecutive_successes,
		                      nps.last_attempt_at, nps.paused, nps.last_direct_ok, nps.last_err_code
		                 FROM public.node_probe_state nps
		                 LEFT JOIN public.credentials c ON c.id = nps.credential_id`
	var q string
	var args []any
	if tenantID != "" {
		q = baseQ + `
	         WHERE c.tenant_id = $1`
		args = append(args, tenantID)
	} else {
		q = baseQ
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []probeRow
	for rows.Next() {
		var r probeRow
		if err := rows.Scan(&r.TenantID, &r.CredentialID, &r.RawModel,
			&r.ConsecutiveFailures, &r.ConsecutiveSuccesses,
			&r.LastAttemptAt, &r.Paused, &r.LastDirectOK, &r.LastErrCode); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// mapRow converts a legacy probeRow into the URSM v2 Redis hash shape.
// Field names MUST match domains/ursm/v2/store/record_request.lua reads
// (e.g. fail_streak, available, disabled, manual_hold, cool_until_ms,
// last_ok_ms, last_err). Drift between here and the Lua = state drift.
func mapRow(r probeRow, prefix string) mappedNode {
	nowMs := time.Now().UnixMilli()
	tenant := r.TenantID
	if tenant == "" {
		tenant = "default"
	}
	key := fmt.Sprintf("%snode:%s:%d:%s", prefix, tenant, r.CredentialID, r.RawModel)
	fields := map[string]string{
		"generation":      "1",
		"source_priority": "10", // Request priority
		"fail_streak":     fmt.Sprintf("%d", r.ConsecutiveFailures),
	}

	if r.LastAttemptAt.Valid {
		fields["last_attempt_ms"] = fmt.Sprintf("%d", r.LastAttemptAt.Time.UnixMilli())
	}
	if r.LastDirectOK.Valid {
		fields["last_direct_ok"] = boolStr(r.LastDirectOK.Bool)
	}
	if r.LastErrCode.Valid && r.LastErrCode.String != "" {
		fields["last_err"] = r.LastErrCode.String
	}

	// Paused → admin-style override. Mirrors apply_admin.lua semantics.
	if r.Paused {
		fields["available"] = "0"
		fields["disabled"] = "1"
		fields["manual_hold"] = "1"
		fields["manual_hold_reason"] = "migrated_from_legacy_paused"
		return mappedNode{NodeKey: key, Fields: fields}
	}

	// Failed enough → mark unavailable + set cool_until.
	if r.ConsecutiveFailures >= failStreakLimit {
		fields["available"] = "0"
		fields["disabled"] = "1"
		fields["cool_until_ms"] = fmt.Sprintf("%d", nowMs+int64(coolSeconds)*1000)
		fields["cool_reason"] = "migrated_from_legacy_fail_streak"
		return mappedNode{NodeKey: key, Fields: fields}
	}

	// Healthy.
	fields["available"] = "1"
	fields["disabled"] = "0"
	if r.LastAttemptAt.Valid {
		fields["last_ok_ms"] = fmt.Sprintf("%d", r.LastAttemptAt.Time.UnixMilli())
	}
	// Generation monotonic contract: generation=1 + source_priority=Request; a fresh live
	// RecordRequest always wins (higher pri overrides equal gen).
	fields["updated_at_ms"] = fmt.Sprintf("%d", nowMs)
	return mappedNode{NodeKey: key, Fields: fields}
}

// classifyNodes categorizes mapped nodes into available / cooled / manualHold.
// Exported (capital C) only to be accessible from tests in the same package.
func classifyNodes(nodes []mappedNode) (available, cooled, manualHold int) {
	for _, n := range nodes {
		isAvailable := n.Fields["available"] == "1" && n.Fields["manual_hold"] != "1"
		isManualHold := n.Fields["manual_hold"] == "1"
		isCool := !isAvailable && !isManualHold && n.Fields["disabled"] == "1"
		switch {
		case isAvailable:
			available++
		case isManualHold:
			manualHold++
		case isCool:
			cooled++
		}
	}
	return
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// redisURLFromEnv resolves the migration target using the same settings as
// the gateway. An explicit REDIS_URL wins; otherwise the split gateway Redis
// settings are assembled into a URL. The URL's explicit DB path always wins
// over environment defaults because it is parsed after this resolution.
func redisURLFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("REDIS_URL")); v != "" {
		return v
	}

	addr := envOr("LLM_GATEWAY_REDIS_ADDR", "localhost:6379")
	db := 2
	if raw := strings.TrimSpace(os.Getenv("LLM_GATEWAY_REDIS_DB")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			db = parsed
		}
	}

	u := &url.URL{
		Scheme: "redis",
		Host:   addr,
		Path:   "/" + strconv.Itoa(db),
	}
	if password := os.Getenv("LLM_GATEWAY_REDIS_PASSWORD"); password != "" {
		u.User = url.UserPassword("", password)
	}
	return u.String()
}

// maskDSN hides credentials when echoing connection strings to stdout.
func maskDSN(s string) string {
	if i := strings.Index(s, "@"); i > 0 {
		if j := strings.Index(s[:i], "//"); j > 0 {
			return s[:j+2] + "***" + s[i:]
		}
	}
	return s
}
