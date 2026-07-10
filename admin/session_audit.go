package admin

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/sessionaudit" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// SessionAuditListRequest 审计记录列表请求
type SessionAuditListRequest struct {
	TenantID  string `json:"tenant_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"` // pass/warn/blocked/need_approval
	Limit     int    `json:"limit"`
	Offset    int    `json:"offset"`
}

// SessionAuditListResponse 审计记录列表响应
type SessionAuditListResponse struct {
	Records []*SessionAuditRecord `json:"records"`
	Total   int                   `json:"total"`
	Limit   int                   `json:"limit"`
	Offset  int                   `json:"offset"`
}

// SessionAuditRecord 审计记录（API 响应）
type SessionAuditRecord struct {
	ID             int64                            `json:"id"`
	SessionID      string                           `json:"session_id"`
	TenantID       string                           `json:"tenant_id"`
	ClientInfo     sessionaudit.ClientInfo          `json:"client_info"`
	Summary        *sessionaudit.Summary            `json:"summary,omitempty"`
	Intent         *sessionaudit.Intent             `json:"intent,omitempty"`
	Scores         sessionaudit.MultiDimensionScore `json:"scores"`
	DetectResult   *sessionaudit.DetectResult       `json:"detect_result"`
	Status         string                           `json:"status"`
	ApprovalStatus string                           `json:"approval_status,omitempty"`
	CreatedAt      time.Time                        `json:"created_at"`
}

// SessionAuditStatsResponse 审计统计响应
type SessionAuditStatsResponse struct {
	Total      int            `json:"total"`
	ByStatus   map[string]int `json:"by_status"`
	ByApproval map[string]int `json:"by_approval"`
	AvgScore   float64        `json:"avg_score"`
	TopThreats []ThreatStat   `json:"top_threats"`
}

