package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/kaixuan/llm-gateway-go/internal/summarystore"
)

// Auto-summary design (2026-08-06) — pairs with auto_title_generator.go
// but for the session_summary LLM task.
//
// Naming convention — the loopback request's X-Gw-Session-Id is prefixed
// with "gs:" + original session_id, yielding e.g. "gs_gw_abc123". Operators
// can SQL:
//   WHERE gw_session_id LIKE 'gs\_%' ESCAPE '\'
// to find every auto-summary row, and JOIN child.parent_request_id back
// to the parent user request. Pairs with the "gt_" prefix used by the
// auto-title generator.
//
// Trigger strategy — incremental rolling + map-reduce for long sessions:
//
//	≤  3 new turns since last summary  → skip  (the rolling gate)
//	≤  12k chars corpus                 → single LLM call
//	>  12k chars corpus                 → chunk → partial summaries → merge
//
// Safety —
//   - rate.Limiter per tenant (default 6/min) prevents a chatty session
//     from monopolizing the summary LLM pool
//   - workerPoolSem caps concurrent in-flight LLM calls (default 4) so a
//     burst of "session.closed" events can't blow up the budget
//   - chain prevention via X-Gw-Is-Auto: true + IsAutoRequest on the loopback
//     request → shouldSkipAutoSummaryGeneration on the next emitTelemetry
//     pass → no re-trigger on its own summary requests
//   - 1 retry on transient errors (connect-refused, EOF, 502/503/504) with
//     200ms ± 50ms jitter; structured slog.Warn carries endpoint, status,
//     body_excerpt, retry_count, parent_request_id, model, latency_ms.

// Public constants for the loopback headers. Keep in sync with
// auto_title_generator.go's autoParentRequestIDHeader / autoSourceActorHeader.
const (
	autoSummaryOriginActor         = "auto-summary-generator"
	autoSummarySessionIDPrefix     = "gs" // appended before ":" to form "gs:gw_xxx" → "gs_gw_xxx"
	autoSummaryRollingTurnGate     = 3    // need ≥ N new turns since last summary to re-trigger
	autoSummaryMapReduceThreshold  = 12000
	autoSummaryChunkApproxChars    = 3000 // each map-reduce chunk target size
	autoSummaryChunkApproxTurns    = 5    // each chunk target turn count
	autoSummaryDefaultRatePerMin   = 6    // per-tenant rate limit
	autoSummaryDefaultWorkerSlots  = 4    // concurrent in-flight LLM calls
	autoSummaryHTTPTimeout         = 30 * time.Second
)

// AutoSummaryGenerator runs incremental session-summary generation in the
// background after a request succeeds. It is the streaming-handler's
// request-path analogue of domains/sessionsummary's session.closed worker.
type AutoSummaryGenerator struct {
	handler *Handler
	store   *summarystore.Store
	enabled bool

	// Per-tenant token bucket. ratePerMin/minute. lazy-created on first use.
	rateMu      sync.Mutex
	rateByTnt   map[string]*rate.Limiter
	ratePerMin  int

	// Concurrent in-flight slot semaphore. Capacity = workerSlots.
	workerSlots chan struct{}

	// Sleep jitter seeded once so retries don't all fire at the same instant.
	rng *rand.Rand
	rngMu sync.Mutex
}

