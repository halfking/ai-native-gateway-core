package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type routingHandler struct {
	db     *pgxpool.Pool
	secret string
}

func (h *Handler) logAudit(r *http.Request, action string, details map[string]any) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	detailsJSON, _ := json.Marshal(details)
	actor := r.Header.Get("X-Actor")
	if actor == "" {
		actor = "admin"
	}

	h.db.Exec(ctx, `
		INSERT INTO routing_audit_log (actor, action, details)
		VALUES ($1, $2, $3)
	`, actor, action, detailsJSON)
}

func (h *Handler) handleRoutingResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	model := queryString(r, "model")
	if model == "" {
		writeError(w, http.StatusBadRequest, "model parameter required")
		return
	}
	profile := queryString(r, "client_profile")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	type candidate struct {
		CredentialID      int     `json:"credential_id"`
		ProviderID        int     `json:"provider_id"`
		BaseURL           string  `json:"base_url"`
		Protocol          string  `json:"protocol"`
		Tier              int     `json:"tier"`
		Weight            int     `json:"weight"`
		RawModel          string  `json:"raw_model"`
		StandardizedName  string  `json:"standardized_name"`
		OutboundModel     string  `json:"outbound_model"`
		SuccessRate       float64 `json:"success_rate"`
		P95LatencyMs      int     `json:"p95_latency_ms"`
		ConcurrencyLimit  *int    `json:"concurrency_limit"`
		BalanceUSD        *float64 `json:"balance_usd"`
		CircuitState      string  `json:"circuit_state"`
		LifecycleStatus   string  `json:"lifecycle_status"`
		AvailabilityState string  `json:"availability_state"`
		QuotaState        string  `json:"quota_state"`
		RuntimeRoutable   bool    `json:"runtime_routable"`
		BlockReason       string  `json:"block_reason,omitempty"`
	}

	rawModels := []string{model}
	rows, err := h.db.Query(ctx, `
		SELECT
			c.id AS credential_id,
			p.id AS provider_id,
			p.base_url,
			COALESCE(p.protocol, 'openai-completions'),
			COALESCE(mo.routing_tier, 2)::int,
			COALESCE(mo.weight, 100)::int,
			mo.raw_model_name,
			mo.standardized_name,
			COALESCE(mo.outbound_model_name, mo.raw_model_name),
			COALESCE(mo.success_rate, 0.9)::float8,
			COALESCE(mo.p95_latency_ms, 9999)::int,
			c.concurrency_limit,
			c.balance_usd::float8,
			COALESCE(c.circuit_state, 'closed'),
			COALESCE(c.lifecycle_status, 'active'),
			COALESCE(c.availability_state, 'ready'),
			COALESCE(c.quota_state, 'ok')
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE p.tenant_id = 'default'
		  AND lower(mo.raw_model_name) = ANY($1)
		  AND mo.available IS TRUE
		  AND c.status IN ('active','cooling','degraded')
		  AND p.enabled IS TRUE
		ORDER BY COALESCE(mo.routing_tier, 2), COALESCE(mo.weight, 100) DESC, COALESCE(mo.success_rate, 0.9) DESC
	`, rawModels)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	candidates := make([]candidate, 0)
	for rows.Next() {
		var c candidate
		if err := rows.Scan(
			&c.CredentialID, &c.ProviderID, &c.BaseURL, &c.Protocol,
			&c.Tier, &c.Weight, &c.RawModel, &c.StandardizedName, &c.OutboundModel,
			&c.SuccessRate, &c.P95LatencyMs, &c.ConcurrencyLimit,
			&c.BalanceUSD, &c.CircuitState, &c.LifecycleStatus,
			&c.AvailabilityState, &c.QuotaState,
		); err != nil {
			continue
		}
		c.RuntimeRoutable = isRoutable(c)
		if !c.RuntimeRoutable {
			c.BlockReason = blockReason(c)
		}
		candidates = append(candidates, c)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"model":      model,
		"profile":    profile,
		"candidates": candidates,
	})
}

func isRoutable(c interface{}) bool {
	type routable interface {
		getLifecycleStatus() string
		getAvailabilityState() string
		getQuotaState() string
		getCircuitState() string
		getBalanceUSD() *float64
	}
	return true
}

func blockReason(c interface{}) string {
	return "state check"
}

