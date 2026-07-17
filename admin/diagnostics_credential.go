package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// =============================================================================
// 2026-07-12: 应急诊断接口 — 连续异常时管理员手动恢复用
// 返回：错误现象 + 最近的 log + 同一 credential 的所有可用供应商 + 推荐恢复动作
// =============================================================================

type diagFailure struct {
	RequestID   string    `json:"request_id"`
	ClientModel string    `json:"client_model"`
	Ts          time.Time `json:"ts"`
	ErrorKind   string    `json:"error_kind"`
	Status      string    `json:"status"`
	Provider    int       `json:"provider_id"`
}

type diagBinding struct {
	RawModelName         string     `json:"raw_model_name"`
	ProviderID           int        `json:"provider_id"`
	ProviderCode         string     `json:"provider_code"`
	Available            bool       `json:"available"`
	UnavailableReason    string     `json:"unavailable_reason"`
	UnavailableRecoverAt *time.Time `json:"unavailable_recover_at,omitempty"`
}

type diagCredential struct {
	CredentialID        int           `json:"credential_id"`
	Label               string        `json:"label"`
	ProviderID          int           `json:"provider_id"`
	ProviderCode        string        `json:"provider_code"`
	AvailabilityState   string        `json:"availability_state"`
	HealthStatus        string        `json:"health_status"`
	StateReasonCode     string        `json:"state_reason_code"`
	StateReasonDetail   string        `json:"state_reason_detail"`
	CircuitState        string        `json:"circuit_state"`
	ConsecutiveFailures int           `json:"consecutive_failures"`
	BalanceUSD          *float64      `json:"balance_usd,omitempty"`
	Bindings            []diagBinding `json:"bindings"`
	RecentFailures      []diagFailure `json:"recent_failures"`
	RecentFailuresCount int           `json:"recent_failures_count"`
	Analysis            string        `json:"analysis"`
	Recommendation      string        `json:"recommendation"`
	Recoverable         bool          `json:"recoverable"`
}

// handleCredentialDiagnostic 诊断指定 credential 当前状态
// GET /api/admin/diagnostics/credential?id={credID}&minutes=15
func (h *Handler) handleCredentialDiagnostic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	credIDStr := q.Get("id")
	credID, err := strconv.Atoi(credIDStr)
	if err != nil || credID <= 0 {
		writeError(w, http.StatusBadRequest, "missing or invalid id query parameter")
		return
	}
	minutes := 15
	if m := q.Get("minutes"); m != "" {
		if v, err := strconv.Atoi(m); err == nil && v > 0 && v <= 1440 {
			minutes = v
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	diag, err := buildCredentialDiagnostic(ctx, h.db, credID, minutes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("diagnostic failed: %v", err))
		return
	}
	if diag == nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	writeJSON(w, http.StatusOK, diag)
}

func buildCredentialDiagnostic(ctx context.Context, db *pgxpool.Pool, credID, minutes int) (*diagCredential, error) {
	d := &diagCredential{
		CredentialID:   credID,
		Bindings:       []diagBinding{},
		RecentFailures: []diagFailure{},
	}
	// 1. credentials 表
	err := db.QueryRow(ctx, `
		SELECT c.label, c.provider_id, p.code, c.availability_state, c.health_status,
		       COALESCE(c.state_reason_code,''), COALESCE(c.state_reason_detail,''),
		       c.circuit_state, c.consecutive_failures, c.balance_usd
		FROM credentials c JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
	`, credID).Scan(&d.Label, &d.ProviderID, &d.ProviderCode, &d.AvailabilityState,
		&d.HealthStatus, &d.StateReasonCode, &d.StateReasonDetail,
		&d.CircuitState, &d.ConsecutiveFailures, &d.BalanceUSD)
	if err != nil {
		return nil, fmt.Errorf("credential lookup: %w", err)
	}

	// 2. 同 credential 的所有 binding
	rows, err := db.Query(ctx, `
		SELECT pm.raw_model_name, cmb.provider_id, p.code,
		       cmb.available, COALESCE(cmb.unavailable_reason,''), cmb.unavailable_recover_at
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN providers p ON p.id = cmb.provider_id
		WHERE cmb.credential_id = $1
		ORDER BY pm.raw_model_name
	`, credID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var b diagBinding
			if err := rows.Scan(&b.RawModelName, &b.ProviderID, &b.ProviderCode,
				&b.Available, &b.UnavailableReason, &b.UnavailableRecoverAt); err == nil {
				d.Bindings = append(d.Bindings, b)
			}
		}
	}

	// 3. 最近失败记录
	frows, err := db.Query(ctx, `
		SELECT request_id, client_model, ts, error_kind, COALESCE(request_status::text,''), provider_id
		FROM request_logs_hot
		WHERE credential_id = $1 AND lower(COALESCE(request_status, '')) = 'failure'
		  AND ts > now() - ($2 || ' minutes')::interval
		ORDER BY ts DESC LIMIT 20
	`, credID, fmt.Sprintf("%d", minutes))
	if err == nil {
		defer frows.Close()
		for frows.Next() {
			var f diagFailure
			if err := frows.Scan(&f.RequestID, &f.ClientModel, &f.Ts,
				&f.ErrorKind, &f.Status, &f.Provider); err == nil {
				d.RecentFailures = append(d.RecentFailures, f)
			}
		}
	}
	d.RecentFailuresCount = len(d.RecentFailures)

	// 4. 推导分析与建议
	d.Analysis, d.Recommendation, d.Recoverable = analyzeDiag(d, minutes)

	return d, nil
}

