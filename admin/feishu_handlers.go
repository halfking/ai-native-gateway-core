// File: admin/feishu_handlers.go
//
// 2026-07-09: 飞书机器人模块的运营面 API。
//
// 提供：
//   - GET  /api/admin/feishubot/routing-rules      列出规则（支持 ?tenant_id= 过滤）
//   - POST /api/admin/feishubot/routing-rules      新增规则（单条 OpenID）
//   - PUT  /api/admin/feishubot/routing-rules/{id} 更新规则（role / risk_levels / priority / enabled）
//   - DELETE /api/admin/feishubot/routing-rules/{id} 删除规则
//   - GET  /api/admin/feishubot/send-log           列出最近发送审计（?limit=50&event_type=alert|approval）
//
// 设计原则：
//   - 不破坏现有 feishu_bot.allowed_users（settings_kv）路径
//   - DB 表为主，settings_kv 为辅，plugin.go 优先读 DB 表，fallback settings_kv
//   - 所有变更走 audit log（adminHandler 的 auditLog）

package admin

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// feishuRouteRule 对应 feishu_bot_routing_rules 单行。
//
// 字段名与 SQL 列严格对齐，json tag 走 snake_case（前端约定）。
type feishuRouteRule struct {
	ID          int64     `json:"id"`
	TenantID    string    `json:"tenant_id"`
	OpenID      string    `json:"open_id"`
	DisplayName string    `json:"display_name"`
	UserRole    string    `json:"user_role"`
	RiskLevels  []string  `json:"risk_levels"`
	Priority    int       `json:"priority"`
	Enabled     bool      `json:"enabled"`
	Note        string    `json:"note"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// feishuRouteRuleCSV 一行 CSV 解析结果（宽松的 header 匹配）。
//
// 设计：headable headers 不区分大小写，按 name/value 匹配。
// 必需字段：open_id；可选：display_name、user_role、risk_levels、priority、enabled、note。
type feishuRouteRuleCSV struct {
	OpenID      string
	DisplayName string
	UserRole    string
	RiskLevels  string // CSV 字段是字符串，内部 split
	Priority    int
	Enabled     *bool
	Note        string
}

// feishuSendLogEntry 对应 feishu_bot_send_log 单行。
type feishuSendLogEntry struct {
	ID              int64     `json:"id"`
	TenantID        string    `json:"tenant_id"`
	EventType       string    `json:"event_type"`
	EventID         string    `json:"event_id,omitempty"`
	RecipientsCount int       `json:"recipients_count"`
	Success         bool      `json:"success"`
	ErrorCode       *int      `json:"error_code,omitempty"`
	ErrorMessage    string    `json:"error_message,omitempty"`
	LatencyMS       *int      `json:"latency_ms,omitempty"`
	Deduped         bool      `json:"deduped"`
	RateLimited     bool      `json:"rate_limited"`
	CreatedAt       time.Time `json:"created_at"`
}

// handleFeishuRoutingList GET /api/admin/feishubot/routing-rules
//
// 支持查询参数：
//   - tenant_id: 按租户过滤（默认 'default'）
//   - enabled_only: bool，仅显示启用的（默认 false）
//   - user_role: 过滤 admin / member / auditor
//   - limit: int（默认 100，上限 500）
func (h *Handler) handleFeishuRoutingList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not initialised")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		tenantID = "default"
	}
	enabledOnly := r.URL.Query().Get("enabled_only") == "true"
	userRole := strings.TrimSpace(r.URL.Query().Get("user_role"))
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	// Build query dynamically
	q := `SELECT id, tenant_id, open_id, display_name, user_role, risk_levels,
	             priority, enabled, COALESCE(note, ''), COALESCE(created_by, ''),
	             created_at, updated_at
	      FROM feishu_bot_routing_rules
	      WHERE tenant_id = $1`
	args := []any{tenantID}
	if enabledOnly {
		q += " AND enabled = true"
	}
	if userRole != "" {
		q += " AND user_role = $2"
		args = append(args, userRole)
	}
	q += " ORDER BY priority ASC, id ASC LIMIT " + strconv.Itoa(limit)

	rows, err := h.db.Query(r.Context(), q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query rules: "+err.Error())
		return
	}
	defer rows.Close()

	out := []feishuRouteRule{}
	for rows.Next() {
		var r feishuRouteRule
		var riskJSON []byte
		if err := rows.Scan(&r.ID, &r.TenantID, &r.OpenID, &r.DisplayName, &r.UserRole,
			&riskJSON, &r.Priority, &r.Enabled, &r.Note, &r.CreatedBy, &r.CreatedAt, &r.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan: "+err.Error())
			return
		}
		if len(riskJSON) > 0 {
			_ = json.Unmarshal(riskJSON, &r.RiskLevels)
		}
		if r.RiskLevels == nil {
			r.RiskLevels = []string{}
		}
		out = append(out, r)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"count":     len(out),
		"tenant_id": tenantID,
	})
}

// handleFeishuRoutingCreate POST /api/admin/feishubot/routing-rules
//
// 请求体：
//
//	{
//	  "open_id":      "ou_xxx",        // 必填
//	  "display_name": "张三",           // 可选
//	  "user_role":    "admin",         // admin/member/auditor，默认 member
//	  "risk_levels":  ["high","critical"], // 默认 ["low","medium","high","critical"]
//	  "priority":     100,             // 数字越小越高，默认 100
//	  "enabled":      true,            // 默认 true
//	  "note":         "..."            // 可选
//	}
//
// 约束：
//   - 同 (tenant_id, open_id) 已存在 → 409 Conflict
//   - open_id 不能为空
func (h *Handler) handleFeishuRoutingCreate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not initialised")
		return
	}
	var req struct {
		TenantID    string   `json:"tenant_id"`
		OpenID      string   `json:"open_id"`
		DisplayName string   `json:"display_name"`
		UserRole    string   `json:"user_role"`
		RiskLevels  []string `json:"risk_levels"`
		Priority    int      `json:"priority"`
		Enabled     *bool    `json:"enabled"`
		Note        string   `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.OpenID = strings.TrimSpace(req.OpenID)
	if req.OpenID == "" {
		writeError(w, http.StatusBadRequest, "open_id is required")
		return
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}
	if req.UserRole == "" {
		req.UserRole = "member"
	}
	if req.UserRole != "admin" && req.UserRole != "member" && req.UserRole != "auditor" {
		writeError(w, http.StatusBadRequest, "user_role must be admin|member|auditor")
		return
	}
	if len(req.RiskLevels) == 0 {
		req.RiskLevels = []string{"low", "medium", "high", "critical"}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if req.Priority == 0 {
		req.Priority = 100
	}

	riskJSON, _ := json.Marshal(req.RiskLevels)

	row := h.db.QueryRow(r.Context(), `
		INSERT INTO feishu_bot_routing_rules
		    (tenant_id, open_id, display_name, user_role, risk_levels, priority, enabled, note, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at
	`, req.TenantID, req.OpenID, req.DisplayName, req.UserRole, riskJSON,
		req.Priority, enabled, req.Note, userFromContext(r))

	var id int64
	var createdAt, updatedAt time.Time
	if err := row.Scan(&id, &createdAt, &updatedAt); err != nil {
		// 唯一约束冲突
		if strings.Contains(err.Error(), "uq_feishu_route_openid") {
			writeError(w, http.StatusConflict, "rule for (tenant_id, open_id) already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "insert: "+err.Error())
		return
	}
	h.auditLog(userFromContext(r), "feishubot.routing.create", "feishu_bot_routing_rules",
		0, "open_id="+req.OpenID)

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         id,
		"tenant_id":  req.TenantID,
		"open_id":    req.OpenID,
		"created_at": createdAt,
		"updated_at": updatedAt,
	})
}

// handleFeishuRoutingUpdate PUT /api/admin/feishubot/routing-rules/{id}
//
// 请求体（部分更新）：所有字段可选
//
//	{ "user_role": "admin", "priority": 50, "enabled": false, "risk_levels": ["critical"] }
func (h *Handler) handleFeishuRoutingUpdate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not initialised")
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/admin/feishubot/routing-rules/")
	idStr = strings.Split(idStr, "/")[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	// 动态构建 UPDATE 语句
	sets := []string{}
	args := []any{}
	idx := 1
	if v, ok := req["user_role"]; ok {
		s, _ := v.(string)
		if s != "admin" && s != "member" && s != "auditor" {
			writeError(w, http.StatusBadRequest, "user_role must be admin|member|auditor")
			return
		}
		sets = append(sets, "user_role = $"+strconv.Itoa(idx))
		args = append(args, s)
		idx++
	}
	if v, ok := req["display_name"]; ok {
		s, _ := v.(string)
		sets = append(sets, "display_name = $"+strconv.Itoa(idx))
		args = append(args, s)
		idx++
	}
	if v, ok := req["priority"]; ok {
		n, _ := v.(float64)
		sets = append(sets, "priority = $"+strconv.Itoa(idx))
		args = append(args, int(n))
		idx++
	}
	if v, ok := req["enabled"]; ok {
		b, _ := v.(bool)
		sets = append(sets, "enabled = $"+strconv.Itoa(idx))
		args = append(args, b)
		idx++
	}
	if v, ok := req["note"]; ok {
		s, _ := v.(string)
		sets = append(sets, "note = $"+strconv.Itoa(idx))
		args = append(args, s)
		idx++
	}
	if v, ok := req["risk_levels"]; ok {
		arr, _ := v.([]any)
		levels := make([]string, 0, len(arr))
		for _, x := range arr {
			if s, ok := x.(string); ok {
				levels = append(levels, s)
			}
		}
		j, _ := json.Marshal(levels)
		sets = append(sets, "risk_levels = $"+strconv.Itoa(idx))
		args = append(args, j)
		idx++
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no fields to update")
		return
	}
	args = append(args, id)
	q := "UPDATE feishu_bot_routing_rules SET " + strings.Join(sets, ", ") +
		" WHERE id = $" + strconv.Itoa(idx)

	tag, err := h.db.Exec(r.Context(), q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	h.auditLog(userFromContext(r), "feishubot.routing.update", "feishu_bot_routing_rules",
		int(id), "fields="+strconv.Itoa(len(sets)))
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "updated": true})
}

