package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// AutoTitleGenerator handles automatic session title generation.
// It runs asynchronously after the first request in a session completes.
type AutoTitleGenerator struct {
	handler *Handler
	enabled bool
}

// NewAutoTitleGenerator creates a new auto title generator.
func NewAutoTitleGenerator(handler *Handler) *AutoTitleGenerator {
	return &AutoTitleGenerator{
		handler: handler,
		enabled: true, // TODO: make configurable via env var
	}
}

// MaybeGenerateTitle checks if a session needs a title and generates one.
// Called asynchronously after the first request in a session completes.
// requestBody is the full (redacted) inbound request body JSON — used to
// extract the actual user message for title generation. requestPreview is the
// 320-byte summary used as fallback.
// This function is fire-and-forget and will not block the main request path.
func (g *AutoTitleGenerator) MaybeGenerateTitle(sessionID, tenantID, requestBody, requestPreview string) {
	if !g.enabled || g.handler == nil || g.handler.db == nil {
		return
	}

	// Run in a separate goroutine to avoid blocking
	go g.generateTitleAsync(sessionID, tenantID, requestBody, requestPreview)
}

func (g *AutoTitleGenerator) generateTitleAsync(sessionID, tenantID, requestBody, requestPreview string) {
	// Use background context with timeout (not tied to the request context)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Add structured logging for observability
	logger := slog.With(
		"component", "auto_title_generator",
		"session_id", sessionID,
		"tenant_id", tenantID,
	)

	// Step 1: Check if title already exists (avoid duplicate work)
	hasTitle, err := g.checkSessionHasTitle(ctx, sessionID)
	if err != nil {
		logger.Warn("failed to check existing title", "error", err)
		return
	}
	if hasTitle {
		logger.Debug("session already has title, skipping")
		return
	}

	start := time.Now()
	title, model, keyID, err := g.generateTitleFromFirstRequest(ctx, sessionID, tenantID, requestBody, requestPreview)
	elapsed := time.Since(start)
	if err != nil {
		logger.Warn("failed to generate title from first request", "error", err, "elapsed_ms", elapsed.Milliseconds())
		return
	}
	logger.Info("auto_title: title generated", "title", title, "model", model, "elapsed_ms", elapsed.Milliseconds())

	// Step 2: Save title to database (with conflict handling).
	// ON CONFLICT DO NOTHING: if another goroutine already saved a title for
	// this session, we keep theirs and discard ours (first writer wins).
	if err := g.saveSessionTitle(ctx, sessionID, title, model, keyID); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			logger.Debug("title already saved by another goroutine")
			return
		}
		logger.Error("failed to save session title", "error", err)
		return
	}

	logger.Info("auto title saved successfully", "title", title, "length", len(title), "model", model)
}

