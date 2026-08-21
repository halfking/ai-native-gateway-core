package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/kaixuan/llm-gateway-go/internal/titlestore"
	"github.com/kaixuan/llm-gateway-go/metrics"
)

// 2026-08-06: title-generator caller identity for request_logs.origin_actor.
// Operators can SQL `WHERE origin_actor = 'auto-title-generator'` to find every
// auto-title loopback request and its parent_request_id link.
const autoTitleOriginActor = "auto-title-generator"

// 2026-08-06: header names carried into the loopback request so the handler
// can record parent/actor correlation. Must stay in sync with
// domains/streaming/handler.go (header read) and the X-Gw-* convention used
// by other internal callers (node_probe, credential_selfcheck, etc.).
const (
	autoParentRequestIDHeader = "X-Gw-Parent-Request-Id"
	autoSourceActorHeader     = "X-Gw-Source-Actor"
)

// Key shape uses the shared Cluster-safe builder and a stable logical key.
// The kind keeps auto-title and manual-title independently concurrency-gated.
func titleDistLockKey(kind, taskID, sessionID string) string {
	logicalKey := strings.TrimSpace(taskID) + "\x00" + strings.TrimSpace(sessionID)
	return distlock.BuildKey("title:"+strings.TrimSpace(kind), logicalKey)
}

// AutoTitleGenerator handles automatic session title generation.
// It runs asynchronously after the first request in a session completes.
type AutoTitleGenerator struct {
	handler        *Handler
	enabled        bool
	firstTurnCheck func(sessionID, tenantID, requestID string) bool
}

// NewAutoTitleGenerator creates a new auto title generator.
func NewAutoTitleGenerator(handler *Handler) *AutoTitleGenerator {
	return &AutoTitleGenerator{
		handler: handler,
		enabled: readAutoGeneratorEnabled("LLM_GATEWAY_AUTO_TITLE_ENABLED", true),
	}
}

// readAutoGeneratorEnabled returns the default-enabled state unless the
// environment contains a valid boolean override.
func readAutoGeneratorEnabled(envName string, fallback bool) bool {
	raw, ok := os.LookupEnv(envName)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		slog.Warn("auto generator enabled flag invalid; using default",
			"env", envName,
			"value", raw,
			"fallback", fallback,
		)
		return fallback
	}
	return value
}

// successful user turn in its session before generating a title. Continuing
// sessions keep their existing title until a summary refreshes it.
// requestBody is the full (redacted) inbound request body JSON — used to
// extract the actual user message for title generation. requestPreview is the
// 320-byte summary used as fallback.
// parentRequestID (added 2026-08-06) is the user request's request_id; we
// forward it as X-Gw-Parent-Request-Id so request_logs_hot.parent_request_id
// makes the title loopback linkable back to its parent user request.
// taskID (added 2026-08-06) is the session's gw_task_id from request_logs.
// It is stored alongside the title so the request-logs list JOIN
// (admin/logs.go requestLogsJoins) can match on (task_id, scoped_session_id);
// historically auto titles were hardcoded to task_id='auto', which never
// matched the request's actual gw_task_id (typically 'default') and made the
// list show no titles. Empty taskID falls back to 'auto' to keep the legacy
// marker for any caller that cannot resolve a real task.
// This function is fire-and-forget and will not block the main request path.
func (g *AutoTitleGenerator) MaybeGenerateTitle(sessionID, tenantID, taskID, requestBody, requestPreview, parentRequestID, requestID string) {
	if !g.enabled || g.handler == nil || g.handler.db == nil {
		return
	}
	if strings.TrimSpace(sessionID) == "" || !g.isFirstSuccessfulUserTurn(sessionID, tenantID, requestID) {
		return
	}

	// 2026-08-19: short-user-message gate. If the LAST user message in the
	// request body already fits inside the title budget (sessionTitleMaxRunes),
	// the LLM round-trip would only produce a title no shorter than the input.
	// Skip the goroutine entirely so we don't pay an upstream LLM call on a
	// single-line user prompt. Falls through to the normal pipeline when the
	// body has no parseable user message (preview/DB fallback still runs).
	if n := extractLastUserMessageRuneCount(requestBody); n > 0 && n <= sessionTitleMaxRunes {
		metrics.AutoTitleTrigger.WithLabelValues("short_user_message").Inc()
		slog.Debug("auto_title: user message already short, skipping LLM call",
			"component", "auto_title_generator",
			"session_id", sessionID,
			"tenant_id", tenantID,
			"parent_request_id", parentRequestID,
			"user_msg_runes", n,
			"title_budget_runes", sessionTitleMaxRunes,
		)
		return
	}

	// Run in a separate goroutine to avoid blocking
	go g.generateTitleAsync(sessionID, tenantID, taskID, requestBody, requestPreview, parentRequestID)
}

