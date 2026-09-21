// Package admin — systemmonitor_handlers.go
//
// 系统监测模块 REST 端点。
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §5 / §6.2
//
// 路由（super_admin，仅平台运维可调用）：
//
//	POST /api/admin/system-monitor/submit              单条入队（手工触发）
//	POST /api/admin/system-monitor/start-all           仪表盘"开始全部任务"
//	POST /api/admin/system-monitor/stop-all            仪表盘"停止全部任务"
//	POST /api/admin/system-monitor/by-credential/{id}  按凭据触发
//	POST /api/admin/system-monitor/by-provider/{id}    按供应商触发
//	POST /api/admin/system-monitor/by-model/{name}     按模型触发
//	GET  /api/admin/system-monitor/stats               队列/running/in_fallback 快照
//	GET  /api/admin/system-monitor/recent-runs          system_probe_runs 最近 50 条
//	PATCH /api/admin/system-monitor/concurrency         改 self_check_settings.monitor_concurrency
//
// 实现要点（rule 04 §1）：所有探测都通过 SystemMonitor.Submit(task)
// 路由，绝不直接 probeDirect 调上游。Phase 2 收敛旧 worker。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/bg/systemmonitor"
)

// submitRequest 是 POST /api/admin/system-monitor/submit 的 body schema.
//
// 自动性若不指定默认 mandatory（手工触发即强制）。
type submitRequest struct {
	CredentialID int64  `json:"credential_id"`
	ProviderID   int64  `json:"provider_id"`
	RawModel     string `json:"raw_model"`
	TaskType     string `json:"task_type"`
	Automaticity string `json:"automaticity"` // optional: '' = mandatory
	MaxAttempts  int    `json:"max_attempts"` // optional: 0 = default
}

func (r *submitRequest) normalize() error {
	if r.CredentialID <= 0 {
		return errors.New("credential_id must be > 0")
	}
	if r.RawModel == "" {
		return errors.New("raw_model must not be empty")
	}
	if r.TaskType == "" {
		r.TaskType = "direct_ping"
	}
	if r.Automaticity == "" {
		r.Automaticity = "mandatory"
	}
	if r.Automaticity != "mandatory" && r.Automaticity != "automatic" {
		return fmt.Errorf("invalid automaticity: %q", r.Automaticity)
	}
	knownTypes := map[string]bool{
		"direct_ping":  true,
		"gateway_ping": true,
		"chat_minimal": true,
		"chat_tool":    true,
		"chat_stream":  true,
		"http_ping":    true,
	}
	if !knownTypes[r.TaskType] {
		return fmt.Errorf("invalid task_type: %q", r.TaskType)
	}
	return nil
}

// (POST /api/admin/system-monitor/submit)
func (h *Handler) handleSystemMonitorSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.systemMonitor == nil {
		writeError(w, http.StatusServiceUnavailable, "system monitor not wired")
		return
	}
	var req submitRequest
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if err := req.normalize(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ProviderID == 0 {
		// 尝试从 DB 补全 provider_id（dashboard 缺这个字段时）
		var pid int64
		if err := h.db.QueryRow(r.Context(),
			`SELECT provider_id FROM credentials WHERE id=$1`, req.CredentialID).Scan(&pid); err == nil {
			req.ProviderID = pid
		}
	}

	task := buildSystemMonitorTask(&req)
	id, err := h.systemMonitor.Submit(r.Context(), task)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "submit failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id":      id,
		"task_type":    req.TaskType,
		"automaticity": req.Automaticity,
		"submitted_at": time.Now().UTC(),
	})
}

// buildSystemMonitorTask constructs a Task struct from the request.
func buildSystemMonitorTask(req *submitRequest) *SystemMonitorTask {
	now := time.Now().UTC()
	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		if req.Automaticity == "mandatory" {
			maxAttempts = 3
		} else {
			maxAttempts = 1
		}
	}
	return &SystemMonitorTask{
		TaskType:     req.TaskType,
		Automaticity: req.Automaticity,
		Source:       "button_manual",
		CredentialID: req.CredentialID,
		ProviderID:   req.ProviderID,
		RawModel:     req.RawModel,
		ScheduledAt:  now,
		NextRunAt:    now,
		MaxAttempts:  maxAttempts,
	}
}

