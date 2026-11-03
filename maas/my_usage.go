package maas

// W3-T1（2026-11-04 运营管理与价格计费体系，总体设计 §2.5 用户级 API 面）：
// 「我的」自助查询底座 —— 请求历史（按人收窄）/ 预算 / 活动。
//
// owner 收窄（A-11）：成员仅本人（api_key_owner_user = JWT username），
// 管理员全租户由 admin 层判定（见 admin/maas_my_handlers.go）。
// 前置核实（W3-T0，211 真库抽样 2026-11-04）：api_key_owner_user 与
// users.username 同空间（huangxt / kaixuan-1 / …），映射成立。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MyRequestRow is one request-history row for the owner-scoped view.
type MyRequestRow struct {
	RequestID        string   `json:"request_id"`
	Ts               string   `json:"ts"`
	Model            string   `json:"model"`
	ProviderID       *int64   `json:"provider_id"`
	ProviderName     string   `json:"provider_name"`
	CredentialID     *int64   `json:"credential_id"`
	CredentialLabel  string   `json:"credential_label"`
	PromptTokens     int64    `json:"prompt_tokens"`
	CompletionTokens int64    `json:"completion_tokens"`
	CacheReadTokens  int64    `json:"cache_read_tokens"`
	Success          *bool    `json:"success"`
	ErrorKind        *string  `json:"error_kind"`
	CreditsCharged   *int64   `json:"credits_charged"`
	CostUSD          *float64 `json:"cost_usd"`
	LatencyMs        *int     `json:"latency_ms"`
}

// MyRequestsPage is one page of the owner-scoped request history plus the
// window summary (四层下钻的「请求」层，账单→用量→请求共用筛选器).
type MyRequestsPage struct {
	TenantID  string          `json:"tenant_id"`
	OwnerUser string          `json:"owner_user"`
	Days      int             `json:"days"`
	Page      int             `json:"page"`
	PageSize  int             `json:"page_size"`
	Total     int64           `json:"total"`
	Summary   MyRequestTotals `json:"summary"`
	Rows      []MyRequestRow  `json:"rows"`
}

// MyRequestTotals aggregates the filtered window (not just the page).
type MyRequestTotals struct {
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	CreditsCharged   int64   `json:"credits_charged"`
	CostUSD          float64 `json:"cost_usd"`
}

// ClampMyRequestsDays bounds the lookback window (usage.go 的 ClampUsageDays
// 同族口径；「我的」面给更小的上限，防止深翻页拖垮分区扫描).
func ClampMyRequestsDays(days int) int {
	if days <= 0 {
		return 7
	}
	if days > 31 {
		return 31
	}
	return days
}

