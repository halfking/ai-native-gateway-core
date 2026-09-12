package admin

// 免费资源自动发现 Admin API (/api/free-discovery/*).
//
// 数据模型: sql/migrations/084-freediscovery-schema.sql
// 领域包:   domains/freediscovery (模板 CRUD / 发现引擎 / 批量导入)
//
// 路由在 registerRoutes 中挂载; 服务依赖经 SetFreeDiscovery 在 main.go
// 启动时注入 (dbConn.Stdlib() 桥接 + credential keyring).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/freediscovery"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// freeDiscoveryDeps 聚合三个领域服务, 共享同一个 stdlib DB 桥接.
type freeDiscoveryDeps struct {
	templates *freediscovery.TemplateManager
	engine    *freediscovery.DiscoveryEngine
	importer  *freediscovery.ImportService
}

// SetFreeDiscovery 注入免费资源发现服务. 启动时调用一次;
// stdlibDB 为 nil (no-DB 模式) 时跳过, 相关路由在请求时返回 503.
func (h *Handler) SetFreeDiscovery(stdlibDB *sql.DB, kr *secret.Keyring) {
	if stdlibDB == nil {
		return
	}
	tm := freediscovery.NewTemplateManager(stdlibDB, kr)
	h.freeDiscovery = &freeDiscoveryDeps{
		templates: tm,
		engine:    freediscovery.NewDiscoveryEngine(stdlibDB, tm),
		importer:  freediscovery.NewImportService(stdlibDB),
	}
}

// fdDeps 取依赖; 未注入时写 503 并返回 nil.
func (h *Handler) fdDeps(w http.ResponseWriter) *freeDiscoveryDeps {
	if h.freeDiscovery == nil {
		writeError(w, http.StatusServiceUnavailable, "free-discovery is not available (database disabled)")
		return nil
	}
	return h.freeDiscovery
}

func (h *Handler) fdTenant(r *http.Request) string {
	// EffectiveTenantID: tenant_admin → 自有租户; super_admin/legacy → "default".
	return EffectiveTenantID(r)
}

// ── 模板管理 ────────────────────────────────────────────────────────────

