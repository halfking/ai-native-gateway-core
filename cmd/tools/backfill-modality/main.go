// Command backfill-modality upgrades models_canonical.modality for rows that
// are stuck at the default 'text' value but should be vision/audio/multimodal
// based on the current inference rules in modelname.InferModality.
//
// Context:
//
//	discovery.Service used `modality = COALESCE(models_canonical.modality, $4)`
//	in its ON CONFLICT upsert. Since the column is NOT NULL DEFAULT 'text', the
//	COALESCE always returned the existing value, making modality sticky. Any row
//	seeded before an inference-rule fix kept its stale value forever. This caused
//	vision-capable models like glm-4.5v to remain tagged as 'text', dropping them
//	from the routing candidate set when an image request arrived → 503.
//
//	The sticky-upsert bug was fixed in 2026-08-09, but existing production rows
//	remain stale. This tool repairs them by re-running InferModality and upgrading
//	any row still at modality='text' when inference now says otherwise.
//
// Usage:
//
//	export DATABASE_URL="postgres://user:pass@host/db"
//	go run ./cmd/tools/backfill-modality --dry-run
//	# review output, then commit:
//	go run ./cmd/tools/backfill-modality
//
// Safety:
//   - Upgrade-only: never downgrades a row (source-of-truth is the inference rules).
//   - Respects manual overrides: only touches rows where modality='text' (the column
//     default). Any super_admin PATCH sets a non-'text' value, so those are skipped.
//   - Idempotent: safe to re-run; already-upgraded rows are no-ops.
//
// Exit codes:
//
//	0 — success (upgraded count printed)
//	1 — arguments / DB / query error
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN (default: $DATABASE_URL)")
	dryRun := flag.Bool("dry-run", true, "dry-run mode: print SQL but don't commit")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("--dsn required (or set DATABASE_URL)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	start := time.Now()

	// Fetch all rows where modality='text' (candidate for upgrade).
	rows, err := pool.Query(ctx, `
		SELECT id, canonical_name, modality
		FROM models_canonical
		WHERE modality = 'text'
		  AND status != 'disabled'
		ORDER BY id
	`)
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type row struct {
		id              int
		canonicalName   string
		currentModality string
	}
	var candidates []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.canonicalName, &r.currentModality); err != nil {
			log.Fatalf("scan: %v", err)
		}
		candidates = append(candidates, r)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	log.Printf("found %d models with modality='text'", len(candidates))

	// Re-infer modality for each and collect upgrade candidates.
	type upgrade struct {
		id          int
		name        string
		newModality string
	}
	var upgrades []upgrade
	for _, c := range candidates {
		inferred := modelname.InferModality(c.canonicalName)
		if inferred != "text" {
			upgrades = append(upgrades, upgrade{
				id:          c.id,
				name:        c.canonicalName,
				newModality: inferred,
			})
		}
	}

	log.Printf("upgrade candidates: %d", len(upgrades))
	if len(upgrades) == 0 {
		log.Println("no upgrades needed; exiting")
		return
	}

	// Print upgrade plan.
	for _, u := range upgrades {
		fmt.Printf("  id=%d  %-40s  text → %s\n", u.id, u.name, u.newModality)
	}

	if *dryRun {
		log.Println("DRY-RUN mode: no changes committed")
		return
	}

	// Execute upgrades in a single transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx) // no-op if committed

	var updated int
	for _, u := range upgrades {
		tag, err := tx.Exec(ctx, `
			UPDATE models_canonical
			SET modality = $1, updated_at = now()
			WHERE id = $2
			  AND modality = 'text'
		`, u.newModality, u.id)
		if err != nil {
			log.Fatalf("update id=%d: %v", u.id, err)
		}
		updated += int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("commit: %v", err)
	}

	log.Printf("backfill complete: updated=%d elapsed=%s", updated, time.Since(start))
}
