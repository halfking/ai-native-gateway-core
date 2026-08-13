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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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
	Title          string `json:"title"`
	Summary        string `json:"summary"`
	TurnsAnalyzed  int    `json:"turns_analyzed"`
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	summary, err := api.generateSummary(ctx, &req)
	if err != nil {
		writeExportJSONError(w, http.StatusInternalServerError, fmt.Sprintf("summary failed: %v", err))
		return
	}

	writeExportJSON(w, http.StatusOK, summary)
}

func (api *SessionSummaryV2API) generateSummary(
	ctx context.Context,
	req *SessionSummaryRequest,
) (*SessionSummaryResponse, error) {
	// 1. Query turns from session_turns + session_bodies
	turns, err := api.queryTurnsForSummary(ctx, req.SessionID, req.Tenant, req.UpToTurn)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}

	if len(turns) == 0 {
		return nil, fmt.Errorf("no turns found for session %s", req.SessionID)
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
	query := `
		SELECT 
			t.turn_no,
			b.request_delta, 
			b.response_delta
		FROM public.session_turns t
		LEFT JOIN public.session_bodies b 
			ON t.session_id = b.session_id 
			AND t.turn_no = b.turn_no
			AND t.partition_date = b.partition_date
		WHERE t.session_id = $1 AND t.tenant_id = $2
	`
	args := []any{sessionID, tenantID}

	if upToTurn != nil {
		query += " AND t.turn_no <= $3"
		args = append(args, *upToTurn)
	}

	query += " ORDER BY t.turn_no ASC"  // Chronological order for summary

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

// buildConversationText 将turns转换为适合LLM分析的文本格式
func buildConversationText(turns []turnForSummary) string {
	var buf bytes.Buffer
	
	for _, t := range turns {
		buf.WriteString(fmt.Sprintf("=== Turn %d ===\n", t.TurnNo))
		
		// Request
		buf.WriteString("User: ")
		if t.RequestDelta != nil {
			if msg, ok := extractMessageContent(t.RequestDelta); ok {
				buf.WriteString(msg)
			} else {
				buf.WriteString(fmt.Sprintf("%v", t.RequestDelta))
			}
		}
		buf.WriteString("\n\n")
		
		// Response
		buf.WriteString("Assistant: ")
		if t.ResponseDelta != nil {
			if msg, ok := extractMessageContent(t.ResponseDelta); ok {
				buf.WriteString(msg)
			} else {
				buf.WriteString(fmt.Sprintf("%v", t.ResponseDelta))
			}
		}
		buf.WriteString("\n\n")
	}
	
	return buf.String()
}

// extractMessageContent 从delta JSON中提取文本内容
func extractMessageContent(delta any) (string, bool) {
	// Delta format: {"role": "user", "content": "text"}
	// Or: [{"role": "user", "content": "text"}]
	
	if deltaMap, ok := delta.(map[string]any); ok {
		if content, ok := deltaMap["content"].(string); ok {
			return content, true
		}
	}
	
	if deltaSlice, ok := delta.([]any); ok && len(deltaSlice) > 0 {
		if msg, ok := deltaSlice[0].(map[string]any); ok {
			if content, ok := msg["content"].(string); ok {
				return content, true
			}
		}
	}
	
	return "", false
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
		"max_tokens": 500,
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