// QueryMyRequests reads one owner's request history with a shared filter
// (days/model) and page/window-summary split. ownerUser 空 = 不过滤（管理面
// 全租户视角专用；租户成员路径永远带 owner）。
func (s *Service) QueryMyRequests(ctx context.Context, tenantID, ownerUser string, days int, model string, page, pageSize int) (MyRequestsPage, error) {
	out := MyRequestsPage{TenantID: tenantID, OwnerUser: ownerUser, Rows: []MyRequestRow{}}
	if !s.Enabled() {
		return out, fmt.Errorf("maas service not enabled")
	}
	if strings.TrimSpace(tenantID) == "" {
		return out, fmt.Errorf("tenant_id required")
	}
	days = ClampMyRequestsDays(days)
	out.Days = days
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	out.Page, out.PageSize = page, pageSize

	logsTable, alias := requestLogsSource(days)
	where := alias + `.tenant_id = $1
		  AND ` + alias + `.ts >= now() - ($2 * INTERVAL '1 day')`
	args := []any{tenantID, days}
	if ownerUser != "" {
		args = append(args, ownerUser)
		where += fmt.Sprintf("\n\t\t  AND COALESCE(%s.api_key_owner_user, '') = $%d", alias, len(args))
	}
	if model != "" {
		args = append(args, model)
		where += fmt.Sprintf("\n\t\t  AND (%s.outbound_model = $%d OR %s.client_model = $%d OR COALESCE((SELECT mc.canonical_name FROM models_canonical mc WHERE mc.id = %s.canonical_id), '') = $%d)",
			alias, len(args), alias, len(args), alias, len(args))
	}

	totalsQuery := `
		SELECT COUNT(*)::bigint,
		       COALESCE(SUM(` + alias + `.prompt_tokens), 0)::bigint,
		       COALESCE(SUM(` + alias + `.completion_tokens), 0)::bigint,
		       COALESCE(SUM(` + alias + `.credits_charged), 0)::bigint,
		       COALESCE(SUM(` + alias + `.cost_usd), 0)::float8
		FROM ` + logsTable + `
		WHERE ` + where
	if err := s.pool.QueryRow(ctx, totalsQuery, args...).Scan(
		&out.Summary.Requests, &out.Summary.PromptTokens, &out.Summary.CompletionTokens,
		&out.Summary.CreditsCharged, &out.Summary.CostUSD,
	); err != nil {
		return out, fmt.Errorf("query my requests totals: %w", err)
	}
	out.Total = out.Summary.Requests

	rowsQuery := `
		SELECT ` + alias + `.request_id,
		       ` + alias + `.ts::text,
		       COALESCE(mc.canonical_name, NULLIF(` + alias + `.outbound_model, ''), NULLIF(` + alias + `.client_model, ''), 'unknown'),
		       ` + alias + `.provider_id,
		       COALESCE(p.display_name, ''),
		       ` + alias + `.credential_id,
		       COALESCE(c.label, ''),
		       COALESCE(` + alias + `.prompt_tokens, 0),
		       COALESCE(` + alias + `.completion_tokens, 0),
		       COALESCE(` + alias + `.cache_read_tokens, 0),
		       ` + alias + `.success,
		       ` + alias + `.error_kind,
		       ` + alias + `.credits_charged,
		       ` + alias + `.cost_usd,
		       ` + alias + `.latency_ms
		FROM ` + logsTable + `
		LEFT JOIN providers p ON p.id = ` + alias + `.provider_id
		LEFT JOIN credentials c ON c.id = ` + alias + `.credential_id
		LEFT JOIN models_canonical mc ON mc.id = ` + alias + `.canonical_id
		WHERE ` + where + `
		ORDER BY ` + alias + `.ts DESC
		LIMIT $` + fmt.Sprintf("%d", len(args)+1) + ` OFFSET $` + fmt.Sprintf("%d", len(args)+2)
	rows, err := s.pool.Query(ctx, rowsQuery, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return out, fmt.Errorf("query my requests rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row MyRequestRow
		if err := rows.Scan(&row.RequestID, &row.Ts, &row.Model, &row.ProviderID, &row.ProviderName,
			&row.CredentialID, &row.CredentialLabel,
			&row.PromptTokens, &row.CompletionTokens, &row.CacheReadTokens,
			&row.Success, &row.ErrorKind, &row.CreditsCharged, &row.CostUSD, &row.LatencyMs); err != nil {
			return out, fmt.Errorf("scan my request row: %w", err)
		}
		out.Rows = append(out.Rows, row)
	}
	return out, rows.Err()
}

// TenantBudget mirrors public.tenant_budgets (938) for the self-service API.
type TenantBudget struct {
	TenantID              string         `json:"tenant_id"`
	MonthlyBudgetCents    int64          `json:"monthly_budget_cents"`
	BalanceAlertThreshold float64        `json:"balance_alert_threshold"`
	QuotaAlertThreshold   float64        `json:"quota_alert_threshold"`
	NotifyChannels        map[string]any `json:"notify_channels"`
	UpdatedAt             string         `json:"updated_at,omitempty"`
}

// GetTenantBudget reads the tenant's budget row; missing row = zero budget
// (预算未设置，不是 0 预算 —— 展示层据此区分「未配置」与「配置为 0」).
func (s *Service) GetTenantBudget(ctx context.Context, tenantID string) (TenantBudget, error) {
	out := TenantBudget{TenantID: tenantID, NotifyChannels: map[string]any{}}
	if !s.Enabled() {
		return out, fmt.Errorf("maas service not enabled")
	}
	var channels []byte
	err := s.pool.QueryRow(ctx, `
		SELECT monthly_budget_cents, balance_alert_threshold::float8,
		       quota_alert_threshold::float8, notify_channels, updated_at::text
		FROM tenant_budgets WHERE tenant_id = $1
	`, tenantID).Scan(&out.MonthlyBudgetCents, &out.BalanceAlertThreshold,
		&out.QuotaAlertThreshold, &channels, &out.UpdatedAt)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return out, nil
		}
		return out, err
	}
	if len(channels) > 0 {
		_ = json.Unmarshal(channels, &out.NotifyChannels)
	}
	return out, nil
}

// UpsertTenantBudget creates/updates the tenant's own budget row (938).
// RLS 是租户隔离载体，但本路径仍以应用层 tenant_id 参数为单一来源。
func (s *Service) UpsertTenantBudget(ctx context.Context, tenantID string, monthlyBudgetCents int64, balanceAlert, quotaAlert float64, notifyChannels map[string]any) (TenantBudget, error) {
	if !s.Enabled() {
		return TenantBudget{}, fmt.Errorf("maas service not enabled")
	}
	if strings.TrimSpace(tenantID) == "" {
		return TenantBudget{}, fmt.Errorf("tenant_id required")
	}
	if monthlyBudgetCents < 0 {
		return TenantBudget{}, fmt.Errorf("monthly_budget_cents must be >= 0")
	}
	if balanceAlert < 0 || balanceAlert > 1 || quotaAlert < 0 || quotaAlert > 1 {
		return TenantBudget{}, fmt.Errorf("alert thresholds must be within [0,1]")
	}
	if notifyChannels == nil {
		notifyChannels = map[string]any{}
	}
	channels, err := json.Marshal(notifyChannels)
	if err != nil {
		return TenantBudget{}, err
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO tenant_budgets (tenant_id, monthly_budget_cents, balance_alert_threshold, quota_alert_threshold, notify_channels)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET monthly_budget_cents = EXCLUDED.monthly_budget_cents,
		       balance_alert_threshold = EXCLUDED.balance_alert_threshold,
		       quota_alert_threshold = EXCLUDED.quota_alert_threshold,
		       notify_channels = EXCLUDED.notify_channels,
		       updated_at = now()
	`, tenantID, monthlyBudgetCents, balanceAlert, quotaAlert, channels); err != nil {
		return TenantBudget{}, err
	}
	return s.GetTenantBudget(ctx, tenantID)
}
