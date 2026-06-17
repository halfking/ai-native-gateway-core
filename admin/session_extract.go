package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/memora"
)

const sessionContextPrefix = "/api/system/session-context/"

// handleSessionContextRoutes dispatches session-context sub-routes:
//   - POST .../{taskId}/extract-to-memora
//   - GET  .../{taskId}/extraction-status
//   - POST .../{taskId}/summarize-title
func (h *Handler) handleSessionContextRoutes(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, sessionContextPrefix)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		writeError(w, http.StatusNotFound, "task_id required")
		return
	}
	parts := strings.Split(rest, "/")
	taskID := strings.TrimSpace(parts[0])
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required")
		return
	}
	if len(parts) == 1 {
		writeError(w, http.StatusNotFound, "unknown session-context route")
		return
	}
	switch parts[1] {
	case "extract-to-memora":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleSessionExtractToMemora(w, r, taskID)
	case "extraction-status":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleSessionExtractionStatus(w, r, taskID)
	case "summarize-title":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.handleSessionSummarizeTitle(w, r, taskID)
	default:
		writeError(w, http.StatusNotFound, "unknown session-context route")
	}
}

type extractToMemoraRequest struct {
	DryRun           bool `json:"dry_run"`
	IncludeResponses bool `json:"include_responses"`
}

func (h *Handler) handleSessionExtractToMemora(w http.ResponseWriter, r *http.Request, taskID string) {
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

	var body extractToMemoraRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	includeResponses := true

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	sc := parseSessionScope(r)

	apiKeyID, err := h.sessionAPIKeyID(ctx, taskID, sc, r)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found: "+taskID)
		return
	}
	userID := memora.UserID(apiKeyID, taskID)
	if userID == "" {
		writeError(w, http.StatusBadRequest, "cannot derive memora user_id")
		return
	}

	turns, err := h.loadSessionPreviewTurns(ctx, taskID, sc, r, 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var existingFacts []string
	searchCtx, searchCancel := context.WithTimeout(ctx, 8*time.Second)
	var memories []memora.Memory
	var searchErr error
	if adminSearcher, ok := writer.(interface {
		SearchAdmin(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
	}); ok {
		memories, searchErr = adminSearcher.SearchAdmin(searchCtx, userID, "", 30)
	} else {
		memories, searchErr = writer.Search(searchCtx, userID, "", 30)
	}
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
		"task_id":            taskID,
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
			"task_id":    taskID,
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
			_ = h.upsertExtractionLog(ctx, taskID, written, stats.SkippedNoise, stats.SkippedDuplicate, status, map[string]any{
				"error": writeErr.Error(),
			})
			writeError(w, http.StatusBadGateway, "memora write failed: "+writeErr.Error())
			return
		}
		resp["error"] = writeErr.Error()
	}
	_ = h.upsertExtractionLog(ctx, taskID, written, stats.SkippedNoise, stats.SkippedDuplicate, status, map[string]any{
		"samples": resp["samples"],
	})
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSessionExtractionStatus(w http.ResponseWriter, r *http.Request, taskID string) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if IsTenantAdmin(r) && GetTenantID(r) != "" && GetTenantID(r) != "default" {
		if !assertTaskInTenant(ctx, h.db, taskID, GetTenantID(r)) {
			writeJSON(w, http.StatusOK, map[string]any{
				"task_id":   taskID,
				"extracted": false,
			})
			return
		}
	}

	var extractedAt time.Time
	var written, skippedNoise, skippedDuplicate int
	var status string
	var detail []byte
	err := h.db.QueryRow(ctx, `
		SELECT extracted_at, written, skipped_noise, skipped_duplicate, status, COALESCE(detail, '{}'::jsonb)
		FROM session_memora_extraction_log
		WHERE task_id = $1
	`, taskID).Scan(&extractedAt, &written, &skippedNoise, &skippedDuplicate, &status, &detail)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id":   taskID,
			"extracted": false,
		})
		return
	}
	var detailObj any
	_ = json.Unmarshal(detail, &detailObj)
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":           taskID,
		"extracted":         true,
		"extracted_at":      extractedAt.UTC().Format(time.RFC3339),
		"written":           written,
		"skipped_noise":     skippedNoise,
		"skipped_duplicate": skippedDuplicate,
		"status":            status,
		"detail":            detailObj,
	})
}

func (h *Handler) sessionAPIKeyID(ctx context.Context, taskID string, sc sessionScope, r *http.Request) (int, error) {
	where, args := sessionLogsWhere(taskID, sc, r)
	var apiKeyID *int64
	err := h.db.QueryRow(ctx, `
		SELECT api_key_id FROM request_logs
		`+where+` AND api_key_id IS NOT NULL
		ORDER BY ts DESC LIMIT 1
	`, args...).Scan(&apiKeyID)
	if err != nil || apiKeyID == nil {
		return 0, err
	}
	return int(*apiKeyID), nil
}

func (h *Handler) loadSessionPreviewTurns(ctx context.Context, taskID string, sc sessionScope, r *http.Request, limit int) ([]memora.PreviewTurn, error) {
	where, args := sessionLogsWhere(taskID, sc, r)
	args = append(args, limit)
	limitArg := "$" + strconv.Itoa(len(args))
	rows, err := h.db.Query(ctx, `
		SELECT
			request_preview,
			response_preview,
			work_type,
			request_mode
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

func (h *Handler) upsertExtractionLog(ctx context.Context, taskID string, written, skippedNoise, skippedDuplicate int, status string, detail map[string]any) error {
	if h.db == nil {
		return nil
	}
	b, _ := json.Marshal(detail)
	_, err := h.db.Exec(ctx, `
		INSERT INTO session_memora_extraction_log
			(task_id, extracted_at, written, skipped_noise, skipped_duplicate, status, detail)
		VALUES ($1, NOW(), $2, $3, $4, $5, $6::jsonb)
		ON CONFLICT (task_id) DO UPDATE SET
			extracted_at = EXCLUDED.extracted_at,
			written = EXCLUDED.written,
			skipped_noise = EXCLUDED.skipped_noise,
			skipped_duplicate = EXCLUDED.skipped_duplicate,
			status = EXCLUDED.status,
			detail = EXCLUDED.detail
	`, taskID, written, skippedNoise, skippedDuplicate, status, string(b))
	return err
}

func truncateSamples(candidates []string, n int) []string {
	if len(candidates) <= n {
		return candidates
	}
	return candidates[:n]
}

func (h *Handler) memoraWriteClient() interface {
	Disabled() bool
	AddMessage(ctx context.Context, userID string, messages []memora.Message, info map[string]any) error
	Search(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
} {
	if c, ok := h.memoraClient.(interface {
		Disabled() bool
		AddMessage(ctx context.Context, userID string, messages []memora.Message, info map[string]any) error
		Search(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
	}); ok {
		return c
	}
	return nil
}