func (h *Handler) handleRoutingOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			mo.raw_model_name,
			mo.outbound_model_name,
			c.id,
			p.id,
			p.display_name,
			COALESCE(mo.routing_tier, 2)::int,
			COALESCE(mo.success_rate, 0.9)::float8,
			COALESCE(c.circuit_state, 'closed'),
			c.status
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE p.tenant_id = 'default' AND mo.available IS TRUE
		ORDER BY mo.raw_model_name, COALESCE(mo.routing_tier, 2)
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type entry struct {
		RawModel      string  `json:"raw_model"`
		OutboundModel string  `json:"outbound_model"`
		CredentialID  int     `json:"credential_id"`
		ProviderID    int     `json:"provider_id"`
		ProviderName  string  `json:"provider_name"`
		Tier          int     `json:"tier"`
		SuccessRate   float64 `json:"success_rate"`
		CircuitState  string  `json:"circuit_state"`
		Status        string  `json:"status"`
	}
	entries := make([]entry, 0)
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.RawModel, &e.OutboundModel, &e.CredentialID,
			&e.ProviderID, &e.ProviderName, &e.Tier, &e.SuccessRate,
			&e.CircuitState, &e.Status); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": entries})
}

func (h *Handler) handleRoutingModelTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"families": []any{}})
}

func (h *Handler) handleRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if r.Method == http.MethodGet {
		var pol struct {
			AlgorithmVersion    int      `json:"algorithm_version"`
			RetryPerCredential  int      `json:"retry_per_credential"`
			TierFallbackMax     int      `json:"tier_fallback_max"`
			CircuitOpenSeconds  int      `json:"circuit_open_seconds"`
			CircuitFailureThreshold int `json:"circuit_failure_threshold"`
			CircuitMaxOpenSeconds int   `json:"circuit_max_open_seconds"`
			StickyTTLMilliseconds int   `json:"sticky_ttl_milliseconds"`
			FeaturedModels      []string `json:"featured_models"`
		}
		pol.AlgorithmVersion = 2
		pol.RetryPerCredential = 1
		pol.TierFallbackMax = 3
		pol.CircuitOpenSeconds = 300
		pol.CircuitFailureThreshold = 5
		pol.CircuitMaxOpenSeconds = 1800
		pol.StickyTTLMilliseconds = 1800

		err := h.db.QueryRow(ctx, `
			SELECT
				COALESCE(algorithm_version, 2)::int,
				COALESCE(retry_per_credential, 1)::int,
				COALESCE(tier_fallback_max, 3)::int,
				COALESCE(circuit_open_seconds, 300)::int,
				COALESCE(circuit_failure_threshold, 5)::int,
				COALESCE(circuit_max_open_seconds, 1800)::int,
				COALESCE(sticky_ttl_seconds, 1800)::int * 1000,
				COALESCE(featured_models, '[]'::jsonb)
			FROM routing_policy WHERE tenant_id = 'default' ORDER BY id LIMIT 1
		`).Scan(
			&pol.AlgorithmVersion, &pol.RetryPerCredential, &pol.TierFallbackMax,
			&pol.CircuitOpenSeconds, &pol.CircuitFailureThreshold,
			&pol.CircuitMaxOpenSeconds, &pol.StickyTTLMilliseconds,
			&pol.FeaturedModels,
		)
		if err != nil {
			pol.FeaturedModels = []string{}
		}
		writeJSON(w, http.StatusOK, pol)
		return
	}

	if r.Method == http.MethodPatch {
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		fields := map[string]any{}
		if v, ok := patch["algorithm_version"]; ok {
			fields["algorithm_version"] = v
		}
		if v, ok := patch["retry_per_credential"]; ok {
			fields["retry_per_credential"] = v
		}
		if v, ok := patch["tier_fallback_max"]; ok {
			fields["tier_fallback_max"] = v
		}
		if v, ok := patch["circuit_open_seconds"]; ok {
			fields["circuit_open_seconds"] = v
		}
		if v, ok := patch["circuit_failure_threshold"]; ok {
			fields["circuit_failure_threshold"] = v
		}
		if v, ok := patch["circuit_max_open_seconds"]; ok {
			fields["circuit_max_open_seconds"] = v
		}
		if v, ok := patch["actor"]; ok {
			fields["actor"] = v
		}

		if len(fields) > 0 {
			setClauses := ""
			args := []any{}
			i := 1
			for k, v := range fields {
				if setClauses != "" {
					setClauses += ", "
				}
				setClauses += k + " = $" + strconv.Itoa(i)
				args = append(args, v)
				i++
			}
			q := "UPDATE routing_policy SET " + setClauses + " WHERE tenant_id = 'default'"
			if _, err := h.db.Exec(ctx, q, args...); err != nil {
				writeError(w, http.StatusInternalServerError, "update failed")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) handleRoutingFeatured(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if r.Method == http.MethodGet {
		var models []string
		err := h.db.QueryRow(ctx, `
			SELECT COALESCE(featured_models, '[]'::jsonb)
			FROM routing_policy WHERE tenant_id = 'default' ORDER BY id LIMIT 1
		`).Scan(&models)
		if err != nil {
			models = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"featured_models": models})
		return
	}

	if r.Method == http.MethodPatch {
		var patch struct {
			FeaturedModels []string `json:"featured_models"`
			Actor          string   `json:"actor"`
		}
		if err := readJSON(r, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		modelsJSON, _ := json.Marshal(patch.FeaturedModels)
		_, err := h.db.Exec(ctx, `
			UPDATE routing_policy SET featured_models = $1 WHERE tenant_id = 'default'
		`, modelsJSON)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) handleRoutingAvailableModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"families": []any{}, "total_raw": 0})
}