// isFirstSuccessfulUserTurn determines title eligibility from persisted
// session history. A completed request is eligible when there is no earlier
// successful non-internal request in the same session. Excluding the current
// request keeps this check reliable even when request-log persistence lags the
// telemetry callback. The session_titles conflict guard still makes concurrent
// first-turn attempts idempotent.
func (g *AutoTitleGenerator) isFirstSuccessfulUserTurn(sessionID, tenantID, requestID string) bool {
	if g.firstTurnCheck != nil {
		return g.firstTurnCheck(sessionID, tenantID, requestID)
	}
	if strings.TrimSpace(requestID) == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var hasPrior bool
	err := g.handler.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM request_logs_with_current_month
			WHERE gw_session_id = $1
			  AND tenant_id = $2
			  AND success = TRUE
			  AND COALESCE(is_auto_request, FALSE) = FALSE
			  AND request_id <> $3
		)
	`, sessionID, tenantID, requestID).Scan(&hasPrior)
	if err != nil {
		slog.Warn("auto_title: first-turn check unavailable; skipping title generation",
			"session_id", sessionID, "tenant_id", tenantID, "request_id", requestID, "error", err)
		return false
	}
	return !hasPrior
}

func (g *AutoTitleGenerator) generateTitleAsync(sessionID, tenantID, taskID, requestBody, requestPreview, parentRequestID string) {
	// Use background context with timeout (not tied to the request context)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Add structured logging for observability
	logger := slog.With(
		"component", "auto_title_generator",
		"session_id", sessionID,
		"tenant_id", tenantID,
		"parent_request_id", parentRequestID,
	)

	// 2026-08-19: per-session, per-trigger-type distributed lock.
	//
	// Why: two concurrent first-turn requests (client retry, racing
	// sessions, multi-replica deployment) used to both pass
	// isFirstSuccessfulUserTurn, both spawn a goroutine, both invoke
	// the LLM, and both attempt the same INSERT — wasting N-1 upstream
	// LLM calls per duplicate. The session_titles ON CONFLICT DO NOTHING
	// guard kept the DB consistent but did nothing to suppress the
	// upstream chatter.
	//
	// Semantics (admin/distlock):
	//   - Acquire returns either a leader handle (proceed) or a
	//     follower handle (wait, re-check, skip-or-give-up).
	//   - Leader defers Release; follower Releases too (no-op).
	//   - Redis errors fall through to "proceed without lock" so a
	//     Redis outage cannot stop title generation — the DB ON CONFLICT
	//     guard is the final correctness guarantee.
	var leaderHandle *distlock.Handle
	mgr := g.handler.titleDistLock
	if mgr != nil {
		key := titleDistLockKey("auto", taskID, sessionID)
		h, lerr := mgr.Acquire(ctx, distlock.AcquireOpts{
			Key:   key,
			TTL:   60 * time.Second,
			Scope: "auto",
		})
		if lerr != nil && !errors.Is(lerr, distlock.ErrNotEnabled) {
			// Soft-fail: log and proceed so Redis outages don't block
			// title generation. DB ON CONFLICT keeps us consistent.
			logger.Warn("auto_title: distlock acquire failed; proceeding without lock",
				"key", key, "error", lerr)
		} else if h != nil {
			defer h.Release(context.Background())
			if h.IsLeader() {
				leaderHandle = h
			}
			if !h.IsLeader() {
				// Follower: wait for leader to release, then re-check.
				// Re-check covers two cases:
				//   - Leader wrote the title → skip this round.
				//   - Leader failed → skip this round; the next
				//     first-turn request will retry.
				waitErr := h.Wait(ctx)
				hasTitle, terr := g.checkSessionHasTitle(ctx, tenantID, taskID, sessionID)

				if terr != nil {
					logger.Warn("auto_title: follower re-check failed; skipping round",
						"wait_err", waitErr, "check_err", terr)
					return
				}
				if hasTitle {
					logger.Debug("auto_title: follower skipped; leader already saved title",
						"wait_err", waitErr)
					return
				}
				// Leader didn't save a title (failed, or wait
				// errored). Give up this round — better than
				// hammering an already-stuck upstream. The next
				// first-turn request will retry.
				logger.Info("auto_title: follower skipping after leader release without title",
					"wait_err", waitErr)
				return
			}
		}
	}

	// Step 1: Check if title already exists (avoid duplicate work)
	hasTitle, err := g.checkSessionHasTitle(ctx, tenantID, taskID, sessionID)
	if err != nil {
		logger.Warn("failed to check existing title", "error", err)
		return
	}
	if hasTitle {
		logger.Debug("session already has title, skipping")
		return
	}

	start := time.Now()
	title, model, keyID, err := g.generateTitleFromFirstRequest(ctx, sessionID, tenantID, requestBody, requestPreview, parentRequestID)
	elapsed := time.Since(start)
	if err != nil {
		metrics.AutoTitleTrigger.WithLabelValues("error").Inc()
		logger.Warn("failed to generate title from first request", "error", err, "elapsed_ms", elapsed.Milliseconds())
		return
	}
	logger.Info("auto_title: title generated", "title", title, "model", model, "elapsed_ms", elapsed.Milliseconds())
	metrics.AutoTitleTrigger.WithLabelValues("ok").Inc()

	// Step 2: Save title to database (with conflict handling).
	// ON CONFLICT DO NOTHING: if another goroutine already saved a title for
	// this session, we keep theirs and discard ours (first writer wins).
	if leaderHandle != nil {
		if err := leaderHandle.Check(ctx); err != nil {
			logger.Warn("auto_title: lease lost before save", "error", err)
			return
		}
	}
	if g.handler.titleStore != nil {
		owner := fmt.Sprintf("auto-title:%s:%d", sessionID, time.Now().UnixNano())
		claim, claimErr := g.handler.titleStore.BeginMutation(ctx, titlestore.Claim{
			TenantID: tenantID, SessionID: sessionID, Owner: owner,
			TTL: 60 * time.Second, Source: titlestore.SourceAutoTitle,
			SourcePriority: titlestore.SourcePriorityAuto, TaskID: taskID,
		})
		if claimErr != nil {
			logger.Info("auto_title: canonical mutation blocked", "error", claimErr)
			return
		}
		if _, commitErr := g.handler.titleStore.CommitTitle(ctx, claim, tenantID, sessionID, title, taskID); commitErr != nil {
			logger.Info("auto_title: canonical commit blocked", "error", commitErr)
			return
		}
	} else if err := g.saveSessionTitle(ctx, sessionID, taskID, title, model, keyID); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			logger.Debug("title already saved by another goroutine")
			return
		}
		logger.Error("failed to save session title", "error", err)
		return
	}

	logger.Info("auto title saved successfully", "title", title, "length", len(title), "model", model)
}

func (g *AutoTitleGenerator) checkSessionHasTitle(ctx context.Context, tenantID, taskID, sessionID string) (bool, error) {
	if g.handler.titleStore != nil {
		st, err := g.handler.titleStore.Get(ctx, tenantID, sessionID)
		if err == nil {
			return st.Deleted || strings.TrimSpace(st.Title) != "", nil
		}
	}
	if strings.TrimSpace(taskID) == "" {
		taskID = "auto"
	}
	var exists bool
	err := g.handler.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM session_titles
			WHERE task_id = $1 AND scoped_session_id = $2
		)
	`, taskID, sessionID).Scan(&exists)
	return exists, err
}

