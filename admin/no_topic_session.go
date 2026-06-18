package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/memora"
)

const noTopicSessionPrefix = "/api/system/no-topic-session/"

func noTopicSessionKey(prefix, hourStart string) string {
	h := sha256.Sum256([]byte(prefix + "\x00" + hourStart))
	return "notopic:" + hex.EncodeToString(h[:8]) + ":" + hourStart
}

func noTopicSessionParams(r *http.Request) (prefix string, hours int, hourStart string) {
	prefix = strings.TrimSpace(r.URL.Query().Get("prefix"))
	hours = 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 168 {
			hours = n
		}
	}
	hourStart = strings.TrimSpace(r.URL.Query().Get("hour_start"))
	return
}

func (h *Handler) handleNoTopicSessionRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, noTopicSessionPrefix)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		writeError(w, http.StatusNotFound, "route required")
		return
	}
	switch rest {
	case "messages":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleNoTopicSessionMessages(w, r)
	case "summarize-title":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleNoTopicSessionSummarizeTitle(w, r)
	case "extract-to-memora":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleNoTopicSessionExtractToMemora(w, r)
	case "extraction-status":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleNoTopicSessionExtractionStatus(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown no-topic-session route")
	}
}

func noTopicHourFilter(hourStart string, args []any) (string, []any) {
	if hourStart == "" {
		return "", args
	}
	args = append(args, hourStart)
	n := len(args)
	return fmt.Sprintf(` AND DATE_TRUNC('hour', ts) = DATE_TRUNC('hour', $%d::timestamptz)`, n), args
}

func noTopicLogsWhere(prefix string, hours int, r *http.Request) (clause string, args []any) {
	args = []any{prefix, hours}
	clause = `WHERE gw_task_id IS NULL AND api_key_prefix = $1 AND ts > NOW() - INTERVAL '1 hour' * $2`
	tenantFrag, tenantArgs, _ := tenantLogsClause(r, 3)
	if tenantFrag != "" {
		clause += tenantFrag
		args = append(args, tenantArgs...)
	}
	return clause, args
}