func (h *Handler) handleRoutingAvailableModelsRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT DISTINCT mo.raw_model_name
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE mo.available = TRUE AND c.status = 'active' AND p.enabled = TRUE
		ORDER BY mo.raw_model_name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		names = append(names, name)
	}
	writeJSON(w, http.StatusOK, names)
}

func (h *Handler) handleRoutingDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sinceMin := queryInt(r, "since_minutes", 60)
	limit := queryInt(r, "limit", 100)
	if limit > 1000 {
		limit = 1000
	}
	model := queryString(r, "model")

	q := `
		SELECT ts, request_id, model, chosen_credential_id, chosen_provider_id,
		       tier, candidates_tried, latency_ms, success, error_class,
		       prompt_tokens, completion_tokens, cost_usd, client_model,
		       outbound_model, sticky_hit, client_profile, request_mode
		FROM routing_decision_log
		WHERE ts > now() - interval '` + strconv.Itoa(sinceMin) + ` minutes'
	`
	args := []any{}
	i := 1
	if model != "" {
		q += ` AND (model = $` + strconv.Itoa(i) + ` OR client_model = $` + strconv.Itoa(i) + `)`
		args = append(args, model)
		i++
	}
	q += ` ORDER BY ts DESC LIMIT $` + strconv.Itoa(i)
	args = append(args, limit)

	rows, err := h.db.Query(ctx, q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	decisions := make([]map[string]any, 0)
	for rows.Next() {
		var ts time.Time
		var reqID, mdl string
		var credID, provID *int
		var tier *int
		var tried int
		var latency int
		var success bool
		var errClass *string
		var pTok, cTok *int
		var cost *float64
		var clientModel, outModel *string
		var stickyHit *bool
		var profile, reqMode *string
		if err := rows.Scan(&ts, &reqID, &mdl, &credID, &provID, &tier, &tried,
			&latency, &success, &errClass, &pTok, &cTok, &cost, &clientModel,
			&outModel, &stickyHit, &profile, &reqMode); err != nil {
			continue
		}
		decisions = append(decisions, map[string]any{
			"ts": ts, "request_id": reqID, "model": mdl,
			"chosen_credential_id": credID, "chosen_provider_id": provID,
			"tier": tier, "candidates_tried": tried, "latency_ms": latency,
			"success": success, "error_class": errClass,
			"prompt_tokens": pTok, "completion_tokens": cTok, "cost_usd": cost,
			"client_model": clientModel, "outbound_model": outModel,
			"sticky_hit": stickyHit, "client_profile": profile, "request_mode": reqMode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"decisions": decisions})
}

func (h *Handler) handleRoutingHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT c.id, c.provider_id, p.display_name,
		       COALESCE(c.circuit_state, 'closed'),
		       COALESCE(c.consecutive_failures, 0),
		       c.cooling_until
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE p.tenant_id = 'default' AND c.status != 'disabled'
		ORDER BY c.id
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type credHealth struct {
		CredentialID       int     `json:"credential_id"`
		ProviderID         int     `json:"provider_id"`
		ProviderName       string  `json:"provider_name"`
		CircuitState       string  `json:"circuit_state"`
		ConsecutiveFailures int   `json:"consecutive_failures"`
		CoolingUntil       *time.Time `json:"cooling_until,omitempty"`
	}
	creds := make([]credHealth, 0)
	openCount := 0
	for rows.Next() {
		var c credHealth
		if err := rows.Scan(&c.CredentialID, &c.ProviderID, &c.ProviderName,
			&c.CircuitState, &c.ConsecutiveFailures, &c.CoolingUntil); err != nil {
			continue
		}
		if c.CircuitState == "open" {
			openCount++
		}
		creds = append(creds, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credentials":  creds,
		"total":        len(creds),
		"open":         openCount,
		"closed":       len(creds) - openCount,
	})
}