// generateTitleFromFirstRequest loads session logs and generates a title using LLM.
// v4 (2026-08-05): Accept full requestBody to extract real user messages.
// v5 (2026-08-06): Accept parentRequestID so callAutoTitleLLM can forward it
// as X-Gw-Parent-Request-Id for request_logs_hot.parent_request_id linkage.
// The requestPreview (320-byte summary) is used as fallback only.
// Returns (title, model, apiKeyID, error). On fallback-extract paths model is
// "auto-extract" and apiKeyID is 0.
func (g *AutoTitleGenerator) generateTitleFromFirstRequest(ctx context.Context, sessionID, tenantID, requestBody, requestPreview, parentRequestID string) (string, string, int, error) {
	logger := slog.With("component", "auto_title_generator", "session_id", sessionID)

	// Step 1: Build corpus — prefer full request body (has real user message),
	// fall back to 320-byte preview, then best-effort DB logs.
	corpus := ""
	corpusSource := ""

	// Try extracting a clean conversation summary from the full request body.
	// This gives us the actual user message (not truncated to 320 bytes).
	if requestBody != "" {
		if extracted := extractMessagesForTitle(requestBody); extracted != "" {
			corpus = extracted
			corpusSource = "request_body"
		} else {
			logger.Debug("extractMessagesForTitle returned empty from request body",
				"body_len", len(requestBody),
				"body_head", truncateForLog(requestBody, 200))
		}
	}

	// Fall back to the 320-byte preview if full body extraction yielded nothing.
	if corpus == "" && requestPreview != "" {
		corpus = requestPreview
		corpusSource = "request_preview"
	}

	// Step 2: Only query DB logs if we don't already have a corpus from the
	// in-memory request body. The DB JOIN on request_logs_bodies is expensive
	// (bodies can be megabytes) and unnecessary when we already have the body.
	if corpus == "" {
		if logs, err := g.loadSessionLogsForTitle(ctx, sessionID, tenantID); err == nil && len(logs) > 0 {
			dbCorpus := buildSummaryCorpus(logs)
			if len(strings.TrimSpace(dbCorpus)) > len(strings.TrimSpace(corpus)) {
				corpus = dbCorpus
				corpusSource = "db_logs"
			}
		}
	}

	logger.Info("auto_title: corpus built",
		"source", corpusSource,
		"corpus_len", len(corpus),
		"body_len", len(requestBody),
		"preview_len", len(requestPreview),
		"corpus_head", truncateForLog(corpus, 150))

	if len(strings.TrimSpace(corpus)) < 10 {
		// Fallback to simple extraction from preview
		if requestPreview != "" {
			if title := g.extractTitleFromPreview(requestPreview); title != "" {
				return title, "auto-extract", 0, nil
			}
		}
		return "", "", 0, fmt.Errorf("corpus too short for title generation (body_len=%d, preview_len=%d)", len(requestBody), len(requestPreview))
	}

	// Get API key for LLM call
	keyID, apiKey, err := g.handler.pickFirstAvailableAPIKeyForAuto(ctx, tenantID)
	if err != nil {
		// Fallback: simple extraction from in-memory preview
		if requestPreview != "" {
			if title := g.extractTitleFromPreview(requestPreview); title != "" {
				return title, "auto-extract", 0, nil
			}
		}
		return "", "", 0, fmt.Errorf("no API key available: %w", err)
	}

	// Call LLM to generate title
	turnHint := strings.Count(corpus, "\n[")
	if turnHint < 1 {
		turnHint = 1
	}
	userContent := fmt.Sprintf("以下会话共约 %d 条记录（语料已清洗）。请阅读全部内容后生成标题：\n%s", turnHint, corpus)

	// Title LLM call runs in an isolated context (no X-Gw-Session-Id so it
	// gets its own gw_session_id and does NOT pollute the user's conversation).
	// The parent_request_id is forwarded so request_logs_hot.parent_request_id
	// makes the loopback linkable back to its parent user request.
	llmRes, err := g.callAutoTitleLLM(ctx, apiKey, sessionID, parentRequestID, userContent)
	if err != nil {
		// Fallback: simple extraction from in-memory preview
		if requestPreview != "" {
			if title := g.extractTitleFromPreview(requestPreview); title != "" {
				return title, "auto-extract", 0, nil
			}
		}
		return "", "", 0, fmt.Errorf("LLM title generation failed: %w", err)
	}

	title := normalizeSessionTitle(llmRes.Content)
	if !isValidSessionTitle(title) {
		// Fallback: simple extraction from in-memory preview
		if requestPreview != "" {
			if fallback := g.extractTitleFromPreview(requestPreview); fallback != "" {
				return fallback, "auto-extract", 0, nil
			}
		}
		return "", "", 0, fmt.Errorf("LLM generated invalid title: %q", llmRes.Content)
	}

	return title, llmRes.ResolvedModel, keyID, nil
}