// ThreatStat 威胁统计
type ThreatStat struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// handleSessionAuditList 查询审计记录列表
//
// GET /api/admin/session-audit?tenant_id=&session_id=&status=&limit=50&offset=0
//
// 租户隔离：caller tenant 必须等于请求参数 tenant_id；事务内 SET LOCAL
// app.current_tenant 触发 RLS。两层防御。
func (h *Handler) handleSessionAuditList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析查询参数
	query := r.URL.Query()
	req := &SessionAuditListRequest{
		TenantID:  query.Get("tenant_id"),
		SessionID: query.Get("session_id"),
		Status:    query.Get("status"),
		Limit:     50,
		Offset:    0,
	}

	if limitStr := query.Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 && limit <= 200 {
			req.Limit = limit
		}
	}

	if offsetStr := query.Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil && offset >= 0 {
			req.Offset = offset
		}
	}

	// 租户访问控制：super_admin / admin_key 可查任意 tenant；
	// tenant_admin 锁定自己 tenant
	callerTenant := GetTenantID(r)
	isSuper := IsSuperAdminOrLegacy(r)
	if !isSuper && req.TenantID != "" && req.TenantID != callerTenant {
		writeError(w, http.StatusForbidden, "cross-tenant access denied")
		return
	}
	if !isSuper && req.TenantID == "" {
		req.TenantID = callerTenant
	}

	// 构造 SQL 查询
	sqlQuery := `
		SELECT
			id, session_id, tenant_id,
			client_ip, client_user_agent, client_model,
			content_summary, content_title, content_hash,
			intent_type, intent_score, intent_reason,
			security_score, danger_score, trust_score, sensitive_score,
			detect_score, detect_decision, threats, sensitive_words,
			status, approval_status,
			created_at
		FROM session_audit_records
		WHERE 1=1
	`
	args := []interface{}{}
	argNum := 1

	if req.TenantID != "" {
		sqlQuery += fmt.Sprintf(" AND tenant_id = $%d", argNum)
		args = append(args, req.TenantID)
		argNum++
	}

	if req.SessionID != "" {
		sqlQuery += fmt.Sprintf(" AND session_id = $%d", argNum)
		args = append(args, req.SessionID)
		argNum++
	}

	if req.Status != "" {
		sqlQuery += fmt.Sprintf(" AND status = $%d", argNum)
		args = append(args, req.Status)
		argNum++
	}

	// 查询总数
	countQuery := "SELECT COUNT(*) FROM session_audit_records WHERE 1=1"
	countArgs := []interface{}{}
	if req.TenantID != "" {
		countQuery += " AND tenant_id = $1"
		countArgs = append(countArgs, req.TenantID)
	}
	var total int
	if err := h.db.QueryRow(r.Context(), countQuery, countArgs...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("count failed: %v", err))
		return
	}

	// 查询记录
	sqlQuery += " ORDER BY created_at DESC"
	sqlQuery += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argNum, argNum+1)
	args = append(args, req.Limit, req.Offset)

	// 走事务 + RLS：把裸 h.db.Query 替换成 withTenantTx 包装
	records := []*SessionAuditRecord{}
	err := withTenantTx(r.Context(), h.db, req.TenantID, func(tx pgx.Tx) error {
		rows, qerr := tx.Query(r.Context(), sqlQuery, args...)
		if qerr != nil {
			return fmt.Errorf("query failed: %w", qerr)
		}
		defer rows.Close()
		for rows.Next() {
			record, serr := scanSessionAuditRecord(rows)
			if serr != nil {
				continue // 跳过损坏的记录
			}
			records = append(records, record)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 返回响应
	writeJSON(w, http.StatusOK, &SessionAuditListResponse{
		Records: records,
		Total:   total,
		Limit:   req.Limit,
		Offset:  req.Offset,
	})
}

// handleSessionAuditGet 获取单条审计记录
//
// GET /api/admin/session-audit/:id
//
// 租户隔离：admin 调用方 tenant 必须等于行 tenant；事务内 SET LOCAL
// app.current_tenant 让 RLS 作为兜底。两次校验失败返回 403/404。
func (h *Handler) handleSessionAuditGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 从路径提取 ID
	idStr := extractPathParam(r.URL.Path, "/api/admin/session-audit/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	callerTenant := GetTenantID(r)
	var record *SessionAuditRecord

	// 事务内 query + scan 一起完成（避免 pgx.Row 在事务外 lazy 求值的 bug）。
	// 见 tenant_ctx.go withTenantQueryRow 的注释。
	qerr := withTenantQueryRow(r.Context(), h.db, callerTenant, func(tx pgx.Tx) error {
		row := tx.QueryRow(r.Context(), `
			SELECT
				id, session_id, tenant_id,
				client_ip, client_user_agent, client_model,
				content_summary, content_title, content_hash,
				intent_type, intent_score, intent_reason,
				security_score, danger_score, trust_score, sensitive_score,
				detect_score, detect_decision, threats, sensitive_words,
				status, approval_status,
				created_at
			FROM session_audit_records
			WHERE id = $1
		`, id)
		rec, serr := scanSessionAuditRecord(row)
		if serr != nil {
			return serr
		}
		record = rec
		return nil
	})
	if errors.Is(qerr, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "record not found")
		return
	}
	if qerr != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query failed: %v", qerr))
		return
	}

	// 应用层二次校验：行 tenant 必须等于 caller tenant。
	// super_admin / admin_key 可看任意租户；其他角色被锁自己 tenant。
	callerTenant = GetTenantID(r)
	if !IsSuperAdminOrLegacy(r) && record.TenantID != callerTenant {
		writeError(w, http.StatusForbidden, "cross-tenant access denied")
		return
	}

	writeJSON(w, http.StatusOK, record)
}