// checkSessionHasTitle checks if a session already has a title.
func (g *AutoTitleGenerator) checkSessionHasTitle(ctx context.Context, sessionID string) (bool, error) {
	var exists bool
	err := g.handler.db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM session_titles 
			WHERE scoped_session_id = $1
		)
	`, sessionID).Scan(&exists)
	return exists, err
}

// generateTitleFromFirstRequest loads session logs and generates a title using LLM.
// v4 (2026-08-05): Accept full requestBody to extract real user messages.
// The requestPreview (320-byte summary) is used as fallback only.
// Returns (title, model, apiKeyID, error). On fallback-extract paths model is
// "auto-extract" and apiKeyID is 0.
func (g *AutoTitleGenerator) generateTitleFromFirstRequest(ctx context.Context, sessionID, tenantID, requestBody, requestPreview string) (string, string, int, error) {
	// Step 1: Build corpus — prefer full request body (has real user message),
	// fall back to 320-byte preview, then best-effort DB logs.
	corpus := ""

	// Try extracting a clean conversation summary from the full request body.
	// This gives us the actual user message (not truncated to 320 bytes).
	if requestBody != "" {
		if extracted := extractMessagesForTitle(requestBody); extracted != "" {
			corpus = extracted
		}
	}

	// Fall back to the 320-byte preview if full body extraction yielded nothing.
	if corpus == "" && requestPreview != "" {
		corpus = requestPreview
	}

	// Step 2: Only query DB logs if we don't already have a corpus from the
	// in-memory request body. The DB JOIN on request_logs_bodies is expensive
	// (bodies can be megabytes) and unnecessary when we already have the body.
	if corpus == "" {
		if logs, err := g.loadSessionLogsForTitle(ctx, sessionID, tenantID); err == nil && len(logs) > 0 {
			dbCorpus := buildSummaryCorpus(logs)
			if len(strings.TrimSpace(dbCorpus)) > len(strings.TrimSpace(corpus)) {
				corpus = dbCorpus
			}
		}
	}

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
	llmRes, err := g.callAutoTitleLLM(ctx, apiKey, sessionID, userContent)
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

// extractMessagesForTitle parses a chat completion request body (JSON) and
// extracts the conversation messages into a clean "role: content" text suitable
// for title generation. It focuses on user/assistant messages and truncates
// each message to avoid feeding megabytes of system prompt to the title LLM.
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
	const maxPerMsg = 500   // chars per message
	const maxTotal = 3000   // total chars cap
	const maxMsgs = 6       // at most first 6 messages
	var parts []string
	for i, msg := range parsed.Messages {
		if i >= maxMsgs {
			break
		}
		role := strings.TrimSpace(msg.Role)
		text := contentToString(msg.Content)
		text = strings.Join(strings.Fields(text), " ") // collapse whitespace
		if text == "" {
			continue
		}
		// Skip system messages that are just tool/IDE boilerplate — but keep
		// short system messages since they may describe the task.
		if role == "system" && len(text) > 200 {
			// Truncate very long system prompts (e.g. IDE instructions)
			text = text[:200] + "…"
		}
		if len(text) > maxPerMsg {
			text = text[:maxPerMsg] + "…"
		}
		parts = append(parts, role+": "+text)
	}
	result := strings.Join(parts, "\n")
	if len(result) > maxTotal {
		result = result[:maxTotal] + "…"
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
	maxLen := 80
	if len(title) > maxLen {
		cutoff := maxLen
		if idx := strings.LastIndex(title[:maxLen], " "); idx > 0 && idx > maxLen-20 {
			cutoff = idx
		}
		title = title[:cutoff] + "…"
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

	// Truncate to 60 chars for the prompt part
	if len(userPrompt) > 60 {
		cutoff := 60
		if idx := strings.LastIndex(userPrompt[:60], " "); idx > 40 {
			cutoff = idx
		}
		userPrompt = userPrompt[:cutoff] + "…"
	}

	return userPrompt
}

// saveSessionTitle saves the auto-generated title to session_titles table.
// Uses task_id='auto' to indicate this was auto-generated.
// ON CONFLICT DO NOTHING: first writer wins — concurrent goroutines for the
// same session keep the earliest title and discard the rest.
func (g *AutoTitleGenerator) saveSessionTitle(ctx context.Context, sessionID, title, model string, apiKeyID int) error {
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
		VALUES ('auto', $1, $2, NOW(), $3, $4)
		ON CONFLICT (task_id, scoped_session_id) DO NOTHING
	`, sessionID, title, model, apiKeyID)
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

// callAutoTitleLLM calls the LLM to generate a title (background HTTP request).
func (g *AutoTitleGenerator) callAutoTitleLLM(ctx context.Context, apiKey, sessionID, userContent string) (adminLLMChatResult, error) {
	if g.handler == nil {
		return adminLLMChatResult{}, fmt.Errorf("handler not configured")
	}

	task := g.handler.loadAdminLLMTask(ctx, adminLLMTaskSessionTitle)

	// Make a synthetic HTTP request for the gateway endpoint
	endpoint := g.getGatewayEndpoint() + "/v1/chat/completions"
	
	payload := map[string]any{
		"model": adminLLMModelAuto,
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return adminLLMChatResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Gw-Auto-Profile", task.DefaultProfile)
	req.Header.Set("X-Gw-Task-Hint", task.TaskHint)
	req.Header.Set("X-Gw-Work-Type", task.Key)
	req.Header.Set("X-Gw-Task-Id", sessionID)
	// 2026-08-05: intentionally NOT setting X-Gw-Session-Id so the title LLM
	// call gets its own isolated gw_session_id and does NOT appear in the
	// user's conversation history. The X-Gw-Task-Id links it to the session
	// for billing/analytics without polluting the conversation context.
	// Mark as internal auto-request so it's excluded from user-visible metrics.
	req.Header.Set("X-Gw-Is-Auto", "true")
	if task.DeviceSeed != "" {
		req.Header.Set("X-Device-Seed", task.DeviceSeed)
	}

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return adminLLMChatResult{}, err
	}
	defer resp.Body.Close()

	resolvedModel := adminLLMModelAuto
	if hdr := strings.TrimSpace(resp.Header.Get("X-Gw-Auto-Decision")); hdr != "" {
		var wire struct {
			ChosenModel string `json:"chosen_model"`
		}
		if json.Unmarshal([]byte(hdr), &wire) == nil && wire.ChosenModel != "" {
			resolvedModel = wire.ChosenModel
		}
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return adminLLMChatResult{}, fmt.Errorf("%s", msg)
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
		return adminLLMChatResult{}, err
	}
	if out.Model != "" && out.Model != adminLLMModelAuto {
		resolvedModel = out.Model
	}
	if len(out.Choices) == 0 {
		return adminLLMChatResult{}, fmt.Errorf("empty completion")
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return adminLLMChatResult{}, fmt.Errorf("empty completion content")
	}
	return adminLLMChatResult{Content: content, ResolvedModel: resolvedModel}, nil
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
