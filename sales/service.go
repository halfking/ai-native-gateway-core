// Package sales implements the quotation-system MVP backend (2026-11-04
// plan, W1): the sellable list (941/949 view driven), supply-pool
// annotations (942), demand cards (943), quote versions/items/approvals/
// shares (944–948).
//
// # Data positioning (pre-registered in the brainstorm doc §5)
//
// Every table here is an INTERNAL-OPS table (our own sales organization),
// not tenant-visible data: no tenant_isolation_* RLS — the same platform
// posture as the 940 market-intel family. Role gating happens at the
// application layer (admin/sales_handlers.go): supply mutations are
// super-only (ops face), demand/quote actions carry the A-11 owner
// narrowing precedent (JWT user acts as self; super_admin may override
// explicitly).
//
// # Red lines carried from the plan (§6 of the brainstorm doc)
//
//   - NULL ≠ 0: a missing price is never coerced to zero.
//   - C3 price_confidence is a GATE, not a score: 'trusted' is the
//     admission condition for a formal quote.
//   - Same-canonical multi-price ⇒ the whole group is withheld from the
//     sellable list (mirrors withholdConflictingPrices in
//     cmd/tools/propose-baseline-prices/canonical_price_collision.go).
//   - FX single source: snapshot values come from settings.CNYToUSDFX only.
//   - Expired is a derived state, never stored.
package sales

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ErrNotFound is returned by point lookups that hit no row.
var ErrNotFound = errors.New("sales: not found")

// Service provides the quotation-MVP queries over the sales_* tables.
type Service struct {
	pool *pgxpool.Pool
}

// NewService returns nil when pool is nil (handlers then report 503), the
// maas.Service precedent.
func NewService(pool *pgxpool.Pool) *Service {
	if pool == nil {
		return nil
	}
	return &Service{pool: pool}
}

// Enabled reports whether the service has a database.
func (s *Service) Enabled() bool { return s != nil && s.pool != nil }

// ── 供给池：sellable 清单（trusted 视图活读 + 整组 withhold） ───────────────

// SellableRow is one trusted binding row as shown by the workbench. Prices
// are scanned as numeric text and re-emitted verbatim — exact decimal
// strings, never float round-trips (money red line).
type SellableRow struct {
	CredentialID       int64   `json:"credential_id"`
	ProviderModelID    int64   `json:"provider_model_id"`
	RawModelName       string  `json:"raw_model_name"`
	CanonicalName      string  `json:"canonical_name"`
	ProviderName       string  `json:"provider_name"`
	SupplierCurrency   string  `json:"supplier_currency"`
	BillingMode        string  `json:"billing_mode"`
	SupplierInPer1M    *string `json:"supplier_in_per_1m"`
	SupplierOutPer1M   *string `json:"supplier_out_per_1m"`
	BaselineCurrency   *string `json:"baseline_currency"`
	BaselineInPer1M    *string `json:"baseline_in_per_1m"`
	BaselineOutPer1M   *string `json:"baseline_out_per_1m"`
	CurrencyComparable bool    `json:"currency_comparable"`
	HasBaseline        bool    `json:"has_baseline"`
	MarketRefCurrency  *string `json:"market_ref_currency"`
	MarketRefInPer1M   *string `json:"market_ref_in_per_1m"`
	MarketRefOutPer1M  *string `json:"market_ref_out_per_1m"`
	// A-11（2026-11-05 审计真库腿）：这两列在市场表为空（940 无同步器 =
	// 生产现状）时全 NULL ⇒ 非指针目标把整条 ListSellable 变 500。
	MarketRefPlatform  *string           `json:"market_ref_platform_id"`
	MarketRefPlatforms *int              `json:"market_ref_platform_count"`
	MarketPosition     *string           `json:"market_position"`
	PriceConfidence    string            `json:"price_confidence"`
	Annotation         *SupplyAnnotation `json:"annotation,omitempty"`
}

