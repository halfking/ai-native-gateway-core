package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/metrics"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/maas"
	"github.com/kaixuan/llm-gateway-go/settings"
)

type projectedMaaSDetailRow struct {
	maas.ConsumptionDetailRow
	Upstream                    *float64 `json:"upstream_cost_usd"`
	PostedRevenueUSD            *float64 `json:"tenant_revenue_usd"`
	GrossMarginUSD              *float64 `json:"gross_margin_usd"`
	GrossMarginRate             *float64 `json:"gross_margin_rate"`
	CostRowsOutsideUSD          *int64   `json:"cost_rows_outside_usd"`
	CostRowsWithoutUSDEvidence  *int64   `json:"cost_rows_without_usd_evidence"`
	BilledWithoutCancelEvidence int64    `json:"billed_without_cancellation_evidence"`
	EstimatedRevenueUSD         *float64 `json:"estimated_revenue_usd"`
	EstimatedMarginUSD          *float64 `json:"estimated_margin_usd"`
	EstimatedMarginRate         *float64 `json:"estimated_margin_rate"`
}

type maasCreditReference struct {
	CentsPerCredit float64 `json:"cents_per_credit"`
	Currency       string  `json:"currency"`
}

// Maas tenant-facing routes are tenant-scoped. Personal routes use API-key
// ownership, which is independent of the owner recorded on a session.
func maasUsageScopeV2(r *http.Request, personal bool) (tenant, owner string, ok bool) {
	auth := GetAuthContext(r)
	if auth == nil {
		return "", "", false
	}
	tenant = strings.TrimSpace(auth.TenantID)
	if auth.Role == "admin_key" {
		if tenant == "" {
			tenant = GetTenantID(r)
		}
		return tenant, "", tenant != ""
	}
	if tenant == "" || !auth.IsJWT {
		return "", "", false
	}
	if !personal && !IsRegularUser(r) {
		return tenant, "", auth.Role == "tenant_admin" || auth.Role == "super_admin"
	}
	owner = strings.TrimSpace(auth.Username)
	if personal && auth.Role == "super_admin" {
		if requested := strings.TrimSpace(r.URL.Query().Get("owner_user")); requested != "" {
			owner = requested
		}
	}
	return tenant, owner, owner != ""
}

func (h *Handler) handleMaaSUsageV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	adminTenant := strings.HasPrefix(r.URL.Path, "/api/admin/maas/v2/tenants/")
	personal := strings.HasPrefix(r.URL.Path, "/api/maas/v2/my/")
	section := r.PathValue("section")
	tenant, owner, allowed := maasUsageScopeV2(r, personal)
	includeCost := false
	if adminTenant {
		if !IsSuperAdminOrLegacy(r) {
			writeError(w, http.StatusForbidden, "platform administrator required")
			return
		}
		tenant = strings.TrimSpace(r.PathValue("code"))
		allowed, includeCost = tenant != "", true
		owner = strings.TrimSpace(r.URL.Query().Get("owner_user"))
		if section != "summary" && section != "detail" {
			writeError(w, http.StatusNotFound, "unknown usage section")
			return
		}
	} else if !personal {
		section = "summary"
		if r.URL.Path == "/api/maas/v2/usage/requests" {
			section = "requests"
		}
	} else if section == "usage" {
		section = "detail"
	} else if section != "requests" {
		writeError(w, http.StatusNotFound, "unknown usage section")
		return
	}
	if !allowed || (r.URL.Query().Get("tenant_id") != "" && r.URL.Query().Get("tenant_id") != tenant) {
		writeError(w, http.StatusForbidden, "MaaS tenant or owner scope unavailable")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "request metrics are unavailable")
		return
	}
	query, days, page, err := parseMaaSUsageV2(r, tenant, owner, section, includeCost)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	query.MatchModelAliases = personal
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	result, err := metrics.ReadMaaSUsage(ctx, h.db, query, section)
	if err != nil {
		writeAccountUsageErrorV2(w, err)
		return
	}
	if !result.Ready {
		writeError(w, http.StatusServiceUnavailable, "request metrics backfill is running")
		return
	}
	fields := map[string]any{}
	if section == "detail" {
		rows := []projectedMaaSDetailRow{}
		if err := json.Unmarshal(result.Payload, &rows); err != nil {
			writeAccountUsageErrorV2(w, err)
			return
		}
		if includeCost {
			if ref := h.maasCreditReferenceV2(ctx); ref != nil {
				fields["cents_per_credit"], fields["credit_reference_currency"] = ref.CentsPerCredit, ref.Currency
				if ref.Currency == "CNY" {
					fields["credit_reference_cny_per_usd"] = settings.CNYToUSDFX
				}
				fields["estimate_basis"] = "current_credit_reference"
				for i := range rows {
					setMaaSCreditEstimate(&rows[i], *ref)
				}
			}
		}
		fields["rows"] = rows
	} else {
		var aggregate map[string]json.RawMessage
		if err := json.Unmarshal(result.Payload, &aggregate); err != nil {
			writeAccountUsageErrorV2(w, err)
			return
		}
		for key, value := range aggregate {
			fields[key] = value
		}
		if section == "requests" {
			fields["page"], fields["page_size"] = page, query.Limit
		}
	}
	fields["source"], fields["credit_basis"] = "request_metrics", "request_attribution"
	fields["window_start"], fields["window_end"], fields["timezone"] = query.Start, query.End, query.Timezone
	fields["tenant_id"], fields["owner_user"], fields["days"] = tenant, owner, days
	fields["updated_at"], fields["pending_since"] = result.UpdatedAt, result.PendingSince
	writeJSON(w, http.StatusOK, fields)
}

