package maas

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminModelRateRow is one canonical model with effective and custom credit rates.
type AdminModelRateRow struct {
	CanonicalID     int        `json:"canonical_id"`
	CanonicalName   string     `json:"canonical_name"`
	DisplayName     string     `json:"display_name"`
	Vendor          string     `json:"vendor"`
	Family          *string    `json:"family"`
	Status          string     `json:"status"`
	CreditsPer1MIn  int64      `json:"credits_per_1m_in"`
	CreditsPer1MOut int64      `json:"credits_per_1m_out"`
	CreditsPer1MCacheIn  int64 `json:"credits_per_1m_cache_in"`
	CreditsPer1MCacheOut int64 `json:"credits_per_1m_cache_out"`
	ManualIn        bool       `json:"manual_in"`
	ManualOut       bool       `json:"manual_out"`
	ManualCacheIn   bool       `json:"manual_cache_in"`
	ManualCacheOut  bool       `json:"manual_cache_out"`
	IsCustom        bool       `json:"is_custom"`
	CustomIn        *int64     `json:"custom_credits_per_1m_in"`
	CustomOut       *int64     `json:"custom_credits_per_1m_out"`
	CustomCacheIn   *int64     `json:"custom_credits_per_1m_cache_in"`
	CustomCacheOut  *int64     `json:"custom_credits_per_1m_cache_out"`
	UpdatedAt       *time.Time `json:"updated_at"`
}

// AdminModelRatesResponse bundles global knobs with per-model rows.
type AdminModelRatesResponse struct {
	Settings Settings            `json:"settings"`
	Items    []AdminModelRateRow `json:"items"`
}

// ModelRateUpsert is a manual per-model pricing override.
type ModelRateUpsert struct {
	CreditsPer1MIn       int64 `json:"credits_per_1m_in"`
	CreditsPer1MOut      int64 `json:"credits_per_1m_out"`
	CreditsPer1MCacheIn  int64 `json:"credits_per_1m_cache_in"`
	CreditsPer1MCacheOut int64 `json:"credits_per_1m_cache_out"`
	ManualIn             bool  `json:"manual_in"`
	ManualOut            bool  `json:"manual_out"`
	ManualCacheIn        bool  `json:"manual_cache_in"`
	ManualCacheOut       bool  `json:"manual_cache_out"`
}

// ListAdminModelRates returns all canonical models with effective credit pricing.
func (s *Service) ListAdminModelRates(ctx context.Context) (AdminModelRatesResponse, error) {
	var out AdminModelRatesResponse
	if !s.Enabled() {
		return out, errors.New("maas service disabled")
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return out, err
	}
	out.Settings = settings
	global := globalEffective(settings)

	rows, err := s.pool.Query(ctx, `
		SELECT mc.id,
		       mc.canonical_name,
		       COALESCE(NULLIF(TRIM(mc.display_name), ''), mc.canonical_name),
		       COALESCE(NULLIF(TRIM(mf.vendor), ''), NULLIF(TRIM(mc.family), ''), '其他'),
		       mc.family,
		       COALESCE(mc.status, 'active'),
		       mcr.credits_per_1m_in,
		       mcr.credits_per_1m_out,
		       mcr.credits_per_1m_cache_in,
		       mcr.credits_per_1m_cache_out,
		       COALESCE(mcr.manual_in, FALSE),
		       COALESCE(mcr.manual_out, FALSE),
		       COALESCE(mcr.manual_cache_in, FALSE),
		       COALESCE(mcr.manual_cache_out, FALSE),
		       mcr.updated_at
		FROM models_canonical mc
		LEFT JOIN model_families mf ON mf.id = mc.family AND COALESCE(mf.status, 'active') = 'active'
		LEFT JOIN model_credit_rates mcr ON mcr.canonical_id = mc.id
		WHERE COALESCE(mc.status, 'active') = 'active'
		ORDER BY vendor, mc.canonical_name
	`)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	items := make([]AdminModelRateRow, 0)
	for rows.Next() {
		var r AdminModelRateRow
		var stored storedModelRates
		if err := rows.Scan(
			&r.CanonicalID, &r.CanonicalName, &r.DisplayName, &r.Vendor, &r.Family, &r.Status,
			&stored.In, &stored.Out, &stored.CacheIn, &stored.CacheOut,
			&stored.ManualIn, &stored.ManualOut, &stored.ManualCacheIn, &stored.ManualCacheOut,
			&r.UpdatedAt,
		); err != nil {
			return out, err
		}
		r.CustomIn = stored.In
		r.CustomOut = stored.Out
		r.CustomCacheIn = stored.CacheIn
		r.CustomCacheOut = stored.CacheOut
		r.ManualIn = stored.ManualIn
		r.ManualOut = stored.ManualOut
		r.ManualCacheIn = stored.ManualCacheIn
		r.ManualCacheOut = stored.ManualCacheOut
		r.IsCustom = storedIsManual(stored)
		eff := effectiveModelRates(stored, global)
		r.CreditsPer1MIn = eff.In
		r.CreditsPer1MOut = eff.Out
		r.CreditsPer1MCacheIn = eff.CacheIn
		r.CreditsPer1MCacheOut = eff.CacheOut
		items = append(items, r)
	}
	out.Items = jsonSlice(items)
	return out, rows.Err()
}

