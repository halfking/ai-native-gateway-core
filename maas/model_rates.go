package maas

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminModelRateRow is one canonical model with effective and custom credit rates.
type AdminModelRateRow struct {
	CanonicalID          int        `json:"canonical_id"`
	CanonicalName        string     `json:"canonical_name"`
	DisplayName          string     `json:"display_name"`
	Vendor               string     `json:"vendor"`
	Family               *string    `json:"family"`
	Modality             string     `json:"modality"`
	Status               string     `json:"status"`
	CreditsPer1MIn       int64      `json:"credits_per_1m_in"`
	CreditsPer1MOut      int64      `json:"credits_per_1m_out"`
	CreditsPer1MCacheIn  int64      `json:"credits_per_1m_cache_in"`
	CreditsPer1MCacheOut int64      `json:"credits_per_1m_cache_out"`
	CreditsPer1MImage    int64      `json:"credits_per_1m_image_tokens"`
	CreditsPer1MAudio    int64      `json:"credits_per_1m_audio_tokens"`
	CreditsPer1MVideo    int64      `json:"credits_per_1m_video_tokens"`
	ManualIn             bool       `json:"manual_in"`
	ManualOut            bool       `json:"manual_out"`
	ManualCacheIn        bool       `json:"manual_cache_in"`
	ManualCacheOut       bool       `json:"manual_cache_out"`
	ManualImage          bool       `json:"manual_image"`
	ManualAudio          bool       `json:"manual_audio"`
	ManualVideo          bool       `json:"manual_video"`
	IsCustom             bool       `json:"is_custom"`
	CustomIn             *int64     `json:"custom_credits_per_1m_in"`
	CustomOut            *int64     `json:"custom_credits_per_1m_out"`
	CustomCacheIn        *int64     `json:"custom_credits_per_1m_cache_in"`
	CustomCacheOut       *int64     `json:"custom_credits_per_1m_cache_out"`
	CustomImage          *int64     `json:"custom_credits_per_1m_image_tokens"`
	CustomAudio          *int64     `json:"custom_credits_per_1m_audio_tokens"`
	CustomVideo          *int64     `json:"custom_credits_per_1m_video_tokens"`
	UpdatedAt            *time.Time `json:"updated_at"`
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
	CreditsPer1MImage    int64 `json:"credits_per_1m_image_tokens"`
	CreditsPer1MAudio    int64 `json:"credits_per_1m_audio_tokens"`
	CreditsPer1MVideo    int64 `json:"credits_per_1m_video_tokens"`
	ManualIn             bool  `json:"manual_in"`
	ManualOut            bool  `json:"manual_out"`
	ManualCacheIn        bool  `json:"manual_cache_in"`
	ManualCacheOut       bool  `json:"manual_cache_out"`
	ManualImage          bool  `json:"manual_image"`
	ManualAudio          bool  `json:"manual_audio"`
	ManualVideo          bool  `json:"manual_video"`
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
		       COALESCE(NULLIF(TRIM(mc.modality), ''), 'text'),
		       COALESCE(mc.status, 'active'),
		       mcr.credits_per_1m_in,
		       mcr.credits_per_1m_out,
		       mcr.credits_per_1m_cache_in,
		       mcr.credits_per_1m_cache_out,
		       mcr.credits_per_1m_image_tokens,
		       mcr.credits_per_1m_audio_tokens,
		       mcr.credits_per_1m_video_tokens,
		       COALESCE(mcr.manual_in, FALSE),
		       COALESCE(mcr.manual_out, FALSE),
		       COALESCE(mcr.manual_cache_in, FALSE),
		       COALESCE(mcr.manual_cache_out, FALSE),
		       COALESCE(mcr.manual_image, FALSE),
		       COALESCE(mcr.manual_audio, FALSE),
		       COALESCE(mcr.manual_video, FALSE),
		       mcr.updated_at
		FROM models_canonical mc
		LEFT JOIN model_families mf ON mf.id = mc.family AND COALESCE(mf.status, 'active') = 'active'
		LEFT JOIN model_credit_rates mcr ON mcr.canonical_id = mc.id
		WHERE COALESCE(mc.status, 'active') = 'active'
		ORDER BY mc.canonical_name
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
			&r.CanonicalID, &r.CanonicalName, &r.DisplayName, &r.Vendor, &r.Family, &r.Modality, &r.Status,
			&stored.In, &stored.Out, &stored.CacheIn, &stored.CacheOut,
			&stored.Image, &stored.Audio, &stored.Video,
			&stored.ManualIn, &stored.ManualOut, &stored.ManualCacheIn, &stored.ManualCacheOut,
			&stored.ManualImage, &stored.ManualAudio, &stored.ManualVideo,
			&r.UpdatedAt,
		); err != nil {
			return out, err
		}
		r.CustomIn = stored.In
		r.CustomOut = stored.Out
		r.CustomCacheIn = stored.CacheIn
		r.CustomCacheOut = stored.CacheOut
		r.CustomImage = stored.Image
		r.CustomAudio = stored.Audio
		r.CustomVideo = stored.Video
		r.ManualIn = stored.ManualIn
		r.ManualOut = stored.ManualOut
		r.ManualCacheIn = stored.ManualCacheIn
		r.ManualCacheOut = stored.ManualCacheOut
		r.ManualImage = stored.ManualImage
		r.ManualAudio = stored.ManualAudio
		r.ManualVideo = stored.ManualVideo
		r.IsCustom = storedIsManual(stored)
		eff := effectiveModelRates(stored, global)
		r.CreditsPer1MIn = eff.In
		r.CreditsPer1MOut = eff.Out
		r.CreditsPer1MCacheIn = eff.CacheIn
		r.CreditsPer1MCacheOut = eff.CacheOut
		r.CreditsPer1MImage = eff.Image
		r.CreditsPer1MAudio = eff.Audio
		r.CreditsPer1MVideo = eff.Video
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
	if !req.AnyManual() {
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
	if err := validate(req.ManualImage, req.CreditsPer1MImage, "credits_per_1m_image_tokens"); err != nil {
		return err
	}
	if err := validate(req.ManualAudio, req.CreditsPer1MAudio, "credits_per_1m_audio_tokens"); err != nil {
		return err
	}
	if err := validate(req.ManualVideo, req.CreditsPer1MVideo, "credits_per_1m_video_tokens"); err != nil {
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

	ptr := func(manual bool, val int64) *int64 {
		if !manual {
			return nil
		}
		v := val
		return &v
	}
	inVal := ptr(req.ManualIn, req.CreditsPer1MIn)
	outVal := ptr(req.ManualOut, req.CreditsPer1MOut)
	cacheInVal := ptr(req.ManualCacheIn, req.CreditsPer1MCacheIn)
	cacheOutVal := ptr(req.ManualCacheOut, req.CreditsPer1MCacheOut)
	imageVal := ptr(req.ManualImage, req.CreditsPer1MImage)
	audioVal := ptr(req.ManualAudio, req.CreditsPer1MAudio)
	videoVal := ptr(req.ManualVideo, req.CreditsPer1MVideo)

	_, err := s.pool.Exec(ctx, `
		INSERT INTO model_credit_rates (
			canonical_id,
			credits_per_1m_in, credits_per_1m_out,
			credits_per_1m_cache_in, credits_per_1m_cache_out,
			credits_per_1m_image_tokens, credits_per_1m_audio_tokens, credits_per_1m_video_tokens,
			manual_in, manual_out, manual_cache_in, manual_cache_out,
			manual_image, manual_audio, manual_video,
			updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, now()
		)
		ON CONFLICT (canonical_id) DO UPDATE SET
			credits_per_1m_in = CASE WHEN EXCLUDED.manual_in THEN EXCLUDED.credits_per_1m_in ELSE model_credit_rates.credits_per_1m_in END,
			credits_per_1m_out = CASE WHEN EXCLUDED.manual_out THEN EXCLUDED.credits_per_1m_out ELSE model_credit_rates.credits_per_1m_out END,
			credits_per_1m_cache_in = CASE WHEN EXCLUDED.manual_cache_in THEN EXCLUDED.credits_per_1m_cache_in ELSE model_credit_rates.credits_per_1m_cache_in END,
			credits_per_1m_cache_out = CASE WHEN EXCLUDED.manual_cache_out THEN EXCLUDED.credits_per_1m_cache_out ELSE model_credit_rates.credits_per_1m_cache_out END,
			credits_per_1m_image_tokens = CASE WHEN EXCLUDED.manual_image THEN EXCLUDED.credits_per_1m_image_tokens ELSE model_credit_rates.credits_per_1m_image_tokens END,
			credits_per_1m_audio_tokens = CASE WHEN EXCLUDED.manual_audio THEN EXCLUDED.credits_per_1m_audio_tokens ELSE model_credit_rates.credits_per_1m_audio_tokens END,
			credits_per_1m_video_tokens = CASE WHEN EXCLUDED.manual_video THEN EXCLUDED.credits_per_1m_video_tokens ELSE model_credit_rates.credits_per_1m_video_tokens END,
			manual_in = model_credit_rates.manual_in OR EXCLUDED.manual_in,
			manual_out = model_credit_rates.manual_out OR EXCLUDED.manual_out,
			manual_cache_in = model_credit_rates.manual_cache_in OR EXCLUDED.manual_cache_in,
			manual_cache_out = model_credit_rates.manual_cache_out OR EXCLUDED.manual_cache_out,
			manual_image = model_credit_rates.manual_image OR EXCLUDED.manual_image,
			manual_audio = model_credit_rates.manual_audio OR EXCLUDED.manual_audio,
			manual_video = model_credit_rates.manual_video OR EXCLUDED.manual_video,
			updated_at = now()
	`, canonicalID,
		inVal, outVal, cacheInVal, cacheOutVal,
		imageVal, audioVal, videoVal,
		req.ManualIn, req.ManualOut, req.ManualCacheIn, req.ManualCacheOut,
		req.ManualImage, req.ManualAudio, req.ManualVideo)
	return err
}

// AnyManual reports whether the upsert has any manual field set.
func (u ModelRateUpsert) AnyManual() bool {
	return u.ManualIn || u.ManualOut || u.ManualCacheIn || u.ManualCacheOut ||
		u.ManualImage || u.ManualAudio || u.ManualVideo
}

// ModelRateUpsertWithID pairs a canonical_id with its upsert payload.
type ModelRateUpsertWithID struct {
	CanonicalID int `json:"canonical_id"`
	ModelRateUpsert
}

// BatchResetWithID describes one model whose custom pricing must be reset
// to global base for the given fields. Useful for admin "清空所选"
// operations across many canonical ids.
type BatchResetWithID struct {
	CanonicalID int      `json:"canonical_id"`
	Fields      []string `json:"fields"`
}

// BatchUpsertModelRates applies multiple per-model manual credit rate overrides.
//
// The two shapes accepted are intentionally distinct so the admin UI can
// call this single endpoint with a homogeneous payload (every row either
// upserts or resets) — keeping round trips small and the wire format
// unambiguous.
func (s *Service) BatchUpsertModelRates(ctx context.Context, updates []ModelRateUpsertWithID) (int, error) {
	if !s.Enabled() {
		return 0, errors.New("maas service disabled")
	}
	updated := 0
	for _, u := range updates {
		if err := s.UpsertModelRate(ctx, u.CanonicalID, u.ModelRateUpsert); err != nil {
			continue
		}
		updated++
	}
	return updated, nil
}

// BatchResetModelRates resets the given custom pricing fields across many
// canonical ids. The fields slice supports the same keys as
// ResetModelRateFields (in / out / cache_in / cache_out / image / audio /
// video / all). Rows with no remaining manual flag are physically deleted
// to keep the table sparse.
func (s *Service) BatchResetModelRates(ctx context.Context, items []BatchResetWithID) (int, error) {
	if !s.Enabled() {
		return 0, errors.New("maas service disabled")
	}
	updated := 0
	for _, it := range items {
		if err := s.ResetModelRateFields(ctx, it.CanonicalID, it.Fields); err != nil {
			continue
		}
		updated++
	}
	return updated, nil
}

// BatchFillGlobalWithID describes one model whose pricing should be
// populated with the current global base × discount (i.e. as if the admin
// hand-copied the global base into manual pricing). This makes the row
// "frozen" against future global discount changes.
type BatchFillGlobalWithID struct {
	CanonicalID int `json:"canonical_id"`
	// When true, write the current global × discount values for every
	// manual flag (in/out/cache_in/cache_out/image/audio/video). When
	// false, only the dimensions that are already manual are touched.
	AllDimensions bool `json:"all_dimensions"`
}

// BatchFillGlobalModelRates materialises the global effective rate on the
// given canonical ids. Returns the number of rows actually written.
func (s *Service) BatchFillGlobalModelRates(ctx context.Context, items []BatchFillGlobalWithID) (int, error) {
	if !s.Enabled() {
		return 0, errors.New("maas service disabled")
	}
	if len(items) == 0 {
		return 0, nil
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return 0, err
	}
	global := globalEffective(settings)
	written := 0
	for _, it := range items {
		req := ModelRateUpsert{}
		if it.AllDimensions {
			req.ManualIn = true
			req.ManualOut = true
			req.ManualCacheIn = true
			req.ManualCacheOut = true
			req.ManualImage = true
			req.ManualAudio = true
			req.ManualVideo = true
			req.CreditsPer1MIn = global.In
			req.CreditsPer1MOut = global.Out
			req.CreditsPer1MCacheIn = global.CacheIn
			req.CreditsPer1MCacheOut = global.CacheOut
			req.CreditsPer1MImage = global.Image
			req.CreditsPer1MAudio = global.Audio
			req.CreditsPer1MVideo = global.Video
		} else {
			// Touch only the dimensions already flagged manual. Read
			// existing flags first.
			current, err := s.loadStoredManualFlags(ctx, it.CanonicalID)
			if err != nil {
				continue
			}
			req.ManualIn = current.ManualIn
			req.ManualOut = current.ManualOut
			req.ManualCacheIn = current.ManualCacheIn
			req.ManualCacheOut = current.ManualCacheOut
			req.ManualImage = current.ManualImage
			req.ManualAudio = current.ManualAudio
			req.ManualVideo = current.ManualVideo
			req.CreditsPer1MIn = global.In
			req.CreditsPer1MOut = global.Out
			req.CreditsPer1MCacheIn = global.CacheIn
			req.CreditsPer1MCacheOut = global.CacheOut
			req.CreditsPer1MImage = global.Image
			req.CreditsPer1MAudio = global.Audio
			req.CreditsPer1MVideo = global.Video
		}
		if err := s.UpsertModelRate(ctx, it.CanonicalID, req); err == nil {
			written++
		}
	}
	return written, nil
}

func (s *Service) loadStoredManualFlags(ctx context.Context, canonicalID int) (storedModelRates, error) {
	var stored storedModelRates
	err := s.pool.QueryRow(ctx, `
		SELECT credits_per_1m_in, credits_per_1m_out,
		       credits_per_1m_cache_in, credits_per_1m_cache_out,
		       credits_per_1m_image_tokens, credits_per_1m_audio_tokens, credits_per_1m_video_tokens,
		       COALESCE(manual_in, FALSE), COALESCE(manual_out, FALSE),
		       COALESCE(manual_cache_in, FALSE), COALESCE(manual_cache_out, FALSE),
		       COALESCE(manual_image, FALSE), COALESCE(manual_audio, FALSE), COALESCE(manual_video, FALSE)
		FROM model_credit_rates WHERE canonical_id = $1
	`, canonicalID).Scan(
		&stored.In, &stored.Out, &stored.CacheIn, &stored.CacheOut,
		&stored.Image, &stored.Audio, &stored.Video,
		&stored.ManualIn, &stored.ManualOut, &stored.ManualCacheIn, &stored.ManualCacheOut,
		&stored.ManualImage, &stored.ManualAudio, &stored.ManualVideo,
	)
	return stored, err
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

// ResetModelRateFields clears manual flags for given fields (e.g. "in", "out", "cache_in", "cache_out", "image", "audio", "video", "all").
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
		case "image":
			setClauses = append(setClauses, "manual_image = FALSE, credits_per_1m_image_tokens = NULL")
		case "audio":
			setClauses = append(setClauses, "manual_audio = FALSE, credits_per_1m_audio_tokens = NULL")
		case "video":
			setClauses = append(setClauses, "manual_video = FALSE, credits_per_1m_video_tokens = NULL")
		case "all":
			setClauses = append(setClauses,
				"manual_in = FALSE, credits_per_1m_in = NULL",
				"manual_out = FALSE, credits_per_1m_out = NULL",
				"manual_cache_in = FALSE, credits_per_1m_cache_in = NULL",
				"manual_cache_out = FALSE, credits_per_1m_cache_out = NULL",
				"manual_image = FALSE, credits_per_1m_image_tokens = NULL",
				"manual_audio = FALSE, credits_per_1m_audio_tokens = NULL",
				"manual_video = FALSE, credits_per_1m_video_tokens = NULL",
			)
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
		  AND NOT manual_in AND NOT manual_out
		  AND NOT manual_cache_in AND NOT manual_cache_out
		  AND NOT manual_image AND NOT manual_audio AND NOT manual_video
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