// truncateForLog returns a shortened, log-safe preview of s (replaces newlines).
func truncateForLog(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// extractLastUserMessageRuneCount — v1 (2026-08-19): parses a chat completion
// request body, finds the LAST user message, and returns its rune count
// (whitespace-collapsed). Returns 0 if the body is empty/unparseable or has
// no user message. Used by MaybeGenerateTitle's short-user-message gate to
// decide whether an LLM title round-trip is worth the cost when the user
// message is already shorter than sessionTitleMaxRunes.
func extractLastUserMessageRuneCount(requestBody string) int {
	body := []byte(requestBody)
	if len(body) == 0 {
		return 0
	}
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Messages) == 0 {
		return 0
	}
	for i := len(parsed.Messages) - 1; i >= 0; i-- {
		if !strings.EqualFold(strings.TrimSpace(parsed.Messages[i].Role), "user") {
			continue
		}
		text := strings.Join(strings.Fields(contentToString(parsed.Messages[i].Content)), " ")
		if text == "" {
			return 0
		}
		return utf8.RuneCountInString(text)
	}
	return 0
}

// extractMessagesForTitle parses a chat completion request body (JSON) and
// extracts the conversation messages into a clean "role: content" text suitable
// for title generation. It focuses on user/assistant messages and truncates
// each message to avoid feeding megabytes of system prompt to the title LLM.
//
// v5 (2026-08-06): bumped limits (10 msgs / 1200 chars / 6000 total) and
// preserves the LAST user message in full — that is the actual question the
// user asked, which the title LLM needs to see uncut. Earlier messages are
// truncated as before.
//
// Returns "" if the body cannot be parsed or has no usable messages.
func extractMessagesForTitle(requestBody string) string {
	body := []byte(requestBody)
	if len(body) == 0 {
		return ""
	}
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Messages) == 0 {
		return ""
	}
	const maxPerMsg = 1200  // chars per message (was 500 — bumped 2026-08-06)
	const maxTotal = 6000   // total chars cap (was 3000)
	const maxMsgs = 10      // semantic messages, excluding tool/function traffic
	const maxSysChars = 800 // long system messages (IDE tool descriptions) get truncated at this length
	const sysSnippet = 300  // how much of a long system message to keep

	// 2026-08-06: pre-scan to find the LAST user message; preserve it in full
	// even if doing so pushes the corpus past maxTotal. This is the actual
	// question the user asked and the title LLM needs it uncut.
	lastUserIdx := -1
	for i := len(parsed.Messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(parsed.Messages[i].Role), "user") {
			lastUserIdx = i
			break
		}
	}
	var lastUserText string
	if lastUserIdx >= 0 {
		lastUserText = strings.Join(strings.Fields(contentToString(parsed.Messages[lastUserIdx].Content)), " ")
	}

	var parts []string
	semanticMessages := 0
	for _, msg := range parsed.Messages {
		role := strings.TrimSpace(msg.Role)
		// Tool/function records contain implementation output, not the user's
		// intent. Exclude them before counting the corpus budget so tool-heavy
		// turns cannot crowd out later conversation messages.
		if role == "tool" || role == "function" {
			continue
		}
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		if semanticMessages >= maxMsgs {
			break
		}
		text := strings.Join(strings.Fields(contentToString(msg.Content)), " ") // collapse whitespace
		if text == "" {
			continue
		}
		// Long system prompts (IDE tool descriptions) are usually boilerplate;
		// truncate aggressively so the user question is not crowded out.
		if role == "system" && len(text) > maxSysChars {
			text = text[:sysSnippet] + "… <ide-tool-context truncated>"
		}
		if len(text) > maxPerMsg {
			text = text[:maxPerMsg] + "…"
		}
		parts = append(parts, role+": "+text)
		semanticMessages++
	}
	// Truncate the prefix loop's joined output to maxTotal so a long IDE
	// system prompt doesn't crowd out the preserved user message below.
	result := strings.Join(parts, "\n")
	if len(result) > maxTotal {
		result = result[:maxTotal] + "…"
	}
	// 2026-08-06: append the LAST user message in full so the title LLM sees
	// the actual question. Added AFTER the maxTotal cap so a long user message
	// always survives uncut. Marked with a sentinel comment for human
	// readability when the corpus is debugged.
	if lastUserText != "" {
		result += "\n<latest user message (preserved)> user: " + lastUserText
	}
	return result
}

