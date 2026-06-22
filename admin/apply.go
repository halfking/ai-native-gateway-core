package admin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

func (h *Handler) handleV1KeysApply(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.v1ApplyForkey(w, r)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) handleV1KeysApplyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.v1GetApplicationStatus(w, r)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) v1ApplyForkey(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	var req struct {
		Contact string  `json:"contact"`
		Purpose *string `json:"purpose"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Contact == "" {
		writeError(w, http.StatusBadRequest, "contact is required")
		return
	}

	forwardedFor := r.Header.Get("X-Forwarded-For")
	clientIP := "unknown"
	if forwardedFor != "" {
		for i := 0; i < len(forwardedFor); i++ {
			if forwardedFor[i] == ',' {
				clientIP = forwardedFor[:i]
				break
			}
		}
		if clientIP == "unknown" || clientIP == forwardedFor {
			clientIP = forwardedFor
		}
	}
	userAgent := r.Header.Get("User-Agent")
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	acceptLang := r.Header.Get("Accept-Language")
	if len(acceptLang) > 64 {
		acceptLang = acceptLang[:64]
	}

	fp := fmt.Sprintf("%x", sha256.Sum256([]byte(clientIP+":"+userAgent+":"+acceptLang+":"+req.Contact)))

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var existingID string
	var existingStatus string
	var existingKeyID *int
	err := h.db.QueryRow(ctx, `
		SELECT id::text, status, issued_key_id
		FROM key_applications
		WHERE fingerprint = $1 AND status IN ('pending', 'approved')
		ORDER BY created_at DESC LIMIT 1
	`, fp).Scan(&existingID, &existingStatus, &existingKeyID)
	if err == nil {
		resp := map[string]any{
			"application_id": existingID,
			"status":         existingStatus,
			"poll_url":       "/v1/keys/apply/" + existingID,
		}
		if existingStatus == "pending" {
			resp["message"] = "您的申请已提交，等待管理员审核"
		} else if existingStatus == "approved" && existingKeyID != nil {
			var keyPrefix string
			//nolint:errcheck // best-effort exec, non-critical
			h.db.QueryRow(ctx, `SELECT key_prefix FROM api_keys WHERE id = $1`, *existingKeyID).Scan(&keyPrefix)
			resp["key_prefix"] = keyPrefix
			resp["message"] = "申请已通过，请联系管理员获取完整密钥"
		}
		writeJSON(w, http.StatusCreated, resp)
		return
	}

	purpose := ""
	if req.Purpose != nil {
		purpose = *req.Purpose
	}
	var appID string
	err = h.db.QueryRow(ctx, `
		INSERT INTO key_applications (client_ip, fingerprint, contact, purpose)
		VALUES ($1, $2, $3, $4)
		RETURNING id::text
	`, clientIP, fp, req.Contact, purpose).Scan(&appID)
	if err != nil {
		slog.Error("failed to create key application", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to submit application")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"application_id": appID,
		"status":         "pending",
		"poll_url":       "/v1/keys/apply/" + appID,
		"message":        "申请已提交，管理员审核后将通知您",
	})
}

func (h *Handler) v1GetApplicationStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	remaining := r.URL.Path[len("/v1/keys/apply/"):]
	if remaining == "" {
		writeError(w, http.StatusBadRequest, "application_id required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id, status, contact string
	var createdAt time.Time
	var expiresAt *time.Time
	var issuedKeyID *int
	err := h.db.QueryRow(ctx, `
		SELECT id::text, status, COALESCE(contact,''), created_at, expires_at, issued_key_id
		FROM key_applications WHERE id::text = $1
	`, remaining).Scan(&id, &status, &contact, &createdAt, &expiresAt, &issuedKeyID)
	if err != nil {
		writeError(w, http.StatusNotFound, "申请记录不存在")
		return
	}

	resp := map[string]any{
		"application_id": id,
		"status":         status,
		"contact":        contact,
		"created_at":     createdAt.Format(time.RFC3339),
	}
	if expiresAt != nil {
		resp["expires_at"] = expiresAt.Format(time.RFC3339)
	}

	switch status {
	case "approved":
		if issuedKeyID != nil {
			var keyPrefix string
			//nolint:errcheck // best-effort exec, non-critical
			h.db.QueryRow(ctx, `SELECT key_prefix FROM api_keys WHERE id = $1`, *issuedKeyID).Scan(&keyPrefix)
			resp["key_prefix"] = keyPrefix
			resp["message"] = "申请已通过，请联系管理员获取完整密钥"
		}
	case "pending":
		resp["message"] = "等待管理员审核"
	case "rejected":
		resp["message"] = "申请被拒绝，如有疑问请联系管理员"
	case "expired":
		resp["message"] = "申请已过期，请重新提交"
	}

	writeJSON(w, http.StatusOK, resp)
}
