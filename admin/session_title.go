package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	sessionTitleMaxRunes     = 80
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

	turnHint := strings.Count(corpus, "\n[")
	if turnHint < 1 {
		turnHint = 1
	}
	userContent := fmt.Sprintf("以下会话共约 %d 条记录（语料已清洗）。请阅读全部内容后生成标题：\n%s", turnHint, corpus)

	llmRes, err := h.callAdminLLMChat(ctx, r, apiKey, adminLLMTaskSessionTitle, taskID, userContent)
	if err != nil {
		writeError(w, http.StatusBadGateway, "标题生成失败: "+err.Error())
		return
	}
	title := llmRes.Content
	model := llmRes.ResolvedModel
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
		       rl.request_body::text, rl.response_body::text,
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