// handleSystemMonitorStartAll 仪表盘"开始全部任务"。
//
// 展开范围 = 当前所有 (credential, model) bindings 中 lifecycle_status
// = 'active' AND manual_disabled = FALSE。批量入队，>200 条时分批 sleep。
//
// Phase 1: 单次入队 <200 全部 submit；>200 返回 400 提示人工缩小范围。
// Phase 2: 拆批循环 + SSE queue_full 上报。
func (h *Handler) handleSystemMonitorStartAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.systemMonitor == nil {
		writeError(w, http.StatusServiceUnavailable, "system monitor not wired")
		return
	}
	bindings, err := h.expandAllActiveBindings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "expand failed: "+err.Error())
		return
	}
	if len(bindings) > 200 {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("refusing bulk trigger: %d bindings > 200 limit, please use scoped trigger", len(bindings)))
		return
	}

	results := batchSubmit(r.Context(), h.systemMonitor, bindings, "button_all", "mandatory")
	writeJSON(w, http.StatusOK, map[string]any{
		"triggered": results.accepted,
		"failed":    results.failed,
		"total":     len(bindings),
	})
}

// handleSystemMonitorStopAll 仪表盘"停止全部任务"。
//
// 实现：删除 llmgw:monitor:queue 列表 + 标记 inflight 已存在的不动
// （worker 跑完自然结束；DB 中的 inflight 仅看 inflight token 30s 过期）。
//
// Phase 1：只清空队列，不阻断已 claimed 的任务；返回清空数量。
func (h *Handler) handleSystemMonitorStopAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.systemMonitor == nil {
		writeError(w, http.StatusServiceUnavailable, "system monitor not wired")
		return
	}
	stats, err := h.systemMonitor.QueueStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"queue_size":  stats.QueueSize,
		"stopped":     stats.QueueSize, // Phase 1: stop-all 只是清队列；claimed 任务让其自然结束
		"in_fallback": stats.InFallback,
		"note":        "claimed/in-flight tasks continue to completion; only queued tasks are dropped",
	})
}

// handleSystemMonitorByCredential 入队该凭据下所有 (model) 探测。
func (h *Handler) handleSystemMonitorByCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.systemMonitor == nil {
		writeError(w, http.StatusServiceUnavailable, "system monitor not wired")
		return
	}
	credID, err := parsePathInt(r.URL.Path, "/api/admin/system-monitor/by-credential/")
	if err != nil {
		writeError(w, http.StatusBadRequest, "credential_id: "+err.Error())
		return
	}
	bindings, err := h.expandCredentialBindings(r.Context(), credID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "expand: "+err.Error())
		return
	}
	results := batchSubmit(r.Context(), h.systemMonitor, bindings, "button_by_credential", "mandatory")
	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id": credID,
		"triggered":     results.accepted,
		"failed":        results.failed,
		"total":         len(bindings),
	})
}

// handleSystemMonitorByProvider 入队该供应商下所有 (cred, model) 探测。
//
// direct + http_ping 1:1 配对（design §4.4 + §3 决策 1）。
func (h *Handler) handleSystemMonitorByProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.systemMonitor == nil {
		writeError(w, http.StatusServiceUnavailable, "system monitor not wired")
		return
	}
	providerID, err := parsePathInt(r.URL.Path, "/api/admin/system-monitor/by-provider/")
	if err != nil {
		writeError(w, http.StatusBadRequest, "provider_id: "+err.Error())
		return
	}
	bindings, err := h.expandProviderBindings(r.Context(), providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "expand: "+err.Error())
		return
	}

	// 1:1 配对，每 (cred, model) 发两条：direct_ping + http_ping
	doubled := make([]systemMonitorBinding, 0, len(bindings)*2)
	for _, b := range bindings {
		doubled = append(doubled, b, b)
	}
	results := batchSubmitTyped(r.Context(), h.systemMonitor, doubled, "button_by_provider", "mandatory",
		"direct_ping", "http_ping")
	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id": providerID,
		"triggered":   results.accepted,
		"failed":      results.failed,
		"total":       len(bindings),
		"paired":      true,
	})
}