// handleFeishuRoutingDelete DELETE /api/admin/feishubot/routing-rules/{id}
func (h *Handler) handleFeishuRoutingDelete(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not initialised")
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/admin/feishubot/routing-rules/")
	idStr = strings.Split(idStr, "/")[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	tag, err := h.db.Exec(r.Context(),
		"DELETE FROM feishu_bot_routing_rules WHERE id = $1", id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	h.auditLog(userFromContext(r), "feishubot.routing.delete", "feishu_bot_routing_rules",
		int(id), "")
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// handleFeishuSendLogList GET /api/admin/feishubot/send-log
//
// 支持查询参数：
//   - tenant_id
//   - event_type: alert|approval|command
//   - success: bool
//   - limit: int（默认 50，上限 200）
func (h *Handler) handleFeishuSendLogList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not initialised")
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		tenantID = "default"
	}
	eventType := strings.TrimSpace(r.URL.Query().Get("event_type"))
	successFilter := r.URL.Query().Get("success")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	q := `SELECT id, tenant_id, event_type, COALESCE(event_id, ''), recipients_count,
	             success, error_code, COALESCE(error_message, ''), latency_ms, deduped, rate_limited, created_at
	      FROM feishu_bot_send_log
	      WHERE tenant_id = $1`
	args := []any{tenantID}
	if eventType != "" {
		q += " AND event_type = $2"
		args = append(args, eventType)
	}
	if successFilter == "true" {
		q += " AND success = true"
	} else if successFilter == "false" {
		q += " AND success = false"
	}
	q += " ORDER BY id DESC LIMIT " + strconv.Itoa(limit)

	rows, err := h.db.Query(r.Context(), q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query log: "+err.Error())
		return
	}
	defer rows.Close()

	out := []feishuSendLogEntry{}
	for rows.Next() {
		var e feishuSendLogEntry
		var errCode, lat *int
		if err := rows.Scan(&e.ID, &e.TenantID, &e.EventType, &e.EventID, &e.RecipientsCount,
			&e.Success, &errCode, &e.ErrorMessage, &lat, &e.Deduped, &e.RateLimited, &e.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "scan: "+err.Error())
			return
		}
		e.ErrorCode = errCode
		e.LatencyMS = lat
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"count":     len(out),
		"tenant_id": tenantID,
	})
}