// WithheldGroup reports one same-canonical multi-price group held out of
// the sellable list (整组 withhold 红线：同名多价不可信，不进清单).
type WithheldGroup struct {
	CanonicalName string        `json:"canonical_name"`
	Rows          []SellableRow `json:"-"`
	Reason        string        `json:"reason"`
	PriceVariants [][2]*string  `json:"price_variants"`
}

// SellableList is the workbench supply response.
type SellableList struct {
	Items         []SellableRow   `json:"items"`
	Withheld      []WithheldGroup `json:"withheld"`
	TotalTrusted  int             `json:"total_trusted"`
	WithheldCount int             `json:"withheld_count"`
	// 三档一键报价系数（风暴档 §8-5，settings 热改即生效；仅前端快填用，
	// 服务端不校验零售价与系数的关系——红线在审批）。
	TierCoefficients map[string]float64 `json:"tier_coefficients"`
}

// ListSellable reads v_supplier_price_vs_baseline WHERE
// price_confidence='trusted', joins supply annotations (949 exposes the
// binding identity), excludes suspended annotations, and withholds
// same-canonical multi-price groups.
//
// revealContacts=false（非 super）时供应商联系人不下发（风暴档 §3.5/§8-4：
// 「仅资源团队 + super 可见」——MVP 角色模型没有独立资源团队角色，供给登记
// 写面本就是 super-only，故联系人读面同样收窄到 super，2026-11-08 审计
// P2-4 修复）。
func (s *Service) ListSellable(ctx context.Context, revealContacts bool) (*SellableList, error) {
	rows, err := s.pool.Query(ctx, `
SELECT v.credential_id,
       v.provider_model_id,
       v.raw_model_name,
       v.canonical_name,
       v.provider_name,
       v.supplier_currency,
       v.billing_mode,
       v.supplier_in_per_1m::text,
       v.supplier_out_per_1m::text,
       v.baseline_currency,
       v.baseline_in_per_1m::text,
       v.baseline_out_per_1m::text,
       v.currency_comparable,
       v.has_baseline,
       v.market_ref_currency,
       v.market_ref_in_per_1m::text,
       v.market_ref_out_per_1m::text,
       v.market_ref_platform_id,
       v.market_ref_platform_count,
       v.market_position::text,
       v.price_confidence,
       sa.id,
       sa.status,
       sa.sla_tier,
       sa.capacity_notes,
       sa.vendor_contact,
       sa.valid_from::text,
       sa.valid_until::text,
       sa.maintainer,
       sa.notes
  FROM public.v_supplier_price_vs_baseline v
  LEFT JOIN public.sales_supply_annotations sa
         ON sa.credential_id = v.credential_id
        AND sa.provider_model_id = v.provider_model_id
 WHERE v.price_confidence = 'trusted'
 ORDER BY v.canonical_name, v.credential_id, v.provider_model_id`)
	if err != nil {
		return nil, fmt.Errorf("sales: query sellable: %w", err)
	}
	defer rows.Close()

	all := make([]SellableRow, 0, 64)
	for rows.Next() {
		var r SellableRow
		var annID *int64
		var annStatus, slaTier, capacity, contact, maintainer, notes *string
		var validFrom, validUntil *string
		if err := rows.Scan(&r.CredentialID, &r.ProviderModelID, &r.RawModelName,
			&r.CanonicalName, &r.ProviderName, &r.SupplierCurrency, &r.BillingMode,
			&r.SupplierInPer1M, &r.SupplierOutPer1M,
			&r.BaselineCurrency, &r.BaselineInPer1M, &r.BaselineOutPer1M,
			&r.CurrencyComparable, &r.HasBaseline,
			&r.MarketRefCurrency, &r.MarketRefInPer1M, &r.MarketRefOutPer1M,
			&r.MarketRefPlatform, &r.MarketRefPlatforms, &r.MarketPosition,
			&r.PriceConfidence,
			&annID, &annStatus, &slaTier, &capacity, &contact, &validFrom, &validUntil,
			&maintainer, &notes); err != nil {
			return nil, fmt.Errorf("sales: scan sellable: %w", err)
		}
		if annID != nil {
			contactVal := deref(contact)
			if !revealContacts {
				contactVal = "" // 供应商联系人仅 super（数据最小化，P2-4）
			}
			r.Annotation = &SupplyAnnotation{
				ID:              *annID,
				CredentialID:    r.CredentialID,
				ProviderModelID: r.ProviderModelID,
				Status:          deref(annStatus),
				SLATier:         deref(slaTier),
				CapacityNotes:   deref(capacity),
				VendorContact:   contactVal,
				ValidFrom:       deref(validFrom),
				ValidUntil:      deref(validUntil),
				Maintainer:      deref(maintainer),
				Notes:           deref(notes),
			}
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sales: iterate sellable: %w", err)
	}

	// 整组 withhold：同一 canonical 名出现 ≥2 个不同 (in,out) 价 ⇒ 整组
	// 扣下（canonical_price_collision.go withholdConflictingPrices 同口径；
	// 判不出 canonical 名的行原样放过——问题在名字不在价）。
	groups := map[string][]SellableRow{}
	order := make([]string, 0, 16)
	for _, r := range all {
		if r.CanonicalName == "" {
			continue
		}
		if _, seen := groups[r.CanonicalName]; !seen {
			order = append(order, r.CanonicalName)
		}
		groups[r.CanonicalName] = append(groups[r.CanonicalName], r)
	}
	withheldNames := map[string]bool{}
	var withheld []WithheldGroup
	for _, canon := range order {
		g := groups[canon]
		if len(g) < 2 {
			continue
		}
		variants := make([][2]*string, 0, len(g))
		var seenPrices [][2]float64
		for _, row := range g {
			inF := parseNum(row.SupplierInPer1M)
			outF := parseNum(row.SupplierOutPer1M)
			dup := false
			for _, p := range seenPrices {
				if p[0] == inF && p[1] == outF {
					dup = true
					break
				}
			}
			if !dup {
				seenPrices = append(seenPrices, [2]float64{inF, outF})
				variants = append(variants, [2]*string{row.SupplierInPer1M, row.SupplierOutPer1M})
			}
		}
		if len(seenPrices) < 2 {
			continue // 同价重复：数字唯一，不是冲突
		}
		withheldNames[canon] = true
		withheld = append(withheld, WithheldGroup{
			CanonicalName: canon,
			Reason:        "canonical_multi_price",
			PriceVariants: variants,
		})
	}

	items := make([]SellableRow, 0, len(all))
	for _, r := range all {
		if withheldNames[r.CanonicalName] {
			continue
		}
		// 挂了 suspended 登记的 binding 不进可售清单（登记保留，状态排除）。
		if r.Annotation != nil && r.Annotation.Status == "suspended" {
			continue
		}
		items = append(items, r)
	}

	out := &SellableList{
		Items:        items,
		Withheld:     withheld,
		TotalTrusted: len(all),
		TierCoefficients: map[string]float64{
			"aggressive":   tierCoefficient("aggressive", 1.3),
			"standard":     tierCoefficient("standard", 1.6),
			"conservative": tierCoefficient("conservative", 2.0),
		},
	}
	for i := range out.Withheld {
		out.Withheld[i].Rows = nil
	}
	out.WithheldCount = len(out.Withheld)
	return out, nil
}

func parseNum(s *string) float64 {
	if s == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(*s), 64)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

// tierCoefficient reads one of the three quick-fill coefficients from
// settings_kv, falling back to the registered default (R72f: fallback ==
// spec default, so behavior is identical with or without a kv row).
func tierCoefficient(tier string, fallback float64) float64 {
	key := "sales.quote.tier_coef_" + tier
	v := settings.GetPlatformFloat(key, fallback)
	if v < 1.01 || math.IsNaN(v) || math.IsInf(v, 0) {
		return fallback // Min 边界兜底（0.99× 的「折扣档」不是报价档）
	}
	return v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ── 供给池：登记 CRUD（942） ─────────────────────────────────────────────

// SupplyAnnotation is one manual row of sales_supply_annotations.
type SupplyAnnotation struct {
	ID              int64  `json:"id"`
	CredentialID    int64  `json:"credential_id"`
	ProviderModelID int64  `json:"provider_model_id"`
	CapacityNotes   string `json:"capacity_notes"`
	SLATier         string `json:"sla_tier"`
	ValidFrom       string `json:"valid_from"`
	ValidUntil      string `json:"valid_until"`
	VendorContact   string `json:"vendor_contact"`
	Status          string `json:"status"`
	Maintainer      string `json:"maintainer"`
	Notes           string `json:"notes"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

// UpsertSupplyAnnotation inserts or updates the annotation for one binding
// pair (UNIQUE (credential_id, provider_model_id)).
func (s *Service) UpsertSupplyAnnotation(ctx context.Context, a SupplyAnnotation, actor string) (*SupplyAnnotation, error) {
	if a.CredentialID <= 0 || a.ProviderModelID <= 0 {
		return nil, errors.New("sales: credential_id and provider_model_id are required")
	}
	if a.Status == "" {
		a.Status = "active"
	}
	if a.Status != "active" && a.Status != "suspended" {
		return nil, fmt.Errorf("sales: invalid status %q (active|suspended)", a.Status)
	}
	if a.Maintainer == "" {
		a.Maintainer = actor
	}
	var out SupplyAnnotation
	err := s.pool.QueryRow(ctx, `
INSERT INTO public.sales_supply_annotations
    (credential_id, provider_model_id, capacity_notes, sla_tier,
     valid_from, valid_until, vendor_contact, status, maintainer, notes)
VALUES ($1,$2,$3,$4,
        NULLIF($5,'')::date, NULLIF($6,'')::date, $7, $8, $9, $10)
ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
    capacity_notes = EXCLUDED.capacity_notes,
    sla_tier       = EXCLUDED.sla_tier,
    valid_from     = EXCLUDED.valid_from,
    valid_until    = EXCLUDED.valid_until,
    vendor_contact = EXCLUDED.vendor_contact,
    status         = EXCLUDED.status,
    maintainer     = EXCLUDED.maintainer,
    notes          = EXCLUDED.notes,
    updated_at     = now()
RETURNING id, credential_id, provider_model_id, capacity_notes, sla_tier,
          COALESCE(valid_from::text,''), COALESCE(valid_until::text,''),
          vendor_contact, status, maintainer, notes,
          created_at::text, updated_at::text`,
		a.CredentialID, a.ProviderModelID, a.CapacityNotes, a.SLATier,
		a.ValidFrom, a.ValidUntil, a.VendorContact, a.Status, a.Maintainer, a.Notes,
	).Scan(&out.ID, &out.CredentialID, &out.ProviderModelID, &out.CapacityNotes,
		&out.SLATier, &out.ValidFrom, &out.ValidUntil, &out.VendorContact,
		&out.Status, &out.Maintainer, &out.Notes, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("sales: upsert supply annotation: %w", err)
	}
	return &out, nil
}

// ListSupplyAnnotations lists annotations joined with model/credential names.
// revealContacts=false（非 super）时供应商联系人不下发（P2-4，同 ListSellable）。
func (s *Service) ListSupplyAnnotations(ctx context.Context, revealContacts bool) ([]map[string]any, error) {
	rows, err := s.pool.Query(ctx, `
SELECT sa.id, sa.credential_id, sa.provider_model_id,
       pm.raw_model_name,
       COALESCE(pm.canonical_raw_name, '') AS canonical_name,
       COALESCE(sa.capacity_notes,''), COALESCE(sa.sla_tier,''),
       COALESCE(sa.valid_from::text,''), COALESCE(sa.valid_until::text,''),
       COALESCE(sa.vendor_contact,''), sa.status, COALESCE(sa.maintainer,''),
       COALESCE(sa.notes,''), sa.created_at::text, sa.updated_at::text
  FROM public.sales_supply_annotations sa
  LEFT JOIN public.provider_models pm ON pm.id = sa.provider_model_id
 ORDER BY sa.updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("sales: list supply annotations: %w", err)
	}
	defer rows.Close()
	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		var id, credID, pmID int64
		var rawName, canonName, capacity, sla, validFrom, validUntil, contact, status, maintainer, notes, createdAt, updatedAt string
		if err := rows.Scan(&id, &credID, &pmID, &rawName, &canonName,
			&capacity, &sla, &validFrom, &validUntil, &contact, &status,
			&maintainer, &notes, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("sales: scan supply annotations: %w", err)
		}
		if !revealContacts {
			contact = "" // 供应商联系人仅 super（数据最小化，P2-4）
		}
		out = append(out, map[string]any{
			"id":                id,
			"credential_id":     credID,
			"provider_model_id": pmID,
			"raw_model_name":    rawName,
			"canonical_name":    canonName,
			"capacity_notes":    capacity,
			"sla_tier":          sla,
			"valid_from":        validFrom,
			"valid_until":       validUntil,
			"vendor_contact":    contact,
			"status":            status,
			"maintainer":        maintainer,
			"notes":             notes,
			"created_at":        createdAt,
			"updated_at":        updatedAt,
		})
	}
	return out, rows.Err()
}