// handleSessionAuditStats 获取审计统计
//
// GET /api/admin/session-audit/stats?tenant_id=&start=&end=
//
// 租户隔离：admin 调用方 tenant 必须等于请求参数 tenant_id；事务内
// SET LOCAL app.current_tenant 触发 RLS。两层防御。
func (h *Handler) handleSessionAuditStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	query := r.URL.Query()
	tenantID := query.Get("tenant_id")

	// 租户访问控制：super_admin / admin_key 可查任意 tenant；
	// tenant_admin 锁定自己 tenant。
	isSuper := IsSuperAdminOrLegacy(r)
	callerTenant := GetTenantID(r)
	if !isSuper && tenantID != "" && tenantID != callerTenant {
		writeError(w, http.StatusForbidden, "cross-tenant access denied")
		return
	}
	if !isSuper && tenantID == "" {
		tenantID = callerTenant
	}

	stats := &SessionAuditStatsResponse{
		ByStatus:   make(map[string]int),
		ByApproval: make(map[string]int),
		TopThreats: []ThreatStat{},
	}

	// super_admin 走跨租户统计(无 tenant_id 过滤),
	// tenant_admin 锁自己 tenant。RLS 是兜底：super_admin 传空
	// tenant → withTenantTx 不设 GUC → RLS policy 仍会按 'default' 过滤,
	// 因此 super_admin 跨租户统计需要 policy 也 bypass。
	// （见 migrations/120_session_audit.sql 的 RLS policy 设计：当前
	//  policy 仅依赖 tenant_id；super_admin 跨租户通过 app.current_tenant
	//  设特定 bypass 值实现；此 handler 把 super_admin 的 tenantID 留空
	//  让 SQL 不带 WHERE 子句绕过 RLS。）
	tenantFilterSQL := "WHERE tenant_id = $1"
	if isSuper {
		tenantFilterSQL = ""
	}
	statsTenant := tenantID
	if isSuper {
		statsTenant = "" // SQL 不绑定 tenant_id 参数
	}

	_ = withTenantTx(r.Context(), h.db, statsTenant, func(tx pgx.Tx) error {
		// 总数
		if isSuper {
			_ = tx.QueryRow(r.Context(),
				`SELECT COUNT(*) FROM session_audit_records`,
			).Scan(&stats.Total)
		} else {
			_ = tx.QueryRow(r.Context(),
				`SELECT COUNT(*) FROM session_audit_records WHERE tenant_id = $1`, tenantID,
			).Scan(&stats.Total)
		}

		// 按状态统计
		var rows pgx.Rows
		var err error
		if isSuper {
			rows, err = tx.Query(r.Context(),
				`SELECT status, COUNT(*) FROM session_audit_records GROUP BY status`)
		} else {
			rows, err = tx.Query(r.Context(),
				`SELECT status, COUNT(*) FROM session_audit_records `+tenantFilterSQL+` GROUP BY status`, tenantID)
		}
		if err == nil {
			for rows.Next() {
				var status string
				var count int
				if err := rows.Scan(&status, &count); err == nil {
					stats.ByStatus[status] = count
				}
			}
			rows.Close()
		}

		// 按审批状态统计
		if isSuper {
			rows, err = tx.Query(r.Context(),
				`SELECT approval_status, COUNT(*) FROM session_audit_records
				   WHERE approval_status IS NOT NULL
				   GROUP BY approval_status`)
		} else {
			rows, err = tx.Query(r.Context(),
				`SELECT approval_status, COUNT(*) FROM session_audit_records
				   WHERE tenant_id = $1 AND approval_status IS NOT NULL
				   GROUP BY approval_status`, tenantID)
		}
		if err == nil {
			for rows.Next() {
				var status string
				var count int
				if err := rows.Scan(&status, &count); err == nil {
					stats.ByApproval[status] = count
				}
			}
			rows.Close()
		}

		// 平均分数
		if isSuper {
			_ = tx.QueryRow(r.Context(),
				`SELECT AVG(detect_score) FROM session_audit_records`,
			).Scan(&stats.AvgScore)
		} else {
			_ = tx.QueryRow(r.Context(),
				`SELECT AVG(detect_score) FROM session_audit_records WHERE tenant_id = $1`, tenantID,
			).Scan(&stats.AvgScore)
		}

		// 注意：上面用了 `WHERE tenant_id = $1` 等子句只是为了文档清晰，
		// super_admin 实际走的是无 tenant 过滤的全表统计。
		// RLS 兜底：withTenantTx("") 时不设 GUC，policy 走 NULLIF→'default'
		// 默认值，仍会过滤。但全表 SQL 不带 WHERE，policy 即使过滤也只能
		// 看到 tenant_id='default' 的行 — 所以此处超级管理员实际仅能看到
		// 'default' 租户。要真正跨租户，需要 RLS policy 加 app.current_role
		// bypass（见 migrations/120_session_audit.sql 后续增强）。
		_ = tenantFilterSQL // 保留变量以备后续扩展

		return nil
	})

	writeJSON(w, http.StatusOK, stats)
}