func (h *Handler) handleNoTopicSessionMessages(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	prefix, hours, hourStart := noTopicSessionParams(r)
	if prefix == "" {
		writeError(w, http.StatusBadRequest, "prefix required")
		return
	}

	virtualTaskID := noTopicSessionKey(prefix, hourStart)
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	where, args := noTopicLogsWhere(prefix, hours, r)
	var hourFrag string
	hourFrag, args = noTopicHourFilter(hourStart, args)
	where += hourFrag
	args = append(args, limit)
	limitArg := "$" + strconv.Itoa(len(args))

	var sessionID *string
	var totalPromptTokens, totalCompletionTokens int
	var totalCost float64

	rows, err := h.db.Query(ctx, `
		SELECT
			ts, request_id, client_model, outbound_model,
			request_preview, response_preview,
			prompt_tokens, completion_tokens, latency_ms,
			cost_usd, request_status, error_kind,
			work_type, request_mode, gw_session_id
		FROM request_logs
		`+where+`
		ORDER BY ts ASC
		LIMIT `+limitArg+`
	`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var messages []map[string]any
	seq := 1
	for rows.Next() {
		var m requestMessageRow
		if err := rows.Scan(
			&m.Ts, &m.RequestID, &m.ClientModel, &m.OutboundModel,
			&m.PromptPreview, &m.ResponsePreview,
			&m.PromptTokens, &m.CompletionTokens, &m.LatencyMs,
			&m.CostUSD, &m.RequestStatus, &m.ErrorKind,
			&m.WorkType, &m.RequestMode, &m.GwSessionID,
		); err != nil {
			slog.Warn("no-topic: skip corrupt row", "error", err)
			continue
		}

		if sessionID == nil && m.GwSessionID != nil {
			s := *m.GwSessionID
			sessionID = &s
		}

		direction := "user"
		if m.WorkType != nil && (*m.WorkType == "agent" || *m.WorkType == "memora") {
			direction = "assistant"
		} else if m.RequestMode != nil {
			mode := strings.ToLower(*m.RequestMode)
			if mode == "completion" || mode == "embedding" {
				direction = "assistant"
			}
		}

		if m.PromptTokens != nil {
			totalPromptTokens += *m.PromptTokens
		}
		if m.CompletionTokens != nil {
			totalCompletionTokens += *m.CompletionTokens
		}
		if m.CostUSD != nil {
			totalCost += *m.CostUSD
		}

		msg := map[string]any{
			"ts":                m.Ts.UTC().Format(time.RFC3339),
			"request_id":        m.RequestID,
			"seq":               seq,
			"direction":         direction,
			"client_model":      nilStr(m.ClientModel),
			"outbound_model":    nilStr(m.OutboundModel),
			"prompt_preview":    nilStr(m.PromptPreview),
			"response_preview":  nilStr(m.ResponsePreview),
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"latency_ms":        0,
			"cost_usd":          0.0,
			"status":            nilStr(m.RequestStatus),
		}
		if m.PromptTokens != nil {
			msg["prompt_tokens"] = *m.PromptTokens
		}
		if m.CompletionTokens != nil {
			msg["completion_tokens"] = *m.CompletionTokens
		}
		if m.LatencyMs != nil {
			msg["latency_ms"] = *m.LatencyMs
		}
		if m.CostUSD != nil {
			msg["cost_usd"] = *m.CostUSD
		}
		if m.ErrorKind != nil && *m.ErrorKind != "" {
			msg["error_kind"] = *m.ErrorKind
		}
		messages = append(messages, msg)
		seq++
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if messages == nil {
		messages = []map[string]any{}
	}

	resp := map[string]any{
		"task_id":                 virtualTaskID,
		"session_id":              nilStr(sessionID),
		"messages":                messages,
		"message_count":           len(messages),
		"request_count":           len(messages),
		"hours":                   hours,
		"api_key_prefix":          prefix,
		"total_prompt_tokens":     totalPromptTokens,
		"total_completion_tokens": totalCompletionTokens,
		"total_cost_usd":          totalCost,
	}
	if hourStart != "" {
		resp["hour_start"] = hourStart
	}
	if title, ok := h.loadStoredSessionTitle(ctx, virtualTaskID, ""); ok {
		resp["title"] = title
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleNoTopicSessionSummarizeTitle(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	prefix, hours, hourStart := noTopicSessionParams(r)
	if prefix == "" {
		writeError(w, http.StatusBadRequest, "prefix required")
		return
	}

	virtualTaskID := noTopicSessionKey(prefix, hourStart)
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	logs, err := h.loadNoTopicTaskLogsForTitle(ctx, prefix, hours, hourStart, r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
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

	llmRes, err := h.callAdminLLMChat(ctx, r, apiKey, adminLLMTaskSessionTitle, virtualTaskID, userContent)
	if err != nil {
		writeError(w, http.StatusBadGateway, "标题生成失败: "+err.Error())
		return
	}
	title := normalizeSessionTitle(llmRes.Content)
	model := llmRes.ResolvedModel
	if !isValidSessionTitle(title) {
		writeError(w, http.StatusBadGateway, "标题结果无效，请稍后重试")
		return
	}

	if err := h.upsertSessionTitle(ctx, virtualTaskID, "", title, model, keyID); err != nil {
		writeError(w, http.StatusInternalServerError, "保存标题失败")
		return
	}

	writeJSON(w, http.StatusOK, sessionTitleResponse{
		Title: title,
		Meta: sessionTitleMeta{
			TaskID:      virtualTaskID,
			LogCount:    len(logs),
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			APIKeyID:    keyID,
			Model:       model,
		},
	})
}

func (h *Handler) loadNoTopicTaskLogsForTitle(ctx context.Context, prefix string, hours int, hourStart string, r *http.Request) ([]sessionLogForSummary, error) {
	where, args := noTopicLogsWhere(prefix, hours, r)
	var hourFrag string
	hourFrag, args = noTopicHourFilter(hourStart, args)
	where += hourFrag
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

func (h *Handler) handleNoTopicSessionExtractToMemora(w http.ResponseWriter, r *http.Request) {
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "super_admin role required for extract-to-memora")
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

	prefix, hours, hourStart := noTopicSessionParams(r)
	if prefix == "" {
		writeError(w, http.StatusBadRequest, "prefix required")
		return
	}

	virtualTaskID := noTopicSessionKey(prefix, hourStart)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var body extractToMemoraRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	includeResponses := true

	apiKeyID, tenantID, err := h.noTopicAPIKeyAndTenant(ctx, prefix, hours, r)
	if err != nil {
		writeError(w, http.StatusNotFound, "no logs found for this no-topic session")
		return
	}

	userID := memora.UserID(tenantID, apiKeyID, virtualTaskID)
	if userID == "" {
		writeError(w, http.StatusBadRequest, "cannot derive memora user_id")
		return
	}

	turns, err := h.loadNoTopicPreviewTurns(ctx, prefix, hours, hourStart, r, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var existingFacts []string
	searchCtx, searchCancel := context.WithTimeout(ctx, 8*time.Second)
	memories, searchErr := writer.Search(searchCtx, userID, "", 30)
	searchCancel()
	if searchErr == nil {
		for _, m := range memories {
			if m.Text != "" {
				existingFacts = append(existingFacts, m.Text)
			}
		}
	}

	stats := memora.ExtractFromPreviews(turns, existingFacts, includeResponses)
	projectID := strings.TrimSpace(os.Getenv("MEMORA_PROJECT_ID"))
	if projectID == "" {
		projectID = "kaixuan-1-deploy"
	}

	resp := map[string]any{
		"task_id":            virtualTaskID,
		"user_id":            userID,
		"project_id":         projectID,
		"written":            0,
		"skipped_noise":      stats.SkippedNoise,
		"skipped_duplicate":  stats.SkippedDuplicate,
		"memora_message_ids": []string{},
		"extracted_at":       time.Now().UTC().Format(time.RFC3339),
		"samples":            truncateSamples(stats.Candidates, 5),
	}

	if body.DryRun || len(stats.Candidates) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	written := 0
	var writeErr error
	for _, fact := range stats.Candidates {
		msgs := []memora.Message{
			{Role: "user", Content: "[会话提炼] " + fact},
		}
		info := map[string]any{
			"task_id":    virtualTaskID,
			"source":     "session_extract",
			"project_id": projectID,
			"api_key_id": apiKeyID,
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

	resp["written"] = written
	status := "ok"
	if writeErr != nil {
		status = "partial"
		if written == 0 {
			status = "error"
			_ = h.upsertExtractionLog(ctx, virtualTaskID, written, stats.SkippedNoise, stats.SkippedDuplicate, status, map[string]any{
				"error": writeErr.Error(),
			})
			writeError(w, http.StatusBadGateway, "memora write failed: "+writeErr.Error())
			return
		}
		resp["error"] = writeErr.Error()
	}
	_ = h.upsertExtractionLog(ctx, virtualTaskID, written, stats.SkippedNoise, stats.SkippedDuplicate, status, map[string]any{
		"samples": resp["samples"],
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleNoTopicSessionExtractionStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	prefix, _, hourStart := noTopicSessionParams(r)
	if prefix == "" {
		writeError(w, http.StatusBadRequest, "prefix required")
		return
	}
	virtualTaskID := noTopicSessionKey(prefix, hourStart)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var extractedAt time.Time
	var written, skippedNoise, skippedDuplicate int
	var status string
	var detail []byte
	err := h.db.QueryRow(ctx, `
		SELECT extracted_at, written, skipped_noise, skipped_duplicate, status, COALESCE(detail, '{}'::jsonb)
		FROM session_memora_extraction_log
		WHERE task_id = $1
	`, virtualTaskID).Scan(&extractedAt, &written, &skippedNoise, &skippedDuplicate, &status, &detail)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id":   virtualTaskID,
			"extracted": false,
		})
		return
	}
	var detailObj any
	_ = json.Unmarshal(detail, &detailObj)
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":           virtualTaskID,
		"extracted":         true,
		"extracted_at":      extractedAt.UTC().Format(time.RFC3339),
		"written":           written,
		"skipped_noise":     skippedNoise,
		"skipped_duplicate": skippedDuplicate,
		"status":            status,
		"detail":            detailObj,
	})
}

func (h *Handler) noTopicAPIKeyAndTenant(ctx context.Context, prefix string, hours int, r *http.Request) (apiKeyID int, tenantID string, err error) {
	var apiKeyIDPtr *int64
	var tenantIDPtr *string
	where, args := noTopicLogsWhere(prefix, hours, r)
	err = h.db.QueryRow(ctx, `
		SELECT api_key_id, tenant_id FROM request_logs
		`+where+` AND api_key_id IS NOT NULL
		ORDER BY ts DESC LIMIT 1
	`, args...).Scan(&apiKeyIDPtr, &tenantIDPtr)
	if err != nil || apiKeyIDPtr == nil {
		return 0, "", fmt.Errorf("no logs found")
	}
	if tenantIDPtr != nil {
		tenantID = *tenantIDPtr
	}
	return int(*apiKeyIDPtr), tenantID, nil
}

func (h *Handler) loadNoTopicPreviewTurns(ctx context.Context, prefix string, hours int, hourStart string, r *http.Request, limit int) ([]memora.PreviewTurn, error) {
	where, args := noTopicLogsWhere(prefix, hours, r)
	var hourFrag string
	hourFrag, args = noTopicHourFilter(hourStart, args)
	where += hourFrag
	args = append(args, limit)
	limitArg := "$" + strconv.Itoa(len(args))

	rows, err := h.db.Query(ctx, `
		SELECT request_preview, response_preview, work_type, request_mode
		FROM request_logs
		`+where+`
		ORDER BY ts ASC
		LIMIT `+limitArg+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []memora.PreviewTurn
	for rows.Next() {
		var prompt, response, workType, reqMode *string
		if err := rows.Scan(&prompt, &response, &workType, &reqMode); err != nil {
			continue
		}
		dir := "user"
		if workType != nil && (*workType == "agent" || *workType == "memora") {
			dir = "assistant"
		} else if reqMode != nil {
			mode := strings.ToLower(*reqMode)
			if mode == "completion" || mode == "embedding" {
				dir = "assistant"
			}
		}
		pt := memora.PreviewTurn{Direction: dir}
		if prompt != nil {
			pt.PromptPreview = *prompt
		}
		if response != nil {
			pt.ResponsePreview = *response
		}
		turns = append(turns, pt)
	}
	return turns, rows.Err()
}