// UpsertModelRate sets per-model manual credit rates.
func (s *Service) UpsertModelRate(ctx context.Context, canonicalID int, req ModelRateUpsert) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	if canonicalID <= 0 {
		return errors.New("invalid canonical_id")
	}
	if !req.ManualIn && !req.ManualOut && !req.ManualCacheIn && !req.ManualCacheOut {
		return errors.New("at least one manual_* flag must be true")
	}
	validate := func(manual bool, val int64, field string) error {
		if manual && val <= 0 {
			return errors.New(field + " must be positive when manual")
		}
		return nil
	}
	if err := validate(req.ManualIn, req.CreditsPer1MIn, "credits_per_1m_in"); err != nil {
		return err
	}
	if err := validate(req.ManualOut, req.CreditsPer1MOut, "credits_per_1m_out"); err != nil {
		return err
	}
	if err := validate(req.ManualCacheIn, req.CreditsPer1MCacheIn, "credits_per_1m_cache_in"); err != nil {
		return err
	}
	if err := validate(req.ManualCacheOut, req.CreditsPer1MCacheOut, "credits_per_1m_cache_out"); err != nil {
		return err
	}

	var exists bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM models_canonical
			WHERE id = $1 AND COALESCE(status, 'active') = 'active'
		)
	`, canonicalID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("canonical model not found or inactive")
	}

	var inVal, outVal, cacheInVal, cacheOutVal *int64
	if req.ManualIn {
		v := req.CreditsPer1MIn
		inVal = &v
	}
	if req.ManualOut {
		v := req.CreditsPer1MOut
		outVal = &v
	}
	if req.ManualCacheIn {
		v := req.CreditsPer1MCacheIn
		cacheInVal = &v
	}
	if req.ManualCacheOut {
		v := req.CreditsPer1MCacheOut
		cacheOutVal = &v
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO model_credit_rates (
			canonical_id, credits_per_1m_in, credits_per_1m_out,
			credits_per_1m_cache_in, credits_per_1m_cache_out,
			manual_in, manual_out, manual_cache_in, manual_cache_out, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (canonical_id) DO UPDATE SET
			credits_per_1m_in = CASE WHEN EXCLUDED.manual_in THEN EXCLUDED.credits_per_1m_in ELSE model_credit_rates.credits_per_1m_in END,
			credits_per_1m_out = CASE WHEN EXCLUDED.manual_out THEN EXCLUDED.credits_per_1m_out ELSE model_credit_rates.credits_per_1m_out END,
			credits_per_1m_cache_in = CASE WHEN EXCLUDED.manual_cache_in THEN EXCLUDED.credits_per_1m_cache_in ELSE model_credit_rates.credits_per_1m_cache_in END,
			credits_per_1m_cache_out = CASE WHEN EXCLUDED.manual_cache_out THEN EXCLUDED.credits_per_1m_cache_out ELSE model_credit_rates.credits_per_1m_cache_out END,
			manual_in = model_credit_rates.manual_in OR EXCLUDED.manual_in,
			manual_out = model_credit_rates.manual_out OR EXCLUDED.manual_out,
			manual_cache_in = model_credit_rates.manual_cache_in OR EXCLUDED.manual_cache_in,
			manual_cache_out = model_credit_rates.manual_cache_out OR EXCLUDED.manual_cache_out,
			updated_at = now()
	`, canonicalID, inVal, outVal, cacheInVal, cacheOutVal,
		req.ManualIn, req.ManualOut, req.ManualCacheIn, req.ManualCacheOut)
	return err
}

// DeleteModelRate removes all custom pricing so the model falls back to global base.
func (s *Service) DeleteModelRate(ctx context.Context, canonicalID int) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	if canonicalID <= 0 {
		return errors.New("invalid canonical_id")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM model_credit_rates WHERE canonical_id = $1`, canonicalID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ResetModelRateFields clears manual flags for given fields (e.g. "in", "out", "cache_in", "cache_out").
func (s *Service) ResetModelRateFields(ctx context.Context, canonicalID int, fields []string) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	if canonicalID <= 0 {
		return errors.New("invalid canonical_id")
	}
	if len(fields) == 0 {
		return errors.New("fields required")
	}
	setClauses := make([]string, 0, len(fields))
	for _, f := range fields {
		switch f {
		case "in":
			setClauses = append(setClauses, "manual_in = FALSE, credits_per_1m_in = NULL")
		case "out":
			setClauses = append(setClauses, "manual_out = FALSE, credits_per_1m_out = NULL")
		case "cache_in":
			setClauses = append(setClauses, "manual_cache_in = FALSE, credits_per_1m_cache_in = NULL")
		case "cache_out":
			setClauses = append(setClauses, "manual_cache_out = FALSE, credits_per_1m_cache_out = NULL")
		default:
			return errors.New("unknown field: " + f)
		}
	}
	q := `UPDATE model_credit_rates SET ` + joinClauses(setClauses) + `, updated_at = now() WHERE canonical_id = $1`
	tag, err := s.pool.Exec(ctx, q, canonicalID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	_, _ = s.pool.Exec(ctx, `
		DELETE FROM model_credit_rates
		WHERE canonical_id = $1
		  AND NOT manual_in AND NOT manual_out AND NOT manual_cache_in AND NOT manual_cache_out
	`, canonicalID)
	return nil
}

func joinClauses(parts []string) string {
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += ", " + parts[i]
	}
	return out
}

func effectiveRate(custom *int64, base int64) int64 {
	if custom != nil && *custom > 0 {
		return *custom
	}
	return base
}
