package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/memora"
)

// handleMemoraStatus returns the current Memora connectivity status and
// sink statistics for the admin UI dashboard.
func (h *Handler) handleMemoraStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp := map[string]any{
		"enabled":   false,
		"base_url":  nil,
		"connected": false,
	}
	if h.memoraClient != nil {
		baseURL := h.memoraClient.BaseURL()
		enabled := !h.memoraClient.Disabled()
		resp["enabled"] = enabled
		if enabled {
			resp["base_url"] = baseURL
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		pingErr := h.memoraClient.Ping(ctx)
		cancel()
		if !enabled {
			resp["connected"] = false
		} else if pingErr == nil {
			resp["connected"] = true
		} else {
			resp["connected"] = false
			resp["last_error"] = pingErr.Error()
		}
	}
	if h.memoraSink != nil {
		st := h.memoraSink.Stats()
		resp["sink"] = map[string]any{
			"enqueued":           st.Enqueued,
			"dropped":            st.Dropped,
			"processed":          st.Processed,
			"errored":            st.Errored,
			"queue_len":          st.QueueLen,
			"queue_cap":          st.QueueCap,
			"consecutive_errors": st.ConsecutiveErrors,
			"last_error":         st.LastError,
			"last_error_at":      st.LastErrorAt,
			"paused":             st.Paused,
		}
		if st.Paused {
			resp["sink_paused"] = true
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleMemoraPing performs a manual connectivity test against Memora
// and returns the result with latency.
func (h *Handler) handleMemoraPing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.memoraClient == nil || h.memoraClient.Disabled() {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected": false,
			"error":     "memora not configured",
		})
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	err := h.memoraClient.Ping(ctx)
	cancel()
	latency := time.Since(start).Milliseconds()
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected":   true,
			"latency_ms": latency,
		})
	} else {
		writeJSON(w, http.StatusOK, map[string]any{
			"connected":   false,
			"latency_ms": latency,
			"error":       err.Error(),
		})
	}
}

// handleMemoraSinkControl pauses or resumes the async Memora write sink.
// POST body: {"action":"pause"|"resume"}
func (h *Handler) handleMemoraSinkControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.memoraSink == nil {
		writeError(w, http.StatusServiceUnavailable, "memora sink not configured")
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch strings.ToLower(strings.TrimSpace(body.Action)) {
	case "pause":
		h.memoraSink.Pause()
	case "resume":
		h.memoraSink.Resume()
	default:
		writeError(w, http.StatusBadRequest, "action must be pause or resume")
		return
	}
	st := h.memoraSink.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"paused": st.Paused,
		"sink": map[string]any{
			"queue_len": st.QueueLen,
			"queue_cap": st.QueueCap,
			"processed": st.Processed,
			"errored":   st.Errored,
			"paused":    st.Paused,
		},
	})
}

type sessionRow struct {
	TaskID           *string
	SessionID        *string
	RequestCount     int
	OkCount          int
	FailCount        int
	FirstActivity    time.Time
	LastActivity     time.Time
	LatestModel      *string
	APIKeyPrefix     *string
	APIKeyOwnerUser  *string
	ApplicationCode  *string
	NoTopic          bool
	NoTopicLabel     *string
	HourStart        *time.Time
}

