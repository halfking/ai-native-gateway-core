package admin

// NOTE: All SELECTs on tenant-scoped tables (request_logs etc.) in this file
// use sessionLogsWhere() which embeds tenantLogsClause()
// (admin/session_tenant.go) to inject "AND tenant_id = $N" for tenant_admin
// callers on non-default tenants. Super-admin / legacy admin_key /
// default-tenant callers see all rows.

import (
	"context"
	"errors"
	"fmt"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/kaixuan/llm-gateway-go/internal/observability"
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

	// 2026-08-19: per-session, per-trigger-type distributed lock so two
	// concurrent manual "regenerate" clicks (or cross-replica races) do
	// not both call the LLM. Auto-title and manual-title have
	// independent keys so the user can regenerate while auto-title is
	// mid-run, and vice-versa.
	//
	// Follower semantics: wait for the leader, then re-read the stored
	// title. If the leader wrote one, return it; otherwise fall through
	// to the normal pipeline so the user sees a useful error rather
	// than a silent no-op.
	var lockHandle *distlock.Handle
	if h.titleDistLock != nil {
		key := titleDistLockKey("manual", taskID, scopedKey)
		hh, lerr := h.titleDistLock.Acquire(ctx, distlock.AcquireOpts{
			Key:   key,
			TTL:   60 * time.Second,
			Scope: "manual",
		})
		if lerr != nil && !errors.Is(lerr, distlock.ErrNotEnabled) {
			slog.Warn("session_title: distlock acquire failed; proceeding without lock",
				"task_id", taskID, "session_id", scopedKey, "error", lerr)
		} else if hh != nil {
			lockHandle = hh
			defer lockHandle.Release(context.Background())
			if !lockHandle.IsLeader() {
				waitErr := lockHandle.Wait(ctx)
				if waitErr != nil {
					writeError(w, http.StatusConflict, "标题生成仍在进行，请稍后重试")
					return
				}
				if title, ok := h.loadStoredSessionTitle(ctx, taskID, scopedKey); ok {
					meta := sessionTitleMeta{
						TaskID:          taskID,
						ScopedSessionID: sc.SessionID,
						GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
						Model:           "manual-follower",
					}
					writeJSON(w, http.StatusOK, sessionTitleResponse{Title: title, Meta: meta})
					return
				}
				writeError(w, http.StatusConflict, "标题生成未完成，请稍后重试")
				return
			}
		}
	}

	logs, err := h.loadTaskLogsForTitle(ctx, taskID, sc, r)
	if err != nil {
		// 2026-09-29 (审计二十一轮): 存储不可用走 503 降级契约，不再把
		// 存储抖动当 500 代码缺陷。
		if IsStorageUnavailable(err) {
			WriteStorageDegraded(w, observability.StorageComponentSummary, err)
			return
		}
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
		slog.Error("session title: generation failed", "task_id", taskID, "err", err)
		writeError(w, http.StatusBadGateway, "标题生成失败，请稍后重试")
		return
	}
	title := llmRes.Content
	model := llmRes.ResolvedModel
	title = normalizeSessionTitle(title)
	if !isValidSessionTitle(title) {
		writeError(w, http.StatusBadGateway, "标题结果无效，请稍后重试")
		return
	}

	if lockHandle != nil && lockHandle.IsLeader() {
		if err := lockHandle.Check(ctx); err != nil {
			writeError(w, http.StatusConflict, "标题租约已失效，请稍后重试")
			return
		}
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
		       COALESCE(rb.request_body::text, '') AS request_body,
		       COALESCE(rb.response_body::text, '') AS response_body,
		       `+requestLogStatusExpr+` AS request_status,
		       rl.error_kind, rl.client_model
		FROM request_logs_with_current_month rl
		LEFT JOIN `+dbpkg.SessionBodiesSourceSQL()+` rb /* request_logs_bodies_with_current_month rb */
		  ON rb.request_id = rl.request_id
		`+where+`
		ORDER BY rl.ts ASC
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
			warnRowSkip("loadSessionLogsForSummary", err)
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
	s = strings.Trim(s, `"'「」『』`)
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
	// 2026-08-06: human-edited titles (PUT /title) must contain at least
	// one non-ASCII rune so we don't store "P3-001" or "BUGFIX" as a
	// "title". Pure-ASCII strings used to be rejected by a "mostly ASCII"
	// check (ascii*2 <= total); that was too strict — it also rejected
	// reasonable mixed titles like "测试标题 2026-08-06". Allow any mix
	// as long as one CJK rune anchors the title.
	for _, r := range s {
		if r >= 128 {
			return true
		}
	}
	return false
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

// resolveSessionTitleTaskID chooses the real task scope from user-facing
// requests so summary refreshes update the same title row as request logs.
// Legacy sessions without a task retain the historical "auto" scope.
// R34 (2026-09-17): goal shadow turns (origin_actor LIKE 'goal-%') are
// gateway-synthesized — they carry no X-Gw-Task-Id, so picking one would
// silently degrade the title scope back to legacy 'auto' for any goal-enabled
// session whose latest success is a shadow turn.
func (h *Handler) resolveSessionTitleTaskID(ctx context.Context, sessionID, tenantID string) (string, error) {
	if h == nil || h.db == nil {
		return "", fmt.Errorf("database not configured")
	}
	var taskID string
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(TRIM(t.gw_task_id), ''), 'auto')
		FROM `+dbpkg.SessionFamilyTurnsForSessionSQL()+` t
		WHERE 1 = 1
		  AND t.success = TRUE
		  AND COALESCE(t.is_auto_request, FALSE) = FALSE
		  AND COALESCE(t.origin_actor, '') NOT LIKE 'goal-%'
		ORDER BY t.ts DESC, t.id DESC
		LIMIT 1
	`, sessionID, tenantID).Scan(&taskID)
	if err != nil {
		return "", err
	}
	return taskID, nil
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
			warnRowSkip("loadSessionTitlesBatch", err)
			continue
		}
		out[sessionTitleMapKey(taskID, scopedID)] = title
	}
	// 批量标题富化属降级通道，迭代中断留痕后按已取到的映射继续。
	if rerr := rows.Err(); rerr != nil {
		slog.Warn("loadSessionTitlesBatch iteration aborted; titles degraded", "error", rerr)
	}
	return out
}

func sessionTitleMapKey(taskID, scopedSessionID string) string {
	return taskID + "\x00" + scopedSessionIDKey(scopedSessionID)
}

// titleUpdateRequest is the body shape for PUT
// /api/system/session-context/{taskId}/title (manual override).
type titleUpdateRequest struct {
	Title           string `json:"title"`
	ScopedSessionID string `json:"scoped_session_id,omitempty"`
}

// handleSessionTitleUpdate implements PUT
// /api/system/session-context/{taskId}/title.
//
// Lets a human override the LLM-generated title in session_titles.
// Stores the title under (task_id, scoped_session_id) and stamps
// generated_at=now(), model="manual" so it is distinguishable from
// auto-generated titles. Empty/whitespace titles are rejected so we
// never overwrite a usable title with empty data.
//
// 2026-08-19: per-session distributed lock so two concurrent PUTs
// (e.g. operator race + UI rapid edit) do not interleave. Followers
// wait for the leader, then re-SELECT the row and return whatever the
// leader wrote — guarantees the API consumer sees the same value
// that is now in the database, regardless of who "won" the write.
func (h *Handler) handleSessionTitleUpdate(w http.ResponseWriter, r *http.Request, taskID string) {
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	var body titleUpdateRequest
	if err := readJSONRequired(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	cleaned := normalizeSessionTitle(body.Title)
	if !isValidSessionTitle(cleaned) {
		writeError(w, http.StatusBadRequest, "title must be 2-80 runes, no XML tags, and not all-ASCII")
		return
	}

	scopedKey := scopedSessionIDKey(body.ScopedSessionID)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if !requireSessionTaskAccess(w, r, ctx, h.db, taskID) {
		return
	}

	// 2026-08-19: acquire the per-session distributed lock. Followers
	// wait for the leader and re-read the final row so the operator
	// sees the title that is now persisted, regardless of write order.
	var lockHandle *distlock.Handle
	if h.titleDistLock != nil {
		key := titleDistLockKey("manual", taskID, scopedKey)
		hh, lerr := h.titleDistLock.Acquire(ctx, distlock.AcquireOpts{
			Key:   key,
			TTL:   30 * time.Second,
			Scope: "manual",
		})
		if lerr != nil && !errors.Is(lerr, distlock.ErrNotEnabled) {
			slog.Warn("session_title_update: distlock acquire failed; proceeding without lock",
				"task_id", taskID, "session_id", scopedKey, "error", lerr)
		} else if hh != nil {
			lockHandle = hh
			defer lockHandle.Release(context.Background())
			if !lockHandle.IsLeader() {
				waitErr := lockHandle.Wait(ctx)
				if waitErr != nil {
					writeError(w, http.StatusConflict, "标题生成仍在进行，请稍后重试")
					return
				}
				if title, ok := h.loadStoredSessionTitle(ctx, taskID, scopedKey); ok {
					writeJSON(w, http.StatusOK, map[string]any{
						"task_id":           taskID,
						"scoped_session_id": scopedKey,
						"title":             title,
						"model":             "manual-follower",
						"updated_at":        time.Now().UTC().Format(time.RFC3339),
					})
					return
				}
				writeError(w, http.StatusConflict, "标题生成未完成，请稍后重试")
				return
			}

		}
	}

	// Manual overrides are stamped with model="manual" so future
	// summarize-title calls can preserve the human intent (caller can
	// re-run summarize-title to refresh; the next LLM call will win).
	if lockHandle != nil && lockHandle.IsLeader() {
		if err := lockHandle.Check(ctx); err != nil {
			writeError(w, http.StatusConflict, "标题租约已失效，请稍后重试")
			return
		}
	}
	_, err := h.db.Exec(ctx, `
		INSERT INTO session_titles (task_id, scoped_session_id, title, generated_at, model, api_key_id)
		VALUES ($1, $2, $3, NOW(), 'manual', NULL)
		ON CONFLICT (task_id, scoped_session_id) DO UPDATE SET
			title = EXCLUDED.title,
			generated_at = EXCLUDED.generated_at,
			model = EXCLUDED.model
	`, taskID, scopedKey, cleaned)
	if err != nil {
		writeInternalErr(w, "save title", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":           taskID,
		"scoped_session_id": scopedKey,
		"title":             cleaned,
		"model":             "manual",
		"updated_at":        time.Now().UTC().Format(time.RFC3339),
	})
}

// handleSessionTitleDelete implements DELETE
// /api/system/session-context/{taskId}/title?scoped_session_id=...
//
// Removes the title row so the next list render falls back to the
// short-id display, and so a fresh summarize-title can run unblocked.
// No-op if the row doesn't exist (200 + deleted:false).
//
// 2026-08-19: per-session distributed lock so two concurrent DELETEs
// (or DELETE racing summarize-title) do not interleave. Followers wait
// for the leader, then re-check the row and return whatever the final
// state is — guarantees the API consumer sees the row's final state.
func (h *Handler) handleSessionTitleDelete(w http.ResponseWriter, r *http.Request, taskID string) {
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	scopedKey := scopedSessionIDKey(r.URL.Query().Get("scoped_session_id"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if !requireSessionTaskAccess(w, r, ctx, h.db, taskID) {
		return
	}

	// 2026-08-19: acquire the per-session distributed lock. Followers
	// wait for the leader and re-check the row.
	var lockHandle *distlock.Handle
	if h.titleDistLock != nil {
		key := titleDistLockKey("manual", taskID, scopedKey)
		hh, lerr := h.titleDistLock.Acquire(ctx, distlock.AcquireOpts{
			Key:   key,
			TTL:   30 * time.Second,
			Scope: "manual",
		})
		if lerr != nil && !errors.Is(lerr, distlock.ErrNotEnabled) {
			slog.Warn("session_title_delete: distlock acquire failed; proceeding without lock",
				"task_id", taskID, "session_id", scopedKey, "error", lerr)
		} else if hh != nil {
			lockHandle = hh
			defer lockHandle.Release(context.Background())
			if !lockHandle.IsLeader() {
				waitErr := lockHandle.Wait(ctx)
				if waitErr != nil {
					writeError(w, http.StatusConflict, "标题删除仍在进行，请稍后重试")
					return
				}
				if _, ok := h.loadStoredSessionTitle(ctx, taskID, scopedKey); !ok {
					writeJSON(w, http.StatusOK, map[string]any{
						"task_id":           taskID,
						"scoped_session_id": scopedKey,
						"deleted":           false,
					})
					return
				}
				writeError(w, http.StatusConflict, "标题状态已变化，请稍后重试")
				return
			}

		}
	}

	if lockHandle != nil && lockHandle.IsLeader() {
		if err := lockHandle.Check(ctx); err != nil {
			writeError(w, http.StatusConflict, "标题租约已失效，请稍后重试")
			return
		}
	}
	tag, err := h.db.Exec(ctx, `
		DELETE FROM session_titles
		WHERE task_id = $1 AND scoped_session_id = $2
	`, taskID, scopedKey)
	if err != nil {
		writeInternalErr(w, "delete title", err)
		return
	}
	rows := tag.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":           taskID,
		"scoped_session_id": scopedKey,
		"deleted":           rows > 0,
	})
}

// titlesBatchRequest is the body shape for POST
// /api/system/session-context/titles/batch. Keys are repeated
// (task_id, scoped_session_id) pairs — the same row may be requested
// for many rows in the request-logs list.
type titlesBatchRequest struct {
	Keys []struct {
		TaskID          string `json:"task_id"`
		ScopedSessionID string `json:"scoped_session_id,omitempty"`
	} `json:"keys"`
}

// handleSessionTitlesBatch implements POST
// /api/system/session-context/titles/batch.
//
// Bulk lookup keyed by (task_id, scoped_session_id). Used by the
// request-logs list to enrich each row with its session title in a
// single round-trip. Returns a map keyed by the same sessionTitleMapKey
// shape so the client can correlate results cheaply. Keys that don't
// have a stored title are simply omitted from the response map.
func (h *Handler) handleSessionTitlesBatch(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	var body titlesBatchRequest
	if err := readJSONRequired(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if len(body.Keys) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"titles": map[string]string{}})
		return
	}
	if len(body.Keys) > 500 {
		writeError(w, http.StatusBadRequest, "too many keys (max 500)")
		return
	}

	pairs := make([][2]string, 0, len(body.Keys))
	seen := make(map[string]struct{}, len(body.Keys))
	for _, k := range body.Keys {
		taskID := strings.TrimSpace(k.TaskID)
		if taskID == "" {
			continue
		}
		scoped := scopedSessionIDKey(k.ScopedSessionID)
		key := sessionTitleMapKey(taskID, scoped)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		pairs = append(pairs, [2]string{taskID, scoped})
	}

	// 租户收口（2026-10-07 审计）：task_id 由客户端直接提供，而
	// session_titles 无 tenant_id 列，修复前 tenant_admin 可用任意
	// task_id 跨租户探测会话标题。与 extraction-status 的
	// assertTaskInTenant 门 / requireSessionTaskAccess 的跳过条件对齐
	// （super_admin/admin_key/default 租户不过滤）。FAIL-CLOSED：租户
	// 解析失败 ⇒ 空集 ⇒ 全部 key 视为不在租户内，标题富化降级为空。
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tenantID := ""
	if IsTenantAdmin(r) && GetTenantID(r) != "" && GetTenantID(r) != "default" {
		tenantID = GetTenantID(r)
	}
	if tenantID != "" {
		taskIDs := make([]string, 0, len(pairs))
		for _, p := range pairs {
			taskIDs = append(taskIDs, p[0])
		}
		allowed, err := taskIDsInTenant(ctx, h.db, taskIDs, tenantID)
		if err != nil {
			slog.Warn("titles batch tenant scope failed; degrading to empty", "error", err)
			allowed = map[string]bool{}
		}
		pairs = filterTaskPairsInTenant(pairs, allowed)
	}

	titles := h.loadSessionTitlesBatch(ctx, pairs)
	slog.Debug("admin titles batch lookup",
		"requested", len(body.Keys), "unique", len(pairs), "found", len(titles))

	writeJSON(w, http.StatusOK, map[string]any{
		"titles": titles,
	})
}