// ── 需求池（943） ────────────────────────────────────────────────────────

// DemandCard is one row of sales_demand_cards.
type DemandCard struct {
	ID           int64      `json:"id"`
	CustomerName string     `json:"customer_name"`
	Scenario     string     `json:"scenario"`
	MonthlyUsage string     `json:"monthly_usage"`
	Budget       string     `json:"budget"`
	WindowHint   string     `json:"window_hint"`
	Status       string     `json:"status"`
	ClaimedBy    string     `json:"claimed_by"`
	ClaimedAt    *time.Time `json:"claimed_at,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	Expired      bool       `json:"expired"` // time-actor 派生态：expires_at < now()
	LostReason   string     `json:"lost_reason"`
	CreatedBy    string     `json:"created_by"`
	Notes        string     `json:"notes"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func expiresAtText(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// ListDemandCards lists cards; status filter optional ("" = all).
func (s *Service) ListDemandCards(ctx context.Context, status string) ([]DemandCard, error) {
	q := `
SELECT id, customer_name, scenario, monthly_usage, budget, window_hint,
       status, claimed_by, claimed_at, expires_at, lost_reason, created_by,
       notes, created_at, updated_at,
       (expires_at IS NOT NULL AND expires_at < now()) AS is_expired
  FROM public.sales_demand_cards`
	args := []any{}
	if status != "" {
		q += ` WHERE status = $1`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC LIMIT 500`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("sales: list demand cards: %w", err)
	}
	defer rows.Close()
	out := make([]DemandCard, 0, 16)
	for rows.Next() {
		var c DemandCard
		if err := rows.Scan(&c.ID, &c.CustomerName, &c.Scenario, &c.MonthlyUsage,
			&c.Budget, &c.WindowHint, &c.Status, &c.ClaimedBy, &c.ClaimedAt,
			&c.ExpiresAt, &c.LostReason, &c.CreatedBy, &c.Notes,
			&c.CreatedAt, &c.UpdatedAt, &c.Expired); err != nil {
			return nil, fmt.Errorf("sales: scan demand card: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateDemandCard inserts a card; createdBy is the JWT username.
// expiresAtJSON 是 RFC3339 文本（"" = 无到期）。
func (s *Service) CreateDemandCard(ctx context.Context, c DemandCard, createdBy string) (*DemandCard, error) {
	if strings.TrimSpace(c.CustomerName) == "" {
		return nil, errors.New("sales: customer_name is required")
	}
	var out DemandCard
	err := s.pool.QueryRow(ctx, `
INSERT INTO public.sales_demand_cards
    (customer_name, scenario, monthly_usage, budget, window_hint, expires_at,
     notes, created_by)
VALUES ($1,$2,$3,$4,$5, NULLIF($6,'')::timestamptz, $7, $8)
RETURNING id, customer_name, scenario, monthly_usage, budget, window_hint,
          status, claimed_by, claimed_at, expires_at, lost_reason, created_by,
          notes, created_at, updated_at`,
		c.CustomerName, c.Scenario, c.MonthlyUsage, c.Budget, c.WindowHint,
		expiresAtText(c.ExpiresAt), c.Notes, createdBy,
	).Scan(&out.ID, &out.CustomerName, &out.Scenario, &out.MonthlyUsage,
		&out.Budget, &out.WindowHint, &out.Status, &out.ClaimedBy, &out.ClaimedAt,
		&out.ExpiresAt, &out.LostReason, &out.CreatedBy, &out.Notes,
		&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("sales: create demand card: %w", err)
	}
	return &out, nil
}

// ClaimDemandCard claims an open card for actor.
func (s *Service) ClaimDemandCard(ctx context.Context, id int64, actor string) (*DemandCard, error) {
	if actor == "" {
		return nil, errors.New("sales: claim requires an identity")
	}
	tag, err := s.pool.Exec(ctx, `
UPDATE public.sales_demand_cards
   SET status = 'claimed', claimed_by = $2, claimed_at = now(), updated_at = now()
 WHERE id = $1 AND status = 'open'`, id, actor)
	if err != nil {
		return nil, fmt.Errorf("sales: claim demand card: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, errors.New("sales: card not open or not found")
	}
	return s.GetDemandCard(ctx, id)
}

// CloseDemandCard finishes a card as won/lost/closed with a mandatory reason
// (风暴档 §4-5：丢单原因强制回填). override=true（super 显式越权，A-11 先例）
// 可关他人领取的卡。
func (s *Service) CloseDemandCard(ctx context.Context, id int64, status, reason, actor string, override bool) (*DemandCard, error) {
	if status != "won" && status != "lost" && status != "closed" {
		return nil, fmt.Errorf("sales: invalid close status %q (won|lost|closed)", status)
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("sales: closing requires a reason (won/lost 收口必须写明原因)")
	}
	tag, err := s.pool.Exec(ctx, `
UPDATE public.sales_demand_cards
   SET status = $2, lost_reason = $3, updated_at = now()
 WHERE id = $1
   AND ($5 OR claimed_by = $4)`, id, status, reason, actor, override)
	if err != nil {
		return nil, fmt.Errorf("sales: close demand card: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, errors.New("sales: card not found or not claimed by you (super may override via API)")
	}
	return s.GetDemandCard(ctx, id)
}

// GetDemandCard fetches one card with the derived expiry flag.
func (s *Service) GetDemandCard(ctx context.Context, id int64) (*DemandCard, error) {
	var c DemandCard
	err := s.pool.QueryRow(ctx, `
SELECT id, customer_name, scenario, monthly_usage, budget, window_hint,
       status, claimed_by, claimed_at, expires_at, lost_reason, created_by,
       notes, created_at, updated_at,
       (expires_at IS NOT NULL AND expires_at < now())
  FROM public.sales_demand_cards WHERE id = $1`, id).
		Scan(&c.ID, &c.CustomerName, &c.Scenario, &c.MonthlyUsage, &c.Budget,
			&c.WindowHint, &c.Status, &c.ClaimedBy, &c.ClaimedAt, &c.ExpiresAt,
			&c.LostReason, &c.CreatedBy, &c.Notes, &c.CreatedAt, &c.UpdatedAt,
			&c.Expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sales: get demand card: %w", err)
	}
	return &c, nil
}
