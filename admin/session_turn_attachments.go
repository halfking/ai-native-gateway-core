package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type AttachmentHandler struct {
	hmacKey     []byte
	auditLog    func(string, string, string, int, string, string)
	revokeStore RevokeStore
}
type RevokeStore interface {
	Revoke(context.Context, string, string) error
	IsRevoked(context.Context, string) (bool, error)
}

func NewAttachmentHandler(store RevokeStore, key []byte) *AttachmentHandler {
	return &AttachmentHandler{hmacKey: key, revokeStore: store, auditLog: func(action, tenant, session string, turn int, attID, ip string) {
		slog.Info("attachment_audit", "action", action, "tenant", tenant, "session", session, "turn", turn, "att_id", attID, "ip", ip)
	}}
}
func (h *AttachmentHandler) Routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/signed" && r.Method == http.MethodGet {
			h.serveSigned(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/sessions/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 5 && parts[1] == "turns" && parts[3] == "attachments" {
			if parts[4] == "url" && r.Method == http.MethodGet {
				h.signURL(w, r, parts[0], parts[2], parts[4-1])
				return
			}
			if parts[4] == "revoke" && r.Method == http.MethodDelete {
				h.revoke(w, r, parts[0], parts[2], parts[4-1])
				return
			}
		}
		http.NotFound(w, r)
	})
}

type signedPayload struct {
	TenantID  string `json:"t"`
	SessionID string `json:"s"`
	TurnNo    int    `json:"n"`
	AttID     string `json:"a"`
	ObjectKey string `json:"k"`
	ExpiresAt int64  `json:"e"`
}

const signedURLTTL = 5 * time.Minute

func (h *AttachmentHandler) signURL(w http.ResponseWriter, r *http.Request, sessionID, turnNoText, attID string) {
	turnNo, _ := strconv.Atoi(turnNoText)
	tenant := r.Header.Get("X-Tenant-ID")
	if tenant == "" {
		http.Error(w, "unauthorized", 401)
		return
	}
	if h.revoked(r.Context(), attID) {
		http.Error(w, "revoked", http.StatusGone)
		return
	}
	p := signedPayload{TenantID: tenant, SessionID: sessionID, TurnNo: turnNo, AttID: attID, ObjectKey: "tenant/" + tenant + "/" + sessionID + "/turn_" + turnNoText + "/" + attID, ExpiresAt: time.Now().Add(signedURLTTL).UnixNano()}
	signed, err := h.signPayload(p)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	h.auditLog("sign", tenant, sessionID, turnNo, attID, r.RemoteAddr)
	writeJSON(w, 200, map[string]any{"url": "/signed?p=" + signed, "expires_at": p.ExpiresAt})
}
func (h *AttachmentHandler) revoke(w http.ResponseWriter, r *http.Request, sessionID, turnNoText, attID string) {
	tenant := r.Header.Get("X-Tenant-ID")
	if tenant == "" {
		http.Error(w, "unauthorized", 401)
		return
	}
	if h.revokeStore != nil {
		if err := h.revokeStore.Revoke(r.Context(), attID, tenant); err != nil {
			http.Error(w, "revoke: "+err.Error(), 500)
			return
		}
	}
	n, _ := strconv.Atoi(turnNoText)
	h.auditLog("revoke", tenant, sessionID, n, attID, r.RemoteAddr)
	w.WriteHeader(http.StatusNoContent)
}
func (h *AttachmentHandler) serveSigned(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(r.URL.Query().Get("p"), ".", 2)
	if len(parts) != 2 {
		http.Error(w, "bad signed url", 400)
		return
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		http.Error(w, "bad base64", 400)
		return
	}
	mac := hmac.New(sha256.New, h.hmacKey)
	mac.Write(body)
	sig, err := hex.DecodeString(parts[1])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		http.Error(w, "bad signature", 403)
		return
	}
	var p signedPayload
	if err = json.Unmarshal(body, &p); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if time.Now().UnixNano() > p.ExpiresAt || h.revoked(r.Context(), p.AttID) {
		http.Error(w, "expired or revoked", http.StatusGone)
		return
	}
	http.Redirect(w, r, "/storage/"+p.ObjectKey, http.StatusFound)
}
func (h *AttachmentHandler) revoked(ctx context.Context, id string) bool {
	if h.revokeStore == nil {
		return false
	}
	v, _ := h.revokeStore.IsRevoked(ctx, id)
	return v
}
func (h *AttachmentHandler) signPayload(p signedPayload) (string, error) {
	if len(h.hmacKey) == 0 {
		return "", errors.New("hmac key not set")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, h.hmacKey)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + hex.EncodeToString(mac.Sum(nil)), nil
}
func (h *AttachmentHandler) signForTest(tenant, session string, turn int, att, obj string, ttl time.Duration) (string, error) {
	return h.signPayload(signedPayload{TenantID: tenant, SessionID: session, TurnNo: turn, AttID: att, ObjectKey: obj, ExpiresAt: time.Now().Add(ttl).UnixNano()})
}
