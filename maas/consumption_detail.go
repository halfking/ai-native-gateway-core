package maas

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ConsumptionDetailRow is the smallest auditable billing unit: one tenant,
// user, provider credential, and model combination.
type ConsumptionDetailRow struct {
	TenantID                string  `json:"tenant_id"`
	OwnerUser               string  `json:"owner_user,omitempty"`
	ProviderID              *int64  `json:"provider_id,omitempty"`
	ProviderName            string  `json:"provider_name"`
	CredentialID            *int64  `json:"credential_id,omitempty"`
	CredentialLabel         string  `json:"credential_label"`
	CanonicalID             *int64  `json:"canonical_id,omitempty"`
	Model                   string  `json:"model"`
	Requests                int64   `json:"requests"`
	PromptTokens            int64   `json:"prompt_tokens"`
	CompletionTokens        int64   `json:"completion_tokens"`
	CacheReadTokens         int64   `json:"cache_read_tokens"`
	CacheWriteTokens        int64   `json:"cache_write_tokens"`
	CreditsCharged          int64   `json:"credits_charged"`
	UpstreamCostUSD         float64 `json:"upstream_cost_usd"`
	TenantRevenueUSD        float64 `json:"tenant_revenue_usd"`
	GrossMarginUSD          float64 `json:"gross_margin_usd"`
	GrossMarginRate         float64 `json:"gross_margin_rate"`
	CancelledBilledRequests int64   `json:"cancelled_billed_requests"`
}

// ConsumptionDetail is the response for an administrator's billing audit.
type ConsumptionDetail struct {
	TenantID       string                 `json:"tenant_id"`
	OwnerUser      string                 `json:"owner_user,omitempty"`
	Days           int                    `json:"days"`
	CentsPerCredit float64                `json:"cents_per_credit"`
	Rows           []ConsumptionDetailRow `json:"rows"`
}

func (s *Service) QueryConsumptionDetail(ctx context.Context, tenantID, ownerUser string, days int) (ConsumptionDetail, error) {
	if !s.Enabled() {
		return ConsumptionDetail{}, fmt.Errorf("maas service not enabled")
	}
	if strings.TrimSpace(tenantID) == "" {
		return ConsumptionDetail{}, fmt.Errorf("tenant_id required")
	}
	days = ClampUsageDays(days)
	ownerUser = strings.TrimSpace(ownerUser)
	logsTable, alias := requestLogsSource(days)

	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var centsPerCredit float64
	if err := s.pool.QueryRow(queryCtx, `SELECT cents_per_credit::float8 FROM maas_settings WHERE id = 1`).Scan(&centsPerCredit); err != nil {
		return ConsumptionDetail{}, fmt.Errorf("load credit conversion: %w", err)
	}

	query := `
		SELECT ` + alias + `.tenant_id,
		       COALESCE(` + alias + `.api_key_owner_user, ''),
		       ` + alias + `.provider_id,
		       COALESCE(p.display_name, ''),
		       ` + alias + `.credential_id,
		       COALESCE(c.label, ''),
		       ` + alias + `.canonical_id,
		       COALESCE(mc.canonical_name, NULLIF(` + alias + `.outbound_model, ''), NULLIF(` + alias + `.client_model, ''), 'unknown'),
		       COUNT(*)::bigint,
		       COALESCE(SUM(` + alias + `.prompt_tokens), 0)::bigint,
		       COALESCE(SUM(` + alias + `.completion_tokens), 0)::bigint,
		       COALESCE(SUM(` + alias + `.cache_read_tokens), 0)::bigint,
		       COALESCE(SUM(` + alias + `.cache_write_tokens), 0)::bigint,
		       COALESCE(SUM(` + alias + `.credits_charged), 0)::bigint,
		       COALESCE(SUM(` + alias + `.cost_usd), 0)::float8,
		       COUNT(*) FILTER (WHERE COALESCE(` + alias + `.credits_charged, 0) > 0
		                          AND COALESCE(` + alias + `.stream_interrupted, false)
		                          AND lower(COALESCE(` + alias + `.failure_detail_code, ` + alias + `.error_kind, ''))
		                              IN ('client_cancel', 'client_disconnected'))::bigint
		FROM ` + logsTable + `
		LEFT JOIN providers p ON p.id = ` + alias + `.provider_id
		LEFT JOIN credentials c ON c.id = ` + alias + `.credential_id
		LEFT JOIN models_canonical mc ON mc.id = ` + alias + `.canonical_id
		WHERE ` + alias + `.tenant_id = $1
		  AND ` + alias + `.ts >= now() - ($2 * INTERVAL '1 day')
		  AND ` + alias + `.credits_charged IS NOT NULL
	`
	args := []any{tenantID, days}
	if ownerUser != "" {
		query += " AND " + alias + ".api_key_owner_user = $3\n"
		args = append(args, ownerUser)
	}
	query += `
		GROUP BY ` + alias + `.tenant_id, ` + alias + `.api_key_owner_user, ` + alias + `.provider_id, p.display_name,
		         ` + alias + `.credential_id, c.label, ` + alias + `.canonical_id, mc.canonical_name,
		         ` + alias + `.outbound_model, ` + alias + `.client_model
		ORDER BY SUM(` + alias + `.credits_charged) DESC, COUNT(*) DESC`

	rows, err := s.pool.Query(queryCtx, query, args...)
	if err != nil {
		return ConsumptionDetail{}, fmt.Errorf("query consumption detail: %w", err)
	}
	defer rows.Close()

	out := ConsumptionDetail{TenantID: tenantID, OwnerUser: ownerUser, Days: days, CentsPerCredit: centsPerCredit, Rows: []ConsumptionDetailRow{}}
	for rows.Next() {
		var row ConsumptionDetailRow
		if err := rows.Scan(&row.TenantID, &row.OwnerUser, &row.ProviderID, &row.ProviderName,
			&row.CredentialID, &row.CredentialLabel, &row.CanonicalID, &row.Model,
			&row.Requests, &row.PromptTokens, &row.CompletionTokens, &row.CacheReadTokens,
			&row.CacheWriteTokens, &row.CreditsCharged, &row.UpstreamCostUSD,
			&row.CancelledBilledRequests); err != nil {
			return ConsumptionDetail{}, fmt.Errorf("scan consumption detail: %w", err)
		}
		row.TenantRevenueUSD = float64(row.CreditsCharged) * centsPerCredit / 100
		row.GrossMarginUSD = row.TenantRevenueUSD - row.UpstreamCostUSD
		if row.TenantRevenueUSD != 0 {
			row.GrossMarginRate = row.GrossMarginUSD / row.TenantRevenueUSD
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return ConsumptionDetail{}, fmt.Errorf("iterate consumption detail: %w", err)
	}
	return out, nil
}