// contentToString converts a chat message content field (string or array of
// content blocks) into plain text.
func contentToString(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if block, ok := item.(map[string]any); ok {
				if text, ok := block["text"].(string); ok && text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

// extractTitleFromPreview extracts a title from request_preview.
// Enhanced version: detect IDE source, extract user prompt, generate descriptive title.
func (g *AutoTitleGenerator) extractTitleFromPreview(preview string) string {
	// Remove leading/trailing whitespace
	preview = strings.TrimSpace(preview)
	if preview == "" {
		return ""
	}

	// Step 1: Detect IDE/tool source from system prompt
	idePrefix := g.detectIDESource(preview)

	// Step 2: Extract user prompt (skip system messages)
	userPrompt := g.extractUserPrompt(preview)

	// Step 3: Build title with IDE prefix if detected
	var title string
	if idePrefix != "" && userPrompt != "" {
		// Format: "[IDE] user prompt..."
		title = idePrefix + " " + userPrompt
	} else if userPrompt != "" {
		title = userPrompt
	} else {
		// Fallback: use raw preview (cleaned)
		title = preview
		title = strings.ReplaceAll(title, "\n", " ")
		title = strings.ReplaceAll(title, "\r", " ")
		title = strings.Join(strings.Fields(title), " ")
	}

	// Step 4: Truncate if needed (after combining IDE + prompt)
	titleRunes := []rune(title)
	if len(titleRunes) > sessionTitleMaxRunes {
		cutoff := sessionTitleMaxRunes - 1
		for i := cutoff - 1; i >= 0; i-- {
			if titleRunes[i] == ' ' && i > cutoff-20 {
				cutoff = i
				break
			}
		}
		title = string(titleRunes[:cutoff]) + "…"
	}

	// Step 5: Light normalization (remove quotes, excessive spaces)
	// Do NOT use normalizeSessionTitle yet - it removes brackets
	title = strings.Trim(title, `"'「」『』`)
	title = strings.Join(strings.Fields(title), " ")

	// Step 6: Final validation (relaxed - allow brackets for IDE prefix)
	if len(strings.TrimSpace(title)) < 2 {
		// Fallback to simple prefix if too short
		runes := []rune(preview)
		if len(runes) > 20 {
			return string(runes[:20]) + "…"
		}
		return preview
	}

	return title
}

// detectIDESource detects the IDE/tool from system prompts in the request.
// Returns a short prefix like "[ZCode]", "[Cursor]", "[ZooCode]", etc.
func (g *AutoTitleGenerator) detectIDESource(preview string) string {
	lower := strings.ToLower(preview)

	// Check for known IDE signatures
	ideSignatures := map[string]string{
		"you are zcode":    "[ZCode]",
		"you are zoo":      "[ZooCode]",
		"you are cursor":   "[Cursor]",
		"you are opencode": "[OpenCode]",
		"you are claude":   "[Claude]",
		"you are windsurf": "[Windsurf]",
		"you are cline":    "[Cline]",
		"you are aider":    "[Aider]",
		"you are continue": "[Continue]",
		"you are copilot":  "[Copilot]",
		"you are kiro":     "[Kiro]",
	}

	for signature, prefix := range ideSignatures {
		if strings.Contains(lower, signature) {
			return prefix
		}
	}

	return ""
}

// extractUserPrompt extracts the actual user prompt from request preview.
// Skips system messages and focuses on user content.
func (g *AutoTitleGenerator) extractUserPrompt(preview string) string {
	// Common patterns to skip
	skipPatterns := []string{
		"[system]",
		"system:",
		"You are",
		"You're",
		"Your task is",
		"Your role is",
		"Act as",
		"Behave as",
	}

	lines := strings.Split(preview, "\n")
	var userLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip system-like lines
		isSystemLine := false
		for _, pattern := range skipPatterns {
			if strings.HasPrefix(line, pattern) {
				isSystemLine = true
				break
			}
		}

		if !isSystemLine && len(line) > 10 {
			// Extract content after common prefixes
			if strings.HasPrefix(line, "[user]") {
				line = strings.TrimSpace(strings.TrimPrefix(line, "[user]"))
			}
			if strings.HasPrefix(line, "user:") {
				line = strings.TrimSpace(strings.TrimPrefix(line, "user:"))
			}
			if line != "" {
				userLines = append(userLines, line)
			}
		}
	}

	if len(userLines) == 0 {
		return ""
	}

	// Take first meaningful user line
	userPrompt := userLines[0]

	// Truncate to 60 runes for the prompt part.
	promptRunes := []rune(userPrompt)
	if len(promptRunes) > 60 {
		cutoff := 60
		for i := cutoff - 1; i >= 0; i-- {
			if promptRunes[i] == ' ' && i > 40 {
				cutoff = i
				break
			}
		}
		userPrompt = string(promptRunes[:cutoff]) + "…"
	}

	return userPrompt
}

// saveSessionTitle saves the auto-generated title to session_titles table.
// taskID is the session's gw_task_id from request_logs so the request-logs
// list JOIN (admin/logs.go requestLogsJoins) can match on
// (task_id, scoped_session_id). taskID='auto' was the historical hardcoded
// marker and never matched the request's real gw_task_id (typically 'default');
// we now store the real task when known and fall back to 'auto' only when the
// caller could not resolve one.
// ON CONFLICT DO NOTHING: first writer wins — concurrent goroutines for the
// same session keep the earliest title and discard the rest.
func (g *AutoTitleGenerator) saveSessionTitle(ctx context.Context, sessionID, taskID, title, model string, apiKeyID int) error {
	if taskID == "" {
		taskID = "auto"
	}
	if model == "" {
		model = "auto-llm"
	}
	_, err := g.handler.db.Exec(ctx, `
		INSERT INTO session_titles (
			task_id,
			scoped_session_id,
			title,
			generated_at,
			model,
			api_key_id
		)
		VALUES ($1, $2, $3, NOW(), $4, $5)
		ON CONFLICT (task_id, scoped_session_id) DO NOTHING
	`, taskID, sessionID, title, model, apiKeyID)
	return err
}

// loadSessionLogsForTitle loads session request logs for title generation (first 5 turns).
// Uses request_logs_with_current_month view to include the hot partition.
func (g *AutoTitleGenerator) loadSessionLogsForTitle(ctx context.Context, sessionID, tenantID string) ([]sessionLogForSummary, error) {
	if g.handler == nil || g.handler.db == nil {
		return nil, fmt.Errorf("database not configured")
	}

	rows, err := g.handler.db.Query(ctx, `
		SELECT rl.ts, rl.request_preview, rl.response_preview,
		       COALESCE(rb.request_body::text, rl.request_body::text) AS request_body,
		       COALESCE(rb.response_body::text, rl.response_body::text) AS response_body,
		       `+requestLogStatusExpr+` AS request_status,
		       rl.error_kind, rl.client_model
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
		WHERE rl.gw_session_id = $1 AND rl.tenant_id = $2
		ORDER BY rl.ts ASC
		LIMIT 5
	`, sessionID, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []sessionLogForSummary
	for rows.Next() {
		var row sessionLogForSummary
		var errKind *string
		var clientModel *string
		if err := rows.Scan(&row.Ts, &row.RequestPreview, &row.ResponsePreview,
			&row.RequestBody, &row.ResponseBody,
			&row.RequestStatus, &errKind, &clientModel); err != nil {
			continue
		}
		row.ErrorKind = errKind
		row.ClientModel = clientModel
		logs = append(logs, row)
	}
	return logs, rows.Err()
}

// resolveAutoTitleModel returns the model used for auto title generation.
// 2026-08-05: pins to the work_type_model_route cheap pool for session_title
// (minimax-m2.7 / glm-5.1 / …) instead of model="auto". Sending "auto" let
// the V2 decider route title-gen to the user's expensive relay (observed in
// the 08-05 apiclaude incident: claude-opus-5 / provider 587, two title
// requests stuck in_progress). An explicit cheap model also bypasses the
// decider entirely, keeping title-gen off the user's hot path. Falls back
// to "auto" only when the routing pool is empty.
func (g *AutoTitleGenerator) resolveAutoTitleModel(ctx context.Context) string {
	if g.handler == nil {
		return adminLLMModelAuto
	}
	if m := strings.TrimSpace(g.handler.resolveAdminLLMFallbackModel(ctx, adminLLMTaskSessionTitle)); m != "" {
		return m
	}
	return adminLLMModelAuto
}

// callAutoTitleLLM calls the LLM to generate a title (background HTTP request).
//
// v3 (2026-08-06):
//   - forwards the parent user request_id as X-Gw-Parent-Request-Id so
//     request_logs_hot.parent_request_id makes the loopback linkable back to
//     its parent user request. Operators can SQL JOIN on parent_request_id to
//     find "08aa2a8a → 3a03f7db" instantly.
//   - sets X-Gw-Source-Actor: auto-title-generator so request_logs_hot.origin_actor
//     is populated for all title requests.
//   - sets X-Gw-Session-Id: gt_<session_id> (the "GT" branch namespace) so
//     request_logs_hot.gw_session_id shows "gt_gw_<original>" instead of a
//     fresh gw_<uuid>. Operators can SQL
//     WHERE gw_session_id LIKE 'gt\_%' ESCAPE '\'
//     to find every auto-title row. Pairs with the gs_ prefix used by
//     admin/auto_summary_generator.go.
//   - retries once on transient errors (connect-refused, EOF, 502/503/504) with
//     200ms ± 50ms jitter, so a single blip doesn't surface as "LLM didn't
//     receive the request".
//   - structured slog.Warn on every failure includes endpoint, status_code,
//     body_excerpt (≤200), retry_count, parent_request_id, model, latency_ms.
//     This is the answer to the operator question "did the LLM actually
//     receive the request?" — if status_code >= 200 and < 300 the LLM did;
//     otherwise the body_excerpt reveals what the upstream said.
func (g *AutoTitleGenerator) callAutoTitleLLM(ctx context.Context, apiKey, sessionID, parentRequestID, userContent string) (adminLLMChatResult, error) {
	if g.handler == nil {
		return adminLLMChatResult{}, fmt.Errorf("handler not configured")
	}

	task := g.handler.loadAdminLLMTask(ctx, adminLLMTaskSessionTitle)

	// 2026-08-05: use the pinned cheap model for session_title (see
	// resolveAutoTitleModel) instead of model="auto" so the V2 decider cannot
	// select the user's expensive relay for title-gen.
	model := g.resolveAutoTitleModel(ctx)

	// Make a synthetic HTTP request for the gateway endpoint
	endpoint := g.getGatewayEndpoint() + "/v1/chat/completions"

	// 2026-08-06: 包裹 XML 标签防止 prompt injection。
	wrappedUserContent := "<session_transcript>\n" + userContent + "\n</session_transcript>"

	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": task.SystemPrompt},
			{"role": "user", "content": wrappedUserContent},
		},
		"temperature": task.Temperature,
	}
	if task.MaxTokens > 0 {
		payload["max_tokens"] = task.MaxTokens
	}

	body, _ := json.Marshal(payload)

	// Retry policy (2026-08-06):
	//   - 1 retry on transient errors (connect refused, EOF, 502/503/504)
	//   - 200ms ± 50ms jitter
	//   - do NOT retry on 4xx (caller-side bug) or empty completion (model-side)
	const maxRetries = 1
	var lastErr error
	var lastStatus int
	var lastBodyExcerpt string
	llmStart := time.Now()
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Jittered backoff: 150-250ms.
			jitter := time.Duration(150+rand.Intn(100)) * time.Millisecond
			select {
			case <-ctx.Done():
				return adminLLMChatResult{}, ctx.Err()
			case <-time.After(jitter):
			}
		}
		result, err, status, bodyExcerpt := g.doCallAutoTitleOnce(ctx, endpoint, body, apiKey, sessionID, parentRequestID, task, model)
		lastErr = err
		lastStatus = status
		lastBodyExcerpt = bodyExcerpt
		if err == nil {
			metrics.AutoTitleLLMCall.WithLabelValues("ok").Inc()
			metrics.AutoTitleLLMLatency.Observe(time.Since(llmStart).Seconds())
			return result, nil
		}
		if !isTransientAutoTitleErr(err, status) {
			// 4xx / decode error / empty completion — don't retry, surface immediately.
			break
		}
		metrics.AutoTitleLLMCall.WithLabelValues("transient_retry").Inc()
		// log retry intent for observability
		slog.Warn("auto_title: transient error, retrying",
			"component", "auto_title_generator",
			"session_id", sessionID,
			"parent_request_id", parentRequestID,
			"endpoint", endpoint,
			"model", model,
			"attempt", attempt+1,
			"status_code", status,
			"error", err.Error(),
			"body_excerpt", bodyExcerpt,
		)
	}
	result := "error"
	if lastStatus >= 400 && lastStatus < 500 {
		result = "invalid_response"
	}
	metrics.AutoTitleLLMCall.WithLabelValues(result).Inc()
	metrics.AutoTitleLLMLatency.Observe(time.Since(llmStart).Seconds())
	// Final failure log — has the full operator context to answer
	// "did the LLM actually receive the request".
	slog.Warn("auto_title: LLM call failed",
		"component", "auto_title_generator",
		"session_id", sessionID,
		"parent_request_id", parentRequestID,
		"endpoint", endpoint,
		"model", model,
		"status_code", lastStatus,
		"body_excerpt", lastBodyExcerpt,
		"retries", maxRetries,
		"error", errString(lastErr),
	)
	return adminLLMChatResult{}, lastErr
}

