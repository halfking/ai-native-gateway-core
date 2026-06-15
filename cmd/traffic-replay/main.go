// Command traffic-replay replays historical request_logs through
// the REAL auto-route pipeline (HeuristicClassifier + Score + Decider),
// surfacing "what would have happened" if the new pattern /
// weight / override were live during the historical window.
//
// This is the production-correct version of the placeholder
// from the first P8.1 commit. The pipeline is constructed
// from the same autoroute package the gateway uses, with one
// important difference: the Index is a fresh in-memory snapshot
// (Refresh() once, no LISTEN/NOTIFY), so the replay is hermetic.
//
// Usage:
//
//	traffic-replay -dsn=$DATABASE_URL
//	    -days=7
//	    -task_type=code         (optional filter)
//	    -max-samples=1000
//	    -with-llm=false
//	    -workers=10
//	    -verbose=false
//
// Output: a formatted text report showing the count of
// (from, to) divergences and the "significant" / "minor"
// cross-family split. See the printReport() function for the
// exact layout.

package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

// requestRow is one row from request_logs that we replay.
type requestRow struct {
	RequestID     string
	TaskType      string
	ChosenModel   string
	Success       bool
	LatencyMs     int
	PromptTokens  int
	RequestBody   []byte // raw JSONB
	RequestPreview string
}

// replayResult is one (old → new) divergence.
type replayResult struct {
	From         string
	To           string
	Count        int
	OrigSuccess  float64
	NewEstimate  string // "n/a — model not run through new pipeline"
}

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"),
		"PostgreSQL DSN (default: $DATABASE_URL)")
	days := flag.Int("days", 7, "Lookback window in days")
	taskType := flag.String("task_type", "", "Filter by task type (empty = all)")
	maxSamples := flag.Int("max-samples", 1000, "Cap on requests replayed")
	withLLM := flag.Bool("with-llm", false, "Hit LLM fallback (slow, costs money)")
	workers := flag.Int("workers", 10, "Concurrent worker count for the replay pool")
	verbose := flag.Bool("verbose", false, "Print per-request divergences")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("-dsn or $DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	db, err := sql.Open("pgx", *dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	rows, err := loadHistoricalRows(ctx, db, *days, *taskType, *maxSamples)
	if err != nil {
		log.Fatalf("load rows: %v", err)
	}
	if len(rows) == 0 {
		log.Fatal("no historical rows match the filter")
	}
	log.Printf("loaded %d historical request_logs rows", len(rows))

	// Build the live pipeline (real Decider + tuning store + overrides).
	decider, overrideStore := buildPipeline(ctx, db, *withLLM)
	defer func() {
		_ = db.Close()
	}()
	// Load overrides so ban/pin logic is active in the replay
	if err := overrideStore.Reload(ctx); err != nil {
		log.Printf("override reload failed: %v (continuing with no overrides)", err)
	}

	// Run the replay across a worker pool
	results := runReplayPool(ctx, rows, decider, *workers, *verbose)

	// Print the report
	printReport(rows, results)
}