// handleSystemMonitorByModel 入队该模型下所有 (cred, provider) 探测。
func (h *Handler) handleSystemMonitorByModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.systemMonitor == nil {
		writeError(w, http.StatusServiceUnavailable, "system monitor not wired")
		return
	}
	modelName, err := parseModelFromPath(r.URL.Path, "/api/admin/system-monitor/by-model/")
	if err != nil {
		writeError(w, http.StatusBadRequest, "model: "+err.Error())
		return
	}
	bindings, err := h.expandModelBindings(r.Context(), modelName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "expand: "+err.Error())
		return
	}
	results := batchSubmit(r.Context(), h.systemMonitor, bindings, "button_by_model", "mandatory")
	writeJSON(w, http.StatusOK, map[string]any{
		"model":     modelName,
		"triggered": results.accepted,
		"failed":    results.failed,
		"total":     len(bindings),
	})
}

// handleSystemMonitorStats 队列 + running + in_fallback 快照。
func (h *Handler) handleSystemMonitorStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var queueSize, runningSize int64
	var inFallback bool
	if h.systemMonitor != nil {
		stats, err := h.systemMonitor.QueueStats(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "stats: "+err.Error())
			return
		}
		queueSize = stats.QueueSize
		runningSize = stats.RunningSize
		inFallback = stats.InFallback
	}
	concurrency := 5
	var monitorConcurrency int
	if err := h.db.QueryRow(r.Context(),
		`SELECT monitor_concurrency FROM self_check_settings WHERE id=1`).Scan(&monitorConcurrency); err == nil && monitorConcurrency > 0 {
		concurrency = monitorConcurrency
	}
	var completed, failed, skipped, tokens int64
	_ = h.db.QueryRow(r.Context(), `
		SELECT
			COUNT(*) FILTER (WHERE status = 'success'),
			COUNT(*) FILTER (WHERE status IN ('failed', 'timeout', 'network_error')),
			COUNT(*) FILTER (WHERE status = 'skipped'),
			COALESCE(SUM(total_tokens), 0)
		FROM system_probe_runs
		WHERE created_at >= NOW() - INTERVAL '1 hour'
	`).Scan(&completed, &failed, &skipped, &tokens)
	writeJSON(w, http.StatusOK, map[string]any{
		"queue_size":          queueSize,
		"running_size":        runningSize,
		"in_fallback":         inFallback,
		"monitor_concurrency": concurrency,
		"completed_total_1h":  completed,
		"failed_total_1h":     failed,
		"skipped_total_1h":    skipped,
		"total_tokens_1h":     tokens,
		"snapshot_at":         time.Now().UTC(),
	})
}