// doCallAutoTitleOnce performs one HTTP attempt and returns either a parsed
// adminLLMChatResult or a descriptive error. The caller decides whether to
// retry based on isTransientAutoTitleErr.
func (g *AutoTitleGenerator) doCallAutoTitleOnce(
	ctx context.Context,
	endpoint string,
	body []byte,
	apiKey string,
	sessionID string,
	parentRequestID string,
	task adminLLMTaskConfig,
	model string,
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
	// 2026-08-06: namespace the task id so dashboards don't merge the title
	// request into the user's session view; the original sessionID is still
	// discoverable through the "auto-title:" prefix for triage.
	req.Header.Set("X-Gw-Task-Id", "auto-title:"+sessionID)
	// 2026-08-06 (GT prefix): tag the loopback's own session_id with the
	// "gt_" branch namespace so request_logs_hot.gw_session_id shows
	// "gt_gw_<original>" instead of a fresh gw_<uuid>. Operators can SQL
	//   WHERE gw_session_id LIKE 'gt\_%' ESCAPE '\'
	// to find every auto-title row and JOIN child.parent_request_id back to
	// the parent user request. Pairs with the gs_ prefix used by the
	// auto-summary generator (admin/auto_summary_generator.go).
	// 2026-08-15 fix: prefix MUST be "gt_" (underscore) — sanitizeGwSessionHeader
	// only accepts gw_/gt_/gs_ prefixes; the previous "gt:" (colon) form was
	// silently dropped and the loopback row got a fresh gw_<uuid> instead of
	// the branch namespace documented below.
	req.Header.Set("X-Gw-Session-Id", "gt_"+sessionID)
	// 2026-08-06: parent request correlation — handler entry reads this header
	// and stores it in logCtx.ParentRequestID, which then flows into
	// request_logs_hot.parent_request_id. This is what makes the title loopback
	// linkable back to its parent user request.
	if parentRequestID != "" {
		req.Header.Set(autoParentRequestIDHeader, parentRequestID)
	}
	// 2026-08-06: caller identity for SQL "WHERE origin_actor = ..." filtering.
	req.Header.Set(autoSourceActorHeader, autoTitleOriginActor)
	// Mark as internal auto-request so it's excluded from user-visible
	// metrics and from re-triggering auto title generation (chain prevention).
	req.Header.Set("X-Gw-Is-Auto", "true")
	if task.DeviceSeed != "" {
		req.Header.Set("X-Device-Seed", task.DeviceSeed)
	}

	// Bumped from 20s → 30s (2026-08-06): when the cheap-model pool is busy
	// the upstream may queue a few seconds before answering; 20s was too tight.
	client := &http.Client{Timeout: 30 * time.Second}
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

// isTransientAutoTitleErr reports whether a callAutoTitle error is a transient
// network/upstream condition that justifies a single retry. 4xx errors are
// caller-side bugs and should surface immediately.
func isTransientAutoTitleErr(err error, status int) bool {
	if err == nil {
		return false
	}
	// Connection-level errors are always transient.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	switch status {
	case http.StatusBadGateway, // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return true
	}
	return false
}