// handleFreeDiscoveryTemplates GET(列表) / POST(创建).
func (h *Handler) handleFreeDiscoveryTemplates(w http.ResponseWriter, r *http.Request) {
	deps := h.fdDeps(w)
	if deps == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		enabledOnly := r.URL.Query().Get("enabled") == "true"
		tpls, err := deps.templates.List(r.Context(), h.fdTenant(r), enabledOnly)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if tpls == nil {
			tpls = []*freediscovery.ProviderTemplate{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": tpls})

	case http.MethodPost:
		if RequireSuperAdminForWrite(w, r) {
			return
		}
		var req freediscovery.CreateTemplateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		req.CreatedBy = fdActor(r)
		tpl, err := deps.templates.Create(r.Context(), h.fdTenant(r), &req)
		if err != nil {
			writeError(w, fdStatusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, tpl)

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleFreeDiscoveryTemplateByID GET/PUT/DELETE 单个模板.
func (h *Handler) handleFreeDiscoveryTemplateByID(w http.ResponseWriter, r *http.Request) {
	deps := h.fdDeps(w)
	if deps == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template id")
		return
	}

	switch r.Method {
	case http.MethodGet:
		tpl, err := deps.templates.Get(r.Context(), h.fdTenant(r), id)
		if err != nil {
			writeError(w, fdStatusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, tpl)

	case http.MethodPut, http.MethodPatch:
		if RequireSuperAdminForWrite(w, r) {
			return
		}
		var req freediscovery.UpdateTemplateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		tpl, err := deps.templates.Update(r.Context(), h.fdTenant(r), id, &req)
		if err != nil {
			writeError(w, fdStatusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, tpl)

	case http.MethodDelete:
		if RequireSuperAdminForWrite(w, r) {
			return
		}
		if err := deps.templates.Delete(r.Context(), h.fdTenant(r), id); err != nil {
			writeError(w, fdStatusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})

	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleFreeDiscoveryPresets GET 内置供应商预设 (Groq/OpenRouter/...).
func (h *Handler) handleFreeDiscoveryPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.fdDeps(w) == nil {
		return
	}
	type presetView struct {
		ProviderCode string `json:"provider_code"`
		DisplayName  string `json:"display_name"`
		BaseURL      string `json:"base_url"`
		APIType      string `json:"api_type"`
		APIKeyEnv    string `json:"api_key_env"`
		TosVerdict   string `json:"tos_verdict"`
		TosNotes     string `json:"tos_notes"`
	}
	var out []presetView
	for _, code := range freediscovery.ListPresetCodes() {
		p := freediscovery.GetPreset(code)
		if p == nil {
			continue
		}
		out = append(out, presetView{
			ProviderCode: p.ProviderCode,
			DisplayName:  p.DisplayName,
			BaseURL:      p.BaseURL,
			APIType:      string(p.APIType),
			APIKeyEnv:    p.APIKeyEnv,
			TosVerdict:   p.TosVerdict,
			TosNotes:     p.TosNotes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": out})
}

// handleFreeDiscoveryImportOrbi POST 导入 Orbi pi-providers JSON 模板.
// 请求体即 Orbi 模板文件内容: {"providers": {"groq": {...}}}.
func (h *Handler) handleFreeDiscoveryImportOrbi(w http.ResponseWriter, r *http.Request) {
	deps := h.fdDeps(w)
	if deps == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	// 限制请求体大小, 防止恶意巨文件 (实际 Orbi 模板通常 < 100 KB).
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB
	var file freediscovery.OrbiProviderFile
	if err := json.NewDecoder(r.Body).Decode(&file); err != nil {
		writeError(w, http.StatusBadRequest, "invalid Orbi template JSON: "+err.Error())
		return
	}
	if len(file.Providers) == 0 {
		writeError(w, http.StatusBadRequest, "no providers found in Orbi template")
		return
	}

	tenant := h.fdTenant(r)
	actor := fdActor(r)
	created, failed := 0, 0
	var errs []string
	for code, p := range file.Providers {
		if strings.TrimSpace(code) == "" {
			// 防 panic: 空 provider key 直接跳过 (slice bounds)
			failed++
			errs = append(errs, "<empty provider key>: skipped")
			continue
		}
		// 复用 ValidateCreate 前置校验, 失败记入 errs 而不阻断其它 provider.
		req := &freediscovery.CreateTemplateRequest{
			ProviderCode:   code,
			DisplayName:    freediscovery.TrimmedDisplayName(strings.ToUpper(string([]rune(code)[:1])) + code[1:]),
			BaseURL:        p.BaseURL,
			APIType:        freediscovery.APIType(p.API),
			APIKeyEnv:      "", // Orbi 模板的 apiKey 是 "$VAR" 引用; CreateTemplateRequest 接受后端 env 引用, 由 Create 校验
			ModelsEndpoint: "/models",
			CreatedBy:      actor,
		}
		// Orbi apiKey 字段约定: "$VAR" 表示 env 引用 (保留); 其它值视为字面量, 不作为 env 名解析.
		if strings.HasPrefix(p.APIKey, "$") {
			req.APIKeyEnv = p.APIKey
		}
		if msg := req.ValidateCreate(); msg != "" {
			failed++
			errs = append(errs, code+": "+msg)
			continue
		}
		if _, err := deps.templates.Create(r.Context(), tenant, req); err != nil {
			// 单个 provider 失败不阻断其余导入
			failed++
			errs = append(errs, code+": "+err.Error())
			continue
		}
		created++
	}
	status := "ok"
	if failed > 0 && created > 0 {
		status = "partial" // 部分成功: 调用方可继续查看 errors
	} else if failed > 0 && created == 0 {
		status = "failed"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "failed": failed, "errors": errs, "status": status,
	})
}

// ── 发现任务 ────────────────────────────────────────────────────────────

// handleFreeDiscoveryScan POST 触发一次发现任务.
func (h *Handler) handleFreeDiscoveryScan(w http.ResponseWriter, r *http.Request) {
	deps := h.fdDeps(w)
	if deps == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	var body struct {
		TemplateID int64 `json:"template_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.TemplateID <= 0 {
		writeError(w, http.StatusBadRequest, "template_id is required")
		return
	}

	// 独立超时上下文: 客户端断开不取消扫描 (否则任务永久停留在 running,
	// 且 fail 也无法用已取消的 ctx 写库); 60s 覆盖上游慢响应.
	scanCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()

	task, err := deps.engine.Run(scanCtx, freediscovery.DiscoveryRequest{
		TemplateID:  body.TemplateID,
		TenantID:    h.fdTenant(r),
		TriggeredBy: fdActor(r),
		TriggerType: freediscovery.TriggerManual,
	})
	if err != nil {
		// 任务已落库 failed 状态时返回 200 + 任务体, 让 UI 能展示失败详情
		if task != nil && task.Status == freediscovery.TaskStatusFailed {
			writeJSON(w, http.StatusOK, task)
			return
		}
		writeError(w, fdStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleFreeDiscoveryTasks GET 任务列表.
func (h *Handler) handleFreeDiscoveryTasks(w http.ResponseWriter, r *http.Request) {
	deps := h.fdDeps(w)
	if deps == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tasks, err := deps.engine.ListTasks(r.Context(), h.fdTenant(r), limit)
	if err != nil {
		// List endpoints have no expected sentinel errors today (failures are DB faults,
		// so fdStatusFor falls through to 500); routed through fdStatusFor for consistency.
		writeError(w, fdStatusFor(err), err.Error())
		return
	}
	if tasks == nil {
		tasks = []*freediscovery.DiscoveryTask{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

// handleFreeDiscoveryTask GET 单个任务.
func (h *Handler) handleFreeDiscoveryTask(w http.ResponseWriter, r *http.Request) {
	deps := h.fdDeps(w)
	if deps == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	task, err := deps.engine.GetTask(r.Context(), h.fdTenant(r), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleFreeDiscoveryTaskResults GET 任务发现结果 (?status=pending|all).
func (h *Handler) handleFreeDiscoveryTaskResults(w http.ResponseWriter, r *http.Request) {
	deps := h.fdDeps(w)
	if deps == nil {
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	results, err := deps.engine.ListResults(r.Context(), h.fdTenant(r), id, status)
	if err != nil {
		// See handleFreeDiscoveryTasks: no expected sentinels, fdStatusFor falls through to 500.
		writeError(w, fdStatusFor(err), err.Error())
		return
	}
	if results == nil {
		results = []*freediscovery.DiscoveryResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// handleFreeDiscoveryImport POST 批量导入到 free_resource_catalog.
func (h *Handler) handleFreeDiscoveryImport(w http.ResponseWriter, r *http.Request) {
	deps := h.fdDeps(w)
	if deps == nil {
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	var req freediscovery.ImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	req.TenantID = h.fdTenant(r)
	req.ImportedBy = fdActor(r)
	if req.TaskID <= 0 {
		writeError(w, http.StatusBadRequest, "task_id is required")
		return
	}

	summary, err := deps.importer.Import(r.Context(), req)
	if err != nil {
		writeError(w, fdStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// ── 辅助 ────────────────────────────────────────────────────────────────

// fdActor 操作人标识 (审计; 不含敏感信息).
func fdActor(r *http.Request) string {
	if auth := GetAuthContext(r); auth != nil {
		if auth.Username != "" {
			return auth.Username
		}
		if auth.UserID != 0 {
			return "user-" + strconv.Itoa(auth.UserID)
		}
	}
	return "legacy-admin-key"
}

// fdStatusFor 领域错误 → HTTP 状态码.
//
// 优先用 errors.Is 匹配 sentinel 错误, 避免误把任意含 "not found" 的内部错误
// 映射为 404; 仅当不是 sentinel 时回退到字符串包含判断 (兼容旧错误消息).
func fdStatusFor(err error) int {
	switch {
	case errors.Is(err, freediscovery.ErrTemplateNotFound),
		errors.Is(err, freediscovery.ErrImportTaskNotFound):
		return http.StatusNotFound
	case errors.Is(err, freediscovery.ErrTemplateDisabled),
		errors.Is(err, freediscovery.ErrImportTaskNotReady),
		errors.Is(err, freediscovery.ErrTaskStateConflict):
		return http.StatusConflict
	case strings.Contains(err.Error(), "required"),
		strings.Contains(err.Error(), "invalid"),
		strings.Contains(err.Error(), "must be"),
		strings.Contains(err.Error(), "must start"),
		strings.Contains(err.Error(), "only allows"),
		strings.Contains(err.Error(), "cannot be empty"),
		strings.Contains(err.Error(), "must use"),
		strings.Contains(err.Error(), "userinfo"),
		strings.Contains(err.Error(), "fragment"),
		strings.Contains(err.Error(), "loopback"),
		strings.Contains(err.Error(), "private"),
		strings.Contains(err.Error(), "link-local"),
		strings.Contains(err.Error(), "control characters"),
		strings.Contains(err.Error(), "exceeds maximum length"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
