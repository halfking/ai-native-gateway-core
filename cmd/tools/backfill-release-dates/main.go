// Command backfill-release-dates fills models_canonical.released_at with vendor
// official release dates. This enables lifecycle management (hiding stale models).
//
// Usage:
//
//	export DATABASE_URL="postgres://user:pass@host/db"
//	go run ./cmd/tools/backfill-release-dates --dry-run
//	go run ./cmd/tools/backfill-release-dates          # commit
//
// ★ 2026-11-14 修正（重要）：本工具**只做精确名匹配，不做前缀粗匹配**。
//
// 首版曾用 `strings.HasPrefix(name, pattern+"-")` 把 `deepseek-v4.1-flash`
// 当成 `deepseek-chat`（2024-01-08）写进库，导致 2026 年发布的模型被标成
// 2 年前、随后被路由年龄闸过滤掉——**版本号前缀相同不代表同一代模型**。
// 现在每条规则都是完整 canonical_name 的精确匹配；匹配不到就保持 NULL
// （NULL = 年龄未知 = 路由 fail-open 放行），绝不猜。
//
// Data sources (verified against vendor docs where possible):
//   - OpenAI:    platform.openai.com/docs/models
//   - Anthropic: docs.anthropic.com/en/docs/about-claude/models
//   - Google:    ai.google.dev/gemini-api/docs/models/gemini
//   - Zhipu:     open.bigmodel.cn/dev/howuse/model
//
// Safety:
//   - Only fills NULL released_at (idempotent, safe to re-run)
//   - Never overwrites a non-NULL date
//   - Exact-name only: a wrong date is worse than no date, because a wrong
//     date silently removes a live model from routing.
//
// Exit codes:
//
//	0 — success (filled count printed)
//	1 — arguments / DB / query error
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// releaseDates maps EXACT canonical_name to release date (YYYY-MM-DD).
//
// Only add an entry when the date is verifiable for that exact model name.
// Do NOT add a base-family entry expecting it to cover dated/variant names —
// variants are usually released months later and would be mis-aged.
var releaseDates = map[string]string{
	// ---- OpenAI ----
	"gpt-4o":                 "2024-05-13",
	"gpt-4o-mini":            "2024-07-18",
	"gpt-4-turbo":            "2024-04-09",
	"gpt-4":                  "2023-03-14",
	"gpt-3.5-turbo":          "2022-11-30",
	"o1":                     "2024-12-05",
	"o1-mini":                "2024-09-12",
	"o1-preview":             "2024-09-12",
	"o3-mini":                "2025-01-31",
	"o3-mini-high":           "2025-01-31",
	"whisper-1":              "2022-09-21",
	"tts-1":                  "2023-11-06",
	"text-embedding-3-small": "2024-01-25",
	"text-embedding-3-large": "2024-01-25",
	"text-embedding-ada-002": "2022-12-15",

	// ---- Anthropic ----
	"claude-3-opus":              "2024-03-04",
	"claude-3-sonnet":            "2024-03-04",
	"claude-3-haiku":             "2024-03-04",
	"claude-3-5-sonnet-20240620": "2024-06-20",
	"claude-3-5-sonnet-20241022": "2024-10-22",
	"claude-3-5-haiku-latest":    "2024-11-04",
	"claude-opus-4":              "2024-11-20",
	"claude-sonnet-4":            "2024-11-20",
	"claude-2":                   "2023-07-11",

	// ---- Google Gemini ----
	"gemini-pro":            "2023-12-06",
	"gemini-pro-vision":     "2023-12-06",
	"gemini-1.5-pro":        "2024-05-14",
	"gemini-1.5-flash":      "2024-05-14",
	"gemini-2.0-flash":      "2024-12-11",
	"gemini-2.0-flash-exp":  "2024-12-11",
	"gemini-2.0-flash-lite": "2024-12-11",

	// ---- Zhipu GLM ----
	"glm-4":         "2024-01-16",
	"glm-4v":        "2024-01-16",
	"glm-3-turbo":   "2023-10-27",
	"chatglm-turbo": "2023-10-27",

	// ---- DeepSeek (only the two first-gen names; v3.x/v4.x/r1 are newer) ----
	"deepseek-chat":  "2024-01-08",
	"deepseek-coder": "2024-01-08",

	// ---- Alibaba Qwen (2.5 generation, Sep 2024) ----
	"qwen2.5-7b-instruct":        "2024-09-19",
	"qwen2.5-32b-instruct":       "2024-09-19",
	"qwen2.5-72b-instruct":       "2024-09-19",
	"qwen2.5-coder-7b-instruct":  "2024-09-19",
	"qwen2.5-coder-32b-instruct": "2024-09-19",
	"qwen2.5-vl-72b":             "2024-09-19",
	"qwen2.5-vl-72b-instruct":    "2024-09-19",

	// ---- Cohere ----
	"command":        "2022-11-15",
	"command-r":      "2024-03-11",
	"command-r-plus": "2024-04-04",
}

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "Postgres DSN (default: $DATABASE_URL)")
	dryRun := flag.Bool("dry-run", true, "dry-run mode: print plan but don't commit")
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

	rows, err := pool.Query(ctx, `
		SELECT id, canonical_name
		FROM models_canonical
		WHERE released_at IS NULL
		  AND status != 'disabled'
		ORDER BY id
	`)
	if err != nil {
		log.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type model struct {
		id   int
		name string
	}
	var candidates []model
	for rows.Next() {
		var m model
		if err := rows.Scan(&m.id, &m.name); err != nil {
			log.Fatalf("scan: %v", err)
		}
		candidates = append(candidates, m)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	log.Printf("found %d models with released_at IS NULL", len(candidates))

	// EXACT match only — see the file header for why prefix matching is banned.
	type update struct {
		id   int
		name string
		date string
	}
	var updates []update
	for _, c := range candidates {
		if date, ok := releaseDates[c.name]; ok {
			updates = append(updates, update{id: c.id, name: c.name, date: date})
		}
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].name < updates[j].name })

	log.Printf("exact-match candidates: %d", len(updates))
	if len(updates) == 0 {
		log.Println("no updates needed; exiting")
		return
	}
	for _, u := range updates {
		fmt.Printf("  id=%-7d  %-36s  NULL → %s\n", u.id, u.name, u.date)
	}

	if *dryRun {
		log.Println("DRY-RUN mode: no changes committed")
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx) // no-op after commit

	var updated int
	for _, u := range updates {
		tag, err := tx.Exec(ctx, `
			UPDATE models_canonical
			SET released_at = $1::timestamptz, updated_at = now()
			WHERE id = $2 AND released_at IS NULL
		`, u.date, u.id)
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