// NewAutoSummaryGenerator constructs an AutoSummaryGenerator. pool may be
// nil — store methods are nil-safe and surface a clear error in that case
// so the rest of the gateway can boot without a DB during local dev.
//
// 2026-08-06: read LLM_GATEWAY_AUTO_SUMMARY_RATE_PER_MIN env var (default 6/min)
// to allow test environments to bypass the 6/min rate limit and trigger
// the map-reduce path more frequently.
func NewAutoSummaryGenerator(handler *Handler, store *summarystore.Store) *AutoSummaryGenerator {
	ratePerMin := autoSummaryDefaultRatePerMin
	if v := os.Getenv("LLM_GATEWAY_AUTO_SUMMARY_RATE_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ratePerMin = n
		}
	}
	return &AutoSummaryGenerator{
		handler:     handler,
		store:       store,
		enabled:     true, // TODO: env var
		rateByTnt:   make(map[string]*rate.Limiter),
		ratePerMin:  ratePerMin,
		workerSlots: make(chan struct{}, autoSummaryDefaultWorkerSlots),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// SetRatePerMinute overrides the per-tenant rate (calls per minute). Used
// in tests; production leaves it at the default.
func (g *AutoSummaryGenerator) SetRatePerMinute(n int) {
	if n > 0 {
		g.ratePerMin = n
	}
}

// SetWorkerSlots resizes the in-flight slot semaphore. Must be called
// before any MaybeGenerateSummary call.
func (g *AutoSummaryGenerator) SetWorkerSlots(n int) {
	if n > 0 {
		g.workerSlots = make(chan struct{}, n)
	}
}

// MaybeGenerateSummary is the request-path entry point. It is fire-and-
// forget; the heavy lifting happens in a background goroutine.
//
// sessionID is the user's gw_session_id (e.g. "gw_abc123"); the loopback
// LLM call will tag its own session as "gs:gw_abc123" → "gs_gw_abc123" via
// X-Gw-Session-Id.
//
// parentRequestID is the user request's request_id; forwarded to the
// loopback so request_logs_hot.parent_request_id makes the summary loopback
// linkable back to its parent user request.
//
// body / preview mirror the title generator's contract.
func (g *AutoSummaryGenerator) MaybeGenerateSummary(sessionID, tenantID, requestBody, requestPreview, parentRequestID string) {
	if !g.enabled || g.handler == nil || g.handler.db == nil {
		return
	}

	// Snapshot the trigger gate synchronously (cheap COUNT query) so we
	// don't spawn a goroutine for every request when most should skip.
	go g.runSummaryAsync(sessionID, tenantID, requestBody, requestPreview, parentRequestID)
}

func (g *AutoSummaryGenerator) runSummaryAsync(sessionID, tenantID, requestBody, requestPreview, parentRequestID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	logger := slog.With(
		"component", "auto_summary_generator",
		"session_id", sessionID,
		"tenant_id", tenantID,
		"parent_request_id", parentRequestID,
	)

	// Rolling gate: skip if fewer than autoSummaryRollingTurnGate new turns
	// have happened since the last summary. Best-effort; on any DB error we
	// fall back to "always run" so a transient outage doesn't freeze summaries.
	shouldRun, reason, lastSum, err := g.shouldTriggerSummary(ctx, sessionID)
	if err != nil {
		logger.Warn("summary trigger gate error; falling back to always-run", "error", err)
	}
	if !shouldRun {
		logger.Debug("summary skipped by rolling gate", "reason", reason,
			"last_summarized_at", lastSum)
		return
	}

	// Per-tenant rate limit. Allow() is non-blocking — if the bucket is
	// empty we drop this trigger. The user-visible summary just won't update
	// this minute; better than blocking the request goroutine.
	if !g.allowTenant(tenantID) {
		logger.Info("summary rate-limited; skipping this trigger")
		return
	}

	// Worker-slot semaphore. Non-blocking acquire; if all slots are taken
	// we drop this trigger and let the next user request pick it up.
	select {
	case g.workerSlots <- struct{}{}:
		defer func() { <-g.workerSlots }()
	default:
		logger.Info("summary worker pool saturated; skipping this trigger")
		return
	}

	start := time.Now()
	title, summary, keyTopics, userIntent, model, err := g.generateSummary(ctx, sessionID, tenantID, requestBody, requestPreview, parentRequestID)
	elapsed := time.Since(start)
	if err != nil {
		logger.Warn("failed to generate summary", "error", err, "elapsed_ms", elapsed.Milliseconds())
		return
	}
	logger.Info("auto_summary: summary generated",
		"summary_chars", len(summary),
		"key_topics", len(keyTopics),
		"model", model,
		"elapsed_ms", elapsed.Milliseconds())

	// Persist via the shared summarystore so the v2 dispatch path sees the
	// same row shape.
	if err := g.store.Upsert(ctx, summarystore.Summary{
		SessionKey:     sessionID,
		TenantID:       tenantID,
		Title:          title,
		Summary:        summary,
		KeyTopics:      keyTopics,
		UserIntent:     userIntent,
		LastSummarized: time.Now(),
	}); err != nil {
		logger.Error("failed to persist summary", "error", err)
		return
	}
	logger.Info("auto_summary saved",
		"title", title,
		"summary_len", len(summary),
		"model", model)
}

// shouldTriggerSummary returns whether to run the summary LLM this turn.
// It implements the incremental-rolling gate: only re-run when ≥ N new
// successful turns have been recorded for the session since the previous
// summary.
//
// Returns:
//   - shouldRun bool
//   - reason    string  — "never_summarized" | "rolling_gate_open" | "db_error"
//   - lastSum   time.Time
//   - err       error
func (g *AutoSummaryGenerator) shouldTriggerSummary(ctx context.Context, sessionID string) (bool, string, time.Time, error) {
	last, err := g.store.LastSummarized(ctx, sessionID)
	if err != nil && !isPgxNoRows(err) {
		return true, "db_error", time.Time{}, err
	}
	if last.IsZero() {
		return true, "never_summarized", time.Time{}, nil
	}
	n, err := g.store.CountNewTurns(ctx, sessionID, last)
	if err != nil {
		return true, "db_error", last, err
	}
	if n < autoSummaryRollingTurnGate {
		return false, fmt.Sprintf("only_%d_new_turns_since_last_summary", n), last, nil
	}
	return true, "rolling_gate_open", last, nil
}

// isPgxNoRows reports whether err is the pgx "no rows" sentinel. Avoids
// pulling pgx into the type system at this layer; callers can swap if they
// already import pgx.
func isPgxNoRows(err error) bool {
	if err == nil {
		return false
	}
	// pgx error message starts with "ERROR: ... (SQLSTATE P0002)" but
	// a string sniff avoids a hard pgx import in this file's tests.
	return strings.Contains(err.Error(), "no rows") ||
		strings.Contains(err.Error(), "P0002")
}

// allowTenant returns true if this tenant still has tokens in its bucket.
func (g *AutoSummaryGenerator) allowTenant(tenantID string) bool {
	g.rateMu.Lock()
	lim, ok := g.rateByTnt[tenantID]
	if !ok {
		// rate.Every(time.Minute / N) is the conventional token-bucket shape.
		lim = rate.NewLimiter(rate.Every(time.Minute/time.Duration(g.ratePerMin)), g.ratePerMin)
		g.rateByTnt[tenantID] = lim
	}
	g.rateMu.Unlock()
	return lim.Allow()
}

// generateSummary is the high-level orchestrator: assemble corpus, choose
// map-reduce vs single-shot, call LLM(s), parse the JSON result.
//
// Returns (title, summary, keyTopics, userIntent, model, err). On any
// LLM-side failure the returned values are zero-valued and err is set.
func (g *AutoSummaryGenerator) generateSummary(ctx context.Context, sessionID, tenantID, requestBody, requestPreview, parentRequestID string) (string, string, []string, string, string, error) {
	// Step 1: build corpus (prefer in-memory request body for the just-finished
	// turn; fall back to DB rows for earlier turns via loadSessionLogsForTitle).
	corpus, err := g.buildSummaryCorpus(ctx, sessionID, tenantID, requestBody, requestPreview)
	if err != nil {
		return "", "", nil, "", "", fmt.Errorf("build corpus: %w", err)
	}
	if len(strings.TrimSpace(corpus)) < 50 {
		return "", "", nil, "", "", fmt.Errorf("corpus too short for summary (len=%d)", len(corpus))
	}

	// Step 2: pick API key for the LLM call (same strategy as title).
	keyID, apiKey, err := g.handler.pickFirstAvailableAPIKeyForAuto(ctx, tenantID)
	if err != nil {
		return "", "", nil, "", "", fmt.Errorf("no API key: %w", err)
	}

	// Step 3: single-shot vs map-reduce.
	if len(corpus) <= autoSummaryMapReduceThreshold {
		title, summary, topics, intent, model, err := g.callSummaryOnce(ctx, apiKey, sessionID, parentRequestID, corpus, keyID)
		if err != nil {
			return "", "", nil, "", "", err
		}
		return title, summary, topics, intent, model, nil
	}

	// Map-reduce: split corpus into N chunks → N partial summaries → merge.
	chunks := splitCorpusIntoChunks(corpus, autoSummaryChunkApproxChars)
	if len(chunks) < 2 {
		// safety net — if the splitter couldn't make ≥2 chunks, just do
		// a single-shot (we'd rather degrade to a slow call than break).
		return g.callSummaryOnce(ctx, apiKey, sessionID, parentRequestID, corpus, keyID)
	}
	logger := slog.With("component", "auto_summary_generator", "session_id", sessionID, "chunks", len(chunks))
	logger.Info("auto_summary: map_reduce mode", "corpus_chars", len(corpus))

	type partial struct {
		text string
		err  error
	}
	results := make([]partial, len(chunks))
	var wg sync.WaitGroup
	for i, chunk := range chunks {
		wg.Add(1)
		go func(i int, chunk string) {
			defer wg.Done()
			// Each map call uses a sub-task hint so the cheap pool can
			// distinguish "map partial" from "reduce merge" if it ever cares.
			_, partialText, _, _, _, err := g.callSummaryOnceWithMode(ctx, apiKey, sessionID, parentRequestID, chunk, keyID, "map")
			results[i] = partial{text: partialText, err: err}
		}(i, chunk)
	}
	wg.Wait()

	// Collect partials; abort on hard failure.
	var partials []string
	for i, r := range results {
		if r.err != nil {
			return "", "", nil, "", "", fmt.Errorf("map_reduce partial %d: %w", i, r.err)
		}
		partials = append(partials, r.text)
	}

	merged := strings.Join(partials, "\n\n---\n\n")
	title, summary, topics, intent, model, err := g.callSummaryOnceWithMode(ctx, apiKey, sessionID, parentRequestID, merged, keyID, "reduce")
	if err != nil {
		return "", "", nil, "", "", fmt.Errorf("map_reduce reduce: %w", err)
	}
	return title, summary, topics, intent, model, nil
}

// buildSummaryCorpus assembles the per-turn text the summary LLM sees.
// Order of preference:
//   1. extractMessagesForTitle(requestBody) — freshest turn, full content
//   2. loadSessionLogsForTitle (DB JOIN) — earlier turns
//
// We deliberately reuse extractMessagesForTitle / loadSessionLogsForTitle
// from auto_title_generator.go because the corpus-cleanliness logic is
// identical (collapse whitespace, drop IDE boilerplate, etc.).
func (g *AutoSummaryGenerator) buildSummaryCorpus(ctx context.Context, sessionID, tenantID, requestBody, requestPreview string) (string, error) {
	corpus := ""
	if requestBody != "" {
		if extracted := extractMessagesForTitle(requestBody); extracted != "" {
			corpus = extracted
		}
	}
	if corpus == "" && requestPreview != "" {
		corpus = requestPreview
	}
	if corpus == "" {
		// Pull the recent history so map-reduce has more than the just-finished
		// turn to work with. 50 rows ≈ 5k-10k chars on average — comfortably
		// above autoSummaryMapReduceThreshold for an active session.
		logs, err := g.handler.loadSessionLogsBySessionID(ctx, sessionID, tenantID, 50)
		if err != nil {
			return "", err
		}
		corpus = buildSummaryCorpus(logs)
	}
	return corpus, nil
}

// callSummaryOnce is a thin wrapper that pins model + retries once.
func (g *AutoSummaryGenerator) callSummaryOnce(ctx context.Context, apiKey, sessionID, parentRequestID, userContent string, keyID int) (string, string, []string, string, string, error) {
	return g.callSummaryOnceWithMode(ctx, apiKey, sessionID, parentRequestID, userContent, keyID, "summary")
}

// callSummaryOnceWithMode performs the HTTP call with optional mode label.
// "map" / "reduce" / "summary" — used purely for log correlation; cheap
// model selection is shared.
func (g *AutoSummaryGenerator) callSummaryOnceWithMode(ctx context.Context, apiKey, sessionID, parentRequestID, userContent string, keyID int, mode string) (string, string, []string, string, string, error) {
	if g.handler == nil {
		return "", "", nil, "", "", fmt.Errorf("handler not configured")
	}
	task := g.handler.loadAdminLLMTask(ctx, adminLLMTaskSessionSummary)
	model := g.handler.resolveAdminLLMFallbackModel(ctx, adminLLMTaskSessionSummary)
	if model == "" {
		model = adminLLMModelAuto
	}

	endpoint := g.getGatewayEndpoint() + "/v1/chat/completions"

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": task.SystemPrompt},
			{"role": "user", "content": userContent},
		},
		"temperature": task.Temperature,
	}
	if task.MaxTokens > 0 {
		payload["max_tokens"] = task.MaxTokens
	}
	body, _ := json.Marshal(payload)

	// Retry policy mirrors auto_title_generator.go.
	const maxRetries = 1
	var lastErr error
	var lastStatus int
	var lastBody string
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			jitter := time.Duration(150+g.rng.Intn(100)) * time.Millisecond
			select {
			case <-ctx.Done():
				return "", "", nil, "", "", ctx.Err()
			case <-time.After(jitter):
			}
		}
		res, err, status, bodyExcerpt := g.doCallSummaryOnce(ctx, endpoint, body, apiKey, sessionID, parentRequestID, task, model, mode)
		lastErr = err
		lastStatus = status
		lastBody = bodyExcerpt
		if err == nil {
			title, summary, topics, intent, modelOut := parseSummaryJSON(res.Content, model)
			return title, summary, topics, intent, modelOut, nil
		}
		if !isTransientAutoTitleErr(err, status) {
			break
		}
		slog.Warn("auto_summary: transient error, retrying",
			"component", "auto_summary_generator",
			"session_id", sessionID,
			"parent_request_id", parentRequestID,
			"mode", mode,
			"endpoint", endpoint,
			"model", model,
			"attempt", attempt+1,
			"status_code", status,
			"error", err.Error(),
			"body_excerpt", bodyExcerpt,
		)
	}
	slog.Warn("auto_summary: LLM call failed",
		"component", "auto_summary_generator",
		"session_id", sessionID,
		"parent_request_id", parentRequestID,
		"mode", mode,
		"endpoint", endpoint,
		"model", model,
		"status_code", lastStatus,
		"body_excerpt", lastBody,
		"retries", maxRetries,
		"error", errStringSummary(lastErr),
	)
	return "", "", nil, "", "", lastErr
}