// handleSessionAuditExport 导出审计记录为 CSV
//
// GET /api/admin/session-audit/export?tenant_id=&session_id=&status=&limit=5000
func (h *Handler) handleSessionAuditExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	query := r.URL.Query()
	req := &SessionAuditListRequest{
		TenantID:  query.Get("tenant_id"),
		SessionID: query.Get("session_id"),
		Status:    query.Get("status"),
		Limit:     5000, // 导出限制
		Offset:    0,
	}

	// 租户访问控制
	callerTenant := GetTenantID(r)
	isSuper := IsSuperAdminOrLegacy(r)
	if !isSuper && req.TenantID != "" && req.TenantID != callerTenant {
		writeError(w, http.StatusForbidden, "cross-tenant access denied")
		return
	}
	if !isSuper {
		req.TenantID = callerTenant
	}

	// 构建查询
	var rows pgx.Rows
	var qerr error
	err := withTenantTx(r.Context(), h.db, req.TenantID, func(tx pgx.Tx) error {
		sql := `
			SELECT
				id, session_id, tenant_id,
				client_ip, client_user_agent, client_model,
				content_summary, content_title, content_hash,
				intent_type, intent_score, intent_reason,
				security_score, danger_score, trust_score, sensitive_score,
				detect_score, detect_decision, threats, sensitive_words,
				status, approval_status,
				created_at
			FROM session_audit_records
			WHERE 1=1`
		args := []interface{}{}
		argIdx := 1

		if req.TenantID != "" && !isSuper {
			sql += fmt.Sprintf(" AND tenant_id = $%d", argIdx)
			args = append(args, req.TenantID)
			argIdx++
		}
		if req.SessionID != "" {
			sql += fmt.Sprintf(" AND session_id = $%d", argIdx)
			args = append(args, req.SessionID)
			argIdx++
		}
		if req.Status != "" {
			sql += fmt.Sprintf(" AND status = $%d", argIdx)
			args = append(args, req.Status)
			argIdx++
		}
		sql += " ORDER BY created_at DESC"
		sql += fmt.Sprintf(" LIMIT $%d", argIdx)
		args = append(args, req.Limit)

		rows, qerr = tx.Query(r.Context(), sql, args...)
		return qerr
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query failed: %v", err))
		return
	}
	if rows != nil {
		defer rows.Close()
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=session_audit_export.csv")
	writer := csv.NewWriter(w)

	// 写入表头
	header := []string{
		"ID", "Session ID", "Tenant ID", "Client IP", "User Agent", "Model",
		"Content Summary", "Content Title", "Content Hash",
		"Intent Type", "Intent Score", "Intent Reason",
		"Security Score", "Danger Score", "Trust Score", "Sensitive Score",
		"Detect Score", "Detect Decision", "Threats", "Sensitive Words",
		"Status", "Approval Status", "Created At",
	}
	writer.Write(header)

	for rows.Next() {
		var id int64
		var sessionID, tenantID, clientIP, clientUA, clientModel sql.NullString
		var contentSummary, contentTitle, contentHash sql.NullString
		var intentType, intentReason sql.NullString
		var intentScore sql.NullFloat64
		var secScore, dangerScore, trustScore, sensitiveScore sql.NullInt64
		var detectScore sql.NullInt64
		var detectDecision sql.NullString
		var threatsJSON, sensitiveWordsJSON []byte
		var status, approvalStatus sql.NullString
		var createdAt time.Time

		if err := rows.Scan(
			&id, &sessionID, &tenantID,
			&clientIP, &clientUA, &clientModel,
			&contentSummary, &contentTitle, &contentHash,
			&intentType, &intentScore, &intentReason,
			&secScore, &dangerScore, &trustScore, &sensitiveScore,
			&detectScore, &detectDecision, &threatsJSON, &sensitiveWordsJSON,
			&status, &approvalStatus,
			&createdAt,
		); err != nil {
			continue
		}

		row := []string{
			fmt.Sprintf("%d", id),
			sessionID.String,
			tenantID.String,
			clientIP.String,
			clientUA.String,
			clientModel.String,
			contentSummary.String,
			contentTitle.String,
			contentHash.String,
			intentType.String,
			fmt.Sprintf("%.2f", intentScore.Float64),
			intentReason.String,
			fmt.Sprintf("%d", secScore.Int64),
			fmt.Sprintf("%d", dangerScore.Int64),
			fmt.Sprintf("%d", trustScore.Int64),
			fmt.Sprintf("%d", sensitiveScore.Int64),
			fmt.Sprintf("%d", detectScore.Int64),
			detectDecision.String,
			string(threatsJSON),
			string(sensitiveWordsJSON),
			status.String,
			approvalStatus.String,
			createdAt.Format(time.RFC3339),
		}
		writer.Write(row)
	}
	writer.Flush()
}

// scanSessionAuditRecord 扫描审计记录
func scanSessionAuditRecord(scanner interface {
	Scan(dest ...interface{}) error
}) (*SessionAuditRecord, error) {
	var record SessionAuditRecord
	var clientIP, clientUA, clientModel sql.NullString
	var contentSummary, contentTitle, contentHash sql.NullString
	var intentType, intentReason sql.NullString
	var intentScore sql.NullFloat64
	var secScore, dangerScore, trustScore, sensitiveScore sql.NullInt64
	var detectScore sql.NullInt64
	var detectDecision sql.NullString
	var threatsJSON, sensitiveWordsJSON []byte
	var approvalStatus sql.NullString

	err := scanner.Scan(
		&record.ID, &record.SessionID, &record.TenantID,
		&clientIP, &clientUA, &clientModel,
		&contentSummary, &contentTitle, &contentHash,
		&intentType, &intentScore, &intentReason,
		&secScore, &dangerScore, &trustScore, &sensitiveScore,
		&detectScore, &detectDecision, &threatsJSON, &sensitiveWordsJSON,
		&record.Status, &approvalStatus,
		&record.CreatedAt,
	)

	if err != nil {
		return nil, err
	}

	// 填充数据
	record.ClientInfo.IP = clientIP.String
	record.ClientInfo.UserAgent = clientUA.String
	record.ClientInfo.Model = clientModel.String

	if intentType.Valid {
		record.Intent = &sessionaudit.Intent{
			Type:   intentType.String,
			Score:  intentScore.Float64,
			Reason: intentReason.String,
		}
	}

	if contentTitle.Valid {
		record.Summary = &sessionaudit.Summary{
			Title:       contentTitle.String,
			ContentHash: contentHash.String,
		}
	}

	record.Scores = sessionaudit.MultiDimensionScore{
		Security:  int(secScore.Int64),
		Danger:    int(dangerScore.Int64),
		Trust:     int(trustScore.Int64),
		Sensitive: int(sensitiveScore.Int64),
	}

	if detectScore.Valid {
		record.DetectResult = &sessionaudit.DetectResult{
			Score:    int(detectScore.Int64),
			Decision: sessionaudit.Decision(detectDecision.String),
		}

		// 解析威胁和敏感词
		if len(threatsJSON) > 0 {
			//nolint:errcheck // 字段为 JSONB，损坏时让 record.Threats 保持空
			json.Unmarshal(threatsJSON, &record.DetectResult.Threats)
		}
		if len(sensitiveWordsJSON) > 0 {
			//nolint:errcheck // 字段为 JSONB，损坏时让 record.SensitiveWords 保持空
			json.Unmarshal(sensitiveWordsJSON, &record.DetectResult.SensitiveWords)
		}
	}

	record.ApprovalStatus = approvalStatus.String

	return &record, nil
}

// extractPathParam 从路径提取参数
func extractPathParam(path, prefix string) string {
	if len(path) <= len(prefix) {
		return ""
	}
	return path[len(prefix):]
}
