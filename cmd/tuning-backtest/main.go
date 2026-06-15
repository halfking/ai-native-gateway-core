// Command tuning-backtest simulates the effect of candidate tuning
// proposals against historical tuning_signals.
//
// Usage:
//
//	tuning-backtest -proposal-id=N
//	  # Replays proposal N against last 7 days of signals
//	  # Outputs: "would fix X misclassifications, would miss Y"
//
//	tuning-backtest -keyword="求解方程式" -channel=reasoning
//	  # Tests a hypothetical keyword against all reasoning requests
//	  # in the last 7 days
//
//	tuning-backtest -weights="weights.smart:match=20"
//	  # Simulates a weight change
//
// This tool never modifies the live tuning_params. It only reads
// tuning_signals + requests_logs and computes the hypothetical impact.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"),
		"PostgreSQL DSN (default: $DATABASE_URL)")
	proposalID := flag.Int64("proposal-id", 0,
		"Test a specific pending proposal by ID")
	keyword := flag.String("keyword", "",
		"Test adding a single keyword to a channel")
	channel := flag.String("channel", "reasoning",
		"Channel for -keyword (reasoning/code/creative)")
	weightsArg := flag.String("weights", "",
		"Single weight change, format: 'weights.smart:match=20'")
	windowDays := flag.Int("window-days", 7,
		"Lookback window in days")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("-dsn or $DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Dispatch on mode
	switch {
	case *proposalID > 0:
		err = backtestProposal(ctx, pool, *proposalID, *windowDays)
	case *keyword != "":
		err = backtestKeyword(ctx, pool, *keyword, *channel, *windowDays)
	case *weightsArg != "":
		err = backtestWeight(ctx, pool, *weightsArg, *windowDays)
	default:
		log.Fatal("must specify -proposal-id, -keyword, or -weights")
	}

	if err != nil {
		log.Fatalf("backtest failed: %v", err)
	}
}

// ── Proposal replay ─────────────────────────────────────────────

func backtestProposal(ctx context.Context, pool *pgxpool.Pool, id int64, days int) error {
	// Fetch the proposal
	var category, status string
	var taskType *string
	var proposalJSON, evidenceJSON []byte
	err := pool.QueryRow(ctx, `
		SELECT category, task_type, proposal, evidence, status
		FROM tuning_proposals WHERE id = $1
	`, id).Scan(&category, &taskType, &proposalJSON, &evidenceJSON, &status)
	if err != nil {
		return fmt.Errorf("fetch proposal: %w", err)
	}

	fmt.Printf("Replaying proposal #%d (category=%s, status=%s)\n", id, category, status)
	if taskType != nil {
		fmt.Printf("  target task_type: %s\n", *taskType)
	}
	fmt.Printf("  proposal payload: %s\n", string(proposalJSON))
	fmt.Printf("  evidence summary:  %s\n\n", string(evidenceJSON))

	var proposal map[string]any
	if err := json.Unmarshal(proposalJSON, &proposal); err != nil {
		return fmt.Errorf("unmarshal proposal: %w", err)
	}

	switch category {
	case "keyword_add":
		return backtestKeywordAdd(ctx, pool, proposal, days)
	case "weight_adjust":
		return backtestWeightAdjust(ctx, pool, proposal, days)
	default:
		return fmt.Errorf("unsupported category for backtest: %s", category)
	}
}

