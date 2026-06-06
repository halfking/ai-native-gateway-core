package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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

func parseKeyActionRoute(remaining string) keyActionRoute {
	if remaining == "" {
		return keyActionRoute{kind: "root"}
	}

	switch remaining {
	case "verify", "budget-check", "apply":
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
	}

	idStr := route.idPart
	subPath := route.subPath

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
	case "detail":
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

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var appID int
	err := h.db.QueryRow(ctx, `SELECT id FROM applications WHERE code = $1 AND tenant_id = 'default'`, req.ApplicationCode).Scan(&appID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "application not found")
		return
	}

	// Check for existing available key (prevent duplicate)
	var existingID int
	var existingPrefix string
	err = h.db.QueryRow(ctx, `
		SELECT id, key_prefix FROM api_keys
		WHERE application_id = $1 AND tenant_id = 'default'
		  AND enabled = TRUE AND COALESCE(status, 'active') = 'active'
		  AND (expires_at IS NULL OR expires_at > now())
		ORDER BY id ASC LIMIT 1
	`, appID).Scan(&existingID, &existingPrefix)
	if err == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"该应用已有可用密钥 (id=%d, prefix=%s****)，请勿重复创建。如需新建，请先禁用或吊销现有密钥。",
			existingID, existingPrefix[:8],
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
		INSERT INTO api_keys (application_id, tenant_id, key_hash, key_prefix, key_ciphertext, owner_user, enabled, budget_usd, rate_limit_rpm, status, remark)
		VALUES ($1, 'default', $2, $3, $4, $5, TRUE, $6, $7, 'active', $8)
		RETURNING id
	`, appID, keyHash, keyPrefix, keyCiphertext, req.OwnerUser, req.BudgetUSD, req.RateLimitRPM, remark).Scan(&id)
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

func includeRevokedKeys(r *http.Request) bool {
	return strings.EqualFold(queryString(r, "include_revoked"), "true")
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	appCode := queryString(r, "application")
	includeRevoked := includeRevokedKeys(r)
	rows, err := h.db.Query(ctx, `
		SELECT ak.id, ak.key_prefix, ak.owner_user, ak.enabled,
		       ak.budget_usd::float8, ak.rate_limit_rpm,
		       app.code AS application_code,
		       ak.created_at, ak.last_used_at, ak.remark
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.tenant_id = 'default'
		  AND ($1 OR COALESCE(ak.status, 'active') <> 'revoked')
		  AND ($2 = '' OR app.code = $2)
		ORDER BY ak.id DESC
	`, includeRevoked, appCode)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type key struct {
		ID              int        `json:"id"`
		KeyPrefix       string     `json:"key_prefix"`
		OwnerUser       *string    `json:"owner_user"`
		Enabled         bool       `json:"enabled"`
		BudgetUSD       *float64   `json:"budget_usd"`
		RateLimitRPM    *int       `json:"rate_limit_rpm"`
		ApplicationCode string     `json:"application_code"`
		CreatedAt       *time.Time `json:"created_at"`
		LastUsedAt      *time.Time `json:"last_used_at"`
		Remark          *string    `json:"remark"`
	}
	keys := make([]key, 0)
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.ID, &k.KeyPrefix, &k.OwnerUser, &k.Enabled,
			&k.BudgetUSD, &k.RateLimitRPM, &k.ApplicationCode,
			&k.CreatedAt, &k.LastUsedAt, &k.Remark); err != nil {
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
		WHERE id = $1 AND tenant_id = 'default' AND COALESCE(status, 'active') <> 'revoked'
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
		WHERE id = $1 AND tenant_id = 'default' AND COALESCE(status, 'active') <> 'revoked'
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
	}
	var id, appID int
	var tenantID, appCode string
	var dcp, owner *string
	var rpm *int
	var budget *float64
	err := h.db.QueryRow(ctx, `
		SELECT ak.id, ak.tenant_id, ak.application_id, app.code,
		       app.default_client_profile, ak.owner_user,
		       ak.rate_limit_rpm, ak.budget_usd
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.key_hash = $1 AND ak.enabled = TRUE
		  AND COALESCE(ak.status, 'active') <> 'revoked'
		  AND (ak.expires_at IS NULL OR ak.expires_at > now())
	`, keyHash).Scan(&id, &tenantID, &appID, &appCode, &dcp, &owner, &rpm, &budget)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"api_key_id": req.APIKeyID,
		"budget_usd": budgetUSD,
		"spent_usd":  spent,
		"exceeded":   exceeded,
	})
}

func (h *Handler) getKeyDetail(w http.ResponseWriter, r *http.Request, id int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	includeRevoked := includeRevokedKeys(r)

	var k struct {
		ID              int      `json:"id"`
		KeyPrefix       string   `json:"key_prefix"`
		OwnerUser       *string  `json:"owner_user"`
		Enabled         bool     `json:"enabled"`
		BudgetUSD       *float64 `json:"budget_usd"`
		RateLimitRPM    *int     `json:"rate_limit_rpm"`
		ApplicationCode string   `json:"application_code"`
	}
	err := h.db.QueryRow(ctx, `
		SELECT ak.id, COALESCE(ak.key_prefix,''), ak.owner_user, ak.enabled,
		       ak.budget_usd::float8, ak.rate_limit_rpm, app.code
		FROM api_keys ak
		JOIN applications app ON app.id = ak.application_id
		WHERE ak.id = $1 AND ak.tenant_id = 'default'
		  AND ($2 OR COALESCE(ak.status, 'active') <> 'revoked')
	`, id, includeRevoked).Scan(&k.ID, &k.KeyPrefix, &k.OwnerUser, &k.Enabled,
		&k.BudgetUSD, &k.RateLimitRPM, &k.ApplicationCode)
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