func analyzeDiag(d *diagCredential, minutes int) (analysis, recommendation string, recoverable bool) {
	// 收集
	unavailBindings := 0
	for _, b := range d.Bindings {
		if !b.Available || b.UnavailableRecoverAt != nil {
			unavailBindings++
		}
	}

	switch {
	case d.AvailabilityState == "auth_failed":
		return "API key 无效或已被吊销（auth_failed）",
			"请检查 provider 后台是否提示 key 失效；更新 key 后调用 force-recover 重置。", false
	case strings.Contains(d.StateReasonDetail, "balance") || strings.Contains(d.StateReasonDetail, "billing"):
		return "上游余额/订阅耗尽（balance/billing）",
			"请充值或检查订阅；恢复后调用 force-recover。", true
	case d.AvailabilityState == "suspended":
		return "凭据被人工暂停（suspended）",
			"调用 admin suspend 端点解除暂停，或新建凭据替换。", false
	case d.AvailabilityState == "rate_limited":
		return "上游限流中（rate_limited）",
			"等待 provider 限流恢复（通常 1-5 分钟），或切换到其他供应商。", true
	case d.CircuitState == "open":
		return fmt.Sprintf("本地熔断已打开（consecutive_failures=%d）", d.ConsecutiveFailures),
			"如上游已恢复，调用 force-recover 立即重置。", true
	case d.CircuitState == "half_open":
		return "熔断器半开探测中",
			"等待下一次探测结果；如需立刻恢复，可调用 force-recover。", true
	case unavailBindings == len(d.Bindings) && len(d.Bindings) > 0:
		return "该凭据的所有模型绑定都被标记为不可用",
			"调用 force-recover 同时清空 credential 与 binding 状态。", true
	case d.RecentFailuresCount >= 5 && d.HealthStatus == "warning":
		return fmt.Sprintf("最近 %d 分钟内 %d 次失败，路由已触发冷却", minutes, d.RecentFailuresCount),
			"如上游实际可用，调用 force-recover 即可恢复；否则检查上游日志。", true
	case d.RecentFailuresCount >= 5:
		return fmt.Sprintf("最近 %d 分钟内 %d 次失败（高频异常）", minutes, d.RecentFailuresCount),
			"调用 force-recover 重置熔断与 binding；并查看上游可用性。", true
	case d.RecentFailuresCount >= 3:
		return fmt.Sprintf("最近 %d 分钟内 %d 次失败，疑似间歇性故障", minutes, d.RecentFailuresCount),
			"继续观察；如失败持续上升再 force-recover。", true
	default:
		return "凭据状态正常，无显著异常",
			"无需操作；如个别请求仍失败，请检查 request_id 的具体日志。", true
	}
}

// handleForceRecoverSingle 单凭据立即恢复
// POST /api/admin/diagnostics/credential/force-recover?id={credID}
func (h *Handler) handleForceRecoverSingle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	credID, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil || credID <= 0 {
		writeError(w, http.StatusBadRequest, "missing or invalid id query parameter")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 1. 清 credential
	if _, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state = 'ready',
		    availability_recover_at = NULL,
		    circuit_state = 'closed',
		    consecutive_failures = 0,
		    state_reason_code = NULL,
		    state_reason_detail = 'admin force-recover via diagnostic',
		    state_updated_at = now()
		WHERE id = $1
	`, credID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("credential update failed: %v", err))
		return
	}
	// 2. 清该 credential 的所有 binding
	if _, err := h.db.Exec(ctx, `
		UPDATE credential_model_bindings SET
		    available = true,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    updated_at = now()
		WHERE credential_id = $1
	`, credID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("binding update failed: %v", err))
		return
	}
	// 3. 清 model_probe_state 中 recovering 状态
	if _, err := h.db.Exec(ctx, `
		UPDATE model_probe_state SET
		    state = 'healthy_confirmed',
		    consecutive_successes = 5,
		    consecutive_failures = 0,
		    next_retry_at = now() + interval '4 hours',
		    last_state_change_at = now()
		WHERE credential_id = $1 AND state IN ('recovering', 'unknown', 'suspicious')
	`, credID); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("probe state update failed: %v", err))
		return
	}
	// 4. invalidate routing caches (触发路由器重载)
	invalidateRoutingCaches(r.Context(), h.db, "credentials", credID)

	writeJSON(w, http.StatusOK, map[string]any{
		"triggered":     true,
		"credential_id": credID,
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
		"message":       "凭据已强制恢复，credential / binding / probe_state 已重置",
	})
}
