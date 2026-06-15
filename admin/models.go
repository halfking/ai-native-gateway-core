package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	// Block write operations for tenant_admin
	if RequireSuperAdminForWrite(w, r) {
		return
	}

	remaining := r.URL.Path[len("/api/models/"):]

	if remaining == "" {
		if r.Method == http.MethodGet {
			h.listModels(w, r)
		} else if r.Method == http.MethodPost {
			h.createModel(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	idStr := remaining
	subPath := ""
	for i, c := range remaining {
		if c == '/' {
			idStr = remaining[:i]
			subPath = remaining[i+1:]
			break
		}
	}

	id, err := strconv.Atoi(idStr)
	if err != nil {
		switch idStr {
		case "families":
			h.listModelFamilies(w, r)
		case "by-tag-matrix":
			h.getTagMatrix(w, r)
		case "discover":
			if subPath == "status" {
				h.getDiscoverStatus(w, r)
			} else if r.Method == http.MethodPost {
				h.triggerDiscover(w, r)
			} else {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		default:
			http.NotFound(w, r)
		}
		return
	}

	switch {
	case subPath == "":
		if r.Method == http.MethodGet {
			h.getModel(w, r, id)
		} else if r.Method == http.MethodPatch {
			h.updateModel(w, r, id)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case subPath == "aliases":
		if r.Method == http.MethodPost {
			h.handleModelAliases(w, r, id)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case subPath == "aliases/bulk":
		if r.Method == http.MethodPost {
			h.createAliasesBulk(w, r, id)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case len(subPath) > 8 && subPath[:8] == "aliases/":
		aliasRemaining := subPath[8:]
		h.handleAliasSubPath(w, r, id, aliasRemaining)
	case subPath == "tags":
		if r.Method == http.MethodPatch {
			h.updateModelTags(w, r, id)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case subPath == "tags/reset":
		if r.Method == http.MethodPost {
			h.resetModelTags(w, r, id)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleModelsRoot(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	// Block write operations for tenant_admin
	if RequireSuperAdminForWrite(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		h.listModels(w, r)
	} else if r.Method == http.MethodPost {
		h.createModel(w, r)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	where := []string{"1=1"}
	args := []any{}
	argIdx := 1
	if status := queryString(r, "status"); status != "" {
		where = append(where, fmt.Sprintf("mc.status = $%d", argIdx))
		args = append(args, status)
		argIdx++
	}
	if family := queryString(r, "family"); family != "" {
		where = append(where, fmt.Sprintf("mc.family = $%d", argIdx))
		args = append(args, family)
		argIdx++
	}
	if modality := queryString(r, "modality"); modality != "" {
		where = append(where, fmt.Sprintf("mc.modality = $%d", argIdx))
		args = append(args, modality)
		argIdx++
	}

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT mc.id, mc.canonical_name, COALESCE(mc.display_name,''),
		       COALESCE(mc.modality,'text'), mc.context_window, mc.parameters_b,
		       COALESCE(mc.status,'active'), COALESCE(mc.tags::text,'[]'),
		       COALESCE(mc.input_price_cny,0), COALESCE(mc.output_price_cny,0)
		FROM models_canonical mc
		WHERE %s
		ORDER BY mc.family NULLS LAST, mc.status, mc.canonical_name
	`, strings.Join(where, " AND ")), args...)
	if err != nil {
		slog.Error("listModels query failed", "error", err)
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	defer rows.Close()

	type model struct {
		ID            int             `json:"id"`
		CanonicalName string          `json:"canonical_name"`
		DisplayName   string          `json:"display_name"`
		Modality      string          `json:"modality"`
		ContextWindow *int            `json:"context_window"`
		ParametersB   *float64        `json:"parameters_b"`
		Status        string          `json:"status"`
		Tags          json.RawMessage `json:"tags"`
		InputPriceCNY float64         `json:"input_price_cny"`
		OutputPriceCNY float64       `json:"output_price_cny"`
	}
	models := make([]model, 0)
	for rows.Next() {
		var m model
		var tagsStr string
		if err := rows.Scan(&m.ID, &m.CanonicalName, &m.DisplayName, &m.Modality,
			&m.ContextWindow, &m.ParametersB, &m.Status, &tagsStr,
			&m.InputPriceCNY, &m.OutputPriceCNY); err != nil {
			slog.Error("listModels scan failed", "error", err)
			continue
		}
		if !json.Valid([]byte(tagsStr)) {
			tagsStr = "[]"
		}
		m.Tags = json.RawMessage(tagsStr)
		models = append(models, m)
	}
	if err := rows.Err(); err != nil {
		slog.Error("listModels rows.Err", "error", err)
	}
	slog.Info("listModels result", "count", len(models))
	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(models),
		"items": models,
	})
}

func (h *Handler) createModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CanonicalName   string   `json:"canonical_name"`
		DisplayName     *string  `json:"display_name"`
		Modality        string   `json:"modality"`
		InputPriceCNY   *float64 `json:"input_price_cny"`
		OutputPriceCNY  *float64 `json:"output_price_cny"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	displayName := req.CanonicalName
	if req.DisplayName != nil && *req.DisplayName != "" {
		displayName = *req.DisplayName
	}
	modality := "text"
	if req.Modality != "" {
		modality = req.Modality
	}
	inputPrice := 0.0
	if req.InputPriceCNY != nil {
		inputPrice = *req.InputPriceCNY
	}
	outputPrice := 0.0
	if req.OutputPriceCNY != nil {
		outputPrice = *req.OutputPriceCNY
	}

	var id int
	err := h.db.QueryRow(ctx, `
		INSERT INTO models_canonical (canonical_name, display_name, modality, status, input_price_cny, output_price_cny)
		VALUES ($1, $2, $3, 'active', $4, $5)
		ON CONFLICT (canonical_name) DO NOTHING
		RETURNING id
	`, req.CanonicalName, displayName, modality, inputPrice, outputPrice).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "message": "ok"})
}

func (h *Handler) getModel(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	type modelRow struct {
		ID             int             `json:"id"`
		CanonicalName  string          `json:"canonical_name"`
		DisplayName    string          `json:"display_name"`
		Family         *string         `json:"family"`
		Modality       string          `json:"modality"`
		ContextWindow  *int            `json:"context_window"`
		ParametersB    *float64        `json:"parameters_b"`
		Notes          *string         `json:"notes"`
		Status         string          `json:"status"`
		DisabledReason *string         `json:"disabled_reason"`
		Source         *string         `json:"source"`
		Tags           json.RawMessage `json:"tags"`
		TagsLocked     *bool           `json:"tags_locked"`
		TagsUpdatedAt  *time.Time      `json:"tags_updated_at"`
		CreatedAt      *time.Time      `json:"created_at"`
		UpdatedAt      *time.Time      `json:"updated_at"`
		InputPriceCNY  float64         `json:"input_price_cny"`
		OutputPriceCNY float64         `json:"output_price_cny"`
	}
	m := modelRow{}
	err := h.db.QueryRow(ctx, `
		SELECT id, canonical_name, COALESCE(display_name,''), family,
		       COALESCE(modality,'text'), context_window, parameters_b,
		       notes, COALESCE(status,'active'), disabled_reason, source,
		       COALESCE(array_to_json(tags)::jsonb, '[]'::jsonb), tags_locked, tags_updated_at,
		       created_at, updated_at,
		       COALESCE(input_price_cny,0), COALESCE(output_price_cny,0)
		FROM models_canonical WHERE id = $1
	`, id).Scan(&m.ID, &m.CanonicalName, &m.DisplayName, &m.Family, &m.Modality,
		&m.ContextWindow, &m.ParametersB, &m.Notes, &m.Status, &m.DisabledReason,
		&m.Source, &m.Tags, &m.TagsLocked, &m.TagsUpdatedAt, &m.CreatedAt,
		&m.UpdatedAt, &m.InputPriceCNY, &m.OutputPriceCNY)
	if err != nil {
		writeError(w, http.StatusNotFound, "model not found")
		return
	}

	type aliasRow struct {
		ID           int       `json:"id"`
		RawName      string    `json:"raw_name"`
		Quantization *string   `json:"quantization"`
		Surface      *string   `json:"surface"`
		Status       string    `json:"status"`
		Notes        *string   `json:"notes"`
		UpdatedAt    time.Time `json:"updated_at"`
	}
	aliases := make([]aliasRow, 0)
	aliasRows, err := h.db.Query(ctx, `
		SELECT id, raw_name, quantization, surface, COALESCE(status,'active'),
		       notes, COALESCE(updated_at, NOW())
		FROM model_aliases WHERE canonical_id = $1
		ORDER BY status, raw_name
	`, id)
	if err != nil {
		slog.Warn("getModel aliases query failed", "error", err, "id", id)
	} else {
		for aliasRows.Next() {
			var a aliasRow
			if err := aliasRows.Scan(&a.ID, &a.RawName, &a.Quantization, &a.Surface,
				&a.Status, &a.Notes, &a.UpdatedAt); err != nil {
				continue
			}
			aliases = append(aliases, a)
		}
		aliasRows.Close()
	}

	type offerRow struct {
		ProviderID        int      `json:"provider_id"`
		ProviderName      string   `json:"provider_name"`
		CatalogCode       *string  `json:"catalog_code"`
		BaseURL           *string  `json:"base_url"`
		ProviderEnabled   bool     `json:"provider_enabled"`
		CredentialID      int      `json:"credential_id"`
		CredentialLabel   *string  `json:"credential_label"`
		CredentialStatus  *string  `json:"credential_status"`
		HealthStatus      *string  `json:"health_status"`
		ConcurrencyLimit  *int     `json:"concurrency_limit"`
		RawModelName      string   `json:"raw_model_name"`
		StandardizedName  *string  `json:"standardized_name"`
		P95LatencyMs      *float64 `json:"p95_latency_ms"`
		SuccessRate       *float64 `json:"success_rate"`
		Available         *bool    `json:"available"`
		InputPrice        *float64 `json:"input_price"`
		OutputPrice       *float64 `json:"output_price"`
		CacheReadPrice    *float64 `json:"cache_read_price"`
		CacheWritePrice   *float64 `json:"cache_write_price"`
	}
	offers := make([]offerRow, 0)
	offerRows, err := h.db.Query(ctx, `
		SELECT DISTINCT
		    p.id AS provider_id,
		    COALESCE(p.display_name, '') AS provider_name,
		    p.catalog_code,
		    p.base_url,
		    COALESCE(p.enabled, FALSE) AS provider_enabled,
		    c.id AS credential_id,
		    c.label AS credential_label,
		    c.status AS credential_status,
		    c.health_status,
		    c.concurrency_limit,
		    COALESCE(mo.raw_model_name, '') AS raw_model_name,
		    mo.standardized_name,
		    mo.p95_latency_ms,
		    mo.success_rate,
		    mo.available,
		    COALESCE(mo.unit_price_in_per_1m, mc.input_price_cny) AS input_price,
		    COALESCE(mo.unit_price_out_per_1m, mc.output_price_cny) AS output_price,
		    mo.cache_read_price_per_1m,
		    mo.cache_write_price_per_1m
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers   p ON p.id = c.provider_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		WHERE mo.canonical_id = $1
		   OR mo.raw_model_name IN (SELECT raw_name FROM model_aliases WHERE canonical_id = $1)
		ORDER BY provider_name, credential_id
	`, id)
	if err != nil {
		slog.Warn("getModel offers query failed", "error", err, "id", id)
	} else {
		for offerRows.Next() {
			var o offerRow
			if err := offerRows.Scan(&o.ProviderID, &o.ProviderName, &o.CatalogCode,
				&o.BaseURL, &o.ProviderEnabled, &o.CredentialID, &o.CredentialLabel,
				&o.CredentialStatus, &o.HealthStatus, &o.ConcurrencyLimit,
				&o.RawModelName, &o.StandardizedName, &o.P95LatencyMs, &o.SuccessRate,
				&o.Available, &o.InputPrice, &o.OutputPrice, &o.CacheReadPrice,
				&o.CacheWritePrice); err != nil {
				continue
			}
			offers = append(offers, o)
		}
		offerRows.Close()
	}

	resp := map[string]any{
		"id":              m.ID,
		"canonical_name":  m.CanonicalName,
		"display_name":    m.DisplayName,
		"family":          m.Family,
		"modality":        m.Modality,
		"context_window":  m.ContextWindow,
		"parameters_b":    m.ParametersB,
		"notes":           m.Notes,
		"status":          m.Status,
		"disabled_reason": m.DisabledReason,
		"source":          m.Source,
		"tags":            m.Tags,
		"tags_locked":     m.TagsLocked,
		"tags_updated_at": m.TagsUpdatedAt,
		"created_at":      m.CreatedAt,
		"updated_at":      m.UpdatedAt,
		"input_price_cny": m.InputPriceCNY,
		"output_price_cny": m.OutputPriceCNY,
		"aliases":         aliases,
		"offers":          offers,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) updateModel(w http.ResponseWriter, r *http.Request, id int) {
	var req struct {
		DisplayName *string `json:"display_name"`
		Status      *string `json:"status"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if req.DisplayName != nil {
		h.db.Exec(ctx, `UPDATE models_canonical SET display_name = $1 WHERE id = $2`, *req.DisplayName, id)
	}
	if req.Status != nil {
		h.db.Exec(ctx, `UPDATE models_canonical SET status = $1 WHERE id = $2`, *req.Status, id)
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

var knownFamilyMetadata = map[string]struct {
	DisplayName string
	Vendor      string
}{
	"openai-gpt":       {"OpenAI GPT", "OpenAI"},
	"anthropic-claude": {"Anthropic Claude", "Anthropic"},
	"google-gemini":    {"Google Gemini", "Google"},
	"deepseek":         {"DeepSeek", "DeepSeek"},
	"qwen":             {"Qwen (通义千问)", "Alibaba"},
	"doubao":           {"Doubao (豆包)", "ByteDance"},
	"zhipu-glm":        {"Zhipu GLM", "Zhipu AI"},
	"meta-llama":       {"Meta Llama", "Meta"},
	"minimax":          {"MiniMax", "MiniMax"},
	"xiaomi-mimo":      {"Xiaomi MiMo", "小米"},
}

func familyDisplayAndVendor(familyID string) (string, string) {
	if familyID == "" {
		return "", ""
	}
	if m, ok := knownFamilyMetadata[familyID]; ok {
		return m.DisplayName, m.Vendor
	}
	display := familyID
	parts := strings.SplitN(familyID, "-", 2)
	if len(parts) == 2 {
		display = parts[0] + " " + titleWord(parts[1])
	} else {
		display = titleWord(familyID)
	}
	return display, ""
}

func titleWord(s string) string {
	if s == "" {
		return s
	}
	upper := strings.ToUpper(s[:1])
	if len(s) == 1 {
		return upper
	}
	return upper + s[1:]
}

func (h *Handler) listModelFamilies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	type family struct {
		ID          string  `json:"id"`
		DisplayName string  `json:"display_name"`
		Vendor      string  `json:"vendor"`
		Status      string  `json:"status"`
		Source      string  `json:"source"`
		Notes       *string `json:"notes"`
		ModelCount  int     `json:"model_count"`
	}
	items := make(map[string]*family)

	rows, err := h.db.Query(ctx, `
		SELECT mf.id, mf.display_name, COALESCE(mf.vendor,''),
		       COALESCE(mf.status,'active'), COALESCE(mf.source,'registered'),
		       mf.notes,
		       COUNT(mc.id) AS model_count
		FROM model_families mf
		LEFT JOIN models_canonical mc ON mc.family = mf.id
		GROUP BY mf.id, mf.display_name, mf.vendor, mf.status, mf.source, mf.notes
		ORDER BY mf.status, mf.display_name
	`)
	if err != nil {
		slog.Error("listModelFamilies query failed", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	for rows.Next() {
		var f family
		if err := rows.Scan(&f.ID, &f.DisplayName, &f.Vendor, &f.Status, &f.Source, &f.Notes, &f.ModelCount); err != nil {
			slog.Warn("listModelFamilies scan failed", "error", err)
			continue
		}
		items[f.ID] = &f
	}
	rows.Close()

	derivedRows, err := h.db.Query(ctx, `
		SELECT mc.family AS id, COUNT(mc.id) AS model_count
		FROM models_canonical mc
		LEFT JOIN model_families mf ON mf.id = mc.family
		WHERE mc.family IS NOT NULL
		  AND mc.family <> ''
		  AND mf.id IS NULL
		GROUP BY mc.family
	`)
	if err != nil {
		slog.Warn("listModelFamilies derived query failed", "error", err)
	} else {
		for derivedRows.Next() {
			var id string
			var modelCount int
			if err := derivedRows.Scan(&id, &modelCount); err != nil {
				continue
			}
			display, vendor := familyDisplayAndVendor(id)
			if display == "" {
				display = id
			}
			items[id] = &family{
				ID:          id,
				DisplayName: display,
				Vendor:      vendor,
				Status:      "active",
				Source:      "derived",
				Notes:       nil,
				ModelCount:  modelCount,
			}
		}
		derivedRows.Close()
	}

	list := make([]*family, 0, len(items))
	for _, v := range items {
		list = append(list, v)
	}
	sort.Slice(list, func(i, j int) bool {
		si, sj := list[i].Status, list[j].Status
		if si != sj {
			return si < sj
		}
		di, dj := list[i].DisplayName, list[j].DisplayName
		if di == "" {
			di = list[i].ID
		}
		if dj == "" {
			dj = list[j].ID
		}
		return di < dj
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (h *Handler) getTagMatrix(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tagParam := r.URL.Query()["tag"]
	params := []any{"active", "active"}
	whereExtra := ""
	if len(tagParam) > 0 {
		whereExtra = " AND mc.tags @> $3::jsonb"
		params = append(params, tagParam[0])
	}

	query := `SELECT mc.canonical_name, COALESCE(mc.tags,'[]'::jsonb),
	                 p.id, COALESCE(p.display_name,''), COUNT(DISTINCT mo.id) AS offer_count
	          FROM models_canonical mc
	          JOIN model_aliases ma ON ma.canonical_id = mc.id
	          JOIN model_offers mo ON lower(mo.raw_model_name) = lower(ma.raw_name)
	          JOIN credentials c ON c.id = mo.credential_id
	          JOIN providers p ON p.id = c.provider_id
	          WHERE mo.available = TRUE AND c.status = 'active' AND p.enabled = TRUE
	            AND mc.status = $1 AND ma.status = 'active'` + whereExtra + `
	          GROUP BY mc.canonical_name, mc.tags, p.id, p.display_name
	          ORDER BY mc.canonical_name, p.display_name`

	rows, err := h.db.Query(ctx, query, params...)
	if err != nil {
		slog.Warn("tag matrix query failed", "error", err)
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}, "error": err.Error()})
		return
	}
	defer rows.Close()

	type providerInfo struct {
		ProviderID   int    `json:"provider_id"`
		ProviderName string `json:"provider_name"`
		OfferCount   int    `json:"offer_count"`
	}
	type matrixEntry struct {
		CanonicalName string          `json:"canonical_name"`
		Tags          json.RawMessage `json:"tags"`
		Providers     []providerInfo  `json:"providers"`
	}
	matrix := make(map[string]*matrixEntry)

	for rows.Next() {
		var cn string
		var tagsJSON []byte
		var pid int
		var pname string
		var offers int
		if err := rows.Scan(&cn, &tagsJSON, &pid, &pname, &offers); err != nil {
			continue
		}
		if _, ok := matrix[cn]; !ok {
			matrix[cn] = &matrixEntry{CanonicalName: cn, Tags: tagsJSON}
		}
		matrix[cn].Providers = append(matrix[cn].Providers, providerInfo{
			ProviderID: pid, ProviderName: pname, OfferCount: offers,
		})
	}

	items := make([]*matrixEntry, 0, len(matrix))
	for _, v := range matrix {
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"total":      len(items),
		"by_tag":     tagParam != nil,
		"filter_tag": tagParam,
	})
}

func (h *Handler) getDiscoverStatus(w http.ResponseWriter, r *http.Request) {
	if h.discSvc != nil {
		status := h.discSvc.Status()
		writeJSON(w, http.StatusOK, status)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "idle", "last_run": nil})
}

func (h *Handler) triggerDiscover(w http.ResponseWriter, r *http.Request) {
	if h.discSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "discovery service not available")
		return
	}
	go func() { _ = h.discSvc.RunOnce(r.Context(), 0) }()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (h *Handler) handleModelAliases(w http.ResponseWriter, r *http.Request, modelID int) {
	var req struct {
		RawName      string  `json:"raw_name"`
		Quantization *string `json:"quantization"`
		Surface      *string `json:"surface"`
		Notes        *string `json:"notes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.RawName == "" {
		writeError(w, http.StatusBadRequest, "raw_name required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var exists int
	h.db.QueryRow(ctx, `SELECT 1 FROM models_canonical WHERE id = $1`, modelID).Scan(&exists)

	quantization := ""
	if req.Quantization != nil {
		quantization = *req.Quantization
	}
	surface := ""
	if req.Surface != nil {
		surface = *req.Surface
	}
	notes := ""
	if req.Notes != nil {
		notes = *req.Notes
	}

	var aliasID int
	err := h.db.QueryRow(ctx, `
		INSERT INTO model_aliases (canonical_id, raw_name, quantization, surface, notes, status, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'active', NOW())
		ON CONFLICT (raw_name, COALESCE(quantization,''), COALESCE(surface,''))
		DO UPDATE SET canonical_id = EXCLUDED.canonical_id, notes = EXCLUDED.notes, status = 'active', updated_at = NOW()
		RETURNING id
	`, modelID, req.RawName, quantization, surface, notes).Scan(&aliasID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create alias failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":            aliasID,
		"canonical_id":  modelID,
		"raw_name":      req.RawName,
		"quantization":  quantization,
		"surface":       surface,
		"status":        "active",
		"notes":         notes,
	})
}

func (h *Handler) createAliasesBulk(w http.ResponseWriter, r *http.Request, modelID int) {
	var req struct {
		RawNames       []string `json:"raw_names"`
		Notes          *string  `json:"notes"`
		ClientProfiles []string `json:"client_profiles"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	notes := "bulk import"
	if req.Notes != nil {
		notes = *req.Notes
	}

	var created []map[string]any
	for _, name := range req.RawNames {
		if name == "" {
			continue
		}
		var aliasID int
		err := h.db.QueryRow(ctx, `
			INSERT INTO model_aliases (canonical_id, raw_name, notes, status, client_profiles, updated_at)
			VALUES ($1, $2, $3, 'active', $4, NOW())
			ON CONFLICT (raw_name, COALESCE(quantization,''), COALESCE(surface,''))
			DO UPDATE SET canonical_id = EXCLUDED.canonical_id,
			              notes = COALESCE(EXCLUDED.notes, model_aliases.notes),
			              status = 'active',
			              client_profiles = COALESCE(EXCLUDED.client_profiles, model_aliases.client_profiles),
			              updated_at = NOW()
			RETURNING id
		`, modelID, name, notes, req.ClientProfiles).Scan(&aliasID)
		if err != nil {
			continue
		}
		created = append(created, map[string]any{
			"id":             aliasID,
			"canonical_id":   modelID,
			"raw_name":       name,
			"status":         "active",
			"client_profiles": req.ClientProfiles,
			"notes":          notes,
		})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"created": created,
		"count":   len(created),
	})
}

func (h *Handler) handleAliasSubPath(w http.ResponseWriter, r *http.Request, modelID int, remaining string) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	aliasID, err := strconv.Atoi(remaining)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid alias id")
		return
	}
	h.patchAlias(w, r, modelID, aliasID)
}

func (h *Handler) patchAlias(w http.ResponseWriter, r *http.Request, canonicalID, aliasID int) {
	var req struct {
		Quantization *string `json:"quantization"`
		Surface      *string `json:"surface"`
		Status       *string `json:"status"`
		Notes        *string `json:"notes"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id, cid int
	var rawName, quant, surf, status, notes string
	err := h.db.QueryRow(ctx, `
		UPDATE model_aliases SET updated_at = NOW(),
		       quantization = COALESCE($1, quantization),
		       surface = COALESCE($2, surface),
		       status = COALESCE($3, status),
		       notes = COALESCE($4, notes)
		WHERE id = $5 AND canonical_id = $6
		RETURNING id, canonical_id, raw_name, COALESCE(quantization,''), COALESCE(surface,''), status, COALESCE(notes,'')
	`, req.Quantization, req.Surface, req.Status, req.Notes, aliasID, canonicalID).Scan(&id, &cid, &rawName, &quant, &surf, &status, &notes)
	if err != nil {
		writeError(w, http.StatusNotFound, "alias not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "canonical_id": cid, "raw_name": rawName,
		"quantization": quant, "surface": surf, "status": status, "notes": notes,
	})
}

func (h *Handler) updateModelTags(w http.ResponseWriter, r *http.Request, id int) {
	var req struct {
		Tags json.RawMessage `json:"tags"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.db.Exec(ctx, `UPDATE models_canonical SET tags = $1 WHERE id = $2`, req.Tags, id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *Handler) resetModelTags(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.db.Exec(ctx, `UPDATE models_canonical SET tags = '[]'::jsonb WHERE id = $1`, id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "reset"})
}
