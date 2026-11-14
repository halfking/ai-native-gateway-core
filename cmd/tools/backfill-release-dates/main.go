// Command backfill-release-dates fills models_canonical.released_at with vendor
// official release dates. This allows lifecycle management (hiding stale models).
//
// Usage:
//
//	export DATABASE_URL="postgres://user:pass@host/db"
//	go run ./cmd/tools/backfill-release-dates --dry-run
//	# review output, then commit:
//	go run ./cmd/tools/backfill-release-dates
//
// Data sources:
//   - OpenAI: https://platform.openai.com/docs/models
//   - Anthropic: https://docs.anthropic.com/en/docs/about-claude/models
//   - Google Gemini: https://ai.google.dev/gemini-api/docs/models/gemini
//   - Zhipu GLM: https://open.bigmodel.cn/dev/howuse/model
//   - Other vendors: best-effort from official docs
//
// Safety:
//   - Only fills NULL released_at (idempotent, safe to re-run)
//   - Does not touch manually set dates
//   - Upgrade-only (never changes existing non-NULL dates)
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
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// releaseDates maps canonical_name patterns to release dates.
// Dates are best-effort from official vendor timelines.
var releaseDates = map[string]string{
	// OpenAI — https://platform.openai.com/docs/models
	"gpt-4o":                  "2024-05-13",
	"gpt-4o-mini":             "2024-07-18",
	"gpt-4-turbo":             "2024-04-09",
	"gpt-4":                   "2023-03-14",
	"gpt-3.5-turbo":           "2022-11-30",
	"o1":                      "2024-12-05",
	"o1-mini":                 "2024-09-12",
	"o1-preview":              "2024-09-12",
	"o3-mini":                 "2025-01-31",
	"whisper-1":               "2022-09-21",
	"tts-1":                   "2023-11-06",
	"text-embedding-3-small": "2024-01-25",
	"text-embedding-3-large": "2024-01-25",
	"text-embedding-ada-002": "2022-12-15",

	// Anthropic — https://docs.anthropic.com/en/docs/about-claude/models
	"claude-3-opus":   "2024-03-04",
	"claude-3-sonnet": "2024-03-04",
	"claude-3-haiku":  "2024-03-13",
	"claude-3-5-sonnet-20240620": "2024-06-20",
	"claude-3-5-sonnet-20241022": "2024-10-22",
	"claude-3-5-haiku":           "2024-11-04",
	"claude-sonnet-4":            "2024-11-20", // Claude 4 release
	"claude-opus-4":              "2024-11-20",
	"claude-2":                   "2023-07-11",
	"claude-instant-1":           "2023-03-14",

	// Google Gemini — https://ai.google.dev/gemini-api/docs/models/gemini
	"gemini-pro":         "2023-12-06",
	"gemini-pro-vision":  "2023-12-06",
	"gemini-1.5-pro":     "2024-05-14",
	"gemini-1.5-flash":   "2024-05-14",
	"gemini-2.0-flash":   "2024-12-11",
	"gemini-ultra":       "2024-02-08",
	"text-embedding-004": "2024-05-14",

	// Zhipu GLM — https://open.bigmodel.cn/dev/howuse/model
	"chatglm-turbo": "2023-10-27",
	"glm-3-turbo":   "2023-10-27",
	"glm-4":         "2024-01-16",
	"glm-4v":        "2024-01-16",
	"glm-4-air":     "2024-06-05",
	"glm-4-flash":   "2024-06-05",

	// Moonshot / Kimi
	"moonshot-v1-8k":   "2023-10-09",
	"moonshot-v1-32k":  "2023-10-09",
	"moonshot-v1-128k": "2024-01-17",

	// ByteDance Doubao
	"doubao-pro-4k":      "2024-05-15",
	"doubao-lite-4k":     "2024-05-15",
	"doubao-pro-32k":     "2024-08-26",
	"doubao-pro-128k":    "2024-08-26",
	"doubao-1.5-pro-32k": "2024-11-28",

	// Alibaba Qwen
	"qwen-turbo":      "2023-09-25",
	"qwen-plus":       "2023-09-25",
	"qwen-max":        "2024-01-09",
	"qwen2-72b":       "2024-06-07",
	"qwen2.5-72b":     "2024-09-19",
	"qwen-vl-plus":    "2023-12-01",
	"qwen-vl-max":     "2023-12-01",
	"qwen-audio-turbo": "2023-11-01",

	// DeepSeek
	"deepseek-chat": "2024-01-08",
	"deepseek-coder": "2023-11-02",

	// MiniMax
	"abab5.5-chat": "2023-09-25",
	"abab6-chat":   "2024-07-01",

	// Baichuan
	"baichuan2-turbo":      "2023-09-06",
	"baichuan2-turbo-192k": "2024-05-28",

	// 01.AI Yi
	"yi-34b-chat": "2023-11-02",
	"yi-vl-plus":  "2024-01-23",

	// iFlytek Spark
	"spark-lite": "2023-05-06",
	"spark-v2":   "2023-08-15",
	"spark-v3":   "2024-01-30",
	"spark-v3.5": "2024-05-30",

	// Baidu Ernie
	"ernie-bot-turbo": "2023-07-05",
	"ernie-bot-4":     "2024-03-26",
	"ernie-speed":     "2024-03-26",

	// Tencent Hunyuan
	"hunyuan-lite":   "2023-09-07",
	"hunyuan-pro":    "2024-05-30",
	"hunyuan-turbo":  "2024-05-30",
	"hunyuan-vision": "2024-11-25",

	// SenseTime SenseNova
	"sensenova-5":    "2024-04-08",
	"sensechat-5":    "2024-04-08",
	"sensechat-lite": "2024-04-08",

	// StepFun
	"step-1-8k":       "2024-03-06",
	"step-1-32k":      "2024-03-06",
	"step-1v-8k":      "2024-04-25",
	"step-1v-32k":     "2024-04-25",
	"step-2-16k":      "2024-09-19",
	"step-1.5-mini-4k": "2024-11-21",

	// xAI Grok
	"grok-beta": "2024-08-13",

	// Mistral
	"mistral-small":  "2024-02-26",
	"mistral-medium": "2024-02-26",
	"mistral-large":  "2024-02-26",
	"mixtral-8x7b":   "2023-12-11",
	"mixtral-8x22b":  "2024-04-10",
	"pixtral-12b":    "2024-09-11",

	// Meta Llama
	"llama-2-7b":  "2023-07-18",
	"llama-2-13b": "2023-07-18",
	"llama-2-70b": "2023-07-18",
	"llama-3-8b":  "2024-04-18",
	"llama-3-70b": "2024-04-18",
	"llama-3.1-8b":  "2024-07-23",
	"llama-3.1-70b": "2024-07-23",
	"llama-3.2-1b":  "2024-09-25",
	"llama-3.2-3b":  "2024-09-25",

	// Cohere
	"command":             "2022-11-15",
	"command-light":       "2022-11-15",
	"command-r":           "2024-03-11",
	"command-r-plus":      "2024-04-04",
	"embed-english-v3.0":  "2023-11-02",
	"embed-multilingual-v3.0": "2023-11-02",
}

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

	// Fetch all models where released_at IS NULL
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
		id            int
		canonicalName string
	}
	var candidates []model
	for rows.Next() {
		var m model
		if err := rows.Scan(&m.id, &m.canonicalName); err != nil {
			log.Fatalf("scan: %v", err)
		}
		candidates = append(candidates, m)
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("rows: %v", err)
	}

	log.Printf("found %d models with released_at IS NULL", len(candidates))

	// Match models to release dates
	type update struct {
		id          int
		name        string
		releasedAt  string
		matchedKey  string
	}
	var updates []update
	for _, c := range candidates {
		// Try exact match first
		if date, ok := releaseDates[c.canonicalName]; ok {
			updates = append(updates, update{
				id:         c.id,
				name:       c.canonicalName,
				releasedAt: date,
				matchedKey: c.canonicalName,
			})
			continue
		}

		// Try prefix match (for dated variants like gpt-4o-2024-08-06)
		for pattern, date := range releaseDates {
			if strings.HasPrefix(c.canonicalName, pattern+"-") {
				updates = append(updates, update{
					id:         c.id,
					name:       c.canonicalName,
					releasedAt: date,
					matchedKey: pattern,
				})
				break
			}
		}
	}

	log.Printf("matched %d models to release dates", len(updates))
	if len(updates) == 0 {
		log.Println("no updates needed; exiting")
		return
	}

	// Print update plan
	for _, u := range updates {
		fmt.Printf("  id=%-7d  %-40s  NULL → %s  (matched: %s)\n",
			u.id, u.name, u.releasedAt, u.matchedKey)
	}

	if *dryRun {
		log.Println("DRY-RUN mode: no changes committed")
		return
	}

	// Execute updates in a single transaction
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(ctx) // no-op if committed

	var updated int
	for _, u := range updates {
		tag, err := tx.Exec(ctx, `
			UPDATE models_canonical
			SET released_at = $1, updated_at = now()
			WHERE id = $2
			  AND released_at IS NULL
		`, u.releasedAt, u.id)
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