func (h *Handler) handleRoutingAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	limit := queryInt(r, "limit", 50)
	if limit > 500 {
		limit = 500
	}

	rows, err := h.db.Query(ctx, `
		SELECT ts, COALESCE(actor,''), COALESCE(action,''), COALESCE(details::text,'{}')
		FROM routing_audit_log
		ORDER BY ts DESC LIMIT $1
	`, limit)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"audits": []any{}, "error": err.Error()})
		return
	}
	defer rows.Close()

	audits := make([]map[string]any, 0)
	for rows.Next() {
		var ts time.Time
		var actor, action string
		var detailsStr string
		if err := rows.Scan(&ts, &actor, &action, &detailsStr); err != nil {
			continue
		}
		var d any
		json.Unmarshal([]byte(detailsStr), &d)
		audits = append(audits, map[string]any{
			"ts": ts, "actor": actor, "action": action, "details": d,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"audits": audits})
}

func (h *Handler) handleRoutingProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Model         string       `json:"model"`
		Messages      []map[string]any `json:"messages"`
		MaxTokens     int          `json:"max_tokens"`
		ClientProfile *string      `json:"client_profile"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model required")
		return
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 20
	}
	if len(req.Messages) == 0 {
		req.Messages = []map[string]any{{"role": "user", "content": "Hello, please reply with one word: OK"}}
	}
	clientProfile := "roocode"
	if req.ClientProfile != nil {
		clientProfile = *req.ClientProfile
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT c.id, c.provider_id, p.base_url, COALESCE(p.protocol,'openai'),
		       c.secret_ciphertext, COALESCE(mo.raw_model_name, $1), COALESCE(mo.outbound_model_name, $1)
		FROM model_offers mo
		JOIN model_aliases ma ON lower(ma.raw_name) = lower($1)
		JOIN models_canonical mc ON mc.id = ma.canonical_id AND mo.canonical_id = mc.id
		JOIN credentials c ON c.id = mo.credential_id AND c.status = 'active'
		JOIN providers p ON p.id = c.provider_id AND p.enabled = TRUE
		WHERE mo.available = TRUE
		  AND COALESCE(c.lifecycle_status,'active') = 'active'
		  AND COALESCE(c.availability_state,'ready') = 'ready'
		ORDER BY mo.priority NULLS LAST LIMIT 1
	`, req.Model)
	if err != nil || !rows.Next() {
		writeError(w, http.StatusServiceUnavailable, "no available provider for model "+req.Model)
		return
	}

	var credID, provID int
	var baseURL, protocol, rawModel, outModel string
	var ciphertext []byte
	rows.Scan(&credID, &provID, &baseURL, &protocol, &ciphertext, &rawModel, &outModel)
	rows.Close()

	_, decErr := h.decryptCredStr(string(ciphertext))
	if decErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to decrypt credential")
		return
	}

	var provName string
	h.db.QueryRow(ctx, `SELECT COALESCE(display_name,'') FROM providers WHERE id = $1`, provID).Scan(&provName)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"provider_id":   provID,
		"provider_name": provName,
		"credential_id": credID,
		"model":         req.Model,
		"raw_model":     rawModel,
		"outbound_model": outModel,
		"client_profile": clientProfile,
		"message":       "probe resolved provider (actual call not implemented)",
	})
}