// handleMemoraSessions lists recent task/session activity from
// request_logs, grouped by gw_task_id. Includes both topic sessions
// (with gw_task_id) and no-topic sessions (aggregated by api_key + hour).
func (h *Handler) handleMemoraSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 168 {
			hours = n
		}
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	ownerUser := strings.TrimSpace(r.URL.Query().Get("owner_user"))
	keyPrefix := strings.TrimSpace(r.URL.Query().Get("key_prefix"))
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	noTopicWindow := 1
	if v := r.URL.Query().Get("no_topic_window"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && (n == 1 || n == 2 || n == 6) {
			noTopicWindow = n
		}
	}
	includeNoTopic := true
	if v := r.URL.Query().Get("include_no_topic"); v == "0" || v == "false" {
		includeNoTopic = false
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		WITH base AS (
			SELECT
				COALESCE(NULLIF(TRIM(gw_task_id), ''), NULL) AS task_id,
				COALESCE(NULLIF(TRIM(gw_session_id), ''), NULL) AS session_id,
				ts,
				request_status,
				client_model,
				COALESCE(NULLIF(TRIM(api_key_prefix), ''), NULL) AS api_key_prefix,
				COALESCE(NULLIF(TRIM(api_key_owner_user), ''), NULL) AS api_key_owner_user,
				COALESCE(NULLIF(TRIM(application_code), ''), NULL) AS application_code
			FROM request_logs
			WHERE ts > NOW() - INTERVAL '1 hour' * $1
		),
		topic_sessions AS (
			SELECT
				task_id,
				session_id,
				COUNT(*) AS request_count,
				COUNT(*) FILTER (WHERE request_status = 'success') AS ok_count,
				COUNT(*) FILTER (WHERE request_status = 'failure') AS fail_count,
				MIN(ts) AS first_activity,
				MAX(ts) AS last_activity,
				(SELECT client_model FROM base b2 WHERE b2.task_id = base.task_id ORDER BY b2.ts DESC LIMIT 1) AS latest_model,
				(SELECT api_key_prefix FROM base b2 WHERE b2.task_id = base.task_id LIMIT 1) AS api_key_prefix,
				(SELECT api_key_owner_user FROM base b2 WHERE b2.task_id = base.task_id LIMIT 1) AS api_key_owner_user,
				(SELECT application_code FROM base b2 WHERE b2.task_id = base.task_id LIMIT 1) AS application_code,
				FALSE AS no_topic,
				NULL::text AS no_topic_label,
				NULL::timestamptz AS hour_start
			FROM base
			WHERE task_id IS NOT NULL
			GROUP BY task_id, session_id
		),
		no_topic_sessions AS (
			SELECT
				NULL::text AS task_id,
				NULL::text AS session_id,
				COUNT(*) AS request_count,
				COUNT(*) FILTER (WHERE request_status = 'success') AS ok_count,
				COUNT(*) FILTER (WHERE request_status = 'failure') AS fail_count,
				MIN(ts) AS first_activity,
				MAX(ts) AS last_activity,
				(SELECT client_model FROM base b2 WHERE b2.task_id IS NULL AND b2.api_key_prefix IS NOT DISTINCT FROM base.api_key_prefix ORDER BY b2.ts DESC LIMIT 1) AS latest_model,
				api_key_prefix,
				api_key_owner_user,
				application_code,
				TRUE AS no_topic,
				CONCAT(
					COALESCE(api_key_prefix, '[空]'), ' @ ',
					DATE_TRUNC('hour', MIN(ts))::text, '~',
					(DATE_TRUNC('hour', MIN(ts)) + (CASE WHEN $2 = 1 THEN interval '1 hour' WHEN $2 = 2 THEN interval '2 hours' ELSE interval '6 hours' END))::text
				) AS no_topic_label,
				DATE_TRUNC('hour', MIN(ts)) AS hour_start
			FROM base
			WHERE task_id IS NULL AND api_key_prefix IS NOT NULL
			GROUP BY api_key_prefix, api_key_owner_user, application_code, DATE_TRUNC('hour', ts)
		)
		SELECT * FROM topic_sessions
		UNION ALL
		SELECT * FROM no_topic_sessions
		ORDER BY first_activity DESC
		LIMIT $3
	`, hours, noTopicWindow, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	var sessions []map[string]any
	for rows.Next() {
		var s sessionRow
		if err := rows.Scan(
			&s.TaskID, &s.SessionID, &s.RequestCount, &s.OkCount, &s.FailCount,
			&s.FirstActivity, &s.LastActivity, &s.LatestModel,
			&s.APIKeyPrefix, &s.APIKeyOwnerUser, &s.ApplicationCode,
			&s.NoTopic, &s.NoTopicLabel, &s.HourStart,
		); err != nil {
			continue
		}

		if q != "" {
			qLower := strings.ToLower(q)
			match := false
			if s.TaskID != nil && strings.Contains(strings.ToLower(*s.TaskID), qLower) {
				match = true
			}
			if s.SessionID != nil && strings.Contains(strings.ToLower(*s.SessionID), qLower) {
				match = true
			}
			if s.LatestModel != nil && strings.Contains(strings.ToLower(*s.LatestModel), qLower) {
				match = true
			}
			if !match {
				continue
			}
		}
		if ownerUser != "" && (s.APIKeyOwnerUser == nil || !strings.Contains(strings.ToLower(*s.APIKeyOwnerUser), strings.ToLower(ownerUser))) {
			continue
		}
		if keyPrefix != "" && (s.APIKeyPrefix == nil || !strings.Contains(strings.ToLower(*s.APIKeyPrefix), strings.ToLower(keyPrefix))) {
			continue
		}
		if !includeNoTopic && s.NoTopic {
			continue
		}

		entry := map[string]any{
			"request_count":      s.RequestCount,
			"ok_count":           s.OkCount,
			"fail_count":         s.FailCount,
			"first_activity":     s.FirstActivity.UTC().Format(time.RFC3339),
			"last_activity":      s.LastActivity.UTC().Format(time.RFC3339),
			"no_topic":           s.NoTopic,
			"api_key_prefix":     nilStr(s.APIKeyPrefix),
			"api_key_owner_user": nilStr(s.APIKeyOwnerUser),
			"application_code":   nilStr(s.ApplicationCode),
			"latest_model":       nilStr(s.LatestModel),
		}
		if s.NoTopic {
			entry["task_id"] = nil
			entry["session_id"] = nil
			entry["no_topic_label"] = nilStr(s.NoTopicLabel)
		} else {
			entry["task_id"] = nilStr(s.TaskID)
			entry["session_id"] = nilStr(s.SessionID)
		}
		sessions = append(sessions, entry)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if sessions == nil {
		sessions = []map[string]any{}
	}

	topicCount := 0
	noTopicCount := 0
	for _, s := range sessions {
		if s["no_topic"] == true {
			noTopicCount++
		} else {
			topicCount++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"sessions":        sessions,
		"hours":           hours,
		"no_topic_window": noTopicWindow,
		"topic_count":     topicCount,
		"no_topic_count":  noTopicCount,
	})
}

func nilStr(s *string) any {
	if s == nil {
		return "[空]"
	}
	return *s
}

// handleMemoraContext returns the L1 Memora memories stored for a
// specific task, alongside basic request metadata and a derived title
// (first Memora fact truncated to 60 chars).
func (h *Handler) handleMemoraContext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, "/api/system/memora-context/")
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required in URL path")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	sc := parseSessionScope(r)

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	where, whereArgs := sessionLogsWhere(taskID, sc)
	var requestCount int
	var latestModel *string
	err := h.db.QueryRow(ctx, `
		SELECT
			COUNT(*),
			(SELECT client_model FROM request_logs `+where+` ORDER BY ts DESC LIMIT 1)
		FROM request_logs `+where+`
	`, whereArgs...).Scan(&requestCount, &latestModel)
	if err != nil || requestCount == 0 {
		writeError(w, http.StatusNotFound, "task not found: "+taskID)
		return
	}

	apiKeyID, err := h.sessionAPIKeyID(ctx, taskID, sc)
	userID := ""
	if err == nil && apiKeyID > 0 {
		userID = memora.UserID(apiKeyID, taskID)
	}

	var facts []map[string]any
	var factsSearchErr string
	if h.memoraClient != nil && !h.memoraClient.Disabled() && userID != "" {
		searchCtx, searchCancel := context.WithTimeout(ctx, 8*time.Second)
		var memories []memora.Memory
		var searchErr error
		if mc, ok := h.memoraClient.(interface {
			SearchAdmin(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
		}); ok {
			memories, searchErr = mc.SearchAdmin(searchCtx, userID, "", 100)
		} else if mc, ok := h.memoraClient.(interface {
			Search(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
		}); ok {
			memories, searchErr = mc.Search(searchCtx, userID, "", 100)
		}
		searchCancel()
		if searchErr == nil {
			for _, m := range memories {
				facts = append(facts, map[string]any{
					"id":     m.ID,
					"memory": m.Text,
					"score":  m.Score,
					"tags":   m.Tags,
				})
			}
		} else {
			factsSearchErr = searchErr.Error()
		}
	}
	if facts == nil {
		facts = []map[string]any{}
	}

	var writtenFromLog int
	var extractedAt *time.Time
	_ = h.db.QueryRow(ctx, `
		SELECT written, extracted_at FROM session_memora_extraction_log WHERE task_id = $1
	`, taskID).Scan(&writtenFromLog, &extractedAt)

	var title string
	if len(facts) > 0 {
		if mem, ok := facts[0]["memory"].(string); ok && len(mem) > 0 {
			if len(mem) > 60 {
				title = mem[:60] + "..."
			} else {
				title = mem
			}
		}
	}
	if title == "" {
		title = "[无标题]"
	}

	resp := map[string]any{
		"task_id":           taskID,
		"user_id":           userID,
		"request_count":     requestCount,
		"facts":             facts,
		"facts_visible":     len(facts),
		"facts_written":     writtenFromLog,
		"title":             title,
		"hours":             sc.Hours,
		"scoped_session_id": nil,
	}
	if sc.SessionID != "" {
		resp["scoped_session_id"] = sc.SessionID
	}
	if factsSearchErr != "" {
		resp["facts_search_error"] = factsSearchErr
	}
	if latestModel != nil {
		resp["latest_model"] = *latestModel
	}
	if extractedAt != nil {
		resp["extracted_at"] = extractedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

type requestMessageRow struct {
	Ts               time.Time
	RequestID        string
	ClientModel      *string
	OutboundModel    *string
	PromptPreview    *string
	ResponsePreview  *string
	PromptTokens     *int
	CompletionTokens *int
	LatencyMs        *int
	CostUSD          *float64
	RequestStatus    *string
	ErrorKind        *string
	WorkType         *string
	RequestMode      *string
	GwSessionID      *string
}

// handleSessionMessages returns the ordered list of request_logs entries
// for a specific gw_task_id, suitable for rendering a conversation timeline.
// Each entry includes direction (user/assistant), prompt/response previews,
// token counts, latency, cost, and status.
func (h *Handler) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	taskID := strings.TrimPrefix(r.URL.Path, "/api/system/session-messages/")
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required in URL path")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	sc := parseSessionScope(r)
	limit := 500
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	where, args := sessionLogsWhere(taskID, sc)
	args = append(args, limit)
	limitArg := "$" + strconv.Itoa(len(args))

	var sessionID *string
	var totalPromptTokens, totalCompletionTokens int
	var totalCost float64

	rows, err := h.db.Query(ctx, `
		SELECT
			ts,
			request_id,
			client_model,
			outbound_model,
			request_preview,
			response_preview,
			prompt_tokens,
			completion_tokens,
			latency_ms,
			cost_usd,
			request_status,
			error_kind,
			work_type,
			request_mode,
			gw_session_id
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

	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":                  taskID,
		"session_id":               nilStr(sessionID),
		"messages":                 messages,
		"message_count":            len(messages),
		"request_count":            len(messages),
		"hours":                    sc.Hours,
		"scoped_session_id":        scopedSessionIDResp(sc.SessionID),
		"total_prompt_tokens":      totalPromptTokens,
		"total_completion_tokens":  totalCompletionTokens,
		"total_cost_usd":           totalCost,
	})
}

func scopedSessionIDResp(sessionID string) any {
	if sessionID == "" {
		return nil
	}
	return sessionID
}
