package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	sessionTitleMaxRunes     = 18
	sessionTitleMinCorpusLen = 40
	sessionTitleMaxCorpusLen = sessionSummaryMaxCorpusLen
)

type sessionTitleMeta struct {
	TaskID          string `json:"task_id"`
	ScopedSessionID string `json:"scoped_session_id,omitempty"`
	LogCount        int    `json:"log_count"`
	GeneratedAt     string `json:"generated_at"`
	APIKeyID        int    `json:"api_key_id"`
	Model           string `json:"model"`
}

type sessionTitleResponse struct {
	Title string           `json:"title"`
	Meta  sessionTitleMeta `json:"meta"`
}

func (h *Handler) handleSessionSummarizeTitle(w http.ResponseWriter, r *http.Request, taskID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	sc := parseSessionScope(r)
	scopedKey := scopedSessionIDKey(sc.SessionID)

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	if !requireSessionTaskAccess(w, r, ctx, h.db, taskID) {
		return
	}

	logs, err := h.loadTaskLogsForTitle(ctx, taskID, sc, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if len(logs) < 1 {
		writeError(w, http.StatusBadRequest, "该会话暂无请求记录，无法生成标题")
		return
	}

	corpus := buildSummaryCorpus(logs)
	if len(strings.TrimSpace(corpus)) < sessionTitleMinCorpusLen {
		writeError(w, http.StatusBadRequest, "会话有效语料不足，无法生成可靠标题")
		return
	}

	keyID, apiKey, err := h.pickFirstAvailableAPIKey(ctx, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := sessionTitleModel(logs)

	title, err := callSessionTitleLLM(ctx, r, apiKey, model, taskID, corpus)
	if err != nil {
		writeError(w, http.StatusBadGateway, "标题生成失败: "+err.Error())
		return
	}
	title = normalizeSessionTitle(title)
	if !isValidSessionTitle(title) {
		writeError(w, http.StatusBadGateway, "标题结果无效，请稍后重试")
		return
	}

	if err := h.upsertSessionTitle(ctx, taskID, scopedKey, title, model, keyID); err != nil {
		writeError(w, http.StatusInternalServerError, "保存标题失败")
		return
	}

	meta := sessionTitleMeta{
		TaskID:          taskID,
		ScopedSessionID: sc.SessionID,
		LogCount:        len(logs),
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		APIKeyID:        keyID,
		Model:           model,
	}
	writeJSON(w, http.StatusOK, sessionTitleResponse{Title: title, Meta: meta})
}

func (h *Handler) loadTaskLogsForTitle(ctx context.Context, taskID string, sc sessionScope, r *http.Request) ([]sessionLogForSummary, error) {
	where, args := sessionLogsWhere(taskID, sc, r)
	args = append(args, 300)
	limitArg := "$" + strconv.Itoa(len(args))
	rows, err := h.db.Query(ctx, `
		SELECT rl.ts, rl.request_preview, rl.response_preview,
		       `+requestLogStatusExpr+` AS request_status,
		       rl.error_kind, rl.client_model
		FROM request_logs rl
		`+where+`
		ORDER BY ts ASC
		LIMIT `+limitArg+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []sessionLogForSummary
	for rows.Next() {
		var row sessionLogForSummary
		var errKind *string
		var clientModel *string
		if err := rows.Scan(&row.Ts, &row.RequestPreview, &row.ResponsePreview, &row.RequestStatus, &errKind, &clientModel); err != nil {
			continue
		}
		row.ErrorKind = errKind
		row.ClientModel = clientModel
		logs = append(logs, row)
	}
	return logs, rows.Err()
}

func sessionTitleModel(logs []sessionLogForSummary) string {
	// Use a stable chat model for titles. pickSummaryModel mirrors the session's
	// last client_model, which may be a reasoning model that echoes
	// <think> or similar markers instead of a plain Chinese title.
	_ = logs
	return "gpt-4o-mini"
}

func callSessionTitleLLM(ctx context.Context, r *http.Request, apiKey, model, taskID, corpus string) (string, error) {
	turnHint := strings.Count(corpus, "\n[")
	if turnHint < 1 {
		turnHint = 1
	}
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": "你是会话标题生成助手。根据下方完整多轮会话日志，用中文生成一个简短准确的标题（不超过18字），概括用户目标与会话结果。" +
					"只输出标题纯文本：不要引号、编号、解释、XML/HTML 标签、thinking/redacted 标记或英文占位符。",
			},
			{
				"role": "user",
				"content": fmt.Sprintf("以下会话共约 %d 条记录（语料已清洗）。请阅读全部内容后生成标题：\n%s", turnHint, corpus),
			},
		},
		"temperature": 0.2,
		"max_tokens":  48,
	}
	body, _ := json.Marshal(payload)
	endpoint := gatewayEndpointFromRequest(r) + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Gw-Task-Id", taskID)
	req.Header.Set("X-Device-Seed", "admin-session-title")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("%s", msg)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("empty completion")
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("empty completion content")
	}
	return content, nil
}