func (h *Handler) handleRoutingManualPriority(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		CredentialID   int    `json:"credential_id"`
		ModelName      string `json:"model_name"`
		ManualPriority int    `json:"manual_priority"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ManualPriority < 1 || req.ManualPriority > 99 {
		writeError(w, http.StatusBadRequest, "manual_priority must be between 1 and 99")
		return
	}
	if req.CredentialID == 0 || req.ModelName == "" {
		writeError(w, http.StatusBadRequest, "credential_id and model_name required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE model_offers SET manual_priority = $1
		WHERE credential_id = $2 AND lower(raw_model_name) = lower($3)
	`, req.ManualPriority, req.CredentialID, req.ModelName)
	if err != nil {
		slog.Error("manual-priority update failed", "error", err, "cred_id", req.CredentialID, "model", req.ModelName)
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "model offer not found")
		return
	}

	h.logAudit(r, "manual_priority_update", map[string]any{
		"credential_id":   req.CredentialID,
		"model_name":      req.ModelName,
		"manual_priority": req.ManualPriority,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleRoutingScoreDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	model := queryString(r, "model")
	if model == "" {
		writeError(w, http.StatusBadRequest, "model parameter required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	weights := h.getScoringWeights(ctx)

	sqlQuery := `
		SELECT
			c.id AS credential_id,
			p.id AS provider_id,
			p.display_name,
			COALESCE(mo.raw_model_name, '') AS raw_model,
			COALESCE(mo.manual_priority, 99)::int AS manual_priority,
			COALESCE(mo.unit_price_in_per_1m, 0)::float8 AS price_in,
			COALESCE(mo.unit_price_out_per_1m, 0)::float8 AS price_out,
			COALESCE(mo.active_sessions, 0)::int AS active_sessions,
			COALESCE(mo.consecutive_failures, 0)::int AS consecutive_failures,
			c.concurrency_limit,
			COALESCE(mo.currency, 'USD') AS currency,
			CASE
				WHEN mo.unit_price_in_per_1m = 0 AND mo.unit_price_out_per_1m = 0 THEN 0
				WHEN mo.unit_price_in_per_1m IS NULL AND mo.unit_price_out_per_1m IS NULL THEN 5.0
				ELSE COALESCE(mo.unit_price_in_per_1m, 0) + COALESCE(mo.unit_price_out_per_1m, 0)
			END AS blended_cost
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE p.tenant_id = 'default'
		  AND lower(mo.raw_model_name) = lower($1)
		  AND mo.available IS TRUE
		ORDER BY mo.manual_priority NULLS LAST
	`
	slog.Info("score-details executing SQL", "sql", sqlQuery, "model", model)
	rows, err := h.db.Query(ctx, sqlQuery, model)
	if err != nil {
		slog.Error("score-details query failed", "error", err, "model", model)
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type scoreDetail struct {
		CredentialID       int     `json:"credential_id"`
		ProviderID         int     `json:"provider_id"`
		ProviderName       string  `json:"provider_name"`
		RawModel           string  `json:"raw_model"`
		ManualPriority     int     `json:"manual_priority"`
		PriceIn            float64 `json:"price_in"`
		PriceOut           float64 `json:"price_out"`
		BlendedCost        float64 `json:"blended_cost"`
		ActiveSessions     int     `json:"active_sessions"`
		ConsecutiveFailures int    `json:"consecutive_failures"`
		ConcurrencyLimit   *int    `json:"concurrency_limit"`
		Currency           string  `json:"currency"`
		NormalizedCost     float64 `json:"normalized_cost"`
		SessionLoad        float64 `json:"session_load"`
		CompositeScore     float64 `json:"composite_score"`
	}

	details := make([]scoreDetail, 0)
	for rows.Next() {
		var d scoreDetail
		if err := rows.Scan(
			&d.CredentialID, &d.ProviderID, &d.ProviderName, &d.RawModel,
			&d.ManualPriority, &d.PriceIn, &d.PriceOut,
			&d.ActiveSessions, &d.ConsecutiveFailures, &d.ConcurrencyLimit,
			&d.Currency, &d.BlendedCost,
		); err != nil {
			continue
		}

		var defaultPrice float64
		if d.Currency == "CNY" {
			defaultPrice = weights["default_price_cny"]
		} else {
			defaultPrice = weights["default_price_usd"]
		}
		if defaultPrice <= 0 {
			defaultPrice = 5.0
		}

		if d.BlendedCost == 0 {
			d.NormalizedCost = 0
		} else {
			d.NormalizedCost = d.BlendedCost / defaultPrice
		}

		if d.ConcurrencyLimit != nil && *d.ConcurrencyLimit > 0 {
			d.SessionLoad = float64(d.ActiveSessions) / float64(*d.ConcurrencyLimit)
			if d.SessionLoad > 1 {
				d.SessionLoad = 1
			}
		}

		d.CompositeScore = float64(d.ManualPriority) + d.NormalizedCost*weights["price"] +
			d.SessionLoad*weights["session_load"] + float64(d.ConsecutiveFailures)*weights["failure_penalty"]

		details = append(details, d)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"model":     model,
		"weights":   weights,
		"candidates": details,
	})
}

func (h *Handler) handleRoutingScoringWeights(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if r.Method == http.MethodGet {
		weights := h.getScoringWeights(ctx)
		writeJSON(w, http.StatusOK, weights)
		return
	}

	if r.Method == http.MethodPatch {
		var patch map[string]float64
		if err := readJSON(r, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}

		current := h.getScoringWeights(ctx)
		if v, ok := patch["price"]; ok {
			current["price"] = v
		}
		if v, ok := patch["session_load"]; ok {
			current["session_load"] = v
		}
		if v, ok := patch["failure_penalty"]; ok {
			current["failure_penalty"] = v
		}
		if v, ok := patch["default_price_cny"]; ok {
			current["default_price_cny"] = v
		}
		if v, ok := patch["default_price_usd"]; ok {
			current["default_price_usd"] = v
		}

		weightsJSON, _ := json.Marshal(current)
		_, err := h.db.Exec(ctx, `
			UPDATE routing_policy SET scoring_weights_json = $1 WHERE tenant_id = 'default'
		`, weightsJSON)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}

		auditMap := make(map[string]any)
		for k, v := range current {
			auditMap[k] = v
		}
		h.logAudit(r, "scoring_weights_update", auditMap)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) getScoringWeights(ctx context.Context) map[string]float64 {
	defaultWeights := map[string]float64{
		"price":           10,
		"session_load":    5,
		"failure_penalty": 20,
		"default_price_cny": 5.0,
		"default_price_usd": 5.0,
	}

	var weightsJSON []byte
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(scoring_weights_json, '{}'::jsonb)
		FROM routing_policy WHERE tenant_id = 'default' ORDER BY id LIMIT 1
	`).Scan(&weightsJSON)
	if err != nil || len(weightsJSON) == 0 {
		return defaultWeights
	}

	var weights map[string]float64
	if err := json.Unmarshal(weightsJSON, &weights); err != nil {
		return defaultWeights
	}

	for k, v := range defaultWeights {
		if _, ok := weights[k]; !ok {
			weights[k] = v
		}
	}
	return weights
}

func (h *Handler) handleRoutingFeaturedModelsDynamic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT model, COUNT(*) as cnt
		FROM routing_decision_log
		WHERE ts > NOW() - INTERVAL '7 days'
		  AND model IS NOT NULL AND model != ''
		GROUP BY model
		ORDER BY cnt DESC
		LIMIT 10
	`)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"models": []any{}})
		return
	}
	defer rows.Close()

	type featuredModel struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	models := make([]featuredModel, 0)
	for rows.Next() {
		var m featuredModel
		if err := rows.Scan(&m.Name, &m.Count); err != nil {
			continue
		}
		models = append(models, m)
	}

	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (h *Handler) handleFreePoolStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			p.catalog_code,
			p.display_name AS provider_name,
			c.id AS credential_id,
			c.label AS credential_label,
			c.status AS credential_status,
			c.availability_state,
			c.quota_state,
			COUNT(mo.id) AS total_offers,
			COUNT(mo.id) FILTER (WHERE mo.available) AS available_offers,
			COUNT(mo.id) FILTER (WHERE mo.unit_price_in_per_1m = 0 AND mo.unit_price_out_per_1m = 0) AS free_offers
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN model_offers mo ON mo.credential_id = c.id
		WHERE c.pool_group = 'free'
		GROUP BY p.catalog_code, p.display_name, c.id, c.label,
				 c.status, c.availability_state, c.quota_state
		ORDER BY p.display_name
	`)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"pool": []any{}, "error": err.Error()})
		return
	}
	defer rows.Close()

	type poolEntry struct {
		CatalogCode       string `json:"catalog_code"`
		ProviderName      string `json:"provider_name"`
		CredentialID      int    `json:"credential_id"`
		CredentialLabel   string `json:"credential_label"`
		CredentialStatus  string `json:"credential_status"`
		AvailabilityState string `json:"availability_state"`
		QuotaState        string `json:"quota_state"`
		TotalOffers       int    `json:"total_offers"`
		AvailableOffers   int    `json:"available_offers"`
		FreeOffers        int    `json:"free_offers"`
	}
	pool := make([]poolEntry, 0)
	for rows.Next() {
		var e poolEntry
		if err := rows.Scan(
			&e.CatalogCode, &e.ProviderName, &e.CredentialID,
			&e.CredentialLabel, &e.CredentialStatus, &e.AvailabilityState,
			&e.QuotaState, &e.TotalOffers, &e.AvailableOffers, &e.FreeOffers,
		); err != nil {
			continue
		}
		pool = append(pool, e)
	}

	var totalProviders, availableProviders, totalModels, freeModels int
	for _, e := range pool {
		totalProviders++
		if e.CredentialStatus == "active" {
			availableProviders++
		}
		totalModels += e.TotalOffers
		freeModels += e.FreeOffers
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pool": pool,
		"stats": map[string]int{
			"total_providers":    totalProviders,
			"available_providers": availableProviders,
			"total_models":       totalModels,
			"free_models":        freeModels,
		},
	})
}

func (h *Handler) handleFreePoolRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		CatalogCode string `json:"catalog_code"`
		DisplayName string `json:"display_name"`
		BaseURL     string `json:"base_url"`
		Protocol    string `json:"protocol"`
		APIKey      string `json:"api_key"`
		Models      []string `json:"models"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if req.CatalogCode == "" || req.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "catalog_code and base_url required")
		return
	}

	if req.DisplayName == "" {
		req.DisplayName = req.CatalogCode
	}
	if req.Protocol == "" {
		req.Protocol = "openai-completions"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Upsert provider (align with Python free_pool_manager)
	_, err := h.db.Exec(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, catalog_code, is_custom,
			kind, category, protocol, base_url, egress_profile, domestic, enabled)
		VALUES ('default', $1, $2, $1, false,
			'cloud', 'aggregator', $3, $4, 'direct', true, true)
		ON CONFLICT (tenant_id, code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			base_url = EXCLUDED.base_url,
			enabled = true
	`, req.CatalogCode, req.DisplayName, req.Protocol, req.BaseURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "insert provider failed")
		return
	}

	var providerID int
	h.db.QueryRow(ctx, `
		SELECT id FROM providers WHERE catalog_code = $1 AND tenant_id = 'default'
	`, req.CatalogCode).Scan(&providerID)

	credLabel := req.CatalogCode + "-free-key"
	if req.APIKey != "" {
		encrypted, encErr := h.encryptCred([]byte(req.APIKey))
		if encErr != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		var existingID int
		findErr := h.db.QueryRow(ctx, `
			SELECT id FROM credentials WHERE provider_id = $1 AND label = $2
		`, providerID, credLabel).Scan(&existingID)
		if findErr != nil {
			h.db.Exec(ctx, `
				INSERT INTO credentials (provider_id, tenant_id, label, secret_ciphertext,
					trust_level, status, lifecycle_status, availability_state, quota_state,
					pool_group)
				VALUES ($1, 'default', $2, $3, 'degraded', 'active',
					'active', 'ready', 'ok', 'free')
			`, providerID, credLabel, encrypted)
		} else {
			h.db.Exec(ctx, `
				UPDATE credentials SET
					secret_ciphertext = $3,
					status = 'active',
					pool_group = 'free',
					updated_at = NOW()
				WHERE provider_id = $1 AND label = $2
			`, providerID, credLabel, encrypted)
		}
	}

	// Insert model offers
	for _, model := range req.Models {
		h.db.Exec(ctx, `
			INSERT INTO model_offers (credential_id, raw_model_name, available,
				routing_tier, billing_mode, currency, unit_price_in_per_1m, unit_price_out_per_1m,
				pricing_source, pricing_updated_at)
			SELECT c.id, $1, true, 9, 'free', 'CNY', 0, 0, 'free_pool', NOW()
			FROM credentials c WHERE c.provider_id = $2 AND c.pool_group = 'free' LIMIT 1
			ON CONFLICT (credential_id, raw_model_name) DO NOTHING
		`, model, providerID)
	}

	h.logAudit(r, "free_pool_register", map[string]any{
		"catalog_code": req.CatalogCode,
		"base_url":     req.BaseURL,
		"models":       req.Models,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "registered",
		"provider_id": providerID,
	})
}
