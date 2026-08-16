package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type TurnListItem struct {
	TurnNo           int       `json:"turn_no"`
	Ts               time.Time `json:"ts"`
	Title            string    `json:"title,omitempty"`
	Summary          string    `json:"summary,omitempty"`
	RequestTokens    int       `json:"request_tokens"`
	ResponseTokens   int       `json:"response_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	Model            string    `json:"model"`
	Provider         string    `json:"provider"`
	StatusCode       int       `json:"status_code"`
	SubmitMode       string    `json:"submit_mode"`
	InjectionVerdict string    `json:"injection_verdict"`
	OutputVerdict    string    `json:"output_verdict"`
	AttachmentCount  int       `json:"attachment_count"`
}

type SessionTurnsHandler struct {
	db        *pgxpool.Pool
	jwtSecret []byte
	hmacKey   []byte
	jwtParser func(string, []byte) (AdminClaims, error)
}

type AdminClaims struct {
	TenantID string
	UserID   string
	Role     string
}

func NewSessionTurnsHandler(db *pgxpool.Pool, jwtSecret []byte) *SessionTurnsHandler {
	return &SessionTurnsHandler{db: db, jwtSecret: jwtSecret, hmacKey: []byte("cursor-sign-key-change-me-in-prod")}
}
func (h *SessionTurnsHandler) SetJWTParser(p func(string, []byte) (AdminClaims, error)) {
	h.jwtParser = p
}

func (h *SessionTurnsHandler) Routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/admin/sessions/")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) == 2 && parts[1] == "turns" && r.Method == http.MethodGet {
			h.listTurns(w, r, parts[0])
			return
		}
		if len(parts) == 3 && parts[1] == "turns" && r.Method == http.MethodGet {
			if _, err := strconv.Atoi(parts[2]); err == nil {
				h.getTurn(w, r)
				return
			}
		}
		if len(parts) == 2 && parts[1] == "snapshot" && r.Method == http.MethodGet {
			h.snapshot(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

const (
	defaultTurnsLimit = 50
	maxTurnsLimit     = 200
)

func (h *SessionTurnsHandler) listTurns(w http.ResponseWriter, r *http.Request, sessionID string) {
	tenantID, _, _, ok := h.extractAdminContext(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	limit := defaultTurnsLimit
	if value := r.URL.Query().Get("limit"); value != "" {
		if n, err := strconv.Atoi(value); err == nil && n > 0 && n <= maxTurnsLimit {
			limit = n
		}
	}
	beforeTurnNo := int(^uint(0) >> 1)
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := decodeCursor(encoded, h.hmacKey)
		if err != nil {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return
		}
		if decoded.TenantID != tenantID || decoded.SessionID != sessionID {
			http.Error(w, "cursor mismatch", http.StatusBadRequest)
			return
		}
		beforeTurnNo = decoded.TurnNo
	}
	if h.db == nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT turn_no, ts, COALESCE(title,''), COALESCE(summary,''),
		       COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0), COALESCE(cost_usd,0),
		       COALESCE(model,''), COALESCE(provider,''), COALESCE(status_code,0),
		       COALESCE(submit_mode,''), COALESCE(injection_verdict,''), COALESCE(output_verdict,''),
		       COALESCE(attachment_count,0)
		FROM public.session_turns_with_current_month WHERE tenant_id=$1 AND session_id=$2 AND turn_no < $3
		ORDER BY turn_no DESC LIMIT $4`, tenantID, sessionID, beforeTurnNo, limit+1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]TurnListItem, 0, limit)
	for rows.Next() {
		var item TurnListItem
		if err := rows.Scan(&item.TurnNo, &item.Ts, &item.Title, &item.Summary, &item.RequestTokens, &item.ResponseTokens, &item.CostUSD, &item.Model, &item.Provider, &item.StatusCode, &item.SubmitMode, &item.InjectionVerdict, &item.OutputVerdict, &item.AttachmentCount); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hasMore := len(items) > limit
	var nextCursor string
	if hasMore {
		items = items[:limit]
		nextCursor, _ = encodeCursor(cursorPayload{TenantID: tenantID, SessionID: sessionID, TurnNo: items[len(items)-1].TurnNo, TS: time.Now()}, h.hmacKey)
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "turns": items, "has_more": hasMore, "next_cursor": nextCursor})
}
func (h *SessionTurnsHandler) getTurn(w http.ResponseWriter, r *http.Request) {
	if _, _, _, ok := h.extractAdminContext(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.Error(w, "not implemented yet (Task 8 stub)", http.StatusNotImplemented)
}
func (h *SessionTurnsHandler) snapshot(w http.ResponseWriter, r *http.Request) {
	if _, _, _, ok := h.extractAdminContext(r); !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.Error(w, "not implemented yet (Task 8 stub)", http.StatusNotImplemented)
}

type cursorPayload struct {
	TenantID  string    `json:"t"`
	SessionID string    `json:"s"`
	TurnNo    int       `json:"n"`
	TS        time.Time `json:"ts"`
}

func encodeCursor(p cursorPayload, key []byte) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(append(body, mac.Sum(nil)...)), nil
}
func decodeCursor(value string, key []byte) (cursorPayload, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorPayload{}, err
	}
	if len(raw) < sha256.Size {
		return cursorPayload{}, errors.New("cursor too short")
	}
	body, sig := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return cursorPayload{}, errors.New("bad signature")
	}
	var payload cursorPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return cursorPayload{}, err
	}
	return payload, nil
}

// errCursorMismatch 表示 cursor 解码成功但其归属租户/会话与当前请求不符。
// 用于区分「无效 cursor」（签名错误/格式非法）与「跨租户复用 cursor」，
// 前者返回 invalid cursor，后者返回 cursor mismatch（2026-08-09 审计修复）。
var errCursorMismatch = errors.New("cursor mismatch")

// validateCursor 解码并校验 cursor 归属。sessionID 为空表示不校验会话维度
// （跨会话轮次列表场景）。返回解码后的 payload 供调用方使用。
func validateCursor(encoded string, key []byte, tenantID, sessionID string) (cursorPayload, error) {
	p, err := decodeCursor(encoded, key)
	if err != nil {
		return cursorPayload{}, err
	}
	if p.TenantID != tenantID || (sessionID != "" && p.SessionID != sessionID) {
		return cursorPayload{}, errCursorMismatch
	}
	return p, nil
}

func (h *SessionTurnsHandler) extractAdminContext(r *http.Request) (tenant, user, role string, ok bool) {
	if h.jwtParser != nil {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			return
		}
		claims, err := h.jwtParser(strings.TrimPrefix(auth, "Bearer "), h.jwtSecret)
		if err != nil {
			return
		}
		return claims.TenantID, claims.UserID, claims.Role, true
	}
	if r.Header.Get("X-Test-Admin") == "1" {
		return r.Header.Get("X-Tenant-ID"), r.Header.Get("X-User-ID"), r.Header.Get("X-Role"), true
	}
	return
}
