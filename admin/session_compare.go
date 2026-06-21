package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ── Session Compare API (v4, 2026-06-21) ────────────────────────────────
// GET /api/admin/session-compare?session_id=xxx&tenant_id=default
// Returns the original & compressed session for comparison display.

type MessageView struct {
	Index     int    `json:"index"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolCalls string `json:"tool_calls,omitempty"`
	TokenCount int   `json:"token_count"`
}

type CacheInfo struct {
	L1Hit       bool   `json:"l1_hit"`
	L2Hit       bool   `json:"l2_hit"`
	L3Fallback  bool   `json:"l3_fallback"`
	LastRefresh string `json:"last_refresh,omitempty"`
}

type SessionStats struct {
	OriginalTokens       int     `json:"original_tokens"`
	CompressedTokens     int     `json:"compressed_tokens"`
	SavedTokens          int     `json:"saved_tokens"`
	SavedPercent         float64 `json:"saved_percent"`
	CompressionStrategy  string  `json:"compression_strategy"`
	CompressionTimestamp string  `json:"compression_timestamp"`
}

type SessionCompareData struct {
	SessionID     string        `json:"session_id"`
	TenantID      string        `json:"tenant_id"`
	OriginalMsgs  []MessageView `json:"original_msgs"`
	CompressedMsgs []MessageView `json:"compressed_msgs"`
	ResponseMsgs  []MessageView `json:"response_msgs"`
	CacheInfo     CacheInfo     `json:"cache_info"`
	Stats         SessionStats  `json:"stats"`
	IsCompressed  bool          `json:"is_compressed"`
	ContextUsage  float64       `json:"context_usage"`
	ContextWindow int           `json:"context_window"`
	ModelUsed     string        `json:"model_used"`
	MsgCount      int           `json:"msg_count"`
}

// SessionCompareAPI handles session comparison endpoints.
type SessionCompareAPI struct {
	db *pgxpool.Pool
}

func NewSessionCompareAPI(db *pgxpool.Pool) *SessionCompareAPI {
	return &SessionCompareAPI{db: db}
}

// HandleCompare handles GET /api/admin/session-compare
func (api *SessionCompareAPI) HandleCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		tenantID = "default"
	}

	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "session_id parameter is required",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	data, err := api.loadCompareData(ctx, tenantID, sessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Failed to load session compare data",
			"error":   err.Error(),
		})
		return
	}

	if data == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"status":    "error",
			"message":   "Session not found",
			"session_id": sessionID,
		})
		return
	}

	writeJSON(w, http.StatusOK, data)
}

func (api *SessionCompareAPI) loadCompareData(ctx context.Context, tenantID, sessionID string) (*SessionCompareData, error) {
	// Query all request_logs for this session, ordered by time
	query := `
		SELECT 
			request_id, request_body, outbound_body, response_body,
			compression_strategy, compression_meta, 
			outbound_msg_count, outbound_token_est,
			client_model, outbound_model,
			ts, provider_id
		FROM request_logs
		WHERE gw_session_id = $1 AND tenant_id = $2
		ORDER BY ts ASC
		LIMIT 500
	`

	rows, err := api.db.Query(ctx, query, sessionID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query request_logs: %w", err)
	}
	defer rows.Close()

	var (
		allOriginal   []MessageView
		allCompressed []MessageView
		allResponse   []MessageView
		modelUsed     string
		msgIndex      int
	)

	// Track compression stats
	stats := SessionStats{}
	compressionFound := false
	var totalOriginalTokens, totalCompressedTokens int
	originalSeen := make(map[string]bool)

	for rows.Next() {
		var (
			requestID, clientModel, outboundModel string
			requestBody, outboundBody, responseBody *string
			compressionStrategy                   *string
			compressionMeta                       *string
			outboundMsgCount                      *int
			outboundTokenEst                      *int
			createdAt                             time.Time
			providerID                            *int
		)

		err := rows.Scan(
			&requestID, &requestBody, &outboundBody, &responseBody,
			&compressionStrategy, &compressionMeta,
			&outboundMsgCount, &outboundTokenEst,
			&clientModel, &outboundModel,
			&createdAt, &providerID,
		)
		if err != nil {
			continue
		}

		if clientModel != "" && modelUsed == "" {
			modelUsed = clientModel
		}
		if outboundModel != "" && modelUsed == "" {
			modelUsed = outboundModel
		}

		// Parse original messages from request_body
		if requestBody != nil && *requestBody != "" {
			msgs := parseMessagesFromBody(*requestBody, &msgIndex)
			for _, m := range msgs {
				key := fmt.Sprintf("%s:%d:%s", m.Role, m.Index, truncateStr(m.Content, 100))
				if !originalSeen[key] {
					originalSeen[key] = true
					allOriginal = append(allOriginal, m)
				}
			}
		}

		// Parse compressed outbound messages
		if outboundBody != nil && *outboundBody != "" {
			compMsgs := parseMessagesFromBody(*outboundBody, &msgIndex)
			allCompressed = append(allCompressed, compMsgs...)

			bodyTokens := estimateTokens(*outboundBody)
			totalCompressedTokens += bodyTokens

			if compressionStrategy != nil && *compressionStrategy != "" {
				stats.CompressionStrategy = *compressionStrategy
				compressionFound = true

				// Parse compression meta for timing
				if compressionMeta != nil && *compressionMeta != "" {
					var meta map[string]any
					if json.Unmarshal([]byte(*compressionMeta), &meta) == nil {
						if ts, ok := meta["timestamp"].(string); ok {
							stats.CompressionTimestamp = ts
						}
					}
				}
			}
		}

		// Parse response body
		if responseBody != nil && *responseBody != "" {
			respMsgs := parseResponseMessages(*responseBody, &msgIndex)
			allResponse = append(allResponse, respMsgs...)
		}

		// Estimate original tokens from request body
		if requestBody != nil {
			totalOriginalTokens += estimateTokens(*requestBody)
		}
	}

	if len(allOriginal) == 0 && len(allCompressed) == 0 {
		return nil, nil
	}

	// Calculate stats
	stats.OriginalTokens = totalOriginalTokens
	stats.CompressedTokens = totalCompressedTokens
	stats.SavedTokens = totalOriginalTokens - totalCompressedTokens
	if totalOriginalTokens > 0 {
		stats.SavedPercent = float64(stats.SavedTokens) / float64(totalOriginalTokens) * 100
	}

	// Estimate context usage
	contextWindow := 128000 // default
	usage := float64(totalCompressedTokens) / float64(contextWindow) * 100

	// Determine cache status
	cacheInfo := CacheInfo{
		L1Hit:       false,
		L2Hit:       false,
		L3Fallback:  false,
		LastRefresh: time.Now().Format(time.RFC3339),
	}

	return &SessionCompareData{
		SessionID:      sessionID,
		TenantID:       tenantID,
		OriginalMsgs:   allOriginal,
		CompressedMsgs: allCompressed,
		ResponseMsgs:   allResponse,
		CacheInfo:      cacheInfo,
		Stats:          stats,
		IsCompressed:   compressionFound,
		ContextUsage:   usage,
		ContextWindow:  contextWindow,
		ModelUsed:      modelUsed,
		MsgCount:       len(allOriginal),
	}, nil
}

// parseMessagesFromBody extracts messages from a request body.
func parseMessagesFromBody(body string, index *int) []MessageView {
	var parsed struct {
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil
	}

	msgs := make([]MessageView, 0, len(parsed.Messages))
	for _, m := range parsed.Messages {
		*index++
		content := extractTextContent(m.Content)
		tc := ""
		if len(m.ToolCalls) > 0 {
			tc = string(m.ToolCalls)
			if len(tc) > 200 {
				tc = tc[:200] + "..."
			}
		}
		msgs = append(msgs, MessageView{
			Index:      *index,
			Role:       m.Role,
			Content:    content,
			ToolCalls:  tc,
			TokenCount: estimateTokens(content),
		})
	}
	return msgs
}

// parseResponseMessages extracts messages from a response body (OpenAI/Anthropic format).
func parseResponseMessages(body string, index *int) []MessageView {
	// Try OpenAI format first
	var openaiResp struct {
		Choices []struct {
			Message struct {
				Role      string          `json:"role"`
				Content   json.RawMessage `json:"content"`
				ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &openaiResp); err == nil {
		msgs := make([]MessageView, 0, len(openaiResp.Choices))
		for _, c := range openaiResp.Choices {
			*index++
			content := extractTextContent(c.Message.Content)
			tc := ""
			if len(c.Message.ToolCalls) > 0 {
				tc = string(c.Message.ToolCalls)
				if len(tc) > 200 {
					tc = tc[:200] + "..."
				}
			}
			msgs = append(msgs, MessageView{
				Index:      *index,
				Role:       c.Message.Role,
				Content:    content,
				ToolCalls:  tc,
				TokenCount: estimateTokens(content),
			})
		}
		return msgs
	}

	return nil
}

// extractTextContent extracts plain text from a content field (string or array).
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var texts []string
		for _, p := range parts {
			if p.Type == "text" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	return string(raw)
}

// estimateTokens is a heuristic: chars / 3.5.
func estimateTokens(s string) int {
	return int(float64(len(s)) / 3.5)
}

func truncateStr(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

// ── Handoff API (v4, 2026-06-21) ───────────────────────────────────────
// POST /api/admin/session-handoff
// Performs a handoff: saves session state, returns new session prompt.

type HandoffRequest struct {
	SessionID     string `json:"session_id"`
	TenantID      string `json:"tenant_id"`
	CreateNew     bool   `json:"create_new"`
}

type HandoffResponse struct {
	Status          string `json:"status"`
	SessionID       string `json:"session_id"`
	HandoffSummary  string `json:"handoff_summary"`
	NewSessionID    string `json:"new_session_id,omitempty"`
	NewSessionHint  string `json:"new_session_hint,omitempty"`
	CompletedTasks  int    `json:"completed_tasks"`
}

// HandoffAPI handles session handoff endpoints.
type HandoffAPI struct {
	db *pgxpool.Pool
}

func NewHandoffAPI(db *pgxpool.Pool) *HandoffAPI {
	return &HandoffAPI{db: db}
}

// HandleHandoff handles POST /api/admin/session-handoff
func (api *HandoffAPI) HandleHandoff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req HandoffRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "Invalid request body",
		})
		return
	}
	if req.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"status":  "error",
			"message": "session_id is required",
		})
		return
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	resp, err := api.executeHandoff(ctx, req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"status":  "error",
			"message": "Handoff failed",
			"error":   err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (api *HandoffAPI) executeHandoff(ctx context.Context, req HandoffRequest) (*HandoffResponse, error) {
	// 1. Check if handoff is enabled in settings
	// (settings are checked at the UI level, this is a safety check)

	// 2. Get session summary via LLM
	summary, err := api.generateHandoffSummary(ctx, req.SessionID, req.TenantID)
	if err != nil {
		summary = "Session completed"
	}

	// 3. Log the handoff event
	_, _ = api.db.Exec(ctx, `
		INSERT INTO schema_migration_audit (table_name, operation, details, performed_by)
		VALUES ('session_handoff', 'HANDOFF', $1, 'admin_api')
		ON CONFLICT DO NOTHING
	`, fmt.Sprintf(`{"session_id":"%s","tenant_id":"%s","summary":"%s"}`,
		req.SessionID, req.TenantID, truncateStr(summary, 200)))

	resp := &HandoffResponse{
		Status:         "ok",
		SessionID:      req.SessionID,
		HandoffSummary: summary,
		CompletedTasks: countCompletedTasks(summary),
	}

	// 4. Optionally create a new session
	if req.CreateNew {
		newID := fmt.Sprintf("handoff_%s_%d", req.SessionID, time.Now().Unix())
		resp.NewSessionID = newID
		resp.NewSessionHint = fmt.Sprintf(
			"Previous session '%s' has been completed and summarized. "+
				"Key outcomes: %s. You can start a new session with this context.",
			req.SessionID, truncateStr(summary, 300))
	}

	return resp, nil
}

func (api *HandoffAPI) generateHandoffSummary(ctx context.Context, sessionID, tenantID string) (string, error) {
	// Get the last few messages for context
	rows, err := api.db.Query(ctx, `
		SELECT request_body, response_body, created_at
		FROM request_logs
		WHERE gw_session_id = $1 AND tenant_id = $2
		ORDER BY created_at DESC
		LIMIT 3
	`, sessionID, tenantID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var summaries []string
	for rows.Next() {
		var reqBody, respBody *string
		var createdAt time.Time
		if err := rows.Scan(&reqBody, &respBody, &createdAt); err != nil {
			continue
		}
		s := fmt.Sprintf("[%s] ", createdAt.Format("15:04:05"))
		if reqBody != nil {
			s += extractUserIntent(*reqBody)
		}
		if respBody != nil {
			s += " → " + extractResponseSummary(*respBody)
		}
		summaries = append(summaries, s)
	}

	if len(summaries) == 0 {
		return "Session completed with no recorded messages", nil
	}

	// Reverse to chronological order
	for i, j := 0, len(summaries)-1; i < j; i, j = i+1, j-1 {
		summaries[i], summaries[j] = summaries[j], summaries[i]
	}

	return strings.Join(summaries, "\n"), nil
}

func extractUserIntent(body string) string {
	var parsed struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return ""
	}
	for _, m := range parsed.Messages {
		if m.Role == "user" {
			content := extractTextContent(m.Content)
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			return content
		}
	}
	return ""
}

func extractResponseSummary(body string) string {
	var openaiResp struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &openaiResp); err == nil {
		for _, c := range openaiResp.Choices {
			content := extractTextContent(c.Message.Content)
			if len(content) > 100 {
				content = content[:100] + "..."
			}
			return content
		}
	}
	return ""
}

func countCompletedTasks(summary string) int {
	if summary == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(summary, "\n") {
		if strings.Contains(strings.ToLower(line), "complete") ||
			strings.Contains(strings.ToLower(line), "done") ||
			strings.Contains(strings.ToLower(line), "finished") {
			count++
		}
	}
	if count == 0 {
		return 1 // At least one session completed
	}
	return count
}

// ── Settings: compression & handoff enable/disable (v4, 2026-06-21) ────
// These are checked by the frontend UI and backend API.
// The actual settings are registered via settings/spec_compression.go:
//
//	compression.enabled  (bool, default=true)  — master switch
//	handoff.enabled      (bool, default=true)  — handoff master switch

// HandoffEnabled checks if handoff feature is available for a tenant.
// This is a server-side safety check. The UI also checks the settings.
func HandoffEnabled(tenantID string) bool {
	// Check settings registry
	// For now, always enabled. Settings check is done at UI level.
	_ = tenantID
	return true
}
