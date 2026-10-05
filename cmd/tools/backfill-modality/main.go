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
	//
	// ★ 2026-10-05：加 modality_source 闸门。
	//
	// 本工具原先只按名字推断模态，既不看出处、也不盖出处，于是它是
	// 「按名推断 vs 语义核实」这道老冲突的**第三处**（前两处是
	// discovery 的 upsert 与 catalog.EffectiveModality，都已在迁移 825
	// 之后带上闸门）。跑一次本工具会把
	//   - 语义核实判负降级成 text 的行（source='semantic'）翻回 multimodal；
	//   - 运维 PATCH 盖过 manual 章的 text 覆盖翻回去；
	// 而且更糟：UPDATE 不写 modality_source，于是**值被按名换掉、章还留着**
	// —— provenance 从此说谎，而读路径（catalog.EffectiveModality）正是按
	// 章决定信不信值的那一处。
	rows, err := pool.Query(ctx, `
		SELECT id, canonical_name, modality, COALESCE(modality_source, '')
		FROM models_canonical
		WHERE modality = 'text'
		  AND status != 'disabled'
		  AND COALESCE(modality_source, '') NOT IN ('semantic', 'manual')
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
		modalitySource  string
	}
	var candidates []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.canonicalName, &r.currentModality, &r.modalitySource); err != nil {
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
	//
	// 闸门在 UPDATE 里**再写一遍**，不是为了防御性编程：SELECT 与 UPDATE 之间
	// 隔着一次完整的推断循环，期间核实 worker 完全可能把同一行降级成 text
	// 并盖上 semantic。只在 SELECT 筛的话，这个工具就是「读到候选之后、
	// 写下去之前」的竞态窗口。
	//
	// modality_source='inferred' 一起写：本工具产出的是**按名推断**，不是核实
	// 结论。让它留着一个 semantic/manual 的旧章，等于让出处对值说谎。
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx) // no-op if committed

	var updated, skippedStamped int
	for _, u := range upgrades {
		tag, err := tx.Exec(ctx, `
			UPDATE models_canonical
			SET modality = $1, modality_source = 'inferred', updated_at = now()
			WHERE id = $2
			  AND modality = 'text'
			  AND COALESCE(modality_source, '') NOT IN ('semantic', 'manual')
		`, u.newModality, u.id)
		if err != nil {
			log.Fatalf("update id=%d: %v", u.id, err)
		}
		if n := int(tag.RowsAffected()); n > 0 {
			updated += n
		} else {
			// 只在真的没写进去时说。这不是理论分支：候选集是 SELECT 之前
			// 算的，核实 worker 随时可能在中间把某一行判负并盖上 semantic。
			// 不说的话，「updated=0」会被读成「没有可升级的模型」，
			// 而真实原因可能是「有的，但都被标注保护住了」。
			skippedStamped++
			log.Printf("  skipped id=%d %s: 行已被 semantic/manual 标注保护，未改动",
				u.id, u.name)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("commit: %v", err)
	}

	log.Printf("backfill complete: updated=%d skipped_annotated=%d elapsed=%s",
		updated, skippedStamped, time.Since(start))
	if skippedStamped > 0 {
		log.Printf("skipped_annotated>0 说明有模型已被语义核实或人工标注，" +
			"本工具不与它们争——那是刻意设计，不是失败")
	}
}