func normalizeSessionTitle(raw string) string {
	s := strings.TrimSpace(raw)
	s = reXMLLikeTag.ReplaceAllString(s, "")
	s = strings.Trim(s, `"'「」『』""`)
	if idx := strings.IndexAny(s, "\n\r"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	if strings.HasPrefix(s, "标题：") || strings.HasPrefix(s, "标题:") {
		s = strings.TrimSpace(s[strings.Index(s, "：")+len("："):])
		if strings.HasPrefix(s, "标题") {
			s = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "标题:"), "标题："))
		}
	}
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > sessionTitleMaxRunes {
		runes := []rune(s)
		s = string(runes[:sessionTitleMaxRunes]) + "…"
	}
	return strings.TrimSpace(s)
}

func isValidSessionTitle(title string) bool {
	s := strings.TrimSpace(title)
	if utf8.RuneCountInString(s) < 2 {
		return false
	}
	if strings.ContainsAny(s, "<>") {
		return false
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "redacted") || strings.Contains(lower, "thinking") {
		return false
	}
	if strings.Contains(lower, "无法") && strings.Contains(lower, "标题") {
		return false
	}
	if strings.Contains(lower, "无足够") || strings.Contains(lower, "信息不足") {
		return false
	}
	// Reject titles that are mostly ASCII (likely tag names / placeholders).
	ascii := 0
	for _, r := range s {
		if r < 128 {
			ascii++
		}
	}
	if ascii*2 > len([]rune(s)) {
		return false
	}
	return true
}

func scopedSessionIDKey(sessionID string) string {
	return strings.TrimSpace(sessionID)
}

func (h *Handler) loadStoredSessionTitle(ctx context.Context, taskID, scopedSessionID string) (string, bool) {
	if h.db == nil {
		return "", false
	}
	var title string
	err := h.db.QueryRow(ctx, `
		SELECT title FROM session_titles
		WHERE task_id = $1 AND scoped_session_id = $2
	`, taskID, scopedSessionIDKey(scopedSessionID)).Scan(&title)
	if err != nil || strings.TrimSpace(title) == "" {
		return "", false
	}
	return title, true
}

func (h *Handler) upsertSessionTitle(ctx context.Context, taskID, scopedSessionID, title, model string, apiKeyID int) error {
	if h.db == nil {
		return fmt.Errorf("database not configured")
	}
	_, err := h.db.Exec(ctx, `
		INSERT INTO session_titles (task_id, scoped_session_id, title, generated_at, model, api_key_id)
		VALUES ($1, $2, $3, NOW(), $4, $5)
		ON CONFLICT (task_id, scoped_session_id) DO UPDATE SET
			title = EXCLUDED.title,
			generated_at = EXCLUDED.generated_at,
			model = EXCLUDED.model,
			api_key_id = EXCLUDED.api_key_id
	`, taskID, scopedSessionIDKey(scopedSessionID), title, model, apiKeyID)
	return err
}

func (h *Handler) loadSessionTitlesBatch(ctx context.Context, keys [][2]string) map[string]string {
	out := make(map[string]string, len(keys))
	if h.db == nil || len(keys) == 0 {
		return out
	}
	taskIDs := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k[0] == "" {
			continue
		}
		if _, ok := seen[k[0]]; ok {
			continue
		}
		seen[k[0]] = struct{}{}
		taskIDs = append(taskIDs, k[0])
	}
	if len(taskIDs) == 0 {
		return out
	}
	rows, err := h.db.Query(ctx, `
		SELECT task_id, scoped_session_id, title
		FROM session_titles
		WHERE task_id = ANY($1)
	`, taskIDs)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, scopedID, title string
		if err := rows.Scan(&taskID, &scopedID, &title); err != nil {
			continue
		}
		out[sessionTitleMapKey(taskID, scopedID)] = title
	}
	return out
}

func sessionTitleMapKey(taskID, scopedSessionID string) string {
	return taskID + "\x00" + scopedSessionIDKey(scopedSessionID)
}
