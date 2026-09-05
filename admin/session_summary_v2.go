// Package admin - session_summary_v2.go
//
// 会话总结 API v2 — 调用LLM对会话内容进行即时总结
//
//   POST /api/admin/sessions/summary
//   Body: {
//     "session_id": "xxx",
//     "tenant": "default",
//     "up_to_turn": 3  // optional, summarize up to this turn
//   }
//
// 返回格式：
//   {
//     "title": "...",
//     "summary": "...",
//     "turns_analyzed": 3
//   }
//
// 仅 super 用户可用。

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
)

// SessionSummaryV2API 提供会话总结端点
type SessionSummaryV2API struct {
	pool *pgxpool.Pool
}

// NewSessionSummaryV2API 构造函数
func NewSessionSummaryV2API(pool *pgxpool.Pool) *SessionSummaryV2API {
	return &SessionSummaryV2API{pool: pool}
}

// SessionSummaryRequest 是总结请求的结构
type SessionSummaryRequest struct {
	SessionID string `json:"session_id"`
	Tenant    string `json:"tenant"`
	UpToTurn  *int   `json:"up_to_turn,omitempty"`
}

// SessionSummaryResponse 是总结响应的结构
type SessionSummaryResponse struct {
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	TurnsAnalyzed int    `json:"turns_analyzed"`
}

