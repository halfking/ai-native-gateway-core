package admin

// 错误处理手册管理面 API（/api/admin/error-playbook/*）。
//
// ★ 与 PI 分析 handler 同款纪律：
//
//	① 鉴权材料缺失 ⇒ 拒绝注册（fail-closed）——/api/admin/ 前缀路由在
//	  ServeMux 上**绕过** Echo 的 JWT 中间件（最长前缀派发），必须显式
//	  AdminMiddleware；
//	② 业务性失败（422/404/409）与驱动失败（500 + 固定文案）分流，
//	  内部错误不回显；
//	③ rows 循环终检（rowsguard 门）。
//
// ★ 手册自动化（worker/自动分析/自动执行）由装配侧注入的 engine 承担；
//
//	本 handler 即使 engine 为 nil（自动化未配置）也提供手册 CRUD 与
//	手动执行 —— 「管理手册」与「自动化运转」是两件事，前者永远可用。

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/errorplaybook"
)

// ErrorPlaybookHandler 是错误处理手册的管理面处理器。
type ErrorPlaybookHandler struct {
	store   *errorplaybook.SQLStore
	actions errorplaybook.ActionRegistry
	engine  *errorplaybook.Engine

	pool   *pgxpool.Pool
	secret string

	prefix string
}

// NewErrorPlaybookHandler 创建处理器（store 必须非 nil；engine 可为 nil）。
func NewErrorPlaybookHandler(
	store *errorplaybook.SQLStore,
	engine *errorplaybook.Engine,
	actions errorplaybook.ActionRegistry,
	pool *pgxpool.Pool,
	secret string,
) *ErrorPlaybookHandler {
	return &ErrorPlaybookHandler{
		store:   store,
		actions: actions,
		engine:  engine,
		pool:    pool,
		secret:  secret,
	}
}

// RegisterRoutes 注册路由（fail-closed：无鉴权材料 ⇒ 不注册）。
func (h *ErrorPlaybookHandler) RegisterRoutes(mux *http.ServeMux, prefix string) {
	if h == nil || h.store == nil || h.pool == nil || strings.TrimSpace(h.secret) == "" {
		slog.Error("error_playbook: store/auth material missing; endpoints NOT registered",
			"prefix", prefix,
			"has_store", h != nil && h.store != nil,
			"has_pool", h != nil && h.pool != nil,
			"has_secret", h != nil && strings.TrimSpace(h.secret) != "")
		return
	}
	h.prefix = prefix
	wrap := func(fn http.HandlerFunc) http.HandlerFunc {
		return AdminMiddleware(fn, h.pool, h.secret)
	}
	mux.HandleFunc(prefix+"/entries", wrap(h.handleEntries))
	mux.HandleFunc(prefix+"/entries/", wrap(h.handleEntryItem))
	mux.HandleFunc(prefix+"/executions", wrap(h.handleExecutions))
	mux.HandleFunc(prefix+"/executions/", wrap(h.handleExecutionItem))
	mux.HandleFunc(prefix+"/analyze", wrap(h.handleAnalyze))
	mux.HandleFunc(prefix+"/stats", wrap(h.handleStats))
}

