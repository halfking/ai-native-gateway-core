package bg

// feedback_analyzer.go — daily offline analysis worker for auto-route tuning.
//
// This worker runs at 02:00 UTC daily and analyzes the last 7 days of
// tuning_signals to generate improvement proposals:
//
//  1. Keyword discovery: extracts high-frequency terms from low-quality
//     requests that are NOT in the current keyword set. These are candidate
//     keywords that would improve classification accuracy.
//
//  2. Weight adjustment: detects (task_type, model) pairs where the
//     predicted match score was high but actual quality was low, suggesting
//     a weight rebalance.
//
// All proposals are written to tuning_proposals with status='pending'.
// Nothing is auto-applied — admins review via the Phase 5 API.
//
// Failure handling: transient DB errors are logged and retried next day.
// The worker never blocks request serving (it's a separate goroutine).

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// default analysis cadence and window
const (
	defaultAnalysisHour    = 2 // 02:00 UTC
	defaultAnalysisWindow  = 7 * 24 * time.Hour
	defaultAnalysisTimeout = 5 * time.Minute

	// Minimum samples for a proposal to be generated
	minKeywordSamples = 10
	minWeightSamples  = 20

	// Quality thresholds
	lowQualityThreshold  = 0.5 // requests below this are "low quality"
	highPredictedMatch   = 0.7 // predicted match above this but low actual success
	lowActualSuccess     = 0.6 // actual success below this triggers weight proposal
)

// FeedbackAnalyzer runs the daily tuning analysis.
type FeedbackAnalyzer struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	// AnalysisWindow is the lookback period. Default: 7 days.
	AnalysisWindow time.Duration

	// RunHour is the UTC hour to run (0-23). Default: 2 (02:00 UTC).
	RunHour int
}

// NewFeedbackAnalyzer constructs the worker.
func NewFeedbackAnalyzer(db *pgxpool.Pool) *FeedbackAnalyzer {
	return &FeedbackAnalyzer{
		db:             db,
		done:           make(chan struct{}),
		AnalysisWindow: defaultAnalysisWindow,
		RunHour:        defaultAnalysisHour,
	}
}

// Start spawns the background goroutine.
func (a *FeedbackAnalyzer) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	go a.run(cctx)
	slog.Info("feedback analyzer started",
		"schedule", fmt.Sprintf("%02d:00 UTC daily", a.RunHour),
		"window", a.AnalysisWindow.String(),
	)
}

// Stop terminates the goroutine and waits for it.
func (a *FeedbackAnalyzer) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	<-a.done
}

func (a *FeedbackAnalyzer) run(ctx context.Context) {
	defer close(a.done)
	for {
		now := time.Now().UTC()
		next := time.Date(now.Year(), now.Month(), now.Day(), a.RunHour, 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			if err := a.AnalyzeOnce(ctx); err != nil {
				slog.Warn("feedback analyzer run failed", "error", err)
			}
		}
	}
}

// AnalyzeOnce runs one analysis cycle. Exposed for admin-triggered
// on-demand analysis and testing.
func (a *FeedbackAnalyzer) AnalyzeOnce(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, defaultAnalysisTimeout)
	defer cancel()

	// Run both analyses; track the first error so callers can react.
	var firstErr error
	keywordCount, err := a.discoverKeywordCandidates(timeoutCtx)
	if err != nil {
		slog.Warn("keyword discovery failed", "error", err)
		firstErr = err
	}

	weightCount, err := a.discoverWeightAdjustments(timeoutCtx)
	if err != nil {
		slog.Warn("weight adjustment discovery failed", "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	slog.Info("feedback analysis complete",
		"keyword_proposals", keywordCount,
		"weight_proposals", weightCount,
	)
	return firstErr
}

