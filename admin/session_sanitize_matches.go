package admin

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

// SanitizeMatchEntry is one placeholder→value row (value always masked).
type SanitizeMatchEntry struct {
	Placeholder string `json:"placeholder"`
	Type        string `json:"type"`
	Index       int    `json:"index"`
	ValueMasked string `json:"value_masked"`
	InRequest   bool   `json:"in_request,omitempty"`
}

// SanitizeMatchesResponse is GET /api/admin/sessions/{id}/sanitize-matches.
type SanitizeMatchesResponse struct {
	SessionID   string               `json:"session_id"`
	MapRef      string               `json:"map_ref,omitempty"`
	Source      string               `json:"source"` // redis | empty
	Entries     []SanitizeMatchEntry `json:"entries"`
	Stats       map[string]int       `json:"stats"`
	RequestID   string               `json:"request_id,omitempty"`
	Placeholder int                  `json:"placeholder_count"`
}

var errSanitizeAccessDenied = errors.New("access denied")

// serveSessionSanitizeMatches handles GET …/sanitize-matches?request_id=.
func (h *Handler) serveSessionSanitizeMatches(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "session id required")
		return
	}

	tenantID, err := h.resolveSessionTenant(r.Context(), r, sessionID)
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}

	requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
	resp := SanitizeMatchesResponse{
		SessionID: sessionID,
		RequestID: requestID,
		Source:    "empty",
		Entries:   []SanitizeMatchEntry{},
		Stats:     map[string]int{},
	}

	rc := h.liveActionsRedisClient()
	if rc == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	mapRef, vals := loadSanitizeMapForSession(r.Context(), rc, tenantID, sessionID)
	resp.MapRef = mapRef
	if len(vals) == 0 {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Source = "redis"

	inRequest := map[string]bool{}
	if requestID != "" && h.db != nil {
		body := h.loadOutboundBodySnippet(r.Context(), requestID)
		for _, m := range sanitize.PlaceholderPattern.FindAllString(body, -1) {
			inRequest[m] = true
		}
	}

	entries := make([]SanitizeMatchEntry, 0, len(vals))
	for ph, raw := range vals {
		p, ok := sanitize.ParsePlaceholder(ph)
		if !ok {
			continue
		}
		typ := string(p.Type)
		resp.Stats[typ]++
		entries = append(entries, SanitizeMatchEntry{
			Placeholder: ph,
			Type:        typ,
			Index:       p.Index,
			ValueMasked: maskSensitiveValue(typ, raw),
			InRequest:   inRequest[ph],
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type < entries[j].Type
		}
		return entries[i].Index < entries[j].Index
	})
	resp.Entries = entries
	resp.Placeholder = len(entries)
	resp.Stats["placeholder_count"] = len(entries)
	writeJSON(w, http.StatusOK, resp)
}

func loadSanitizeMapForSession(ctx context.Context, rc *redis.Client, tenantID, sessionID string) (string, map[string]string) {
	keys := make([]string, 0, 3)
	if tenantID != "" {
		keys = append(keys, sanitize.SanitizeRedisKey(sanitize.HashTenant(tenantID), sessionID))
	}
	keys = append(keys,
		sanitize.SanitizeRedisKey(sanitize.HashTenant("_unknown"), sessionID),
		sanitize.SanitizeRedisKey(sessionID),
	)
	for _, key := range keys {
		// audit-24h-20260828-r4 P2: Use SafeHGetAll to prevent WRONGTYPE
		// errors when the sanitize map key collides with a non-hash type.
		// Errors continue to the next key — best-effort aggregation
		// semantics preserved (the loop tries 3 key shapes in priority
		// order and accepts whichever returns a non-empty map first).
		vals, err := redissafe.SafeHGetAll(ctx, rc, key)
		if err != nil || len(vals) == 0 {
			continue
		}
		return key, vals
	}
	if tenantID != "" {
		return sanitize.SanitizeRedisKey(sanitize.HashTenant(tenantID), sessionID), nil
	}
	return sanitize.SanitizeRedisKey(sessionID), nil
}

func (h *Handler) resolveSessionTenant(ctx context.Context, r *http.Request, sessionID string) (string, error) {
	callerTenant := GetTenantID(r)
	if h.sessionManager != nil {
		if sess, err := h.sessionManager.Get(ctx, sessionID); err == nil && sess != nil {
			if !IsSuperAdminOrLegacy(r) && sess.TenantID != callerTenant {
				return "", errSanitizeAccessDenied
			}
			return sess.TenantID, nil
		}
	}
	if h.db != nil {
		var tenant string
		err := h.db.QueryRow(ctx, `
			SELECT tenant_id FROM request_logs
			 WHERE gw_session_id = $1
			 ORDER BY created_at DESC
			 LIMIT 1`, sessionID).Scan(&tenant)
		if err == nil && tenant != "" {
			if !IsSuperAdminOrLegacy(r) && tenant != callerTenant {
				return "", errSanitizeAccessDenied
			}
			return tenant, nil
		}
	}
	if !IsSuperAdminOrLegacy(r) {
		if callerTenant == "" {
			return "", errSanitizeAccessDenied
		}
		return callerTenant, nil
	}
	return callerTenant, nil
}

func (h *Handler) loadOutboundBodySnippet(ctx context.Context, requestID string) string {
	if h.db == nil || requestID == "" {
		return ""
	}
	var body *string
	_ = h.db.QueryRow(ctx, `
		SELECT COALESCE(rb.outbound_body, rb.request_body)
		  FROM request_logs_with_current_month rl
		  LEFT JOIN request_logs_bodies_with_current_month rb ON rb.request_id = rl.request_id
		  WHERE rl.request_id = $1 LIMIT 1`, requestID).Scan(&body)
	if body == nil {
		return ""
	}
	const maxScan = 256 * 1024
	if len(*body) > maxScan {
		return (*body)[:maxScan]
	}
	return *body
}

// maskSensitiveValue never returns full plaintext for secrets.
func maskSensitiveValue(typ, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "—"
	}
	n := utf8.RuneCountInString(raw)
	switch typ {
	case "phone", "credit_card":
		if n <= 4 {
			return strings.Repeat("*", n)
		}
		rs := []rune(raw)
		return strings.Repeat("*", n-4) + string(rs[n-4:])
	case "email":
		at := strings.IndexByte(raw, '@')
		if at <= 1 {
			return "***"
		}
		return string(raw[0]) + "***" + raw[at:]
	case "id_card":
		if n <= 4 {
			return strings.Repeat("*", n)
		}
		rs := []rune(raw)
		return string(rs[:2]) + strings.Repeat("*", n-4) + string(rs[n-2:])
	case "secret":
		return "[secret len=" + strconv.Itoa(n) + "]"
	default:
		if n <= 2 {
			return strings.Repeat("*", n)
		}
		rs := []rune(raw)
		return string(rs[0]) + strings.Repeat("*", n-2) + string(rs[n-1])
	}
}