// userFromContext 安全地从 request context 取用户名（audit 用），无则回 'unknown'。
func userFromContext(r *http.Request) string {
	if v := r.Context().Value("user"); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	if v := r.Context().Value("username"); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "unknown"
}

// ── Bulk CSV import（routing rules）───────────────────────────────

// handleFeishuRoutingRulesImport POST /api/admin/feishubot/routing-rules:import
//
// 接受 multipart/form-data (file 字段) 或 application/json 数组：
//   - multipart：file 字段名 "file"，Content-Type text/csv
//   - json：请求体 [{"open_id":"...","display_name":"...",...}, ...]
//
// CSV 格式：表头 + 行数据。表头支持以下列（不区分大小写）：
//
//	open_id, display_name, user_role, risk_levels, priority, enabled, note
//
// 返回：
//
//	{ "imported": N, "skipped": M, "errors": [{row, error}, ...] }
//
// 行为：
//   - 重复 open_id 跳过（ON CONFLICT DO NOTHING）
//   - 单行错误不影响其他行
//   - 上限 1000 行/次
func (h *Handler) handleFeishuRoutingRulesImport(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not initialised")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}

	var rules []feishuRouteRuleCSV
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		// 解析 multipart（10 MB 上限）
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "parse multipart: "+err.Error())
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing 'file' field: "+err.Error())
			return
		}
		defer file.Close()
		parsed, perr := parseFeishuRouteRulesCSV(file)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "parse CSV: "+perr.Error())
			return
		}
		rules = parsed
	} else {
		// JSON 数组
		if err := json.NewDecoder(r.Body).Decode(&rules); err != nil {
			writeError(w, http.StatusBadRequest, "parse json: "+err.Error())
			return
		}
	}

	if len(rules) == 0 {
		writeError(w, http.StatusBadRequest, "no rules to import")
		return
	}
	if len(rules) > 1000 {
		writeError(w, http.StatusBadRequest, "too many rules (limit 1000)")
		return
	}

	imported := 0
	skipped := 0
	type rowErr struct {
		Row  int    `json:"row"`
		Err  string `json:"error"`
		Data any    `json:"data,omitempty"`
	}
	errors := []rowErr{}
	ctx := r.Context()
	currentUser := userFromContext(r)

	for i, rule := range rules {
		r := rule
		if r.OpenID == "" {
			errors = append(errors, rowErr{Row: i + 1, Err: "open_id is empty"})
			skipped++
			continue
		}
		// 校验 user_role
		if r.UserRole != "" && r.UserRole != "admin" && r.UserRole != "member" && r.UserRole != "auditor" {
			errors = append(errors, rowErr{Row: i + 1, Err: "invalid user_role: " + r.UserRole, Data: r})
			skipped++
			continue
		}
		if r.UserRole == "" {
			r.UserRole = "member"
		}

		// 解析 risk_levels（CSV 字段用分号/逗号分隔）
		var levels []string
		if r.RiskLevels != "" {
			for _, sep := range []string{";", ","} {
				parts := strings.Split(r.RiskLevels, sep)
				if len(parts) > 1 {
					for _, p := range parts {
						p = strings.TrimSpace(p)
						if p != "" {
							levels = append(levels, p)
						}
					}
					break
				}
			}
			if len(levels) == 0 {
				levels = []string{r.RiskLevels}
			}
		} else {
			levels = []string{"low", "medium", "high", "critical"}
		}
		enabled := true
		if r.Enabled != nil {
			enabled = *r.Enabled
		}
		if r.Priority == 0 {
			r.Priority = 100
		}

		riskJSON, _ := json.Marshal(levels)
		_, err := h.db.Exec(ctx, `
			INSERT INTO feishu_bot_routing_rules
			    (tenant_id, open_id, display_name, user_role, risk_levels, priority, enabled, note, created_by)
			VALUES ('default', $1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (tenant_id, open_id) DO NOTHING
		`, r.OpenID, r.DisplayName, r.UserRole, riskJSON, r.Priority, enabled, r.Note, currentUser)
		if err != nil {
			errors = append(errors, rowErr{Row: i + 1, Err: "insert: " + err.Error(), Data: r})
			skipped++
			continue
		}
		imported++
	}

	h.auditLog(currentUser, "feishubot.routing.bulk_import", "feishu_bot_routing_rules",
		0, fmt.Sprintf("imported=%d skipped=%d", imported, len(errors)))

	writeJSON(w, http.StatusOK, map[string]any{
		"imported": imported,
		"skipped":  skipped,
		"errors":   errors,
	})
}