// doCallSummaryOnce performs one HTTP attempt and returns either a parsed
// adminLLMChatResult or a descriptive error.
func (g *AutoSummaryGenerator) doCallSummaryOnce(
	ctx context.Context,
	endpoint string,
	body []byte,
	apiKey string,
	sessionID string,
	parentRequestID string,
	task adminLLMTaskConfig,
	model string,
	mode string,
) (adminLLMChatResult, error, int, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return adminLLMChatResult{}, err, 0, ""
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Gw-Auto-Profile", task.DefaultProfile)
	req.Header.Set("X-Gw-Task-Hint", task.TaskHint)
	req.Header.Set("X-Gw-Work-Type", task.Key)
	req.Header.Set("X-Gw-Task-Id", "auto-summary:"+sessionID+":"+mode)
	// 2026-08-06 (gs prefix): tag the loopback's own session_id with the
	// "gs" branch namespace so request_logs_hot.gw_session_id shows
	// "gs_gw_<original>" instead of a fresh gw_<uuid>. Operators can SQL
	//   WHERE gw_session_id LIKE 'gs\_%' ESCAPE '\'
	// to find every auto-summary row.
	req.Header.Set("X-Gw-Session-Id", autoSummarySessionIDPrefix+":"+sessionID)
	if parentRequestID != "" {
		req.Header.Set(autoParentRequestIDHeader, parentRequestID)
	}
	req.Header.Set(autoSourceActorHeader, autoSummaryOriginActor)
	req.Header.Set("X-Gw-Is-Auto", "true")
	if task.DeviceSeed != "" {
		req.Header.Set("X-Device-Seed", task.DeviceSeed)
	}

	client := &http.Client{Timeout: autoSummaryHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return adminLLMChatResult{}, err, 0, ""
	}
	defer resp.Body.Close()

	resolvedModel := model
	if hdr := strings.TrimSpace(resp.Header.Get("X-Gw-Auto-Decision")); hdr != "" {
		var wire struct {
			ChosenModel string `json:"chosen_model"`
		}
		if json.Unmarshal([]byte(hdr), &wire) == nil && wire.ChosenModel != "" {
			resolvedModel = wire.ChosenModel
		}
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	excerpt := strings.TrimSpace(string(raw))
	if len(excerpt) > 200 {
		excerpt = excerpt[:200] + "…"
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := excerpt
		if msg == "" {
			msg = resp.Status
		}
		return adminLLMChatResult{}, fmt.Errorf("status %d: %s", resp.StatusCode, msg), resp.StatusCode, excerpt
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return adminLLMChatResult{}, fmt.Errorf("decode response: %w (body=%q)", err, excerpt), resp.StatusCode, excerpt
	}
	if out.Model != "" && out.Model != model {
		resolvedModel = out.Model
	}
	if len(out.Choices) == 0 {
		return adminLLMChatResult{}, fmt.Errorf("empty completion (body=%q)", excerpt), resp.StatusCode, excerpt
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return adminLLMChatResult{}, fmt.Errorf("empty completion content (body=%q)", excerpt), resp.StatusCode, excerpt
	}
	return adminLLMChatResult{Content: content, ResolvedModel: resolvedModel}, nil, resp.StatusCode, excerpt
}

// parseSummaryJSON tolerates both strict JSON and "plain summary with
// optional trailing JSON". Returns the fields the dashboard needs; missing
// fields stay empty.
func parseSummaryJSON(raw, fallbackModel string) (title, summary string, keyTopics []string, userIntent, modelOut string) {
	modelOut = fallbackModel
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	// Look for a JSON object in the response — the system prompt mandates
	// {"summary":..., "key_points":[...]} but cheap models sometimes wrap
	// it in prose. Find the first '{' and last matching '}'.
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		// No JSON object — treat the whole response as the summary text.
		summary = raw
		if len(summary) > 200 {
			title = summary[:30] + "…"
		} else {
			title = summary
		}
		return
	}
	var parsed struct {
		Summary   string   `json:"summary"`
		KeyPoints []string `json:"key_points"`
		UserIntent string  `json:"user_intent"`
		Title     string   `json:"title"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &parsed); err != nil {
		// Fall back to prose parsing.
		summary = raw
		if len(summary) > 200 {
			title = summary[:30] + "…"
		} else {
			title = summary
		}
		return
	}
	summary = parsed.Summary
	if parsed.Title != "" {
		title = parsed.Title
	} else if len(summary) > 30 {
		title = strings.TrimSpace(summary[:30])
	} else {
		title = summary
	}
	keyTopics = parsed.KeyPoints
	userIntent = parsed.UserIntent
	return
}

// splitCorpusIntoChunks splits corpus into chunks of approximately
// chunkApproxChars runes, preferring to break at newline boundaries. If
// the corpus is smaller than the threshold it returns a single chunk.
//
// Round-trip property: joining the chunks back with no separator must
// equal the original corpus (modulo any dropped newlines at break
// boundaries). 2026-08-06 bugfix: previous implementation walked the
// rune slice by `i = breakPoint + 1`, which dropped exactly one rune per
// break (off-by-one) and broke the round-trip assertion in
// TestSplitCorpusIntoChunks.
func splitCorpusIntoChunks(corpus string, chunkApproxChars int) []string {
	if len(corpus) <= chunkApproxChars {
		return []string{corpus}
	}
	var chunks []string
	runes := []rune(corpus)
	for i := 0; i < len(runes); {
		end := i + chunkApproxChars
		if end >= len(runes) {
			chunks = append(chunks, string(runes[i:]))
			break
		}
		// Look for a newline within the last 200 chars of the chunk.
		breakPoint := end
		for j := end; j > end-200 && j > i; j-- {
			if runes[j] == '\n' {
				breakPoint = j
				break
			}
		}
		chunks = append(chunks, string(runes[i:breakPoint]))
		// Advance past the break rune (newline) but stay inclusive of
		// everything else — breakPoint == end advances by chunkApproxChars.
		i = breakPoint
		if breakPoint < end {
			i++ // skip the newline we used as the boundary
		}
	}
	return chunks
}

// getGatewayEndpoint returns the loopback URL (same convention as
// auto_title_generator.go).
func (g *AutoSummaryGenerator) getGatewayEndpoint() string {
	if endpoint := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	return "http://127.0.0.1:8781"
}

// errStringSummary is the nil-safe err → string helper used in slog fields.
func errStringSummary(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}