// discoverKeywordCandidates finds high-frequency terms in low-quality
// requests that are not yet in the keyword set.
//
// Strategy:
//   1. Query tuning_signals for heuristic-classified requests with
//      quality_score < 0.5 in the analysis window.
//   2. Extract bigrams and trigrams from the user prompt preview.
//   3. Filter out terms already in the current keyword set.
//   4. For terms appearing >= minKeywordSamples times, generate a
//      keyword_add proposal.
func (a *FeedbackAnalyzer) discoverKeywordCandidates(ctx context.Context) (int, error) {
	windowStart := time.Now().UTC().Add(-a.AnalysisWindow)

	// Fetch low-quality request prompts grouped by task_type
	rows, err := a.db.Query(ctx, `
		SELECT
		    task_type,
		    signal_payload->>'last_user_prompt' AS prompt
		FROM tuning_signals
		WHERE ts >= $1
		  AND quality_score < $2
		  AND classifier = 'heuristic'
		  AND signal_payload->>'last_user_prompt' IS NOT NULL
	`, windowStart, lowQualityThreshold)
	if err != nil {
		return 0, fmt.Errorf("query low-quality signals: %w", err)
	}
	defer rows.Close()

	// Extract ngrams and count by task_type
	type ngramKey struct {
		task string
		word string
	}
	ngramCounts := make(map[ngramKey]int)
	for rows.Next() {
		var taskType, prompt string
		if err := rows.Scan(&taskType, &prompt); err != nil {
			continue
		}
		for _, ng := range extractCJKBigrams(prompt) {
			ngramCounts[ngramKey{taskType, ng}]++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Fetch existing keywords to exclude
	existingKeywords := make(map[string]bool)
	kwRows, err := a.db.Query(ctx, `
		SELECT value FROM tuning_params
		WHERE category = 'keywords' AND enabled = TRUE
	`)
	if err != nil {
		return 0, fmt.Errorf("query existing keywords: %w", err)
	}
	defer kwRows.Close() // defer for safety even with explicit close later
	for kwRows.Next() {
		var value []byte
		if err := kwRows.Scan(&value); err != nil {
			continue
		}
		var kws []string
		if json.Unmarshal(value, &kws) == nil {
			for _, k := range kws {
				existingKeywords[k] = true
			}
		}
	}
	if err := kwRows.Err(); err != nil {
		return 0, fmt.Errorf("iterate existing keywords: %w", err)
	}

	// Generate proposals for ngrams above threshold
	proposals := 0
	for key, count := range ngramCounts {
		if count < minKeywordSamples {
			continue
		}
		if existingKeywords[key.word] {
			continue
		}
		// Check if a pending proposal already exists for this keyword
		var exists bool
		err := a.db.QueryRow(ctx, `
			SELECT EXISTS(
			    SELECT 1 FROM tuning_proposals
			    WHERE category = 'keyword_add'
			      AND status = 'pending'
			      AND proposal->>'add' LIKE '%' || $1 || '%'
			)
		`, key.word).Scan(&exists)
		if err != nil || exists {
			continue
		}

		rationale := fmt.Sprintf(
			"'%s' appeared %d times in low-quality (score<%.1f) %s requests",
			key.word, count, lowQualityThreshold, key.task,
		)
		proposalJSON, _ := json.Marshal(map[string]any{
			"key":     fmt.Sprintf("keywords.%s", mapTaskToChannel(key.task)),
			"add":     []string{key.word},
			"channel": mapTaskToChannel(key.task),
		})
		evidenceJSON, _ := json.Marshal(map[string]any{
			"sample_count": count,
			"window_days":  int(a.AnalysisWindow.Hours() / 24),
			"quality_threshold": lowQualityThreshold,
			"rationale":    rationale,
			"confidence":   float64(count) / float64(count+20), // Bayesian-ish
		})

		_, err = a.db.Exec(ctx, `
			INSERT INTO tuning_proposals (category, task_type, proposal, evidence, status)
			VALUES ('keyword_add', $1, $2, $3, 'pending')
		`, key.task, proposalJSON, evidenceJSON)
		if err != nil {
			slog.Warn("failed to insert keyword proposal",
				"word", key.word, "error", err)
			continue
		}
		proposals++
	}

	return proposals, nil
}

// discoverWeightAdjustments detects (task_type, model) pairs where
// predicted match was high but actual success/quality was low.
func (a *FeedbackAnalyzer) discoverWeightAdjustments(ctx context.Context) (int, error) {
	windowStart := time.Now().UTC().Add(-a.AnalysisWindow)

	// Aggregate per (canonical_id, task_type) with predicted vs actual
	rows, err := a.db.Query(ctx, `
		SELECT
		    canonical_id,
		    task_type,
		    COUNT(*) AS samples,
		    AVG(CASE WHEN success_score = 1.0 THEN 1.0 ELSE 0.0 END) AS actual_success,
		    AVG(quality_score) AS avg_quality,
		    AVG((signal_payload->'candidates_top3'->0->>'match_score')::float) AS predicted_match
		FROM tuning_signals
		WHERE ts >= $1
		  AND classifier = 'heuristic'
		  AND canonical_id IS NOT NULL
		GROUP BY canonical_id, task_type
		HAVING COUNT(*) >= $2
	`, windowStart, minWeightSamples)
	if err != nil {
		return 0, fmt.Errorf("query weight analysis: %w", err)
	}
	defer rows.Close()

	proposals := 0
	for rows.Next() {
		var canonicalID int
		var taskType string
		var samples int
		var actualSuccess, avgQuality, predictedMatch float64
		if err := rows.Scan(&canonicalID, &taskType, &samples, &actualSuccess, &avgQuality, &predictedMatch); err != nil {
			continue
		}

		// Generate proposal when predicted match was high but actual success was low
		if predictedMatch > highPredictedMatch && actualSuccess < lowActualSuccess {
			// Suggest reducing the Match weight for the smart profile
			proposalJSON, _ := json.Marshal(map[string]any{
				"key":       "weights.smart",
				"dimension": "match",
				"old":       25,
				"new":       20,
				"profile":   "smart",
				"canonical_id": canonicalID,
			})
			rationale := fmt.Sprintf(
				"model#%d on %s: predicted match=%.2f but actual success=%.2f (n=%d)",
				canonicalID, taskType, predictedMatch, actualSuccess, samples,
			)
			evidenceJSON, _ := json.Marshal(map[string]any{
				"sample_count":      samples,
				"avg_quality":       avgQuality,
				"actual_success":    actualSuccess,
				"predicted_match":   predictedMatch,
				"rationale":         rationale,
				"confidence":        0.8,
			})

			_, err := a.db.Exec(ctx, `
				INSERT INTO tuning_proposals (category, task_type, proposal, evidence, status)
				VALUES ('weight_adjust', $1, $2, $3, 'pending')
			`, taskType, proposalJSON, evidenceJSON)
			if err != nil {
				slog.Warn("failed to insert weight proposal",
					"canonical_id", canonicalID, "error", err)
				continue
			}
			proposals++
		}
	}

	return proposals, rows.Err()
}

// mapTaskToChannel converts a task_type to its keyword channel name.
func mapTaskToChannel(taskType string) string {
	switch taskType {
	case "reasoning":
		return "reasoning"
	case "code":
		return "code"
	case "creative":
		return "creative"
	default:
		return "reasoning" // safe default
	}
}

// extractCJKBigrams extracts 2-character CJK bigrams and ASCII word bigrams
// from text. These are the most informative units for keyword discovery
// (single CJK chars are too generic, trigrams too specific; for ASCII we
// require words of >= 3 chars to filter out articles and short tokens).
func extractCJKBigrams(text string) []string {
	if text == "" {
		return nil
	}

	var result []string
	runes := []rune(text)

	// CJK bigrams
	for i := 0; i+1 < len(runes); i++ {
		r1, r2 := runes[i], runes[i+1]
		if isCJK(r1) && isCJK(r2) {
			result = append(result, string(r1)+string(r2))
		}
	}

	// ASCII word bigrams (for English keyword discovery)
	words := extractASCIIBigrams(text)
	result = append(result, words...)

	return result
}

// extractASCIIBigrams extracts 2-word sequences from ASCII text.
func extractASCIIBigrams(text string) []string {
	var result []string
	var words []string
	var current []byte

	for i := 0; i < len(text); i++ {
		c := text[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			if c >= 'A' && c <= 'Z' {
				c += 32 // lowercase
			}
			current = append(current, c)
		} else {
			if len(current) >= 3 {
				words = append(words, string(current))
			}
			current = current[:0]
		}
	}
	if len(current) >= 3 {
		words = append(words, string(current))
	}

	for i := 0; i+1 < len(words); i++ {
		result = append(result, words[i]+" "+words[i+1])
	}
	return result
}

// isCJK reports whether r is a CJK Unified Ideograph.
func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}