// handleEntries 派发 /entries（GET 列表 / POST 手工录入）。
func (h *ErrorPlaybookHandler) handleEntries(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listEntries(w, r)
	case http.MethodPost:
		h.createEntry(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleEntryItem 派发 /entries/{id}（GET/PUT/DELETE）与 /entries/{id}/execute。
func (h *ErrorPlaybookHandler) handleEntryItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.prefix+"/entries/")
	if path == "" || path == "/" {
		http.Error(w, "entry id required", http.StatusBadRequest)
		return
	}
	rest := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid entry id", http.StatusBadRequest)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "execute":
			h.executeEntry(w, r, id)
		default:
			http.Error(w, "unknown sub-action", http.StatusNotFound)
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.getEntry(w, r, id)
	case http.MethodPut:
		h.updateEntry(w, r, id)
	case http.MethodDelete:
		h.deleteEntry(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *ErrorPlaybookHandler) listEntries(w http.ResponseWriter, r *http.Request) {
	f := errorplaybook.ListFilter{}
	if v := r.URL.Query().Get("enabled"); v != "" {
		enabled := v == "true" || v == "1"
		f.Enabled = &enabled
	}
	f.Source = r.URL.Query().Get("source")
	f.ErrorType = r.URL.Query().Get("error_type")
	f.Query = r.URL.Query().Get("q")
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		f.Limit = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil {
		f.Offset = v
	}

	entries, total, err := h.store.ListEntries(r.Context(), f)
	if err != nil {
		h.writeStoreErr(w, "list playbook entries", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"total":   total,
	})
}

// createEntryBody 是手工录入的请求形状。
type createEntryBody struct {
	ErrorType    string          `json:"error_type"`
	MatchPattern string          `json:"match_pattern"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	RootCause    string          `json:"root_cause"`
	PlanKind     string          `json:"plan_kind"`
	Plan         json.RawMessage `json:"plan"`
	Enabled      *bool           `json:"enabled"`
	AutoExecute  *bool           `json:"auto_execute"`
}

func (h *ErrorPlaybookHandler) createEntry(w http.ResponseWriter, r *http.Request) {
	var body createEntryBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	body.ErrorType = strings.TrimSpace(body.ErrorType)
	if body.ErrorType == "" {
		http.Error(w, "error_type is required", http.StatusUnprocessableEntity)
		return
	}

	plan, err := errorplaybook.NormalizePlan(body.PlanKind, body.Plan, h.actions)
	if err != nil {
		var inv *errorplaybook.ErrPlanInvalid
		if errors.As(err, &inv) {
			http.Error(w, inv.Error(), http.StatusUnprocessableEntity)
			return
		}
		writeInternalTextErr(w, "normalize plan failed", err)
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	autoExecute := false
	if body.AutoExecute != nil {
		autoExecute = *body.AutoExecute
	}

	entry, err := h.store.UpsertManualEntry(r.Context(), errorplaybook.AnalysisEntryInput{
		ErrorType:    body.ErrorType,
		MatchPattern: body.MatchPattern,
		Title:        body.Title,
		Description:  body.Description,
		RootCause:    body.RootCause,
		Plan:         plan,
		Confidence:   1.00,
		Enabled:      enabled,
		AutoExecute:  autoExecute,
		CreatedBy:    piAppliedBy(r),
	})
	if err != nil {
		h.writeStoreErr(w, "upsert playbook entry", err)
		return
	}
	slog.Info("error_playbook: entry upserted via admin API",
		"entry", entry.ID, "error_type", entry.ErrorType, "by", piAppliedBy(r))
	writeJSON(w, http.StatusOK, entry)
}

func (h *ErrorPlaybookHandler) getEntry(w http.ResponseWriter, r *http.Request, id int64) {
	entry, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		h.writeStoreErr(w, "get playbook entry", err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (h *ErrorPlaybookHandler) updateEntry(w http.ResponseWriter, r *http.Request, id int64) {
	var body struct {
		Enabled     *bool           `json:"enabled"`
		AutoExecute *bool           `json:"auto_execute"`
		Title       *string         `json:"title"`
		Description *string         `json:"description"`
		RootCause   *string         `json:"root_cause"`
		PlanKind    string          `json:"plan_kind"`
		Plan        json.RawMessage `json:"plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// 方案替换（可选）：plan 与 plan_kind 成对给出才生效。
	if len(body.Plan) > 0 && string(body.Plan) != "null" {
		plan, err := errorplaybook.NormalizePlan(body.PlanKind, body.Plan, h.actions)
		if err != nil {
			var inv *errorplaybook.ErrPlanInvalid
			if errors.As(err, &inv) {
				http.Error(w, inv.Error(), http.StatusUnprocessableEntity)
				return
			}
			writeInternalTextErr(w, "normalize plan failed", err)
			return
		}
		title, description, rootCause := "", "", ""
		if body.Title != nil {
			title = *body.Title
		}
		if body.Description != nil {
			description = *body.Description
		}
		if body.RootCause != nil {
			rootCause = *body.RootCause
		}
		if err := h.store.UpdatePlanEntry(r.Context(), id, title, description, rootCause, plan); err != nil {
			h.writeStoreErr(w, "update playbook plan", err)
			return
		}
	}

	entry, err := h.store.SetEntryFlags(r.Context(), id, body.Enabled, body.AutoExecute)
	if err != nil {
		h.writeStoreErr(w, "update playbook entry flags", err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (h *ErrorPlaybookHandler) deleteEntry(w http.ResponseWriter, r *http.Request, id int64) {
	if err := h.store.DeleteEntry(r.Context(), id); err != nil {
		h.writeStoreErr(w, "delete playbook entry", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": true, "id": id})
}

// executeEntry 手动按标准模式执行一条条目（管理员显式触发 ⇒ 不看
// auto_execute 开关，但白名单/快照/留痕全量生效）。
func (h *ErrorPlaybookHandler) executeEntry(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	entry, err := h.store.GetByID(r.Context(), id)
	if err != nil {
		h.writeStoreErr(w, "get playbook entry", err)
		return
	}
	if h.engine == nil {
		http.Error(w, "playbook engine not wired (automation not configured)", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	ev := errorplaybook.Event{
		ErrorType:   entry.ErrorType,
		Message:     strings.TrimSpace(body.Message),
		Occurrences: 1,
	}
	// 留痕记操作者身份（piAppliedBy ⇒ "manual:<username>"），与自动化执行可区分。
	rec, execErr := h.engine.ExecuteEntryAs(r.Context(), &entry, ev, errorplaybook.TriggerManual, piAppliedBy(r))
	if execErr != nil {
		// 业务性失败（步骤失败）⇒ 执行留痕已记录，如实带状态回给管理面。
		var inv *errorplaybook.ErrPlanInvalid
		if errors.As(execErr, &inv) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
				"execution": rec,
				"error":     inv.Error(),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"execution": rec,
			"error":     "execution failed (see server log)",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"execution": rec})
}

// handleExecutions 派发 /executions 列表。
func (h *ErrorPlaybookHandler) handleExecutions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var entryID int64
	if v, err := strconv.ParseInt(r.URL.Query().Get("entry_id"), 10, 64); err == nil {
		entryID = v
	}
	status := r.URL.Query().Get("status")
	errorType := r.URL.Query().Get("error_type")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	execs, total, err := h.store.ListVisibleExecutions(r.Context(), entryID, status, errorType, limit, offset, canViewRecommendations(r), r.URL.Query().Get("recommendations") == "only")
	if err != nil {
		h.writeStoreErr(w, "list playbook executions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"executions": execs,
		"total":      total,
	})
}

// handleExecutionItem 派发 /executions/{id}（GET 详情）与 /executions/{id}/revert。
func (h *ErrorPlaybookHandler) handleExecutionItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.prefix+"/executions/")
	if path == "" || path == "/" {
		http.Error(w, "execution id required", http.StatusBadRequest)
		return
	}
	rest := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid execution id", http.StatusBadRequest)
		return
	}
	if len(parts) == 2 && parts[1] == "revert" {
		h.revertExecution(w, r, id)
		return
	}
	if len(parts) == 2 {
		http.Error(w, "unknown sub-action", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rec, err := h.store.GetExecution(r.Context(), id)
	if err != nil {
		h.writeStoreErr(w, "get playbook execution", err)
		return
	}
	if errorplaybook.IsRecommendationActor(rec.ExecutedBy) && !canViewRecommendations(r) {
		h.writeStoreErr(w, "get playbook execution", errorplaybook.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func canViewRecommendations(r *http.Request) bool {
	auth := GetAuthContext(r)
	return auth != nil && auth.Role == "super_admin" && auth.TenantID == "default"
}

// revertExecution 按快照回滚一次执行。
func (h *ErrorPlaybookHandler) revertExecution(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.engine == nil {
		http.Error(w, "playbook engine not wired (automation not configured)", http.StatusServiceUnavailable)
		return
	}
	rec, err := h.store.GetExecution(r.Context(), id)
	if err != nil {
		h.writeStoreErr(w, "get playbook execution", err)
		return
	}
	if errorplaybook.IsRecommendationActor(rec.ExecutedBy) && !canViewRecommendations(r) {
		h.writeStoreErr(w, "get playbook execution", errorplaybook.ErrNotFound)
		return
	}
	if rec.Status != errorplaybook.StatusSucceeded && rec.Status != errorplaybook.StatusFailed {
		http.Error(w, fmt.Sprintf("execution status %q has nothing to revert", rec.Status), http.StatusConflict)
		return
	}
	results, err := h.engine.RevertExecution(r.Context(), rec)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]interface{}{
			"reverted": results,
			"error":    err.Error(),
		})
		return
	}
	slog.Info("error_playbook: execution reverted", "execution", id, "steps", len(results), "by", piAppliedBy(r))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"reverted": results,
	})
}

// handleAnalyze 手动触发一次未知错误分析（管理面按钮）。
func (h *ErrorPlaybookHandler) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.engine == nil {
		http.Error(w, "playbook engine not wired (LLM analysis not configured)", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		ErrorType   string   `json:"error_type"`
		Message     string   `json:"message"`
		Samples     []string `json:"samples"`
		Occurrences int      `json:"occurrences"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	body.ErrorType = strings.TrimSpace(body.ErrorType)
	if body.ErrorType == "" {
		http.Error(w, "error_type is required", http.StatusUnprocessableEntity)
		return
	}

	ev := errorplaybook.Event{
		ErrorType:   body.ErrorType,
		Message:     strings.TrimSpace(body.Message),
		Samples:     body.Samples,
		Occurrences: maxInt(body.Occurrences, 1),
	}
	// 管理员显式触发 ⇒ 不看自动化开关（DefaultConfig 的 AutoExecute=true），
	// 但留痕记操作者身份，与 worker 的自动分析可区分。
	if err := h.engine.AnalyzeUnknownAs(r.Context(), errorplaybook.DefaultConfig(), ev, piAppliedBy(r)); err != nil {
		var inv *errorplaybook.ErrPlanInvalid
		if errors.As(err, &inv) {
			http.Error(w, inv.Error(), http.StatusUnprocessableEntity)
			return
		}
		writeInternalTextErr(w, "analyze unknown error failed", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"message": "analysis completed; entry recorded into playbook",
	})
}

// handleStats 手册统计（条目分布 + 执行分布 + 链路状态）。
func (h *ErrorPlaybookHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	// ★ 审计轮（2026-11-21）：条目统计改 SQL 聚合（EntrySummary）——
	//   原实现走 ListEntries(Limit 200) 逐行累加 ⇒ 条目 >200 时静默欠数。
	entrySummary, entryErr := h.store.EntrySummary(r.Context())
	execSummary, execErr := h.store.VisibleExecutionSummary(r.Context(), canViewRecommendations(r))

	stats := map[string]interface{}{
		"engine_wired": h.engine != nil,
		"llm_wired":    h.engine != nil && h.engine.HasLLM(),
	}
	if entryErr == nil {
		stats["total_entries"] = entrySummary.Total
		stats["enabled_entries"] = entrySummary.Enabled
		stats["auto_execute_entries"] = entrySummary.AutoExecute
		stats["entries_by_source"] = entrySummary.BySource
		stats["entries_by_plan_kind"] = entrySummary.ByPlanKind
	}
	if execErr == nil {
		stats["executions"] = execSummary
	}
	if h.engine != nil {
		stats["pending_unknown"] = h.engine.PendingCount()
		if t := h.engine.LastRun(); !t.IsZero() {
			stats["last_run_at"] = t
		}
	}
	if entryErr != nil && !errors.Is(entryErr, errorplaybook.ErrNotProvisioned) {
		stats["entries_error"] = "query failed (see server log)"
	}
	if errors.Is(execErr, errorplaybook.ErrNotProvisioned) || errors.Is(entryErr, errorplaybook.ErrNotProvisioned) {
		stats["provisioned"] = false
	} else {
		stats["provisioned"] = true
	}
	writeJSON(w, http.StatusOK, stats)
}

// writeStoreErr 把存储层错误分流成 404/503/500。
func (h *ErrorPlaybookHandler) writeStoreErr(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, errorplaybook.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, errorplaybook.ErrNotProvisioned):
		http.Error(w, "playbook tables not provisioned (migration 995 not applied)", http.StatusServiceUnavailable)
	default:
		writeInternalTextErr(w, op, err)
	}
}
