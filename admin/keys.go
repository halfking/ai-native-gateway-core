package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type keyActionRoute struct {
	kind    string
	idPart  string
	subPath string
}

// keyConflictQuerier is the minimum surface area of *pgxpool.Pool needed by
// findActiveKeyConflict.  Defined as an interface so unit tests can supply a
// stub without spinning up Postgres.
type keyConflictQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// existingKeyConflict describes an api_keys row that already represents a
// "valid, active, non-expired" key for the (application, tenant, alias) group
// — i.e. one that the new key creation must refuse to duplicate.
type existingKeyConflict struct {
	ID       int
	Prefix   string
	IsSystem bool
}

// findActiveKeyConflict returns the lowest-id api_keys row matching the given
// (application_id, tenant_id, key_alias) group that is currently enabled,
// non-revoked, and not expired — *or* a system key regardless of the alias
// match.  Returns (nil, nil) when no conflict exists; only returns a non-nil
// error for unexpected DB failures (pgx.ErrNoRows is collapsed to "no
// conflict").
//
// The function is the single source of truth for the "is this createKey call
// going to duplicate an existing valid key?" check, and is intentionally
// conservative: any live system key in the same group blocks the creation
// even if its alias differs, because the frontend groups by (tenant, app,
// alias) and an attacker re-using an admin alias must not be able to slip
// past by giving an empty/different alias.
func findActiveKeyConflict(ctx context.Context, db keyConflictQuerier, appID int, tenantID, alias string) (*existingKeyConflict, error) {
	const sqlText = `
		SELECT id, key_prefix, COALESCE(is_system, FALSE)
		FROM api_keys
		WHERE application_id = $1
		  AND tenant_id = $2
		  AND COALESCE(key_alias, '') = COALESCE($3, '')
		  AND (
		      (enabled = TRUE
		        AND COALESCE(status, 'active') = 'active'
		        AND (expires_at IS NULL OR expires_at > now()))
		      OR (COALESCE(is_system, FALSE) = TRUE
		        AND enabled = TRUE
		        AND (expires_at IS NULL OR expires_at > now()))
		  )
		ORDER BY id ASC
		LIMIT 1
	`
	var c existingKeyConflict
	err := db.QueryRow(ctx, sqlText, appID, tenantID, alias).Scan(&c.ID, &c.Prefix, &c.IsSystem)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func parseKeyActionRoute(remaining string) keyActionRoute {
	if remaining == "" {
		return keyActionRoute{kind: "root"}
	}

	switch remaining {
	case "verify", "budget-check", "apply", "lookup":
		return keyActionRoute{kind: "action", subPath: remaining}
	}

	idPart := remaining
	subPath := ""
	for i, c := range remaining {
		if c == '/' {
			idPart = remaining[:i]
			subPath = remaining[i+1:]
			break
		}
	}

	return keyActionRoute{kind: "resource", idPart: idPart, subPath: subPath}
}

func (h *Handler) handleKeys(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	stripPrefix := "/api/keys/"
	remaining := r.URL.Path[len(stripPrefix):]
	route := parseKeyActionRoute(remaining)

	if route.kind == "root" {
		if r.Method == http.MethodPost {
			h.createKey(w, r)
		} else if r.Method == http.MethodGet {
			h.listKeys(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	switch route.subPath {
	case "verify":
		h.verifyKey(w, r)
		return
	case "budget-check":
		h.budgetCheck(w, r)
		return
	case "apply":
		if r.Method == http.MethodPost {
			h.adminApplyForKey(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	case "lookup":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.lookupKeyConflict(w, r)
		return
	}

	idStr := route.idPart
	subPath := route.subPath

	// Handle /api/keys/detail/{id} where idPart="detail" and subPath is the numeric ID
	if idStr == "detail" && subPath != "" {
		id, err := strconv.Atoi(subPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.getKeyDetail(w, r, id)
		return
	}

	switch subPath {
	case "reveal":
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.revealKey(w, r, id)
	case "approve":
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.approveKey(w, r, id)
	case "disable":
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.setKeyEnabled(w, r, id, false)
	case "enable":
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.setKeyEnabled(w, r, id, true)
	case "limits":
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		h.updateKeyLimits(w, r, id)
	case "detail":
		// idPart is "detail", actual ID is in subPath (e.g. /api/keys/detail/146)
		if subPath != "" {
			id, err := strconv.Atoi(subPath)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid id")
				return
			}
			h.getKeyDetail(w, r, id)
			return
		}
		rest := ""
		for i, c := range idStr {
			if c == '/' {
				rest = idStr[i+1:]
				idStr = idStr[:i]
				break
			}
		}
		if rest != "" {
			id, err := strconv.Atoi(rest)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid id")
				return
			}
			h.getKeyDetail(w, r, id)
		} else {
			http.NotFound(w, r)
		}
	case "applications":
		if idStr != "" && r.Method == http.MethodPatch {
			h.patchApplicationProfile(w, r, idStr)
		} else {
			http.NotFound(w, r)
		}
	default:
		if subPath == "" {
			id, err := strconv.Atoi(idStr)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid id")
				return
			}
			if r.Method == http.MethodDelete {
				h.deleteKey(w, r, id)
			} else if r.Method == http.MethodGet {
				h.getKeyDetail(w, r, id)
			} else if r.Method == http.MethodPatch {
				h.patchKey(w, r, id)
			} else {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		} else {
			http.NotFound(w, r)
		}
	}
}

func (h *Handler) handleKeysRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.createKey(w, r)
	} else if r.Method == http.MethodGet {
		h.listKeys(w, r)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApplicationCode string   `json:"application_code"`
		TenantID        string   `json:"tenant_id"`
		KeyAlias        string   `json:"key_alias"`
		OwnerUser       *string  `json:"owner_user"`
		BudgetUSD       *float64 `json:"budget_usd"`
		RateLimitRPM    *int     `json:"rate_limit_rpm"`
		Remark          *string  `json:"remark"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ApplicationCode == "" {
		req.ApplicationCode = "default"
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var appID int
	err := h.db.QueryRow(ctx, `SELECT id FROM applications WHERE code = $1 AND tenant_id = 'default'`, req.ApplicationCode).Scan(&appID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "application not found")
		return
	}

	alias := req.KeyAlias
	existing, err := findActiveKeyConflict(ctx, h.db, appID, req.TenantID, alias)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if existing != nil {
		prefix := existing.Prefix
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		if existing.IsSystem {
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"该租户+应用+别名组合已存在有效的系统密钥 (id=%d)，请勿重复创建。如需新建，请先禁用或吊销现有系统密钥。",
				existing.ID,
			))
			return
		}
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"该租户+应用+别名组合已有可用密钥 (id=%d, prefix=%s****)，请勿重复创建。如需新建，请先禁用或吊销现有密钥。",
			existing.ID, prefix,
		))
		return
	}

	remark := "admin manual creation"
	if req.Remark != nil && *req.Remark != "" {
		remark = *req.Remark
	}
	raw, keyHash, keyPrefix, keyCiphertext := h.generateAdminKey(h.secret)

	var id int
	err = h.db.QueryRow(ctx, `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, budget_usd, rate_limit_rpm, status, remark, key_alias)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE, $7, $8, 'active', $9, $10)
		RETURNING id
	`, appID, req.TenantID, keyHash, keyPrefix, keyCiphertext, req.OwnerUser, req.BudgetUSD, req.RateLimitRPM, remark, alias).Scan(&id)
	if err != nil {
		slog.Error("createKey insert failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         id,
		"api_key":    raw,
		"key_prefix": keyPrefix,
		"message":    "ok",
	})
}

// lookupKeyConflict answers GET /api/keys/lookup?application_code=&tenant_id=&key_alias=
// with the active (or system) key already occupying that group, if any.
// It is a read-only probe intended for the Keys management UI: the frontend
// calls it while the user is typing the (tenant, application, alias) tuple
// of a new key, so the user can see WHICH key is in the way before clicking
// "签发".  The route is mounted under admin() in handler.go, so the request
// must present a valid admin api key (Authorization: Bearer …) — there is no
// anonymous / unauthenticated path to this endpoint.
//
// Response shape:
//   - 200 + {conflict: null} when no row matches the guard
//   - 200 + {conflict: {id, key_prefix, is_system, ...}} when a row matches
//   - 400 when alias is empty or application_code is missing
//   - 404 when application_code is unknown
func (h *Handler) lookupKeyConflict(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	appCode, tenantID, alias, vErr := extractLookupParams(r.URL.Query())
	if vErr != nil {
		writeError(w, http.StatusBadRequest, vErr.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var appID int
	err := h.db.QueryRow(ctx,
		`SELECT id FROM applications WHERE code = $1 AND tenant_id = 'default'`,
		appCode,
	).Scan(&appID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		slog.Error("lookupKeyConflict: applications lookup failed", "error", err, "code", appCode)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	existing, err := findActiveKeyConflict(ctx, h.db, appID, tenantID, alias)
	if err != nil {
		slog.Error("lookupKeyConflict: findActiveKeyConflict failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if existing == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"conflict":         nil,
			"application_code": appCode,
			"tenant_id":        tenantID,
			"key_alias":        alias,
		})
		return
	}

	conflictResp := map[string]any{
		"id":         existing.ID,
		"key_prefix": existing.Prefix,
		"is_system":  existing.IsSystem,
		"status":     "active",
		"enabled":    true,
		"expires_at": nil,
		"owner_user": "",
	}

	var (
		status    *string
		enabled   *bool
		expiresAt *time.Time
		owner     *string
	)
	err = h.db.QueryRow(ctx, `
		SELECT COALESCE(status, 'active'), enabled, expires_at, owner_user
		FROM api_keys
		WHERE id = $1
	`, existing.ID).Scan(&status, &enabled, &expiresAt, &owner)
	if err != nil {
		slog.Warn("lookupKeyConflict: hydrate conflict row failed, using defaults", "error", err, "id", existing.ID)
	} else {
		conflictResp["status"] = derefStr(status)
		conflictResp["enabled"] = derefBool(enabled)
		conflictResp["expires_at"] = expiresAt
		conflictResp["owner_user"] = derefStr(owner)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conflict":         conflictResp,
		"application_code": appCode,
		"tenant_id":        tenantID,
		"key_alias":        alias,
	})
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefBool(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}

// extractLookupParams pulls (application_code, tenant_id, key_alias) out of a
// url.Values, normalises whitespace, fills in the default tenant, and
// surfaces a descriptive error when a required field is missing.
//
// Kept as a pure function so the validation contract is unit-testable
// without spinning up Postgres.
func extractLookupParams(q url.Values) (appCode, tenantID, alias string, err error) {
	appCode = strings.TrimSpace(q.Get("application_code"))
	tenantID = strings.TrimSpace(q.Get("tenant_id"))
	alias = strings.TrimSpace(q.Get("key_alias"))

	switch {
	case appCode == "":
		return "", "", "", errors.New("application_code is required")
	case alias == "":
		return "", "", "", errors.New("key_alias is required")
	}
	if tenantID == "" {
		tenantID = "default"
	}
	return appCode, tenantID, alias, nil
}

func includeRevokedKeys(r *http.Request) bool {
	return strings.EqualFold(queryString(r, "include_revoked"), "true")
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	appCode := queryString(r, "application")
	tenantFilter := queryString(r, "tenant")
	rows, err := h.db.Query(ctx, `
		SELECT ak.id, ak.key_prefix, ak.owner_user, ak.enabled,
		       COALESCE(ak.status, 'active') AS status,
		       ak.expires_at,
		       ak.budget_usd::float8, ak.rate_limit_rpm,
		       app.code AS application_code,
		       COALESCE(ak.is_system, FALSE) AS is_system,
		       COALESCE(ak.default_client_profile, app.default_client_profile),
		       ak.last_used_at, ak.remark,
		       ak.total_requests, ak.total_prompt_tokens, ak.total_completion_tokens,
		       COALESCE(ak.total_cost_usd, 0)::float8, ak.last_request_at,
		       ak.tenant_id, ak.key_alias
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ($1 = '' OR app.code = $1)
		  AND ($2 = '' OR ak.tenant_id = $2)
		ORDER BY ak.id DESC
	`, appCode, tenantFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type key struct {
		ID                   int        `json:"id"`
		KeyPrefix            string     `json:"key_prefix"`
		OwnerUser            *string    `json:"owner_user"`
		Enabled              bool       `json:"enabled"`
		Status               string     `json:"status"`
		ExpiresAt            *time.Time `json:"expires_at"`
		BudgetUSD            *float64   `json:"budget_usd"`
		RateLimitRPM         *int       `json:"rate_limit_rpm"`
		ApplicationCode      string     `json:"application_code"`
		IsSystem             bool       `json:"is_system"`
		DefaultClientProfile *string    `json:"default_client_profile"`
		LastUsedAt           *time.Time `json:"last_used_at"`
		Remark               *string    `json:"remark"`
		TotalRequests        int64      `json:"total_requests"`
		TotalPromptTokens    int64      `json:"total_prompt_tokens"`
		TotalCompletionTokens int64     `json:"total_completion_tokens"`
		TotalCostUSD         float64    `json:"total_cost_usd"`
		LastRequestAt        *time.Time `json:"last_request_at"`
		TenantID             string     `json:"tenant_id"`
		KeyAlias             *string    `json:"key_alias"`
	}
	keys := make([]key, 0)
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.OwnerUser, &k.Enabled,
			&k.Status, &k.ExpiresAt,
			&k.BudgetUSD, &k.RateLimitRPM, &k.ApplicationCode,
			&k.IsSystem, &k.DefaultClientProfile,
			&k.LastUsedAt, &k.Remark,
			&k.TotalRequests, &k.TotalPromptTokens, &k.TotalCompletionTokens,
			&k.TotalCostUSD, &k.LastRequestAt,
			&k.TenantID, &k.KeyAlias); err != nil {
			continue
		}
		keys = append(keys, k)
	}
	writeJSON(w, http.StatusOK, keys)
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	cmd, err := h.db.Exec(ctx, `
		UPDATE api_keys
		SET status = 'revoked', enabled = FALSE
		WHERE id = $1 AND COALESCE(status, 'active') <> 'revoked'
	`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if cmd.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "revoked"})
}

func (h *Handler) revealKey(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var ciphertext string
	err := h.db.QueryRow(ctx, `
		SELECT key_ciphertext FROM api_keys
		WHERE id = $1 AND COALESCE(status, 'active') <> 'revoked'
	`, id).Scan(&ciphertext)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	if ciphertext == "" {
		writeError(w, http.StatusNotFound, "no ciphertext stored")
		return
	}

	plaintext, err := h.decryptCredStr(ciphertext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decryption failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"api_key": plaintext})
}

func (h *Handler) approveKey(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.db.Exec(ctx, `UPDATE api_keys SET enabled = TRUE WHERE id = $1`, id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "approved"})
}

func (h *Handler) setKeyEnabled(w http.ResponseWriter, r *http.Request, id int, enabled bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.db.Exec(ctx, `UPDATE api_keys SET enabled = $1 WHERE id = $2`, enabled, id)
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *Handler) updateKeyLimits(w http.ResponseWriter, r *http.Request, id int) {
	var body struct {
		RateLimitRPM        *int `json:"rate_limit_rpm"`
		RateLimitConcurrent *int `json:"rate_limit_concurrent"`
		RateLimitTPM        *int `json:"rate_limit_tpm"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var exists bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM api_keys WHERE id = $1)`, id).Scan(&exists); err != nil {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "api_key not found")
		return
	}

	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	argIdx := 1
	if body.RateLimitRPM != nil {
		sets = append(sets, fmt.Sprintf("rate_limit_rpm = $%d", argIdx))
		args = append(args, *body.RateLimitRPM)
		argIdx++
	}
	if body.RateLimitConcurrent != nil {
		sets = append(sets, fmt.Sprintf("rate_limit_concurrent = $%d", argIdx))
		args = append(args, *body.RateLimitConcurrent)
		argIdx++
	}
	if body.RateLimitTPM != nil {
		sets = append(sets, fmt.Sprintf("rate_limit_tpm = $%d", argIdx))
		args = append(args, *body.RateLimitTPM)
		argIdx++
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, id)
	if _, err := h.db.Exec(ctx,
		fmt.Sprintf("UPDATE api_keys SET %s WHERE id = $%d", strings.Join(sets, ", "), argIdx),
		args...,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}

	resp := map[string]any{"status": "ok", "id": id}
	if body.RateLimitRPM != nil {
		resp["rate_limit_rpm"] = *body.RateLimitRPM
	}
	if body.RateLimitConcurrent != nil {
		resp["rate_limit_concurrent"] = *body.RateLimitConcurrent
	}
	if body.RateLimitTPM != nil {
		resp["rate_limit_tpm"] = *body.RateLimitTPM
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) verifyKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	keyHash := hashAPIKey(h.secret, req.APIKey)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var result struct {
		Valid                bool     `json:"valid"`
		KeyID                *int     `json:"key_id,omitempty"`
		TenantID             *string  `json:"tenant_id,omitempty"`
		ApplicationID        *int     `json:"application_id,omitempty"`
		ApplicationCode      *string  `json:"application_code,omitempty"`
		DefaultClientProfile *string  `json:"default_client_profile,omitempty"`
		OwnerUser            *string  `json:"owner_user,omitempty"`
		RateLimitRPM         *int     `json:"rate_limit_rpm,omitempty"`
		BudgetUSD            *float64 `json:"budget_usd,omitempty"`
		KeyAlias             *string  `json:"key_alias,omitempty"`
	}
	var id, appID int
	var tenantID, appCode string
	var dcp, owner, keyAlias *string
	var rpm *int
	var budget *float64
	err := h.db.QueryRow(ctx, `
		SELECT ak.id, ak.tenant_id, ak.application_id, app.code,
		       COALESCE(ak.default_client_profile, app.default_client_profile), ak.owner_user,
		       ak.rate_limit_rpm, ak.budget_usd, ak.key_alias
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.key_hash = $1 AND ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') <> 'revoked'
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
	`, keyHash).Scan(&id, &tenantID, &appID, &appCode, &dcp, &owner, &rpm, &budget, &keyAlias)
	if err == nil {
		result.Valid = true
		result.KeyID = &id
		result.TenantID = &tenantID
		result.ApplicationID = &appID
		result.ApplicationCode = &appCode
		result.DefaultClientProfile = dcp
		result.OwnerUser = owner
		result.RateLimitRPM = rpm
		result.BudgetUSD = budget
		result.KeyAlias = keyAlias
		h.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, id)
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) budgetCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKeyID int `json:"api_key_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var budgetUSD *float64
	err := h.db.QueryRow(ctx, `
		SELECT budget_usd FROM api_keys
		WHERE id = $1 AND tenant_id = 'default' AND COALESCE(status, 'active') <> 'revoked'
	`, req.APIKeyID).Scan(&budgetUSD)
	if err != nil {
		writeError(w, http.StatusNotFound, "api_key not found")
		return
	}

	var spent float64
	h.db.QueryRow(ctx, `SELECT COALESCE(SUM(cost_usd), 0) FROM usage_ledger WHERE api_key_id = $1`, req.APIKeyID).Scan(&spent)

	exceeded := budgetUSD != nil && spent >= *budgetUSD
	var remainingUSD *float64
	if budgetUSD != nil {
		r := *budgetUSD - spent
		remainingUSD = &r
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"api_key_id":    req.APIKeyID,
		"budget_usd":    budgetUSD,
		"spent_usd":     spent,
		"remaining_usd": remainingUSD,
		"exceeded":      exceeded,
	})
}

