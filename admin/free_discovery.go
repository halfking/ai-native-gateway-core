package admin

// Free resource auto-discovery Admin API (/api/free-discovery/*).
//
// Data model:  sql/migrations/084-freediscovery-schema.sql
// Domain pkg:  domains/freediscovery (template CRUD / discovery engine / batch import)
//
// Routes are mounted in registerRoutes; service dependencies are injected
// at startup via SetFreeDiscovery in main.go (dbConn.Stdlib() bridge +
// credential keyring).

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

// freeDiscoveryDeps groups the three domain services and shares the same
// stdlib DB bridge.
type freeDiscoveryDeps struct {
	templates *freediscovery.TemplateManager
	engine    *freediscovery.DiscoveryEngine
	importer  *freediscovery.ImportService
}

// SetFreeDiscovery wires the free-resource-discovery services. Called once at
// startup; when stdlibDB is nil (no-DB mode) this is a no-op and the related
// routes return 503 on request.
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

// fdDeps retrieves dependencies; writes 503 and returns nil when not wired.
func (h *Handler) fdDeps(w http.ResponseWriter) *freeDiscoveryDeps {
	if h.freeDiscovery == nil {
		writeError(w, http.StatusServiceUnavailable, "free-discovery is not available (database disabled)")
		return nil
	}
	return h.freeDiscovery
}

func (h *Handler) fdTenant(r *http.Request) string {
	// EffectiveTenantID: tenant_admin -> own tenant; super_admin/legacy -> "default".
	return EffectiveTenantID(r)
}

// ── Template management ─────────────────────────────────────────────────

// handleFreeDiscoveryTemplates handles GET (list) / POST (create).
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

// handleFreeDiscoveryTemplateByID GET/PUT/DELETE a single template.
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

// handleFreeDiscoveryPresets GET the built-in provider presets (Groq/OpenRouter/...).
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

// handleFreeDiscoveryImportOrbi POST imports Orbi pi-providers JSON templates.
// Request body is the Orbi template file content: {"providers": {"groq": {...}}}.
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
	// Cap request body size to prevent malicious giant files (real Orbi templates are usually < 100 KB).
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
			// Guard against panic: skip empty provider keys outright (slice bounds).
			failed++
			errs = append(errs, "<empty provider key>: skipped")
			continue
		}
		// Reuse ValidateCreate for the upfront check; failures are recorded in errs without aborting the remaining providers.
		req := &freediscovery.CreateTemplateRequest{
			ProviderCode:   code,
			DisplayName:    freediscovery.TrimmedDisplayName(strings.ToUpper(string([]rune(code)[:1])) + code[1:]),
			BaseURL:        p.BaseURL,
			APIType:        freediscovery.APIType(p.API),
			APIKeyEnv:      "", // Orbi templates' apiKey is a "$VAR" reference; CreateTemplateRequest accepts a backend env reference, validated by Create.
			ModelsEndpoint: "/models",
			CreatedBy:      actor,
		}
		// Orbi apiKey field convention: "$VAR" is an env reference (preserved); any other value is treated as a literal and not parsed as an env name.
		if strings.HasPrefix(p.APIKey, "$") {
			req.APIKeyEnv = p.APIKey
		}
		if msg := req.ValidateCreate(); msg != "" {
			failed++
			errs = append(errs, code+": "+msg)
			continue
		}
		if _, err := deps.templates.Create(r.Context(), tenant, req); err != nil {
			// A single provider failure must not abort the rest of the batch.
			failed++
			errs = append(errs, code+": "+err.Error())
			continue
		}
		created++
	}
	status := "ok"
	if failed > 0 && created > 0 {
		status = "partial" // Partial success: the caller can still inspect errors.
	} else if failed > 0 && created == 0 {
		status = "failed"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "failed": failed, "errors": errs, "status": status,
	})
}

// ── Discovery Tasks ─────────────────────────────────────────────────────

// handleFreeDiscoveryScan POST triggers a discovery task.
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

	// Independent timeout context: a client disconnect must NOT cancel the scan
	// (otherwise the task stays stuck in `running` and even `fail` cannot write
	// to the DB with an already-cancelled ctx); 60s covers a slow upstream.
	scanCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()

	task, err := deps.engine.Run(scanCtx, freediscovery.DiscoveryRequest{
		TemplateID:  body.TemplateID,
		TenantID:    h.fdTenant(r),
		TriggeredBy: fdActor(r),
		TriggerType: freediscovery.TriggerManual,
	})
	if err != nil {
		// When the task has already been persisted in `failed` state, return 200 +
		// the task body so the UI can show failure details instead of treating the
		// background failure as an HTTP error.
		if task != nil && task.Status == freediscovery.TaskStatusFailed {
			writeJSON(w, http.StatusOK, task)
			return
		}
		writeError(w, fdStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleFreeDiscoveryTasks GET lists tasks.
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

// handleFreeDiscoveryTask GET retrieves a single discovery task by id.
// Uses fdStatusFor so repository sentinels (ErrTaskNotFound) surface as 404
// instead of 500; other errors fall through to 500 by design.
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
		writeError(w, fdStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// handleFreeDiscoveryTaskResults GET lists results of a task (?status=pending|all).
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

// handleFreeDiscoveryImport POST batch-imports results into free_resource_catalog.
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

// ── Helpers ─────────────────────────────────────────────────────────────

// fdActor is the operator identifier (for audit; not sensitive).
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

// fdStatusFor maps domain errors to HTTP status codes.
//
// Prefer errors.Is against sentinel errors to avoid mapping any internal error
// that happens to contain "not found" to a 404; only fall back to a substring
// check when no sentinel matches (kept for backwards compatibility with older
// error messages).
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
