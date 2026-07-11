// Command compression-bench benchmarks the v3 SessionCompressor on
// historical request_logs from a live database.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("DATABASE_URL"), "PostgreSQL DSN")
	days := flag.Int("days", 7, "Lookback window in days")
	maxSamples := flag.Int("max-samples", 5000, "Max rows to process (0 = all)")
	protocol := flag.String("protocol", "openai", "Protocol: openai or anthropic-messages")
	contextWindow := flag.Int("context-window", 128000, "Model context window in tokens")
	output := flag.String("output", "", "Write results JSON to this path (optional)")
	skipLLMSummary := flag.Bool("skip-llm-summary", false, "Skip LLM-summary path (test mechanical only)")
	shareSession := flag.Bool("share-session", false, "Use one session ID for all rows; requires --serial")
	serialExec := flag.Bool("serial", false, "Execute rows serially to preserve each session's turn ordering")
	testMode := flag.String("test-mode", "prepare", "Test mode: 'prepare' (full pipeline) or 'mechanical' (direct trim test)")
	tempTable := flag.String("temp-table", fmt.Sprintf("compression_bench_results_%d", time.Now().Unix()), "Results table name")
	keepResults := flag.Bool("keep-results", false, "Keep the results table after reporting")
	flag.Parse()

	if *dsn == "" {
		log.Fatal("-dsn or $DATABASE_URL is required")
	}
	if *shareSession && !*serialExec {
		log.Fatal("--share-session requires --serial to preserve turn ordering")
	}
	if *testMode != "prepare" && *testMode != "mechanical" {
		log.Fatal("--test-mode must be prepare or mechanical")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("ping database: %v", err)
	}

	// The production cache starts with the same L1 tier. L2/L3 are intentionally
	// disabled for an isolated replay, but state must persist between turns.
	deps := compression.SessionCompressorDeps{
		Cache:    compression.NewSessionCache(nil, nil),
		Disabled: false,
	}
	if *skipLLMSummary {
		deps.CompactionDeps = nil
	}
	sc := compression.NewSessionCompressor(deps)

	// Load historical rows
	rows, err := loadRequestLogs(ctx, pool, *days, *maxSamples)
	if err != nil {
		log.Fatalf("load request_logs: %v", err)
	}
	log.Printf("loaded %d request_logs rows", len(rows))

	// When share-session is set, use a single session so rows form a conversation sequence
	sharedSessionID := ""
	if *shareSession {
		b := make([]byte, 16)
		rand.Read(b)
		sharedSessionID = hex.EncodeToString(b)
		sharedTenantID := "bench-shared"
		for i := range rows {
			rows[i].GwSessionID = sharedSessionID
			rows[i].TenantID = sharedTenantID
		}
		log.Printf("share-session enabled: all rows use session_id=%s", sharedSessionID[:16])
	}

	// Create temp results table
	if err := createTempTable(ctx, pool, *tempTable); err != nil {
		log.Fatalf("create temp table: %v", err)
	}
	if !*keepResults {
		defer dropResultsTable(ctx, pool, *tempTable)
	}

	// Process each row
	// A historical replay must preserve turn order. Parallelizing individual
	// records can make a later turn observe stale session state and invalidates
	// delta-append measurements. Use independent process runs for throughput.
	if !*serialExec {
		log.Printf("serial execution enforced for valid session replay")
	}
	results := make([]benchResult, 0, len(rows))
	var totalBytes, totalTokens int64
	for _, row := range rows {
		res := processRow(ctx, sc, row, *protocol, *contextWindow, *testMode)
		results = append(results, res)
		totalBytes += int64(res.BytesBefore)
		totalTokens += int64(res.TokensBefore)
	}

	log.Printf("processed %d rows, inserting into %s", len(results), *tempTable)

	// Insert results into DB
	if err := insertResults(ctx, pool, *tempTable, results); err != nil {
		log.Fatalf("insert results: %v", err)
	}

	// Compute summary statistics
	summary := computeSummary(results, totalBytes, totalTokens)

	// Print report
	printReport(summary, results, *protocol, *contextWindow)

	// Write JSON output if requested
	if *output != "" {
		if err := writeJSON(*output, summary, results); err != nil {
			log.Printf("write JSON: %v", err)
		} else {
			log.Printf("results written to %s", *output)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Data loading
// ──────────────────────────────────────────────────────────────────────────────

type requestLogRow struct {
	ID             int64
	RequestID      string
	TenantID       string
	GwSessionID    string
	OutboundBody   []byte
	OutboundTokens int
	MsgCount       int
	CompressionStr string
	Ts             time.Time
}

func loadRequestLogs(ctx context.Context, pool *pgxpool.Pool, days, maxSamples int) ([]requestLogRow, error) {
	limitClause := ""
	if maxSamples > 0 {
		limitClause = fmt.Sprintf("LIMIT %d", maxSamples)
	}

	q := fmt.Sprintf(`
		SELECT
			id,
			request_id,
			tenant_id,
			COALESCE(gw_session_id, '') AS gw_session_id,
			COALESCE(outbound_body::text, request_body::text, '{}') AS body,
			COALESCE(outbound_token_est, 0) AS outbound_tokens,
			COALESCE(outbound_msg_count, 0) AS msg_count,
			COALESCE(compression_strategy, '') AS compression_strategy,
			ts
		FROM request_logs
		WHERE ts >= NOW() - INTERVAL '1 day' * $1
		  AND (outbound_body IS NOT NULL OR request_body IS NOT NULL)
		ORDER BY ts ASC
		%s
	`, limitClause)

	rows, err := pool.Query(ctx, q, days)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	var out []requestLogRow
	for rows.Next() {
		var r requestLogRow
		if err := rows.Scan(
			&r.ID, &r.RequestID, &r.TenantID, &r.GwSessionID,
			&r.OutboundBody, &r.OutboundTokens, &r.MsgCount,
			&r.CompressionStr, &r.Ts,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if r.GwSessionID == "" {
			continue // SessionCompressor intentionally has no state without a session ID.
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ──────────────────────────────────────────────────────────────────────────────
// Processing
// ──────────────────────────────────────────────────────────────────────────────

type benchResult struct {
	RowID       int64     `json:"row_id"`
	RequestID   string    `json:"request_id"`
	TenantID    string    `json:"tenant_id"`
	GwSessionID string    `json:"gw_session_id"`
	Ts          time.Time `json:"ts"`

	BytesBefore    int    `json:"bytes_before"`
	TokensBefore   int    `json:"tokens_before"`
	MsgsBefore     int    `json:"msgs_before"`
	ProtocolBefore string `json:"protocol_before"`

	BytesAfter    int    `json:"bytes_after"`
	TokensAfter   int    `json:"tokens_after"`
	MsgsAfter     int    `json:"msgs_after"`
	ProtocolAfter string `json:"protocol_after"`

	Strategy        string `json:"strategy"`
	WindowTriggered string `json:"window_triggered"`
	SummaryMarker   string `json:"summary_marker"`
	Degraded        bool   `json:"degraded"`
	Lossiness       string `json:"lossiness"`

	BytesRatio  float64 `json:"bytes_ratio"`
	TokensRatio float64 `json:"tokens_ratio"`
	MsgsRatio   float64 `json:"msgs_ratio"`
	BytesSaved  int     `json:"bytes_saved"`
	TokensSaved int     `json:"tokens_saved"`
	MsgsSaved   int     `json:"msgs_saved"`
}

func processRow(ctx context.Context, sc *compression.SessionCompressor, row requestLogRow, protocol string, contextWindow int, testMode string) benchResult {
	res := benchResult{
		RowID:          row.ID,
		RequestID:      row.RequestID,
		TenantID:       row.TenantID,
		GwSessionID:    row.GwSessionID,
		Ts:             row.Ts,
		BytesBefore:    len(row.OutboundBody),
		TokensBefore:   row.OutboundTokens,
		MsgsBefore:     row.MsgCount,
		ProtocolBefore: protocol,
		ProtocolAfter:  protocol,
	}
	if res.TokensBefore == 0 {
		res.TokensBefore = trimmedTokenEst(row.OutboundBody)
	}
	if res.MsgsBefore == 0 {
		res.MsgsBefore = trimmedMsgCount(row.OutboundBody)
	}

	if len(row.OutboundBody) == 0 {
		res.Strategy = "empty_input"
		res.BytesRatio = 1.0
		res.TokensRatio = 1.0
		res.MsgsRatio = 1.0
		return res
	}

	if testMode == "mechanical" {
		// Direct mechanical trim test — bypasses SessionCompressor state requirements
		trimmed := compression.CompressMessagesIfNeededBody(row.OutboundBody, contextWindow)
		if len(trimmed) < len(row.OutboundBody) {
			res.BytesAfter = len(trimmed)
			res.TokensAfter = trimmedTokenEst(trimmed)
			res.MsgsAfter = trimmedMsgCount(trimmed)
			res.Strategy = "mechanical_trim"
			res.Lossiness = "tail"
		} else {
			res.BytesAfter = res.BytesBefore
			res.TokensAfter = res.TokensBefore
			res.MsgsAfter = res.MsgsBefore
			res.Strategy = "noop"
			res.Lossiness = "none"
		}
	} else {
		prepRes := sc.Prepare(ctx, row.OutboundBody, row.TenantID, row.GwSessionID, protocol, contextWindow, false)
		if prepRes.OutboundBody != nil {
			res.BytesAfter = len(prepRes.OutboundBody)
		} else {
			res.BytesAfter = res.BytesBefore
		}
		res.TokensAfter = prepRes.TokenEst
		res.MsgsAfter = prepRes.MsgCount
		res.Strategy = prepRes.CompressionStrategy
		res.WindowTriggered = prepRes.WindowTriggered
		res.SummaryMarker = prepRes.SummaryMarker
		res.Degraded = prepRes.Degraded
		res.Lossiness = prepRes.Lossiness
	}

	if res.BytesBefore > 0 {
		res.BytesRatio = float64(res.BytesAfter) / float64(res.BytesBefore)
	}
	if res.TokensBefore > 0 {
		res.TokensRatio = float64(res.TokensAfter) / float64(res.TokensBefore)
	}
	if res.MsgsBefore > 0 {
		res.MsgsRatio = float64(res.MsgsAfter) / float64(res.MsgsBefore)
	}
	res.BytesSaved = res.BytesBefore - res.BytesAfter
	res.TokensSaved = res.TokensBefore - res.TokensAfter
	res.MsgsSaved = res.MsgsBefore - res.MsgsAfter

	return res
}

func trimmedTokenEst(body []byte) int { return len(body) * 10 / 35 }

func trimmedMsgCount(body []byte) int {
	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return 0
	}
	return len(probe.Messages)
}

// ──────────────────────────────────────────────────────────────────────────────
// Temp table
// ──────────────────────────────────────────────────────────────────────────────

func createTempTable(ctx context.Context, pool *pgxpool.Pool, tableName string) error {
	q := fmt.Sprintf(`
		CREATE TABLE %s (
			id              BIGSERIAL PRIMARY KEY,
			row_id          BIGINT,
			request_id      TEXT,
			tenant_id       TEXT,
			gw_session_id   TEXT,
			ts              TIMESTAMPTZ,
			bytes_before    INT,
			tokens_before   INT,
			msgs_before     INT,
			bytes_after     INT,
			tokens_after    INT,
			msgs_after      INT,
			bytes_ratio     FLOAT,
			tokens_ratio    FLOAT,
			msgs_ratio      FLOAT,
			bytes_saved     INT,
			tokens_saved    INT,
			msgs_saved      INT,
			strategy        TEXT,
			window_triggered TEXT,
			summary_marker  TEXT,
			degraded        BOOLEAN,
			lossiness       TEXT,
			protocol        TEXT,
			created_at      TIMESTAMPTZ DEFAULT NOW()
		)
	`, pgx.Identifier{tableName}.Sanitize())
	_, err := pool.Exec(ctx, q)
	return err
}

func dropResultsTable(ctx context.Context, pool *pgxpool.Pool, tableName string) {
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+pgx.Identifier{tableName}.Sanitize()); err != nil {
		log.Printf("drop results table %q: %v", tableName, err)
	}
}

func insertResults(ctx context.Context, pool *pgxpool.Pool, tableName string, results []benchResult) error {
	batch := &pgxbatch{pool: pool, tableName: tableName}
	for _, r := range results {
		batch.results = append(batch.results, r)
		if len(batch.results) >= 500 {
			if err := batch.flush(ctx); err != nil {
				return err
			}
		}
	}
	return batch.flush(ctx)
}

type pgxbatch struct {
	pool      *pgxpool.Pool
	tableName string
	results   []benchResult
}

func (b *pgxbatch) flush(ctx context.Context) error {
	if len(b.results) == 0 {
		return nil
	}
	copy := b.results
	b.results = nil

	cols := []string{
		"row_id", "request_id", "tenant_id", "gw_session_id", "ts",
		"bytes_before", "tokens_before", "msgs_before",
		"bytes_after", "tokens_after", "msgs_after",
		"bytes_ratio", "tokens_ratio", "msgs_ratio",
		"bytes_saved", "tokens_saved", "msgs_saved",
		"strategy", "window_triggered", "summary_marker",
		"degraded", "lossiness", "protocol",
	}

	_, err := b.pool.CopyFrom(
		ctx,
		pgx.Identifier{b.tableName},
		cols,
		&benchResultCopyFrom{results: copy},
	)
	return err
}

type benchResultCopyFrom struct {
	results []benchResult
	idx     int
}

func (r *benchResultCopyFrom) Err() error { return nil }

func (r *benchResultCopyFrom) Next() bool {
	r.idx++
	return r.idx <= len(r.results)
}

func (r *benchResultCopyFrom) Values() ([]any, error) {
	row := r.results[r.idx-1]
	return []any{
		row.RowID, row.RequestID, row.TenantID, row.GwSessionID, row.Ts,
		row.BytesBefore, row.TokensBefore, row.MsgsBefore,
		row.BytesAfter, row.TokensAfter, row.MsgsAfter,
		row.BytesRatio, row.TokensRatio, row.MsgsRatio,
		row.BytesSaved, row.TokensSaved, row.MsgsSaved,
		row.Strategy, row.WindowTriggered, row.SummaryMarker,
		row.Degraded, row.Lossiness, row.ProtocolBefore,
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Summary statistics
// ──────────────────────────────────────────────────────────────────────────────

type summary struct {
	TotalRows       int
	StrategyCounts  map[string]int
	LossinessCounts map[string]int
	DegradedCount   int

	MedianBytesRatio float64
	P50BytesRatio    float64
	P90BytesRatio    float64
	P95BytesRatio    float64
	P99BytesRatio    float64
	MinBytesRatio    float64
	MaxBytesRatio    float64

	MedianTokensRatio float64
	MedianMsgsRatio   float64

	TotalBytesBefore  int64
	TotalBytesAfter   int64
	TotalTokensBefore int64
	TotalTokensAfter  int64
	TotalMsgsBefore   int
	TotalMsgsAfter    int

	OverallBytesRatio  float64
	OverallTokensRatio float64
	OverallMsgsRatio   float64

	StrategyStats map[string]strategyStat
}

type strategyStat struct {
	Count            int
	AvgBytesRatio    float64
	AvgTokensRatio   float64
	AvgMsgsRatio     float64
	TotalBytesSaved  int64
	TotalTokensSaved int64
}

func computeSummary(results []benchResult, totalBytes, totalTokens int64) summary {
	s := summary{
		TotalRows:         len(results),
		StrategyCounts:    make(map[string]int),
		LossinessCounts:   make(map[string]int),
		StrategyStats:     make(map[string]strategyStat),
		TotalBytesBefore:  totalBytes,
		TotalTokensBefore: totalTokens,
	}

	var bytesRatios, tokensRatios, msgsRatios []float64
	var totalMsgsBefore, totalMsgsAfter int

	for _, r := range results {
		s.StrategyCounts[r.Strategy]++
		s.LossinessCounts[r.Lossiness]++
		if r.Degraded {
			s.DegradedCount++
		}

		bytesRatios = append(bytesRatios, r.BytesRatio)
		tokensRatios = append(tokensRatios, r.TokensRatio)
		msgsRatios = append(msgsRatios, r.MsgsRatio)

		totalMsgsBefore += r.MsgsBefore
		totalMsgsAfter += r.MsgsAfter

		stat := s.StrategyStats[r.Strategy]
		stat.Count++
		stat.AvgBytesRatio += r.BytesRatio
		stat.AvgTokensRatio += r.TokensRatio
		stat.AvgMsgsRatio += r.MsgsRatio
		stat.TotalBytesSaved += int64(r.BytesSaved)
		stat.TotalTokensSaved += int64(r.TokensSaved)
		s.StrategyStats[r.Strategy] = stat
	}

	if len(results) > 0 {
		for name, stat := range s.StrategyStats {
			stat.AvgBytesRatio /= float64(stat.Count)
			stat.AvgTokensRatio /= float64(stat.Count)
			stat.AvgMsgsRatio /= float64(stat.Count)
			s.StrategyStats[name] = stat
		}
	}

	sort.Float64s(bytesRatios)
	s.P50BytesRatio = percentile(bytesRatios, 0.50)
	s.P90BytesRatio = percentile(bytesRatios, 0.90)
	s.P95BytesRatio = percentile(bytesRatios, 0.95)
	s.P99BytesRatio = percentile(bytesRatios, 0.99)
	if len(bytesRatios) > 0 {
		s.MinBytesRatio = bytesRatios[0]
		s.MaxBytesRatio = bytesRatios[len(bytesRatios)-1]
		s.MedianBytesRatio = s.P50BytesRatio
	}

	sort.Float64s(tokensRatios)
	s.MedianTokensRatio = percentile(tokensRatios, 0.50)

	sort.Float64s(msgsRatios)
	s.MedianMsgsRatio = percentile(msgsRatios, 0.50)

	s.TotalMsgsBefore = totalMsgsBefore
	s.TotalMsgsAfter = totalMsgsAfter

	var totalBytesAfter, totalTokensAfter int64
	for _, r := range results {
		totalBytesAfter += int64(r.BytesAfter)
		totalTokensAfter += int64(r.TokensAfter)
	}
	s.TotalBytesAfter = totalBytesAfter
	s.TotalTokensAfter = totalTokensAfter

	if totalBytes > 0 {
		s.OverallBytesRatio = float64(totalBytesAfter) / float64(totalBytes)
	}
	if totalTokens > 0 {
		s.OverallTokensRatio = float64(totalTokensAfter) / float64(totalTokens)
	}
	if totalMsgsBefore > 0 {
		s.OverallMsgsRatio = float64(totalMsgsAfter) / float64(totalMsgsBefore)
	}

	return s
}

func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(len(sorted))*q)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ──────────────────────────────────────────────────────────────────────────────
// Reporting
// ──────────────────────────────────────────────────────────────────────────────

func printReport(s summary, results []benchResult, protocol string, contextWindow int) {
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("          SESSION COMPRESSION BENCHMARK REPORT")
	fmt.Println("============================================================")
	fmt.Printf("Total rows processed:     %d\n", s.TotalRows)
	fmt.Printf("Protocol:                 %s\n", protocol)
	fmt.Printf("Context window:           %d tokens\n", contextWindow)
	fmt.Println()

	fmt.Println("--- Strategy Distribution ---")
	strategies := make([]string, 0, len(s.StrategyCounts))
	for name := range s.StrategyCounts {
		strategies = append(strategies, name)
	}
	sort.Strings(strategies)
	for _, name := range strategies {
		count := s.StrategyCounts[name]
		pct := float64(count) / float64(s.TotalRows) * 100
		fmt.Printf("  %-30s %5d (%5.1f%%)\n", name, count, pct)
	}
	fmt.Println()

	fmt.Println("--- Lossiness Distribution ---")
	lossinesses := make([]string, 0, len(s.LossinessCounts))
	for name := range s.LossinessCounts {
		lossinesses = append(lossinesses, name)
	}
	sort.Strings(lossinesses)
	for _, name := range lossinesses {
		count := s.LossinessCounts[name]
		pct := float64(count) / float64(s.TotalRows) * 100
		fmt.Printf("  %-30s %5d (%5.1f%%)\n", name, count, pct)
	}
	if s.DegradedCount > 0 {
		fmt.Printf("  %-30s %5d (degraded mode fired)\n", "degraded", s.DegradedCount)
	}
	fmt.Println()

	fmt.Println("--- Compression Ratios (after/before) ---")
	fmt.Printf("  Overall bytes ratio:     %.4f  (saved %d bytes)\n",
		s.OverallBytesRatio, s.TotalBytesBefore-s.TotalBytesAfter)
	fmt.Printf("  Overall tokens ratio:    %.4f  (saved %d tokens)\n",
		s.OverallTokensRatio, s.TotalTokensBefore-s.TotalTokensAfter)
	fmt.Printf("  Overall msgs ratio:      %.4f  (saved %d msgs)\n",
		s.OverallMsgsRatio, s.TotalMsgsBefore-s.TotalMsgsAfter)
	fmt.Println()
	fmt.Printf("  Median bytes ratio (P50): %.4f\n", s.MedianBytesRatio)
	fmt.Printf("  P90 bytes ratio:          %.4f\n", s.P90BytesRatio)
	fmt.Printf("  P95 bytes ratio:          %.4f\n", s.P95BytesRatio)
	fmt.Printf("  P99 bytes ratio:          %.4f\n", s.P99BytesRatio)
	fmt.Printf("  Min/Max bytes ratio:      %.4f / %.4f\n", s.MinBytesRatio, s.MaxBytesRatio)
	fmt.Println()

	fmt.Println("--- Per-Strategy Breakdown ---")
	fmt.Printf("%-25s %6s %10s %10s %10s %12s\n",
		"Strategy", "Count", "AvgBytesR", "AvgTokensR", "AvgMsgsR", "BytesSaved")
	fmt.Println(strings.Repeat("-", 75))
	for _, name := range strategies {
		stat := s.StrategyStats[name]
		fmt.Printf("%-25s %6d %10.4f %10.4f %10.4f %12d\n",
			name, stat.Count, stat.AvgBytesRatio, stat.AvgTokensRatio,
			stat.AvgMsgsRatio, stat.TotalBytesSaved)
	}
	fmt.Println()

	printSuggestions(s, results)
}

func printSuggestions(s summary, results []benchResult) {
	fmt.Println("--- Optimisation Suggestions ---")

	noopCount := s.StrategyCounts[""]
	noopPct := float64(noopCount) / float64(s.TotalRows) * 100
	if noopPct > 50 {
		fmt.Printf("⚠ High no-op rate: %.1f%% of rows had no compression. Consider tuning the trigger threshold.\n", noopPct)
	}

	degradedPct := float64(s.DegradedCount) / float64(s.TotalRows) * 100
	if degradedPct > 10 {
		fmt.Printf("⚠ Degraded mode usage: %.1f%% of compressions fell back to mechanical trim. LLM summary may be failing.\n", degradedPct)
	}

	swCount := 0
	swNoEffect := 0
	for _, r := range results {
		if strings.HasPrefix(r.Strategy, "sliding_window") {
			swCount++
			if r.BytesRatio > 0.95 {
				swNoEffect++
			}
		}
	}
	if swCount > 0 && float64(swNoEffect)/float64(swCount) > 0.3 {
		fmt.Printf("⚠ Sliding window: %.1f%% of triggered compressions had minimal effect (ratio > 0.95). Consider adjusting window size.\n",
			float64(swNoEffect)/float64(swCount)*100)
	}

	tailCount := s.LossinessCounts["tail"]
	tailPct := float64(tailCount) / float64(s.TotalRows) * 100
	if tailPct > 30 {
		fmt.Printf("⚠ High tail lossiness: %.1f%% of compressions used mechanical trim. LLM summary could preserve more.\n", tailPct)
	}

	var largeMsgDrops []benchResult
	for _, r := range results {
		if r.MsgsSaved > 20 {
			largeMsgDrops = append(largeMsgDrops, r)
		}
	}
	if len(largeMsgDrops) > 0 {
		fmt.Printf("ℹ %d sessions dropped >20 messages. Consider increasing the window size for long conversations.\n", len(largeMsgDrops))
	}

	fmt.Println()
	fmt.Println("=== Summary ===")
	overallSave := 1 - s.OverallBytesRatio
	if overallSave > 0.05 {
		fmt.Printf("✅ Compression is effective: %.1f%% average byte reduction\n", overallSave*100)
	} else if overallSave > 0 {
		fmt.Printf("⚠ Compression has minor effect: %.1f%% average byte reduction\n", overallSave*100)
	} else {
		fmt.Printf("❌ Compression is not reducing size. Check trigger conditions.\n")
	}

	fmt.Println()
	fmt.Println("Top strategies by bytes saved:")
	type stratEff struct {
		Name       string
		BytesSaved int64
		Count      int
		AvgBytesR  float64
	}
	var effs []stratEff
	for name, stat := range s.StrategyStats {
		if stat.Count > 0 {
			effs = append(effs, stratEff{name, stat.TotalBytesSaved, stat.Count, stat.AvgBytesRatio})
		}
	}
	sort.Slice(effs, func(i, j int) bool {
		return effs[i].BytesSaved > effs[j].BytesSaved
	})
	for i, e := range effs[:min(5, len(effs))] {
		fmt.Printf("  %d. %s: %d bytes saved (count=%d, avg ratio=%.4f)\n",
			i+1, e.Name, e.BytesSaved, e.Count, e.AvgBytesR)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ──────────────────────────────────────────────────────────────────────────────
// JSON output
// ──────────────────────────────────────────────────────────────────────────────

func writeJSON(path string, s summary, results []benchResult) error {
	type output struct {
		Summary summary       `json:"summary"`
		Results []benchResult `json:"results"`
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(output{Summary: s, Results: results})
}