// errString returns err.Error() or "" for nil. Used in slog.Warn fields where
// nil-safety is required by the slog contract.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// getGatewayEndpoint returns the gateway endpoint for auto-title LLM calls.
// Matches the loopback convention used by bg/* internal callers
// (http://127.0.0.1:8781). Override via LLM_GATEWAY_ENDPOINT if needed.
func (g *AutoTitleGenerator) getGatewayEndpoint() string {
	if endpoint := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	return "http://127.0.0.1:8781"
}

// pickFirstAvailableAPIKeyForAuto picks the first available API key for auto title generation.
func (h *Handler) pickFirstAvailableAPIKeyForAuto(ctx context.Context, tenantID string) (id int, apiKey string, err error) {
	if h == nil || h.db == nil {
		return 0, "", fmt.Errorf("database not configured")
	}

	var ciphertext string
	query := `SELECT ak.id, ak.key_ciphertext
		FROM api_keys ak
		WHERE ak.tenant_id = $1
		  AND ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') = 'active'
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
		ORDER BY ak.id ASC
		LIMIT 1`
	if err := h.db.QueryRow(ctx, query, tenantID).Scan(&id, &ciphertext); err != nil {
		return 0, "", fmt.Errorf("no available API key for tenant %s", tenantID)
	}
	if !isRevealableKeyCiphertext(ciphertext) {
		return 0, "", fmt.Errorf("no revealable API key")
	}
	apiKey, err = h.decryptCredStr(ciphertext)
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return 0, "", fmt.Errorf("failed to decrypt API key")
	}
	return id, strings.TrimSpace(apiKey), nil
}