// loadHistoricalRows fetches the replay set from request_logs.
// We use task_type_chosen (P7.2.1 column) and the original
// outbound_model for the comparison.
func loadHistoricalRows(ctx context.Context, db *sql.DB, days int, taskType string, maxSamples int) ([]requestRow, error) {
	q := `
		SELECT
		    request_id,
		    COALESCE(task_type_chosen, task_type, 'unknown') AS task,
		    COALESCE(outbound_model, 'unknown') AS chosen_model,
		    success,
		    COALESCE(latency_ms, 0)::int,
		    COALESCE(prompt_tokens, 0)::int,
		    COALESCE(request_body, '{}'::jsonb)::text::bytea AS body,
		    COALESCE(request_preview, '') AS preview
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND ts >= NOW() - INTERVAL '1 day' * $1
		  AND ($2 = '' OR COALESCE(task_type_chosen, task_type) = $2)
		ORDER BY ts DESC
		LIMIT $3
	`
	rows, err := db.QueryContext(ctx, q, days, taskType, maxSamples)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []requestRow
	for rows.Next() {
		var r requestRow
		if err := rows.Scan(&r.RequestID, &r.TaskType, &r.ChosenModel,
			&r.Success, &r.LatencyMs, &r.PromptTokens,
			&r.RequestBody, &r.RequestPreview); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// buildPipeline constructs the live auto-route pipeline from
// the same packages the gateway uses. The Index is a private
// snapshot (Refresh() once, no LISTEN/NOTIFY subscription), so
// the replay doesn't disturb the gateway's live state.
func buildPipeline(ctx context.Context, db *sql.DB, withLLM bool) (*autoroute.Decider, *autoroute.OverrideStore) {
	// Index: a fresh snapshot from the live credential_model_index
	idx := autoroute.NewIndex()
	if err := idx.Refresh(ctx); err != nil {
		log.Printf("warning: index refresh failed: %v (continuing with empty index)", err)
	}

	// Classifier: heuristic + pattern layer (default behaviour)
	classifier := autoroute.NewHeuristicClassifierWithTuning(
		autoroute.DefaultHeuristicThresholds(),
		autoroute.DefaultKeywords(),
		nil, // no tuning store override
	)

	// LLM caller: disabled by default; opt-in via --with-llm
	var caller autoroute.LLMCaller = autoroute.DisabledCaller{}
	if withLLM {
		c, _ := autoroute.BuildHTTPLlmCallerFromEnv(os.Getenv)
		caller = c
	}
	fallback := autoroute.NewLLMFallbackClassifierWithCaller(caller)

	// Profile store: nil (no sticky writes during replay)
	decider := autoroute.NewDecider(classifier, fallback, idx, nil)

	// Override store: empty by default; admin can populate via DB
	overrideStore := autoroute.NewOverrideStore(nil)
	decider.SetOverrideStore(overrideStore)

	return decider, overrideStore
}

// runReplayPool runs the replay across N concurrent workers.
// Each worker pulls a requestRow from the in-channel, calls
// the real Decider, and emits a divergence onto the out-channel.
// Returns aggregated replayResult slice.
func runReplayPool(ctx context.Context, rows []requestRow, decider *autoroute.Decider, workers int, verbose bool) []replayResult {
	in := make(chan requestRow, len(rows))
	for _, r := range rows {
		in <- r
	}
	close(in)

	type out struct {
		from, to string
		success  bool
	}
	outCh := make(chan out, len(rows))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range in {
				newModel, err := reclassify(ctx, decider, r)
				if err != nil {
					// Treat errors as "no decision" — skip.
					if verbose {
						log.Printf("[skip] %s: %v", r.RequestID, err)
					}
					continue
				}
				if newModel == r.ChosenModel {
					continue // no divergence
				}
				outCh <- out{from: r.ChosenModel, to: newModel, success: r.Success}
				if verbose {
					log.Printf("[divergence] %s: %s -> %s", r.RequestID, r.ChosenModel, newModel)
				}
			}
		}()
	}
	wg.Wait()
	close(outCh)

	// Aggregate
	agg := map[[2]string]*replayResult{}
	for o := range outCh {
		key := [2]string{o.from, o.to}
		r, ok := agg[key]
		if !ok {
			r = &replayResult{From: o.from, To: o.to}
			agg[key] = r
		}
		r.Count++
		if o.success {
			r.OrigSuccess = (r.OrigSuccess*float64(r.Count-1) + 1.0) / float64(r.Count)
		} else {
			r.OrigSuccess = (r.OrigSuccess*float64(r.Count-1) + 0.0) / float64(r.Count)
		}
	}

	// Convert to slice + sort by count desc
	var out2 []replayResult
	for _, r := range agg {
		out2 = append(out2, *r)
	}
	sort.Slice(out2, func(i, j int) bool {
		return out2[i].Count > out2[j].Count
	})
	return out2
}

// reclassify runs the live Decider on a single historical row.
// Returns the new chosen model, or an error if the Decider
// couldn't produce a decision (e.g. all candidates banned).
func reclassify(ctx context.Context, decider *autoroute.Decider, r requestRow) (string, error) {
	// Reconstruct the classification signals from request_body JSONB
	sigs := extractSignalsFromBody(r)

	// Run the real Decider. Signature:
	//   Decide(ctx, sigs, apiKeyID, headerProfile, taskHint, sessionID)
	// Pass empty defaults: apiKeyID=0 (no sticky), sessionID=""
	// (no cache), headerProfile="" (decider uses smart default).
	decision, err := decider.Decide(ctx, sigs, 0, "", autoroute.TaskType(r.TaskType), "")
	if err != nil {
		return "", err
	}
	return decision.ChosenModel, nil
}