func parseMaaSUsageV2(r *http.Request, tenant, owner, section string, includeCost bool) (metrics.MaaSQuery, int, int, error) {
	params := r.URL.Query()
	days := queryInt(r, "days", 7)
	maxDays := 90
	if section == "requests" {
		maxDays = 31
		days = maas.ClampMyRequestsDays(days)
	} else {
		days = maas.ClampUsageDays(days)
	}
	if params.Has("days") {
		// Keep the MaaS slider clamps while sharing validation for exclusive windows.
		params.Set("days", strconv.Itoa(days))
		r = r.Clone(r.Context())
		r.URL.RawQuery = params.Encode()
	}
	window, err := parseRequestStatsV2WithDefaultDays(r, tenant, days)
	if err != nil {
		return metrics.MaaSQuery{}, 0, 0, err
	}
	location, err := time.LoadLocation(window.Timezone)
	if err != nil {
		return metrics.MaaSQuery{}, 0, 0, err
	}
	calendarDay := func(t time.Time) time.Time {
		year, month, day := t.In(location).Date()
		return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	}
	days = int(calendarDay(window.EndTime.Add(-time.Nanosecond)).Sub(calendarDay(window.StartTime))/(24*time.Hour)) + 1
	if days > maxDays {
		return metrics.MaaSQuery{}, 0, 0, fmt.Errorf("MaaS window may span at most %d calendar days", maxDays)
	}
	page := min(max(queryInt(r, "page", 1), 1), maas.MaxMyRequestsPage)
	limit := maas.ClampUsageLimit(queryInt(r, "limit", 10))
	day := ""
	if section == "requests" {
		day = params.Get("bucket_day")
		if day != "" {
			if _, _, err := reconciliation.NaturalDay(day, window.Timezone); err != nil {
				return metrics.MaaSQuery{}, 0, 0, err
			}
		}
		limit = queryInt(r, "page_size", 20)
		if limit < 1 || limit > 100 {
			limit = 20
		}
	}
	return metrics.MaaSQuery{
		Start: window.StartTime, End: window.EndTime, Tenant: tenant, Owner: owner,
		Timezone: window.Timezone, Day: day, Model: strings.TrimSpace(params.Get("model")),
		Limit: limit, Offset: (page - 1) * limit, IncludeUpstreamCost: includeCost,
	}, days, page, nil
}

func (h *Handler) maasCreditReferenceV2(ctx context.Context) *maasCreditReference {
	var ref maasCreditReference
	if err := h.db.QueryRow(ctx, `SELECT cents_per_credit::float8, upper(btrim(currency_display))
		FROM public.maas_settings WHERE id=1`).Scan(&ref.CentsPerCredit, &ref.Currency); err != nil ||
		ref.CentsPerCredit <= 0 || math.IsNaN(ref.CentsPerCredit) || math.IsInf(ref.CentsPerCredit, 0) ||
		(ref.Currency != "USD" && ref.Currency != "CNY") {
		return nil
	}
	return &ref
}

func setMaaSCreditEstimate(row *projectedMaaSDetailRow, ref maasCreditReference) {
	revenue := float64(row.CreditsCharged) * ref.CentsPerCredit / 100
	if ref.Currency == "CNY" {
		revenue /= settings.CNYToUSDFX
	}
	row.EstimatedRevenueUSD = &revenue
	if row.Upstream == nil || row.CostRowsWithoutUSDEvidence == nil || *row.CostRowsWithoutUSDEvidence != 0 {
		return
	}
	margin := revenue - *row.Upstream
	row.EstimatedMarginUSD = &margin
	if revenue != 0 {
		rate := margin / revenue
		row.EstimatedMarginRate = &rate
	}
}
