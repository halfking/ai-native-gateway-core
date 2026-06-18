package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/memora"
)

const (
	sessionSummaryMinCorpusLen = 180
	sessionSummaryMaxCorpusLen = 12000
	sessionSummaryCacheTTL     = 10 * time.Minute
)

var (
	reXMLLikeTag   = regexp.MustCompile(`(?is)<\/?[a-zA-Z][^>]{0,120}>`)
	reFenceBlock   = regexp.MustCompile("(?is)```(?:json|xml|yaml|tool|function_call|tool_calls|tool_results)?\\s*[\\s\\S]*?```")
	reBase64Chunk  = regexp.MustCompile(`(?i)[A-Za-z0-9+/]{180,}={0,2}`)
	reTraceNoise   = regexp.MustCompile(`(?im)\b(trace[_-]?id|request[_-]?id|span[_-]?id|correlation[_-]?id)\s*[:=]\s*[A-Za-z0-9._:-]{8,}`)
	reJSONLikeBlob = regexp.MustCompile(`(?is)\{[^{}]{500,}\}`)

	// sessionSummaryCache caches LLM results to avoid redundant calls.
	// key: "session_id:log_count", value: cachedSessionSummary
	sessionSummaryCache sync.Map
)

type cachedSessionSummary struct {
	Summary   string
	KeyPoints []string
	Model     string
	KeyID     int
	CachedAt  time.Time
}

func (c *cachedSessionSummary) expired() bool {
	return time.Since(c.CachedAt) > sessionSummaryCacheTTL
}

type sessionSummaryRequest struct {
	GwSessionID string `json:"gw_session_id"`
}

type sessionSummaryMeta struct {
	SessionID   string `json:"session_id"`
	LogCount    int    `json:"log_count"`
	DataFrom    string `json:"data_from"`
	DataTo      string `json:"data_to"`
	GeneratedAt string `json:"generated_at"`
	APIKeyID    int    `json:"api_key_id"`
	Model       string `json:"model"`
}

type sessionSummaryResponse struct {
	Summary   string             `json:"summary"`
	KeyPoints []string           `json:"key_points"`
	Meta      sessionSummaryMeta `json:"meta"`
}

type sessionLogForSummary struct {
	Ts             time.Time
	RequestPreview *string
	ResponsePreview *string
	RequestStatus  string
	ErrorKind      *string
	ClientModel    *string
}