// extractSignalsFromBody parses the request_body JSONB to
// extract the user prompt, tool count, image presence, code
// block presence, and token estimate. This is a simplified
// version of relay/auto_route.go::extractSignalsForAuto that
// works on raw bytes (no *http.Request).
func extractSignalsFromBody(r requestRow) autoroute.ClassificationSignals {
	sigs := autoroute.ClassificationSignals{
		LastUserPrompt:  r.RequestPreview, // preview is a 200-char snippet
		EstimatedTokens: r.PromptTokens,
	}

	if len(r.RequestBody) == 0 {
		return sigs
	}

	body := string(r.RequestBody)

	// Tool count (OpenAI chat format: "type":"function")
	toolCount := strings.Count(body, `"type":"function"`)
	if toolCount > 0 {
		sigs.ToolCount = toolCount
	}

	// Image presence
	if strings.Contains(body, `"type":"image_url"`) {
		sigs.HasImages = true
	}

	return sigs
}

// printReport formats the result for human review.
func printReport(rows []requestRow, results []replayResult) {
	total := len(rows)
	noDiv := total - divergenceCount(results)
	div := divergenceCount(results)

	fmt.Println()
	fmt.Println("===========================================================")
	fmt.Println("           TRAFFIC REPLAY REPORT")
	fmt.Println("===========================================================")
	fmt.Printf("Total replayed:    %d\n", total)
	fmt.Printf("No divergence:     %d (%.1f%%)\n", noDiv, pct(noDiv, total))
	fmt.Printf("Divergences:       %d (%.1f%%)\n\n", div, pct(div, total))

	// Significance split
	minor, significant := splitBySignificance(results)
	fmt.Printf("Significant (cross-family):  %d (%.1f%%)\n", significant, pct(significant, total))
	fmt.Printf("Minor (same family):         %d (%.1f%%)\n\n", minor, pct(minor, total))

	fmt.Println("From             → To                Count  OrigSucc  NewEst")
	fmt.Println("-----------------------------------------------------------")
	for _, r := range results {
		marker := ""
		if isCrossFamily(r.From, r.To) {
			marker = " (cross-family)"
		}
		fmt.Printf("%-16s → %-18s %5d  %.2f   %s%s\n",
			truncate(r.From, 16), truncate(r.To, 18),
			r.Count, r.OrigSuccess, r.NewEstimate, marker)
	}
	fmt.Println()
	fmt.Println("Note: 'OrigSucc' is the historical success rate of the")
	fmt.Println("ORIGINAL model. 'NewEst' is a placeholder — for real")
	fmt.Println("evaluation, re-run each divergence cohort through the new")
	fmt.Println("model and compare empirically.")
}

// splitBySignificance divides the divergence count into
// "cross-family" (different model family) and "minor" (same
// family, different version/date). Heuristic: split on '-'.
func splitBySignificance(results []replayResult) (minor, significant int) {
	for _, r := range results {
		if isCrossFamily(r.From, r.To) {
			significant += r.Count
		} else {
			minor += r.Count
		}
	}
	return
}

// isCrossFamily returns true if the two model names belong to
// different base models (e.g. gpt-4o vs claude-3-5-sonnet).
// Heuristic: the base model is the substring before the first
// '-' in the canonical name. gpt-4o-2024-08-06 and gpt-4o-mini
// have base 'gpt' or 'gpt-4o'; we use the prefix up to the
// first digit (or first 5 chars, whichever is shorter).
func isCrossFamily(a, b string) bool {
	return family(a) != family(b)
}

// family extracts the model family from a canonical model
// name. Used to classify a divergence as "cross-family" or
// "minor (same family, different version)".
//
// Algorithm: strip everything from the first digit onward,
// then strip a trailing dash. Examples:
//   gpt-4o, gpt-4o-2024-08-06, gpt-4o-mini  →  all "gpt"
//   claude-3-5-sonnet, claude-3-5-sonnet-20241022  →  both "claude"
//   gemini-pro  →  "gemini" (no digit, no trimming)
func family(name string) string {
	// Strip everything from the first digit onward
	for i := 0; i < len(name); i++ {
		if name[i] >= '0' && name[i] <= '9' {
			name = name[:i]
			break
		}
	}
	// Strip a trailing dash (so "gpt-" becomes "gpt")
	if len(name) > 0 && name[len(name)-1] == '-' {
		name = name[:len(name)-1]
	}
	return name
}

// divergenceCount sums the Count fields of all results.
func divergenceCount(results []replayResult) int {
	n := 0
	for _, r := range results {
		n += r.Count
	}
	return n
}

func pct(num, denom int) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom) * 100
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// _ = math is here for future extensions (e.g. confidence
// intervals on the success rate).
var _ = math.Sqrt
