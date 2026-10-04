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
	// 租户归属判定（会话存储解耦 v3 S3 读端迁移）：先查 session 族唯一事实源，
	// 再回落 v1 request_logs。
	//
	// 为什么不能只留 request_logs：S4 停写（storage.request_logs_write_enabled=false）
	// 后新会话在 request_logs 里没有行，此处会落空并回退到调用方自己的
	// callerTenant —— 下面的跨租户拒绝分支永不触发，租户 A 就能按 A 的口径
	// 去脱敏/读取属于 B 的会话。先查 session 族让 S4 之后判定照常生效。
	//
	// 为什么保留 request_logs 回落：镜像链启用之前的历史窗口只在 v1 族里有行，
	// 直接切走会让那批会话改用 callerTenant 判定，同样是错的。
	if h.db != nil {
		if tenant := h.lookupSessionTenant(ctx, sessionID); tenant != "" {
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

// lookupSessionTenant 解析会话归属租户：先查 session 族唯一事实源，落空再回落
// v1 request_logs。返回 "" 表示两个来源都没有该会话。
//
// session_turns_hot 是独立堆表、session_turns 是按月分区的母表，只读单腿会漏掉
// 尚未 promote 的近期行 —— 与 734 视图体的 hot ∪ parent 形态同构。
// 命中 idx_session_turns_session (session_id, turn_no DESC)。
//
// RLS 姿态与原 request_logs 查询一致：两表都带 tenant_isolation +
// super_admin_bypass 策略（430/725），因此这里不改变可见性语义，只改变
// 「哪张表提供事实」。
func (h *Handler) lookupSessionTenant(ctx context.Context, sessionID string) string {
	if h.db == nil || sessionID == "" {
		return ""
	}
	var tenant string
	err := h.db.QueryRow(ctx, `
		SELECT tenant_id FROM (
			SELECT tenant_id, ts FROM session_turns_hot WHERE session_id = $1
			UNION ALL
			SELECT tenant_id, ts FROM session_turns WHERE session_id = $1
		) session_rows
		ORDER BY ts DESC
		LIMIT 1`, sessionID).Scan(&tenant)
	if err == nil && tenant != "" {
		return tenant
	}
	// 镜像链启用之前的历史窗口只在 v1 族里有行。
	tenant = ""
	if err := h.db.QueryRow(ctx, `
		SELECT tenant_id FROM request_logs
		 WHERE gw_session_id = $1
		 ORDER BY created_at DESC
		 LIMIT 1`, sessionID).Scan(&tenant); err != nil {
		return ""
	}
	return tenant
}

func (h *Handler) loadOutboundBodySnippet(ctx context.Context, requestID string) string {
	if h.db == nil || requestID == "" {
		return ""
	}
	var body *string
	_ = h.db.QueryRow(ctx, `
		SELECT COALESCE(rb.outbound_body, rb.request_body)
		  FROM request_logs_with_current_month rl
		  LEFT JOIN `+sessionBodiesFromSQL()+` ON rb.request_id = rl.request_id
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