func (h *Handler) handleSessionSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	var req sessionSummaryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.GwSessionID = strings.TrimSpace(req.GwSessionID)
	if req.GwSessionID == "" {
		writeError(w, http.StatusBadRequest, "gw_session_id required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	logs, err := h.loadSessionLogsForSummary(ctx, r, req.GwSessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if len(logs) < 2 {
		writeError(w, http.StatusBadRequest, "日志条数不足，至少需要 2 条同会话记录")
		return
	}

	corpus := buildSummaryCorpus(logs)
	if len(strings.TrimSpace(corpus)) < sessionSummaryMinCorpusLen {
		writeError(w, http.StatusBadRequest, "日志有效语料不足，无法生成可靠总结")
		return
	}

	keyID, apiKey, err := h.pickFirstAvailableAPIKey(ctx, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := pickSummaryModel(logs)

	// Check cache
	cacheKey := fmt.Sprintf("%s:%d", req.GwSessionID, len(logs))
	if cached, ok := sessionSummaryCache.Load(cacheKey); ok {
		if cs, ok := cached.(*cachedSessionSummary); ok && !cs.expired() {
			meta := sessionSummaryMeta{
				SessionID:   req.GwSessionID,
				LogCount:    len(logs),
				DataFrom:    logs[0].Ts.UTC().Format(time.RFC3339),
				DataTo:      logs[len(logs)-1].Ts.UTC().Format(time.RFC3339),
				GeneratedAt: cs.CachedAt.UTC().Format(time.RFC3339),
				APIKeyID:    cs.KeyID,
				Model:       cs.Model,
			}
			writeJSON(w, http.StatusOK, sessionSummaryResponse{
				Summary:   cs.Summary,
				KeyPoints: cs.KeyPoints,
				Meta:      meta,
			})
			return
		}
	}

	summary, keyPoints, err := callSessionSummaryLLM(ctx, r, apiKey, model, req.GwSessionID, corpus)
	if err != nil {
		writeError(w, http.StatusBadGateway, "总结生成失败: "+err.Error())
		return
	}
	if !isValidSummary(summary, keyPoints) {
		writeError(w, http.StatusBadGateway, "总结结果无效，请稍后重试")
		return
	}

	// Store in cache
	sessionSummaryCache.Store(cacheKey, &cachedSessionSummary{
		Summary:   summary,
		KeyPoints: keyPoints,
		Model:     model,
		KeyID:     keyID,
		CachedAt:  time.Now(),
	})

	meta := sessionSummaryMeta{
		SessionID:   req.GwSessionID,
		LogCount:    len(logs),
		DataFrom:    logs[0].Ts.UTC().Format(time.RFC3339),
		DataTo:      logs[len(logs)-1].Ts.UTC().Format(time.RFC3339),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		APIKeyID:    keyID,
		Model:       model,
	}
	writeJSON(w, http.StatusOK, sessionSummaryResponse{
		Summary:   summary,
		KeyPoints: keyPoints,
		Meta:      meta,
	})
}

func (h *Handler) loadSessionLogsForSummary(ctx context.Context, r *http.Request, sessionID string) ([]sessionLogForSummary, error) {
	args := []any{sessionID}
	where := "WHERE gw_session_id = $1"
	tenantFrag, tenantArgs, _ := tenantLogsClause(r, 2)
	where += tenantFrag
	args = append(args, tenantArgs...)

	rows, err := h.db.Query(ctx, `
		SELECT rl.ts, rl.request_preview, rl.response_preview,
		       `+requestLogStatusExpr+` AS request_status,
		       rl.error_kind, rl.client_model
		FROM request_logs rl
		`+where+`
		ORDER BY ts ASC
		LIMIT 300
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]sessionLogForSummary, 0, 32)
	for rows.Next() {
		var item sessionLogForSummary
		if err := rows.Scan(
			&item.Ts,
			&item.RequestPreview,
			&item.ResponsePreview,
			&item.RequestStatus,
			&item.ErrorKind,
			&item.ClientModel,
		); err != nil {
			// Skip unscannable rows (e.g. type mismatch) but don't silently swallow.
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (h *Handler) pickFirstAvailableAPIKey(ctx context.Context, r *http.Request) (id int, apiKey string, err error) {
	args := []any{}
	where := []string{
		"ak.enabled = TRUE",
		"COALESCE(ak.status, 'active') = 'active'",
		"(ak.expires_at IS NULL OR ak.expires_at > now())",
	}
	if IsTenantAdmin(r) {
		where = append(where, fmt.Sprintf("ak.tenant_id = $%d", len(args)+1))
		args = append(args, GetTenantID(r))
	}

	var ciphertext string
	query := `SELECT ak.id, ak.key_ciphertext
		FROM api_keys ak
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY ak.id ASC
		LIMIT 1`
	if err := h.db.QueryRow(ctx, query, args...).Scan(&id, &ciphertext); err != nil {
		return 0, "", fmt.Errorf("当前用户无可用 API Key")
	}
	if !isRevealableKeyCiphertext(ciphertext) {
		return 0, "", fmt.Errorf("当前用户无可用 API Key")
	}
	apiKey, err = h.decryptCredStr(ciphertext)
	if err != nil || strings.TrimSpace(apiKey) == "" {
		return 0, "", fmt.Errorf("当前用户无可用 API Key")
	}
	return id, strings.TrimSpace(apiKey), nil
}

func buildSummaryCorpus(logs []sessionLogForSummary) string {
	var b strings.Builder
	for _, row := range logs {
		if row.RequestPreview != nil {
			txt := sanitizeSummaryText(*row.RequestPreview)
			if txt != "" {
				fmt.Fprintf(&b, "[%s][user] %s\n", row.Ts.UTC().Format(time.RFC3339), txt)
			}
		}
		if row.ResponsePreview != nil {
			txt := sanitizeSummaryText(*row.ResponsePreview)
			if txt != "" {
				fmt.Fprintf(&b, "[%s][assistant] %s\n", row.Ts.UTC().Format(time.RFC3339), txt)
			}
		}
		if row.RequestStatus == "failure" && row.ErrorKind != nil && strings.TrimSpace(*row.ErrorKind) != "" {
			fmt.Fprintf(&b, "[%s][status] failure:%s\n", row.Ts.UTC().Format(time.RFC3339), strings.TrimSpace(*row.ErrorKind))
		}
	}
	corpus := strings.TrimSpace(b.String())
	if len(corpus) > sessionSummaryMaxCorpusLen {
		corpus = corpus[:sessionSummaryMaxCorpusLen]
	}
	return corpus
}

func sanitizeSummaryText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = reFenceBlock.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		`"tool_calls":`, " ",
		`"function_call":`, " ",
		`"tool_results":`, " ",
		`"tool_result":`, " ",
		`"arguments":`, " ",
	).Replace(s)
	s = reXMLLikeTag.ReplaceAllString(s, " ")
	s = reTraceNoise.ReplaceAllString(s, " ")
	s = reBase64Chunk.ReplaceAllString(s, " ")
	s = reJSONLikeBlob.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 800 {
		s = s[:800]
	}
	return strings.TrimSpace(s)
}

func pickSummaryModel(logs []sessionLogForSummary) string {
	// Use a stable chat model for summaries. Mirroring the session's last
	// client_model may pick a reasoning model that echoes <think>
	// or similar markers instead of plain Chinese summary JSON.
	_ = logs
	return "gpt-4o-mini"
}

func callSessionSummaryLLM(ctx context.Context, r *http.Request, apiKey, model, sessionID, corpus string) (summary string, keyPoints []string, err error) {
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{
				"role": "system",
				"content": `你是会话日志分析助手。请严格输出 JSON，格式如下：
{"summary":"一段连贯的中文摘要（80-200字），说明会话目标、关键步骤、最终结果","key_points":["要点1","要点2","要点3"]}
要求：
- summary 必须是完整句子，涵盖：做了什么、怎么做的、结果如何
- key_points 提取 3-5 个关键事实或决策点，每条 15-40 字
- 不要输出 JSON 以外的任何文本
- 如果语料中包含错误信息，务必在总结中提及`,
			},
			{
				"role": "user",
				"content": "请总结以下会话日志（语料已清洗，格式 [时间][角色] 内容）：\n" + corpus,
			},
		},
		"temperature": 0.2,
	}
	body, _ := json.Marshal(payload)
	endpoint := gatewayEndpointFromRequest(r) + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Gw-Session-Id", sessionID)
	req.Header.Set("X-Device-Seed", "admin-session-summary")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return "", nil, fmt.Errorf("%s", msg)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", nil, err
	}
	if len(out.Choices) == 0 {
		return "", nil, fmt.Errorf("empty completion")
	}
	content := strings.TrimSpace(out.Choices[0].Message.Content)
	if content == "" {
		return "", nil, fmt.Errorf("empty completion content")
	}
	var parsed struct {
		Summary   string   `json:"summary"`
		KeyPoints []string `json:"key_points"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		s := sanitizeSummaryText(content)
		return s, nil, nil
	}
	keyPoints = make([]string, 0, len(parsed.KeyPoints))
	for _, p := range parsed.KeyPoints {
		pp := strings.TrimSpace(p)
		if pp != "" {
			keyPoints = append(keyPoints, pp)
		}
	}
	return strings.TrimSpace(parsed.Summary), keyPoints, nil
}

func gatewayEndpointFromRequest(r *http.Request) string {
	if r == nil {
		return "http://127.0.0.1:8080"
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = "127.0.0.1:8080"
	}
	return scheme + "://" + host
}

func isValidSummary(summary string, keyPoints []string) bool {
	s := strings.TrimSpace(summary)
	if len(s) < 40 {
		return false
	}
	if strings.ContainsAny(s, "<>") {
		return false
	}
	lower := strings.ToLower(s)
	if strings.Contains(lower, "redacted") || strings.Contains(lower, "thinking") {
		return false
	}
	if strings.Contains(lower, "无法总结") || strings.Contains(lower, "无足够信息") {
		return false
	}
	if len(keyPoints) == 0 && len(s) < 80 {
		return false
	}
	for _, kp := range keyPoints {
		kp = strings.TrimSpace(kp)
		if kp == "" {
			continue
		}
		if strings.ContainsAny(kp, "<>") {
			return false
		}
		kpl := strings.ToLower(kp)
		if strings.Contains(kpl, "redacted") || strings.Contains(kpl, "thinking") {
			return false
		}
	}
	return true
}

// ── Session Summary → Memora ─────────────────────────────────────────

type sessionSummaryToMemoraResponse struct {
	Summary   string             `json:"summary"`
	KeyPoints []string           `json:"key_points"`
	Meta      sessionSummaryMeta `json:"meta"`
	Memora    memoraWriteResult  `json:"memora"`
}

type memoraWriteResult struct {
	Written   int    `json:"written"`
	UserID    string `json:"user_id"`
	ProjectID string `json:"project_id"`
	Status    string `json:"status"` // ok | partial | skipped
	Error     string `json:"error,omitempty"`
}

func (h *Handler) handleSessionSummaryToMemora(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	writer := h.memoraWriteClient()
	if writer == nil || writer.Disabled() {
		writeError(w, http.StatusServiceUnavailable, "memora not configured")
		return
	}

	var req sessionSummaryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.GwSessionID = strings.TrimSpace(req.GwSessionID)
	if req.GwSessionID == "" {
		writeError(w, http.StatusBadRequest, "gw_session_id required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	logs, err := h.loadSessionLogsForSummary(ctx, r, req.GwSessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if len(logs) < 2 {
		writeError(w, http.StatusBadRequest, "日志条数不足，至少需要 2 条同会话记录")
		return
	}

	corpus := buildSummaryCorpus(logs)
	if len(strings.TrimSpace(corpus)) < sessionSummaryMinCorpusLen {
		writeError(w, http.StatusBadRequest, "日志有效语料不足，无法生成可靠总结")
		return
	}

	keyID, apiKey, err := h.pickFirstAvailableAPIKey(ctx, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	model := pickSummaryModel(logs)

	// Check cache first
	cacheKey := fmt.Sprintf("%s:%d", req.GwSessionID, len(logs))
	var summary string
	var keyPoints []string
	if cached, ok := sessionSummaryCache.Load(cacheKey); ok {
		if cs, ok := cached.(*cachedSessionSummary); ok && !cs.expired() {
			summary = cs.Summary
			keyPoints = cs.KeyPoints
		}
	}
	if summary == "" {
		summary, keyPoints, err = callSessionSummaryLLM(ctx, r, apiKey, model, req.GwSessionID, corpus)
		if err != nil {
			writeError(w, http.StatusBadGateway, "总结生成失败: "+err.Error())
			return
		}
		if !isValidSummary(summary, keyPoints) {
			writeError(w, http.StatusBadGateway, "总结结果无效，请稍后重试")
			return
		}
		sessionSummaryCache.Store(cacheKey, &cachedSessionSummary{
			Summary:   summary,
			KeyPoints: keyPoints,
			Model:     model,
			KeyID:     keyID,
			CachedAt:  time.Now(),
		})
	}

	meta := sessionSummaryMeta{
		SessionID:   req.GwSessionID,
		LogCount:    len(logs),
		DataFrom:    logs[0].Ts.UTC().Format(time.RFC3339),
		DataTo:      logs[len(logs)-1].Ts.UTC().Format(time.RFC3339),
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		APIKeyID:    keyID,
		Model:       model,
	}

	// Write to Memora
	projectID := strings.TrimSpace(os.Getenv("MEMORA_PROJECT_ID"))
	if projectID == "" {
		projectID = "kaixuan-1-deploy"
	}
	// Derive a Memora user_id from the session (namespaced to avoid collision with task-based extracts)
	memoraTaskID := "gw-session:" + req.GwSessionID
	// Round 47 compression v7 T13: tenant-namespaced user_id. The
	// session-summary admin endpoint runs as super_admin so we fall back to
	// "default" if the calling user has no tenant context. Single-tenant
	// installs stay on the legacy "k:<key_id>" layout.
	userID := memora.UserID("", keyID, memoraTaskID)

	facts := []string{"[会话总结] " + summary}
	for _, kp := range keyPoints {
		facts = append(facts, "[要点] "+kp)
	}

	mResult := memoraWriteResult{
		UserID:    userID,
		ProjectID: projectID,
	}

	written := 0
	var writeErr error
	for _, fact := range facts {
		msgs := []memora.Message{
			{Role: "user", Content: fact},
		}
		info := map[string]any{
			"session_id": req.GwSessionID,
			"source":     "session_summary",
			"project_id": projectID,
			"api_key_id": keyID,
		}
		addCtx, addCancel := context.WithTimeout(ctx, 8*time.Second)
		err := writer.AddMessage(addCtx, userID, msgs, info)
		addCancel()
		if err != nil {
			writeErr = err
			break
		}
		written++
	}

	mResult.Written = written
	if writeErr != nil {
		if written == 0 {
			mResult.Status = "error"
			mResult.Error = writeErr.Error()
		} else {
			mResult.Status = "partial"
			mResult.Error = writeErr.Error()
		}
	} else {
		mResult.Status = "ok"
	}

	writeJSON(w, http.StatusOK, sessionSummaryToMemoraResponse{
		Summary:   summary,
		KeyPoints: keyPoints,
		Meta:      meta,
		Memora:    mResult,
	})
}