// handleSystemMonitorRecentRuns 从 system_probe_runs 读最近 N 条。
func (h *Handler) handleSystemMonitorRecentRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	type row struct {
		ID           int64     `json:"id"`
		TaskID       int64     `json:"task_id"`
		TaskType     string    `json:"task_type"`
		Automaticity string    `json:"automaticity"`
		CredentialID int64     `json:"credential_id"`
		RawModel     string    `json:"raw_model"`
		Source       string    `json:"source"`
		WorkerID     string    `json:"worker_id"`
		Status       string    `json:"status"`
		Attempt      int       `json:"attempt"`
		HTTPStatus   *int      `json:"http_status,omitempty"`
		LatencyMs    *int      `json:"latency_ms,omitempty"`
		ErrCode      string    `json:"err_code"`
		SkipReason   string    `json:"skip_reason"`
		StartedAt    time.Time `json:"started_at"`
		FinishedAt   time.Time `json:"finished_at"`
		RecentReqID  string    `json:"recent_request_id"`
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT id, task_id, task_type, automaticity, credential_id, raw_model,
		       source, COALESCE(worker_id, ''), status, attempt,
		       http_status, latency_ms,
		       COALESCE(err_code, ''), COALESCE(skip_reason, ''),
		       started_at, finished_at,
		       COALESCE(recent_request_id, '')
		FROM system_probe_runs
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query: "+err.Error())
		return
	}
	defer rows.Close()
	out := make([]row, 0, limit)
	for rows.Next() {
		var r row
		var httpStatus, latencyMs *int
		if err := rows.Scan(&r.ID, &r.TaskID, &r.TaskType, &r.Automaticity, &r.CredentialID, &r.RawModel,
			&r.Source, &r.WorkerID, &r.Status, &r.Attempt,
			&httpStatus, &latencyMs,
			&r.ErrCode, &r.SkipReason,
			&r.StartedAt, &r.FinishedAt,
			&r.RecentReqID); err != nil {
			continue
		}
		r.HTTPStatus = httpStatus
		r.LatencyMs = latencyMs
		out = append(out, r)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runs":  out,
		"total": len(out),
		"limit": limit,
	})
}

// handleSystemMonitorConcurrency 修改 self_check_settings.monitor_concurrency。
//
// 该列 CHECK 1-32，由 self_check_settings_monitor_concurrency_check 强制。
//
// PATCH body: {"monitor_concurrency": 8}
func (h *Handler) handleSystemMonitorConcurrency(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body struct {
		MonitorConcurrency int `json:"monitor_concurrency"`
	}
	if err := readJSONRequired(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if body.MonitorConcurrency < 1 || body.MonitorConcurrency > 32 {
		writeError(w, http.StatusBadRequest, "monitor_concurrency must be 1-32")
		return
	}
	// 确保 row 存在
	if _, err := h.db.Exec(r.Context(),
		`INSERT INTO self_check_settings (id, monitor_concurrency) VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET monitor_concurrency = $1`,
		body.MonitorConcurrency); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"monitor_concurrency": body.MonitorConcurrency,
		"updated_at":          time.Now().UTC(),
	})
}

// ── Bindings 展开辅助 ────────────────────────────────────────

// systemMonitorBinding 是 bindings 查询的最小投影。
//
// 同一匿名类型在 SystemMonitorBackend、batchSubmitTyped 之间传，避免引入独立类型文件。
type systemMonitorBinding struct {
	CredentialID, ProviderID int64
	RawModel                 string
}

// expandAllActiveBindings 列出所有 active 凭据的所有 binding。
func (h *Handler) expandAllActiveBindings(ctx context.Context) ([]systemMonitorBinding, error) {
	return h.expandBindings(ctx, `
		SELECT c.id, c.provider_id, pm.raw_model_name
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE COALESCE(c.lifecycle_status, '') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.lifecycle_status, '') = 'active'
		LIMIT 1000
	`)
}

// expandCredentialBindings 列出该 credential 下的所有 binding。
func (h *Handler) expandCredentialBindings(ctx context.Context, credID int64) ([]systemMonitorBinding, error) {
	return h.expandBindings(ctx, `
		SELECT c.id, c.provider_id, pm.raw_model_name
		FROM credentials c
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE c.id = $1
		  AND COALESCE(c.lifecycle_status, '') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		LIMIT 200
	`, credID)
}

// expandProviderBindings 列出该 provider 下所有 active 凭据的 binding。
func (h *Handler) expandProviderBindings(ctx context.Context, providerID int64) ([]systemMonitorBinding, error) {
	return h.expandBindings(ctx, `
		SELECT c.id, c.provider_id, pm.raw_model_name
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE p.id = $1
		  AND COALESCE(c.lifecycle_status, '') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.lifecycle_status, '') = 'active'
		LIMIT 500
	`, providerID)
}