func backtestKeywordAdd(ctx context.Context, pool *pgxpool.Pool, proposal map[string]any, days int) error {
	addRaw, _ := proposal["add"].([]any)
	if len(addRaw) == 0 {
		return fmt.Errorf("keyword_add proposal has no 'add' field")
	}
	newWords := make([]string, 0, len(addRaw))
	for _, a := range addRaw {
		if s, ok := a.(string); ok {
			newWords = append(newWords, s)
		}
	}

	// Re-classify all heuristic requests in the window WITHOUT the
	// candidate keywords and WITH them; report how many classifications
	// change for the better.
	windowStart := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

	rows, err := pool.Query(ctx, `
		SELECT
		    request_id,
		    task_type,
		    auto_confidence,
		    signal_payload->>'last_user_prompt' AS prompt,
		    success_score,
		    quality_score
		FROM tuning_signals
		WHERE ts >= $1
		  AND classifier = 'heuristic'
		  AND signal_payload->>'last_user_prompt' IS NOT NULL
	`, windowStart)
	if err != nil {
		return err
	}
	defer rows.Close()

	type sample struct {
		requestID, taskType, prompt string
		confidence, success, quality float64
	}

	var samples []sample
	for rows.Next() {
		var s sample
		var confidence *float64
		if err := rows.Scan(&s.requestID, &s.taskType, &confidence, &s.prompt, &s.success, &s.quality); err != nil {
			continue
		}
		if confidence != nil {
			s.confidence = *confidence
		}
		samples = append(samples, s)
	}

	// Estimate: for each sample, if the prompt contains one of the new
	// keywords AND the original quality was low, we count it as a
	// "would be reclassified" event. We can't run the actual heuristic
	// here without importing the whole autoroute package, so this is an
	// approximation.
	matched := 0
	matchedHighQuality := 0
	matchedLowQuality := 0
	for _, s := range samples {
		hit := false
		for _, kw := range newWords {
			if containsASCII(s.prompt, kw) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		matched++
		if s.quality < 0.5 {
			matchedLowQuality++
		} else {
			matchedHighQuality++
		}
	}

	fmt.Printf("Keyword backtest over last %d days:\n", days)
	fmt.Printf("  Candidate keywords:   %v\n", newWords)
	fmt.Printf("  Total heuristic reqs: %d\n", len(samples))
	fmt.Printf("  Matched (contain kw): %d (%.1f%%)\n",
		matched, 100*float64(matched)/float64(max(len(samples), 1)))
	fmt.Printf("    Low-quality (<0.5): %d ← would be reclassified\n", matchedLowQuality)
	fmt.Printf("    High-quality:       %d (false positives expected)\n", matchedHighQuality)
	fmt.Printf("  Estimated impact: %d low-quality requests reclassified ↑, %d false positives ↓\n",
		matchedLowQuality, matchedHighQuality)
	fmt.Println()
	fmt.Println("Note: this is a TEXT-MATCH approximation. For exact reclassification,")
	fmt.Println("      the autoroute library would need to be invoked with the new keyword set.")
	return nil
}

func backtestWeightAdjust(ctx context.Context, pool *pgxpool.Pool, proposal map[string]any, days int) error {
	key, _ := proposal["key"].(string)
	dim, _ := proposal["dimension"].(string)
	newVal, _ := proposal["new"].(float64)

	windowStart := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

	// Fetch all heuristic requests and the predicted match score from
	// the auto_decision JSON. Compute: "would a different weight have
	// changed the top-1 pick?" — too complex to fully simulate here, so
	// we provide a summary of the data we'd need to answer that.
	rows, err := pool.Query(ctx, `
		SELECT
		    task_type,
		    AVG(quality_score) AS avg_quality,
		    AVG(auto_confidence) AS avg_confidence,
		    COUNT(*) AS samples
		FROM tuning_signals
		WHERE ts >= $1
		  AND classifier = 'heuristic'
		GROUP BY task_type
	`, windowStart)
	if err != nil {
		return err
	}
	defer rows.Close()

	fmt.Printf("Weight-adjust backtest over last %d days:\n", days)
	fmt.Printf("  Proposed change: %s[%s] = %.2f\n", key, dim, newVal)
	fmt.Printf("  Per-task-type quality baseline:\n")
	for rows.Next() {
		var taskType string
		var avgQ, avgC *float64
		var samples int
		if err := rows.Scan(&taskType, &avgQ, &avgC, &samples); err != nil {
			continue
		}
		fmt.Printf("    %-20s n=%-6d quality=%.3f confidence=%.3f\n",
			taskType, samples, safeFloat(avgQ), safeFloat(avgC))
	}
	fmt.Println()
	fmt.Println("Note: full weight re-simulation requires re-running autoroute.Score()")
	fmt.Println("      with the proposed weights against model_task_index. See /analyze endpoint.")
	return nil
}

// ── Standalone keyword test ─────────────────────────────────────

func backtestKeyword(ctx context.Context, pool *pgxpool.Pool, keyword, channel string, days int) error {
	proposal := map[string]any{
		"add": []string{keyword},
		"key": fmt.Sprintf("keywords.%s", channel),
	}
	fmt.Printf("Standalone keyword backtest: '%s' → keywords.%s\n", keyword, channel)
	return backtestKeywordAdd(ctx, pool, proposal, days)
}

func backtestWeight(ctx context.Context, pool *pgxpool.Pool, weightsSpec string, days int) error {
	// Parse "weights.smart:match=20"
	var key, dim string
	var newVal float64
	if _, err := fmt.Sscanf(weightsSpec, "%[^:]:%[^=]=%f", &key, &dim, &newVal); err != nil {
		return fmt.Errorf("invalid -weights format (expected 'weights.smart:match=20'): %w", err)
	}
	proposal := map[string]any{
		"key":       key,
		"dimension": dim,
		"new":       newVal,
	}
	fmt.Printf("Standalone weight backtest: %s[%s] = %.2f\n", key, dim, newVal)
	return backtestWeightAdjust(ctx, pool, proposal, days)
}

// ── Helpers ─────────────────────────────────────────────────────

func containsASCII(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	// Case-insensitive ASCII substring match (CJK doesn't need case folding)
	lowHay := []byte(haystack)
	lowNee := []byte(needle)
	for i := 0; i+len(lowNee) <= len(lowHay); i++ {
		match := true
		for j := 0; j < len(lowNee); j++ {
			h := lowHay[i+j]
			n := lowNee[j]
			if h >= 'A' && h <= 'Z' {
				h += 32
			}
			if n >= 'A' && n <= 'Z' {
				n += 32
			}
			if h != n {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func safeFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