func (h *Handler) getKeyDetail(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	includeRevoked := includeRevokedKeys(r)

	var k struct {
		ID                   int      `json:"id"`
		KeyPrefix            string   `json:"key_prefix"`
		OwnerUser            *string  `json:"owner_user"`
		Enabled              bool     `json:"enabled"`
		BudgetUSD            *float64 `json:"budget_usd"`
		RateLimitRPM         *int     `json:"rate_limit_rpm"`
		RateLimitConcurrent  *int     `json:"rate_limit_concurrent"`
		RateLimitTPM         *int     `json:"rate_limit_tpm"`
		ApplicationCode      string   `json:"application_code"`
		TenantID             string   `json:"tenant_id"`
		KeyAlias             *string  `json:"key_alias"`
	}
	err := h.db.QueryRow(ctx, `
		SELECT ak.id, COALESCE(ak.key_prefix,''), ak.owner_user, ak.enabled,
		       ak.budget_usd::float8, ak.rate_limit_rpm,
		       ak.rate_limit_concurrent, ak.rate_limit_tpm,
		       app.code, ak.tenant_id, ak.key_alias
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.id = $1
		  AND ($2 OR COALESCE(ak.status, 'active') <> 'revoked')
	`, id, includeRevoked).Scan(&k.ID, &k.KeyPrefix, &k.OwnerUser, &k.Enabled,
		&k.BudgetUSD, &k.RateLimitRPM,
		&k.RateLimitConcurrent, &k.RateLimitTPM,
		&k.ApplicationCode, &k.TenantID, &k.KeyAlias)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, k)
}