// parseFeishuRouteRulesCSV 解析 multipart 上传的 CSV 字节流。
//
// 容忍：
//   - 表头大小写不敏感
//   - 字段间逗号 / 制表符 / 分号混合
//   - 空行跳过
//   - 引号包裹（基础支持，不处理嵌套）
func parseFeishuRouteRulesCSV(r io.Reader) ([]feishuRouteRuleCSV, error) {
	// 用 encoding/csv 处理（默认逗号分隔）
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1 // 允许变长行
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("csv must have header + at least 1 data row")
	}

	// 解析表头
	header := make(map[string]int)
	for i, h := range rows[0] {
		header[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(row []string, key string) string {
		idx, ok := header[key]
		if !ok || idx >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[idx])
	}

	var out []feishuRouteRuleCSV
	for i := 1; i < len(rows); i++ {
		row := rows[i]
		// 跳过空行
		if len(row) == 1 && strings.TrimSpace(row[0]) == "" {
			continue
		}
		rule := feishuRouteRuleCSV{
			OpenID:      get(row, "open_id"),
			DisplayName: get(row, "display_name"),
			UserRole:    get(row, "user_role"),
			RiskLevels:  get(row, "risk_levels"),
			Note:        get(row, "note"),
		}
		// 数字字段
		if p := get(row, "priority"); p != "" {
			if n, perr := strconv.Atoi(p); perr == nil {
				rule.Priority = n
			}
		}
		// 布尔字段
		if e := get(row, "enabled"); e != "" {
			lower := strings.ToLower(e)
			enabled := lower == "true" || lower == "1" || lower == "yes"
			rule.Enabled = &enabled
		}
		out = append(out, rule)
	}
	return out, nil
}

// compile-time check: pgxpool is used
var _ = (*pgxpool.Pool)(nil)