func (api *SessionSummaryV2API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if api.pool == nil {
		writeExportJSONError(w, http.StatusServiceUnavailable, "session summary v2 API requires database")
		return
	}
	if r.URL.Path != "/api/admin/sessions/summary" {
		writeExportJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeExportJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req SessionSummaryRequest
	if err := jsonbody.DecodeRequest(r, &req, jsonbody.MaxRequiredBody, true); err != nil {
		writeExportJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.SessionID == "" {
		writeExportJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if req.Tenant == "" {
		req.Tenant = "default"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// 2026-08-30: enforce tenant isolation. The standalone summary endpoint
	// is restricted to super_admin + tenant_admin. For tenant_admin, the
	// tenant must be their own — any caller-supplied "tenant" field is
	// ignored. For super_admin, the tenant is taken from the request body.
	authTenant := GetTenantID(r)
	isSuper := IsSuperAdminOrLegacy(r)
	if !isSuper && !IsTenantAdmin(r) {
		writeExportJSONError(w, http.StatusForbidden, "session summary requires super_admin or tenant_admin")
		return
	}

	if !isSuper {
		// tenant_admin: tenant MUST be their own. Ignore any caller-supplied tenant.
		tenantID := authTenant
		summary, err := api.generateSummary(ctx, &SessionSummaryRequest{
			SessionID: req.SessionID,
			Tenant:    tenantID,
			UpToTurn:  req.UpToTurn,
		}, tenantID)
		if err != nil {
			writeExportJSONError(w, http.StatusInternalServerError, fmt.Sprintf("summary failed: %v", err))
			return
		}
		writeExportJSON(w, http.StatusOK, summary)
		return
	}

	// super_admin: caller may specify tenant in body.
	tenantID := req.Tenant
	if tenantID == "" {
		tenantID = "default"
	}
	// Pass an empty authTenant for unrestricted callers so the explicit
	// super-admin tenant selector is not overwritten by GetTenantID's
	// legacy/default fallback value.
	summary, err := api.generateSummary(ctx, &SessionSummaryRequest{
		SessionID: req.SessionID,
		Tenant:    tenantID,
		UpToTurn:  req.UpToTurn,
	}, "")
	if err != nil {
		writeExportJSONError(w, http.StatusInternalServerError, fmt.Sprintf("summary failed: %v", err))
		return
	}

	writeExportJSON(w, http.StatusOK, summary)
}

func (api *SessionSummaryV2API) generateSummary(
	ctx context.Context,
	req *SessionSummaryRequest,
	authTenant string,
) (*SessionSummaryResponse, error) {
	tenantID := req.Tenant
	if authTenant != "" {
		// The authenticated tenant is authoritative. Never let a caller
		// supplied JSON tenant override it; doing so would turn this
		// summary endpoint into a cross-tenant IDOR for tenant_admins.
		tenantID = authTenant
	}

	// 1. Query turns from session_turns + session_bodies_unified.
	turns, err := api.queryTurnsForSummary(ctx, req.SessionID, tenantID, req.UpToTurn)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}

	if len(turns) == 0 {
		// 2026-08-30: many sessions have V2 shadow-write disabled
		// (sessions_v2.enabled=false) so session_turns is empty even though
		// request_logs (the V1 store) has the full conversation. Fall back
		// to request_logs so the operator gets a real summary instead of
		// "no turns found".
		fallback, ferr := api.queryRequestLogsFallback(ctx, req.SessionID, tenantID, req.UpToTurn)
		if ferr != nil {
			return nil, fmt.Errorf("query turns (request_logs fallback): %w", ferr)
		}
		if len(fallback) == 0 {
			return nil, fmt.Errorf("no turns found for session %s", req.SessionID)
		}
		turns = fallback
	}

	// 2. Build conversation text for LLM
	conversationText := buildConversationText(turns)

	// 3. Call LLM to generate summary
	title, summary, err := callLLMForSummary(ctx, conversationText)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	return &SessionSummaryResponse{
		Title:         title,
		Summary:       summary,
		TurnsAnalyzed: len(turns),
	}, nil
}

// queryRequestLogsFallback derives turn-shaped conversation text from the
// V1 request_logs store. request_logs_with_current_month already includes the
// hot write window, so querying request_logs_hot separately would duplicate
// every recent request. Bodies are paired by request identity and timestamp to
// avoid attaching a reused request ID to the wrong turn.
func (api *SessionSummaryV2API) queryRequestLogsFallback(
	ctx context.Context,
	sessionID, tenantID string,
	upToTurn *int,
) ([]turnForSummary, error) {
	query, args := buildRequestLogsFallbackQuery(sessionID, tenantID, upToTurn)
	rows, err := api.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []turnForSummary
	turn := 0
	for rows.Next() {
		turn++
		var requestID string
		var ts time.Time
		var reqRaw, respRaw []byte
		if err := rows.Scan(&requestID, &ts, &reqRaw, &respRaw); err != nil {
			return nil, err
		}
		turns = append(turns, turnForSummary{
			TurnNo:        turn,
			RequestDelta:  decodeStoredJSON("request_body", requestID, reqRaw),
			ResponseDelta: decodeStoredJSON("response_body", requestID, respRaw),
		})
	}
	return turns, rows.Err()
}

func buildRequestLogsFallbackQuery(sessionID, tenantID string, upToTurn *int) (string, []any) {
	query := `
		SELECT rl.request_id,
		       rl.ts,
		       rb.request_body,
		       rb.response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
			ON rb.request_id = rl.request_id
			AND rb.ts = rl.ts
		WHERE rl.gw_session_id = $1`
	args := []any{sessionID}
	if tenantID != "" {
		query += " AND rl.tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " ORDER BY rl.ts ASC"
	if upToTurn != nil {
		query += fmt.Sprintf(" LIMIT $%d", len(args)+1)
		args = append(args, *upToTurn)
	}
	return query, args
}

// turnForSummary 是用于总结的简化turn结构
type turnForSummary struct {
	TurnNo        int
	RequestDelta  any
	ResponseDelta any
}

func (api *SessionSummaryV2API) queryTurnsForSummary(
	ctx context.Context,
	sessionID, tenantID string,
	upToTurn *int,
) ([]turnForSummary, error) {
	// 2026-08-30: read from public.session_bodies_unified so that turns whose
	// body row is still in session_bodies_hot (the recent-write window after
	// migration 614) are visible. Falling back to public.session_bodies
	// directly would silently drop hot rows and surface a misleading
	// "no turns found" error to the caller even when metadata exists.
	query := `
		SELECT
			t.turn_no,
			b.request_delta,
			b.response_delta
		FROM public.session_turns_with_current_month t
		LEFT JOIN public.session_bodies_unified b
			ON t.tenant_id = b.tenant_id
			AND t.session_id = b.session_id
			AND t.turn_no = b.turn_no
			AND t.request_id = b.request_id
		WHERE t.session_id = $1 AND t.tenant_id = $2
	`
	args := []any{sessionID, tenantID}

	if upToTurn != nil {
		query += " AND t.turn_no <= $3"
		args = append(args, *upToTurn)
	}

	query += " ORDER BY t.turn_no ASC" // Chronological order for summary

	rows, err := api.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []turnForSummary
	for rows.Next() {
		var t turnForSummary
		var requestDeltaRaw, responseDeltaRaw []byte

		err := rows.Scan(&t.TurnNo, &requestDeltaRaw, &responseDeltaRaw)
		if err != nil {
			return nil, err
		}

		// Decode failures are logged: a null delta is indistinguishable from a
		// turn that stored no delta at all.
		t.RequestDelta = decodeStoredJSON("request_delta", sessionID, requestDeltaRaw)
		t.ResponseDelta = decodeStoredJSON("response_delta", sessionID, responseDeltaRaw)

		turns = append(turns, t)
	}

	return turns, rows.Err()
}

// buildConversationText 将turns转换为适合LLM分析的文本格式。
// 只纳入 user/assistant 内容，排除 system/developer 样板提示。
// Restored 2026-08-27 after d2cbaf88b stripped the system-skipping form,
// which leaked system prompts back into the summary corpus.
func buildConversationText(turns []turnForSummary) string {
	var buf bytes.Buffer

	for _, t := range turns {
		buf.WriteString(fmt.Sprintf("=== Turn %d ===\n", t.TurnNo))

		userText := extractDialogueContent(t.RequestDelta, "user")
		assistantText := extractDialogueContent(t.ResponseDelta, "assistant")
		if userText == "" {
			userText = extractDialogueContent(t.RequestDelta, "")
		}
		if assistantText == "" {
			assistantText = extractDialogueContent(t.ResponseDelta, "")
		}

		buf.WriteString("User: ")
		buf.WriteString(userText)
		buf.WriteString("\n\n")

		buf.WriteString("Assistant: ")
		buf.WriteString(assistantText)
		buf.WriteString("\n\n")
	}

	return buf.String()
}

// extractDialogueContent extracts plain text for summary corpora.
// Prefer roleFilter when set; always skip system/developer/tool messages.
func extractDialogueContent(delta any, roleFilter string) string {
	if delta == nil {
		return ""
	}
	want := strings.ToLower(strings.TrimSpace(roleFilter))

	appendContent := func(parts *[]string, role string, content any) {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "system" || role == "developer" || role == "tool" || role == "function" {
			return
		}
		if want != "" && role != "" && role != want {
			return
		}
		text := strings.TrimSpace(contentToPlainText(content))
		if text != "" {
			*parts = append(*parts, text)
		}
	}

	var parts []string
	switch v := delta.(type) {
	case map[string]any:
		if msgs, ok := v["messages"].([]any); ok {
			for _, raw := range msgs {
				msg, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				appendContent(&parts, fmt.Sprint(msg["role"]), msg["content"])
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n")
			}
		}
		if choices, ok := v["choices"].([]any); ok && len(choices) > 0 {
			if last, ok := choices[len(choices)-1].(map[string]any); ok {
				if msg, ok := last["message"].(map[string]any); ok {
					appendContent(&parts, fmt.Sprint(msg["role"]), msg["content"])
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n")
			}
		}
		appendContent(&parts, fmt.Sprint(v["role"]), v["content"])
	case []any:
		for _, raw := range v {
			msg, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			appendContent(&parts, fmt.Sprint(msg["role"]), msg["content"])
		}
	case string:
		return strings.TrimSpace(v)
	}
	return strings.Join(parts, "\n")
}

func contentToPlainText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, part := range c {
			switch p := part.(type) {
			case string:
				if strings.TrimSpace(p) != "" {
					texts = append(texts, p)
				}
			case map[string]any:
				if t, ok := p["text"].(string); ok && strings.TrimSpace(t) != "" {
					texts = append(texts, t)
				} else if t, ok := p["content"].(string); ok && strings.TrimSpace(t) != "" {
					texts = append(texts, t)
				}
			}
		}
		return strings.Join(texts, "\n")
	default:
		return ""
	}
}

// extractMessageContent keeps the legacy helper name for call sites that need
// "any readable text" while extractDialogueContent remains the shared parser.
// It is intentionally retained as a compatibility shim: deleting it would
// make otherwise-unrelated admin packages silently miss user/assistant content
// during incremental compilation or downstream embedding.
func extractMessageContent(delta any) (string, bool) {
	text := extractDialogueContent(delta, "")
	if text == "" {
		return "", false
	}
	return text, true
}

// callLLMForSummary 调用LLM生成会话标题和总结
func callLLMForSummary(ctx context.Context, conversationText string) (title string, summary string, err error) {
	// TODO: 这里应该调用实际的LLM API
	// 为了演示，我们先使用一个简化版本

	// 构建LLM请求
	systemPrompt := `你是一个专业的会话分析助手。请分析以下对话内容，生成：
1. 一个简洁的标题（10-20字）
2. 一个详细的总结（100-200字），包括：
   - 用户的主要需求
   - 讨论的关键话题
   - 达成的结论或结果

请以JSON格式返回：
{
  "title": "标题",
  "summary": "总结内容"
}`

	userPrompt := fmt.Sprintf("请分析以下对话：\n\n%s", conversationText)

	// 调用OpenAI API (or internal LLM gateway)
	requestBody := map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"temperature": 0.7,
		"max_tokens":  500,
	}

	requestJSON, err := json.Marshal(requestBody)
	if err != nil {
		return "", "", err
	}

	// TODO: Replace with actual LLM endpoint
	// For now, use a placeholder response
	llmEndpoint := "http://localhost:8080/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", llmEndpoint, bytes.NewReader(requestJSON))
	if err != nil {
		return "", "", err
	}

	req.Header.Set("Content-Type", "application/json")
	// TODO: Add API key if needed
	// req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Fallback: generate a simple summary without LLM
		return generateFallbackSummary(conversationText)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Fallback
		return generateFallbackSummary(conversationText)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return generateFallbackSummary(conversationText)
	}

	// Parse LLM response
	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &llmResp); err != nil {
		return generateFallbackSummary(conversationText)
	}

	if len(llmResp.Choices) == 0 {
		return generateFallbackSummary(conversationText)
	}

	// Parse JSON response from LLM
	var summaryResp struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}

	content := llmResp.Choices[0].Message.Content
	if err := json.Unmarshal([]byte(content), &summaryResp); err != nil {
		// Try to extract from plain text
		return extractTitleAndSummaryFromText(content)
	}

	return summaryResp.Title, summaryResp.Summary, nil
}

// generateFallbackSummary 生成一个简单的回退总结
func generateFallbackSummary(conversationText string) (string, string, error) {
	// Count turns
	turnCount := 0
	for i := 0; i < len(conversationText); i++ {
		if i+10 < len(conversationText) && conversationText[i:i+10] == "=== Turn " {
			turnCount++
		}
	}

	title := fmt.Sprintf("会话总结 (%d轮)", turnCount)

	// Extract first 200 chars as summary
	summary := conversationText
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}

	return title, summary, nil
}

// extractTitleAndSummaryFromText 从纯文本中提取标题和总结
func extractTitleAndSummaryFromText(text string) (string, string, error) {
	// Simple heuristic: first line is title, rest is summary
	lines := bytes.Split([]byte(text), []byte("\n"))

	if len(lines) == 0 {
		return "会话总结", text, nil
	}

	title := string(bytes.TrimSpace(lines[0]))
	if title == "" {
		title = "会话总结"
	}

	summary := ""
	if len(lines) > 1 {
		summary = string(bytes.TrimSpace(bytes.Join(lines[1:], []byte("\n"))))
	}

	if summary == "" {
		summary = text
	}

	return title, summary, nil
}