func (h *Handler) handleKeyApplicationsList(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.listKeyApplications(w, r)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleKeyApplications(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	remaining := r.URL.Path[len("/api/key-applications/"):]
	if remaining == "" {
		if r.Method == http.MethodGet {
			h.listKeyApplications(w, r)
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
	appID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid application id")
		return
	}

	switch subPath {
	case "approve":
		if r.Method == http.MethodPost {
			h.approveKeyApplication(w, r, appID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "reject":
		if r.Method == http.MethodPost {
			h.rejectKeyApplication(w, r, appID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		if r.Method == http.MethodGet {
			h.getKeyApplication(w, r, appID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func (h *Handler) listKeyApplications(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	statusFilter := queryString(r, "status")
	var rows pgx.Rows
	var err error
	if statusFilter != "" {
		rows, err = h.db.Query(ctx, `
			SELECT id::text, client_ip::text, COALESCE(contact,''), COALESCE(purpose,''),
			       status, issued_key_id, COALESCE(admin_notes,''), COALESCE(reviewed_by,''),
			       reviewed_at, created_at, expires_at
			FROM key_applications WHERE status = $1
			ORDER BY created_at DESC LIMIT 500
		`, statusFilter)
	} else {
		rows, err = h.db.Query(ctx, `
			SELECT id::text, client_ip::text, COALESCE(contact,''), COALESCE(purpose,''),
			       status, issued_key_id, COALESCE(admin_notes,''), COALESCE(reviewed_by,''),
			       reviewed_at, created_at, expires_at
			FROM key_applications
			ORDER BY created_at DESC LIMIT 500
		`)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type app struct {
		ID          string     `json:"id"`
		ClientIP    string     `json:"client_ip"`
		Contact     string     `json:"contact"`
		Purpose     string     `json:"purpose"`
		Status      string     `json:"status"`
		IssuedKeyID *int       `json:"issued_key_id"`
		AdminNotes  string     `json:"admin_notes"`
		ReviewedBy  string     `json:"reviewed_by"`
		ReviewedAt  *time.Time `json:"reviewed_at"`
		CreatedAt   *time.Time `json:"created_at"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	apps := make([]app, 0)
	for rows.Next() {
		var a app
		if err := rows.Scan(&a.ID, &a.ClientIP, &a.Contact, &a.Purpose,
			&a.Status, &a.IssuedKeyID, &a.AdminNotes, &a.ReviewedBy,
			&a.ReviewedAt, &a.CreatedAt, &a.ExpiresAt); err != nil {
			continue
		}
		apps = append(apps, a)
	}
	writeJSON(w, http.StatusOK, apps)
}

func (h *Handler) getKeyApplication(w http.ResponseWriter, r *http.Request, appID uuid.UUID) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var a struct {
		ID          string     `json:"id"`
		ClientIP    string     `json:"client_ip"`
		Contact     string     `json:"contact"`
		Purpose     string     `json:"purpose"`
		Status      string     `json:"status"`
		IssuedKeyID *int       `json:"issued_key_id"`
		AdminNotes  string     `json:"admin_notes"`
		ReviewedBy  string     `json:"reviewed_by"`
		ReviewedAt  *time.Time `json:"reviewed_at"`
		CreatedAt   *time.Time `json:"created_at"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}
	err := h.db.QueryRow(ctx, `
		SELECT id::text, client_ip::text, COALESCE(contact,''), COALESCE(purpose,''),
		       status, issued_key_id, COALESCE(admin_notes,''), COALESCE(reviewed_by,''),
		       reviewed_at, created_at, expires_at
		FROM key_applications WHERE id = $1
	`, appID.String()).Scan(&a.ID, &a.ClientIP, &a.Contact, &a.Purpose,
		&a.Status, &a.IssuedKeyID, &a.AdminNotes, &a.ReviewedBy,
		&a.ReviewedAt, &a.CreatedAt, &a.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *Handler) approveKeyApplication(w http.ResponseWriter, r *http.Request, appID uuid.UUID) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var status, contact string
	err := h.db.QueryRow(ctx, `SELECT status, COALESCE(contact,'') FROM key_applications WHERE id = $1::uuid`, appID.String()).Scan(&status, &contact)
	if err != nil {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "can only approve 'pending' applications, current: "+status)
		return
	}

	var dbAppID int
	err = h.db.QueryRow(ctx, `SELECT id FROM applications WHERE code = 'applicant' AND tenant_id = 'default'`).Scan(&dbAppID)
	if err != nil {
		err = h.db.QueryRow(ctx, `
			INSERT INTO applications (tenant_id, code, display_name, owner_user)
			VALUES ('default', 'applicant', '自助申请', 'admin') RETURNING id
		`).Scan(&dbAppID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create applicant application")
			return
		}
	}

	var req struct {
		AdminNotes *string `json:"admin_notes"`
		ReviewedBy *string `json:"reviewed_by"`
	}
	readJSON(r, &req)
	notes := ""
	if req.AdminNotes != nil {
		notes = *req.AdminNotes
	}
	reviewer := "admin"
	if req.ReviewedBy != nil {
		reviewer = *req.ReviewedBy
	}
	noteSummary := notes
	if len(noteSummary) > 200 {
		noteSummary = noteSummary[:200]
	}
	remark := fmt.Sprintf("approved by %s: %s", reviewer, noteSummary)

	// Check for existing active key for this applicant (prevent duplicate)
	var existingKeyID int
	var existingPrefix string
	err = h.db.QueryRow(ctx, `
		SELECT id, key_prefix FROM api_keys
		WHERE application_id = $1 AND tenant_id = 'default'
		  AND enabled = TRUE AND COALESCE(status, 'active') = 'active'
		  AND (expires_at IS NULL OR expires_at > now())
		ORDER BY id ASC LIMIT 1
	`, dbAppID).Scan(&existingKeyID, &existingPrefix)
	if err == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"该申请人的应用已有可用密钥 (id=%d, prefix=%s****)，请勿重复签发。如需新建，请先禁用或吊销现有密钥。",
			existingKeyID, existingPrefix[:8],
		))
		return
	}

	_, keyHash, keyPrefix, keyCiphertext := h.generateAdminKey(h.secret)
	var keyID int
	err = h.db.QueryRow(ctx, `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, status, remark)
		VALUES ($1, 'default', $2, $3, $4, $5, TRUE, 'active', $6) RETURNING id
	`, dbAppID, keyHash, keyPrefix, keyCiphertext, contact, remark).Scan(&keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}

	h.db.Exec(ctx, `
		UPDATE key_applications
		SET status = 'approved', issued_key_id = $1, admin_notes = $2, reviewed_by = $3, reviewed_at = now(), updated_at = now()
		WHERE id = $4::uuid
	`, keyID, notes, reviewer, appID.String())

	writeJSON(w, http.StatusOK, map[string]any{
		"application_id": appID.String(),
		"status":         "approved",
		"key_id":         keyID,
		"key_prefix":     keyPrefix,
		"message":        fmt.Sprintf("Key issued (prefix=%s). Retrieve full key via GET /api/keys/%d/reveal", keyPrefix, keyID),
	})
}

func (h *Handler) rejectKeyApplication(w http.ResponseWriter, r *http.Request, appID uuid.UUID) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var status string
	err := h.db.QueryRow(ctx, `SELECT status FROM key_applications WHERE id = $1::uuid`, appID.String()).Scan(&status)
	if err != nil {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "can only reject 'pending' applications, current: "+status)
		return
	}

	var req struct {
		AdminNotes *string `json:"admin_notes"`
		ReviewedBy *string `json:"reviewed_by"`
	}
	readJSON(r, &req)
	notes := ""
	if req.AdminNotes != nil {
		notes = *req.AdminNotes
	}
	reviewer := "admin"
	if req.ReviewedBy != nil {
		reviewer = *req.ReviewedBy
	}

	h.db.Exec(ctx, `
		UPDATE key_applications
		SET status = 'rejected', admin_notes = $1, reviewed_by = $2, reviewed_at = now(), updated_at = now()
		WHERE id = $3::uuid
	`, notes, reviewer, appID.String())

	writeJSON(w, http.StatusOK, map[string]any{
		"application_id": appID.String(),
		"status":         "rejected",
		"message":        "Application rejected",
	})
}

func (h *Handler) adminApplyForKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ApplicationCode string  `json:"application_code"`
		OwnerUser       *string `json:"owner_user"`
		Description     *string `json:"description"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ApplicationCode == "" {
		req.ApplicationCode = "default"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var appID int
	err := h.db.QueryRow(ctx, `SELECT id FROM applications WHERE code = $1 AND tenant_id = 'default'`, req.ApplicationCode).Scan(&appID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "application not found")
		return
	}

	owner := "applicant"
	if req.OwnerUser != nil {
		owner = *req.OwnerUser
	}
	_, keyHash, keyPrefix, keyCiphertext := h.generateAdminKey(h.secret)
	var keyID int
	err = h.db.QueryRow(ctx, `
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, status)
		VALUES ($1, 'default', $2, $3, $4, $5, FALSE, 'pending') RETURNING id
	`, appID, keyHash, keyPrefix, keyCiphertext, owner).Scan(&keyID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create key")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":               keyID,
		"key_prefix":       keyPrefix,
		"application_code": req.ApplicationCode,
		"status":           "pending",
		"message":          "Key application submitted. Please wait for admin approval.",
	})
}

func (h *Handler) patchApplicationProfile(w http.ResponseWriter, r *http.Request, code string) {
	var req struct {
		DefaultClientProfile *string `json:"default_client_profile"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if req.DefaultClientProfile == nil || *req.DefaultClientProfile == "" {
		writeError(w, http.StatusBadRequest, "default_client_profile required")
		return
	}

	var id int
	var retCode, retProfile string
	err := h.db.QueryRow(ctx, `
		UPDATE applications
		SET default_client_profile = $1, updated_at = now()
		WHERE code = $2 AND tenant_id = 'default'
		RETURNING id, code, default_client_profile
	`, *req.DefaultClientProfile, code).Scan(&id, &retCode, &retProfile)
	if err != nil {
		writeError(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":                     id,
		"code":                   retCode,
		"default_client_profile": retProfile,
	})
}

func (h *Handler) patchKey(w http.ResponseWriter, r *http.Request, id int) {
	var req struct {
		DefaultClientProfile *string `json:"default_client_profile"`
		OwnerUser            *string `json:"owner_user"`
		Remark               *string `json:"remark"`
		KeyAlias             *string `json:"key_alias"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var sets []string
	var args []any
	argIdx := 1

	if req.DefaultClientProfile != nil {
		sets = append(sets, fmt.Sprintf("default_client_profile = $%d", argIdx))
		args = append(args, *req.DefaultClientProfile)
		argIdx++
	}
	if req.OwnerUser != nil {
		sets = append(sets, fmt.Sprintf("owner_user = $%d", argIdx))
		args = append(args, *req.OwnerUser)
		argIdx++
	}
	if req.Remark != nil {
		sets = append(sets, fmt.Sprintf("remark = $%d", argIdx))
		args = append(args, *req.Remark)
		argIdx++
	}
	if req.KeyAlias != nil {
		sets = append(sets, fmt.Sprintf("key_alias = $%d", argIdx))
		args = append(args, *req.KeyAlias)
		argIdx++
	}

	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}

	args = append(args, id)
	idIdx := len(args)

	query := fmt.Sprintf(
		"UPDATE api_keys SET %s WHERE id = $%d",
		strings.Join(sets, ", "), idIdx,
	)

	cmd, err := h.db.Exec(ctx, query, args...)
	if err != nil {
		slog.Error("patchKey SQL failed", "query", query, "args", args, "error", err)
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if cmd.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