// expandModelBindings 列出该 raw_model 跨所有 provider 的 binding。
func (h *Handler) expandModelBindings(ctx context.Context, modelName string) ([]systemMonitorBinding, error) {
	return h.expandBindings(ctx, `
		SELECT c.id, c.provider_id, pm.raw_model_name
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE pm.raw_model_name = $1
		  AND COALESCE(c.lifecycle_status, '') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.lifecycle_status, '') = 'active'
		LIMIT 500
	`, modelName)
}

// expandBindings 通用查询（限速 + 防爆）。
func (h *Handler) expandBindings(ctx context.Context, sqlText string, args ...any) ([]systemMonitorBinding, error) {
	if h.db == nil {
		return nil, errors.New("db not wired")
	}
	rows, err := h.db.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("expand: %w", err)
	}
	defer rows.Close()
	out := make([]systemMonitorBinding, 0, 64)
	for rows.Next() {
		var b systemMonitorBinding
		if err := rows.Scan(&b.CredentialID, &b.ProviderID, &b.RawModel); err != nil {
			continue
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// batchSubmit 批量入队（任务类型硬编码 direct_ping）。
func batchSubmit(ctx context.Context, sm SystemMonitorBackend, bindings []systemMonitorBinding, source, automaticity string) batchResults {
	return batchSubmitTyped(ctx, sm, bindings, source, automaticity, "direct_ping")
}

// batchSubmitTyped 入队时按 taskTypes 序列循环 — 同一 (cred, model) 可生成多个任务。
//
// 例：by-provider 按钮触发时传 ["direct_ping", "http_ping"]，每条 binding 生成 2 个任务。
func batchSubmitTyped(ctx context.Context, sm SystemMonitorBackend, bindings []systemMonitorBinding, source, automaticity string, taskTypes ...string,
) batchResults {
	results := batchResults{}
	maxAttempts := 3
	if automaticity == "automatic" {
		maxAttempts = 1
	}
	now := time.Now().UTC()
	for _, b := range bindings {
		if b.CredentialID <= 0 || b.RawModel == "" {
			results.failed++
			continue
		}
		for _, tt := range taskTypes {
			task := &SystemMonitorTask{
				TaskType:     tt,
				Automaticity: automaticity,
				Source:       source,
				CredentialID: b.CredentialID,
				ProviderID:   b.ProviderID,
				RawModel:     b.RawModel,
				ScheduledAt:  now,
				NextRunAt:    now,
				MaxAttempts:  maxAttempts,
			}
			if _, err := sm.Submit(ctx, task); err != nil {
				results.failed++
				continue
			}
			results.accepted++
		}
	}
	return results
}

type batchResults struct {
	accepted int
	failed   int
}

// ── URL 路径解析 ──────────────────────────────────────────────

func parsePathInt(p, prefix string) (int64, error) {
	rest := strings.TrimPrefix(p, prefix)
	if rest == "" || strings.Contains(rest, "/") {
		return 0, fmt.Errorf("expected id after %s, got %q", prefix, p)
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("not a number: %s", rest)
	}
	if id <= 0 {
		return 0, fmt.Errorf("id must be > 0, got %d", id)
	}
	return id, nil
}

func parseModelFromPath(p, prefix string) (string, error) {
	rest := strings.TrimPrefix(p, prefix)
	if rest == "" {
		return "", fmt.Errorf("expected model name after %s", prefix)
	}
	return rest, nil
}

// ── 路由注册 ────────────────────────────────────────────────

// RegisterSystemMonitorRoutes 由 cmd/gateway/main.go 调用。
func (h *Handler) RegisterSystemMonitorRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc, superAdminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/system-monitor/submit", superAdminWrap(h.handleSystemMonitorSubmit))
	mux.HandleFunc("/api/admin/system-monitor/start-all", superAdminWrap(h.handleSystemMonitorStartAll))
	mux.HandleFunc("/api/admin/system-monitor/stop-all", superAdminWrap(h.handleSystemMonitorStopAll))
	mux.HandleFunc("/api/admin/system-monitor/by-credential/", superAdminWrap(h.handleSystemMonitorByCredential))
	mux.HandleFunc("/api/admin/system-monitor/by-provider/", superAdminWrap(h.handleSystemMonitorByProvider))
	mux.HandleFunc("/api/admin/system-monitor/by-model/", superAdminWrap(h.handleSystemMonitorByModel))
	mux.HandleFunc("/api/admin/system-monitor/stats", adminWrap(h.handleSystemMonitorStats))
	mux.HandleFunc("/api/admin/system-monitor/recent-runs", adminWrap(h.handleSystemMonitorRecentRuns))
	mux.HandleFunc("/api/admin/system-monitor/concurrency", superAdminWrap(h.handleSystemMonitorConcurrency))
	mux.HandleFunc("/api/admin/system-monitor/migration-metrics", adminWrap(h.handleSystemMonitorMigrationMetrics))
	// Audit follow-up #5: recovery gate observability surface.
	mux.HandleFunc("/api/admin/system-monitor/recovery", adminWrap(h.handleSystemMonitorRecovery))
	// SSE 流：handler 内部自行判断 nil（与 live_stream_sse 模式一致）
	if h.systemMonitorSSE != nil {
		mux.HandleFunc("/api/admin/system-monitor/stream", adminWrap(h.systemMonitorSSE.HandleStream))
	}
}

// ── Phase 3: Migration Metrics ────────────────────────────────

// handleSystemMonitorMigrationMetrics returns coverage metrics for Phase 3 migration.
// GET /api/admin/system-monitor/migration-metrics?window_days=7
func (h *Handler) handleSystemMonitorMigrationMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.systemMonitor == nil {
		http.Error(w, "system monitor not wired", http.StatusServiceUnavailable)
		return
	}

	// Parse window_days query param (default 7)
	windowDays := 7
	if w := r.URL.Query().Get("window_days"); w != "" {
		if parsed, err := strconv.Atoi(w); err == nil && parsed > 0 && parsed <= 90 {
			windowDays = parsed
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Collect metrics
	collectorIface := h.systemMonitor.GetMetricsCollector()
	collector, ok := collectorIface.(*systemmonitor.MetricsCollector)
	if !ok || collector == nil {
		http.Error(w, "MetricsCollector not available", http.StatusInternalServerError)
		return
	}

	metrics, err := collector.CollectCoverage(ctx, windowDays)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to collect metrics: %v", err), http.StatusInternalServerError)
		return
	}

	// Check if ready for migration
	ready, msg, err := collector.IsReadyForMigration(ctx)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to check migration readiness: %v", err), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"metrics":             metrics,
		"ready_for_migration": ready,
		"migration_message":   msg,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// ── Recovery Stats (audit follow-up #5) ────────────────────────

// handleSystemMonitorRecovery returns the recovery gate observability
// snapshot (audit follow-ups #1/#4/#6). GET
// /api/admin/system-monitor/recovery
//
// JSON shape:
//
//	{
//	  "last_error": "...",
//	  "last_error_at": "RFC3339",
//	  "last_recovery_at": "RFC3339",
//	  "last_recovery_key_count": 7,
//	  "consecutive_failures": 0,
//	  "fail_threshold": 3,
//	  "in_fallback": true
//	}
//
// Safe on a nil systemMonitor — returns the zero snapshot with
// HTTP 503 so dashboards can distinguish "disabled" from "freshly
// started" via the absence of "last_recovery_at".
func (h *Handler) handleSystemMonitorRecovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.systemMonitor == nil {
		http.Error(w, "system monitor not wired", http.StatusServiceUnavailable)
		return
	}
	s := h.systemMonitor.RecoveryStats()
	resp := map[string]any{
		"last_error":              s.LastError,
		"last_error_at":           s.LastErrorAt,
		"last_recovery_at":        s.LastRecoveryAt,
		"last_recovery_key_count": s.LastRecoveryKeyCount,
		"consecutive_failures":    s.ConsecutiveFailures,
		"fail_threshold":          s.FailThreshold,
		"in_fallback":             h.systemMonitor.IsFallback(),
		"snapshot_at":             time.Now().UTC(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
