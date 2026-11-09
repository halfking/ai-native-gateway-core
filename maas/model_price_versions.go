package maas

// model_price_versions（迁移 952，2026-11-09 运营审计轮项2）—— 内部零售价
// 版本账。
//
// 需求原文：「一个模型一个套餐模式一个价格……不要出现重复的价格，每个
// 价格是额效期的，下一个同一模型同一套餐模式会自动将前一期的结束日期
// 进行修正。并且可以看到我们的订价变化模式。」
//
// 形状（与 939 tenant_model_rates / pricing_plans 的现行期口径同构）：
//   • PK(canonical_id, billing_mode, effective_from)；
//   • effective_to NULL = 现行期；
//   • 部分唯一索引 uq_model_price_versions_current ⇒ 同一 (模型, 模式)
//     任意时刻至多一个现行价（「不出现重复价格」的库级强制）。
//
// 计费热路径零改动：chargeTokensWithMultiplier 继续读 model_credit_rates；
// 本文件只做版本账的写同步与只读查询面。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// BillingMode 闭集（库级 CHECK 同源）：按量（驱动计费）/ 包月套餐价 /
// 加油包价。
const (
	BillingModePayAsYouGo = "pay_as_you_go"
	BillingModeTokenPlan  = "token_plan"
	BillingModeBooster    = "booster_pack"
)

// Price period sources（库级 CHECK 同源）。
const (
	RateSourceManual     = "manual"
	RateSourceBatch      = "batch"
	RateSourceGlobalFill = "global_fill"
	RateSourceReset      = "reset"
	RateSourceSeed       = "seed"
)

// priceVersionValues is the nullable 7-dimension price snapshot shared by
// model_credit_rates rows and model_price_versions periods.
type priceVersionValues struct {
	In, Out, CacheIn, CacheOut, Image, Audio, Video *int64
}

func (v priceVersionValues) anyManual() bool {
	return v.In != nil || v.Out != nil || v.CacheIn != nil || v.CacheOut != nil ||
		v.Image != nil || v.Audio != nil || v.Video != nil
}

func (v priceVersionValues) sameAs(o priceVersionValues) bool {
	return int64PtrEq(v.In, o.In) && int64PtrEq(v.Out, o.Out) &&
		int64PtrEq(v.CacheIn, o.CacheIn) && int64PtrEq(v.CacheOut, o.CacheOut) &&
		int64PtrEq(v.Image, o.Image) && int64PtrEq(v.Audio, o.Audio) &&
		int64PtrEq(v.Video, o.Video)
}

func int64PtrEq(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// syncModelPriceVersion reconciles the open version period for one
// (canonical, mode) with `vals`:
//   - vals empty (row deleted / no manual dimension left) ⇒ close the open
//     period, open nothing (the gap = global default price, faithfully).
//   - vals equal to the open period ⇒ no-op (unchanged prices never open a
//     new period — no duplicate periods).
//   - otherwise ⇒ atomically close the open period at the new period's start
//     and insert the new one.
//
// Callers decide whether this is an auxiliary billing snapshot (best-effort)
// or the primary reference-price write (errors must reach the caller).
func (s *Service) syncModelPriceVersion(ctx context.Context, canonicalID int, billingMode, source, note string, vals priceVersionValues) error {
	if s == nil || s.pool == nil || canonicalID <= 0 {
		return errors.New("price version service unavailable or invalid canonical_id")
	}
	syncCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tx, err := s.pool.Begin(syncCtx)
	if err != nil {
		return fmt.Errorf("begin price version: %w", err)
	}
	defer func() { _ = tx.Rollback(syncCtx) }()

	// FOR UPDATE cannot serialize the first write when no open row exists.
	// Serialize all writers of this model/mode, including an empty ledger.
	lockKey := fmt.Sprintf("model-price/%d/%s", canonicalID, billingMode)
	if _, err := tx.Exec(syncCtx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock price version: %w", err)
	}
	var open priceVersionValues
	var openFrom time.Time
	err = tx.QueryRow(syncCtx, `
		SELECT effective_from, credits_per_1m_in, credits_per_1m_out,
		       credits_per_1m_cache_in, credits_per_1m_cache_out,
		       credits_per_1m_image_tokens, credits_per_1m_audio_tokens, credits_per_1m_video_tokens
		FROM model_price_versions
		WHERE canonical_id = $1 AND billing_mode = $2 AND effective_to IS NULL
		FOR UPDATE
	`, canonicalID, billingMode).Scan(
		&openFrom, &open.In, &open.Out, &open.CacheIn, &open.CacheOut,
		&open.Image, &open.Audio, &open.Video)
	hasOpen := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read open price period: %w", err)
	}
	if hasOpen && vals.anyManual() && open.sameAs(vals) {
		// Unchanged prices never open duplicate periods.
		return nil
	}
	var effectiveAt time.Time
	if err := tx.QueryRow(syncCtx, `SELECT clock_timestamp()`).Scan(&effectiveAt); err != nil {
		return fmt.Errorf("read price transition time: %w", err)
	}
	// Keep periods nonempty even if the server clock moves backwards.
	if hasOpen && !effectiveAt.After(openFrom) {
		effectiveAt = openFrom.Add(time.Microsecond)
	}

	if !vals.anyManual() {
		// 行被清空/删除 ⇒ 只闭期不开新期。
		if hasOpen {
			if _, uerr := tx.Exec(syncCtx, `
				UPDATE model_price_versions
					SET effective_to = $3
				WHERE canonical_id = $1 AND billing_mode = $2 AND effective_to IS NULL
			`, canonicalID, billingMode, effectiveAt); uerr != nil {
				return fmt.Errorf("close price period: %w", uerr)
			}
		}
		if cerr := tx.Commit(syncCtx); cerr != nil {
			return fmt.Errorf("commit closed price period: %w", cerr)
		}
		return nil
	}

	if hasOpen {
		if _, uerr := tx.Exec(syncCtx, `
			UPDATE model_price_versions
			SET effective_to = $3
			WHERE canonical_id = $1 AND billing_mode = $2 AND effective_to IS NULL
		`, canonicalID, billingMode, effectiveAt); uerr != nil {
			return fmt.Errorf("close previous price period: %w", uerr)
		}
	}
	if _, ierr := tx.Exec(syncCtx, `
		INSERT INTO model_price_versions (
			canonical_id, billing_mode, effective_from,
			credits_per_1m_in, credits_per_1m_out,
			credits_per_1m_cache_in, credits_per_1m_cache_out,
			credits_per_1m_image_tokens, credits_per_1m_audio_tokens, credits_per_1m_video_tokens,
			source, note
		) VALUES ($1, $2, $12, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (canonical_id, billing_mode, effective_from) DO UPDATE SET
			credits_per_1m_in = EXCLUDED.credits_per_1m_in,
			credits_per_1m_out = EXCLUDED.credits_per_1m_out,
			credits_per_1m_cache_in = EXCLUDED.credits_per_1m_cache_in,
			credits_per_1m_cache_out = EXCLUDED.credits_per_1m_cache_out,
			credits_per_1m_image_tokens = EXCLUDED.credits_per_1m_image_tokens,
			credits_per_1m_audio_tokens = EXCLUDED.credits_per_1m_audio_tokens,
			credits_per_1m_video_tokens = EXCLUDED.credits_per_1m_video_tokens,
			source = EXCLUDED.source,
			note = EXCLUDED.note
	`, canonicalID, billingMode,
		vals.In, vals.Out, vals.CacheIn, vals.CacheOut, vals.Image, vals.Audio, vals.Video,
		source, note, effectiveAt); ierr != nil {
		return fmt.Errorf("insert price period: %w", ierr)
	}
	if cerr := tx.Commit(syncCtx); cerr != nil {
		return fmt.Errorf("commit price version: %w", cerr)
	}
	slog.Info("model_price_versions: price period recorded",
		"canonical_id", canonicalID, "billing_mode", billingMode, "source", source,
		"had_open", hasOpen)
	return nil
}

// readModelCreditRatesValues loads the current model_credit_rates row values
// (nullable — absent row = all nils).
func (s *Service) readModelCreditRatesValues(ctx context.Context, canonicalID int) (priceVersionValues, error) {
	var vals priceVersionValues
	err := s.pool.QueryRow(ctx, `
		SELECT credits_per_1m_in, credits_per_1m_out,
		       credits_per_1m_cache_in, credits_per_1m_cache_out,
		       credits_per_1m_image_tokens, credits_per_1m_audio_tokens, credits_per_1m_video_tokens
		FROM model_credit_rates WHERE canonical_id = $1
	`, canonicalID).Scan(
		&vals.In, &vals.Out, &vals.CacheIn, &vals.CacheOut,
		&vals.Image, &vals.Audio, &vals.Video)
	if errors.Is(err, pgx.ErrNoRows) {
		return priceVersionValues{}, nil
	}
	return vals, err
}

// SyncPayAsYouGoPriceVersion snapshots the current model_credit_rates row
// into the version ledger (billing mode pay_as_you_go). Called after every
// mutating model-rate write.
func (s *Service) SyncPayAsYouGoPriceVersion(ctx context.Context, canonicalID int, source, note string) {
	if s == nil || !s.Enabled() {
		return
	}
	vals, err := s.readModelCreditRatesValues(ctx, canonicalID)
	if err != nil {
		slog.Warn("model_price_versions: read rates failed; preserving open period", "canonical_id", canonicalID, "error", err)
		return
	}
	if err := s.syncModelPriceVersion(ctx, canonicalID, BillingModePayAsYouGo, source, note, vals); err != nil {
		slog.Warn("model_price_versions: billing snapshot failed", "canonical_id", canonicalID, "error", err)
	}
}

// UpsertModelPriceVersionForMode writes a reference price period for a
// non-billing billing mode (token_plan / booster_pack) straight into the
// version ledger. These prices do not drive the charge hot path — they are
// the 「一个模型一种模式一个价格」的数据面，供套餐/加油包展示与对比。
func (s *Service) UpsertModelPriceVersionForMode(ctx context.Context, canonicalID int, billingMode string, req ModelRateUpsert, note string) error {
	if !s.Enabled() {
		return errors.New("maas service disabled")
	}
	if canonicalID <= 0 {
		return errors.New("invalid canonical_id")
	}
	if billingMode != BillingModeTokenPlan && billingMode != BillingModeBooster {
		return errors.New("invalid billing_mode: " + billingMode)
	}
	var vals priceVersionValues
	mk := func(manual bool, v int64) *int64 {
		if !manual {
			return nil
		}
		x := v
		return &x
	}
	vals.In = mk(req.ManualIn, req.CreditsPer1MIn)
	vals.Out = mk(req.ManualOut, req.CreditsPer1MOut)
	vals.CacheIn = mk(req.ManualCacheIn, req.CreditsPer1MCacheIn)
	vals.CacheOut = mk(req.ManualCacheOut, req.CreditsPer1MCacheOut)
	vals.Image = mk(req.ManualImage, req.CreditsPer1MImage)
	vals.Audio = mk(req.ManualAudio, req.CreditsPer1MAudio)
	vals.Video = mk(req.ManualVideo, req.CreditsPer1MVideo)
	if !vals.anyManual() {
		return errors.New("no manual price dimensions set")
	}
	return s.syncModelPriceVersion(ctx, canonicalID, billingMode, RateSourceManual, note, vals)
}

// ModelPriceVersionRow is one recorded price period.
type ModelPriceVersionRow struct {
	CanonicalID          int        `json:"canonical_id"`
	CanonicalName        string     `json:"canonical_name"`
	DisplayName          string     `json:"display_name"`
	BillingMode          string     `json:"billing_mode"`
	EffectiveFrom        time.Time  `json:"effective_from"`
	EffectiveTo          *time.Time `json:"effective_to"`
	CreditsPer1MIn       *int64     `json:"credits_per_1m_in"`
	CreditsPer1MOut      *int64     `json:"credits_per_1m_out"`
	CreditsPer1MCacheIn  *int64     `json:"credits_per_1m_cache_in"`
	CreditsPer1MCacheOut *int64     `json:"credits_per_1m_cache_out"`
	CreditsPer1MImage    *int64     `json:"credits_per_1m_image_tokens"`
	CreditsPer1MAudio    *int64     `json:"credits_per_1m_audio_tokens"`
	CreditsPer1MVideo    *int64     `json:"credits_per_1m_video_tokens"`
	Source               string     `json:"source"`
	Note                 string     `json:"note"`
}

const modelPriceVersionSelect = `
	SELECT v.canonical_id,
	       COALESCE(mc.display_name, mc.canonical_name, ''),
	       COALESCE(mc.canonical_name, ''),
	       v.billing_mode, v.effective_from, v.effective_to,
	       v.credits_per_1m_in, v.credits_per_1m_out,
	       v.credits_per_1m_cache_in, v.credits_per_1m_cache_out,
	       v.credits_per_1m_image_tokens, v.credits_per_1m_audio_tokens, v.credits_per_1m_video_tokens,
	       v.source, v.note
	FROM model_price_versions v
	LEFT JOIN models_canonical mc ON mc.id = v.canonical_id
`

// ListModelPriceVersions returns the period history for one model (optionally
// one mode), newest first.
func (s *Service) ListModelPriceVersions(ctx context.Context, canonicalID int, billingMode string, limit int) ([]ModelPriceVersionRow, error) {
	if !s.Enabled() {
		return nil, errors.New("maas service disabled")
	}
	if canonicalID <= 0 {
		return nil, errors.New("invalid canonical_id")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := modelPriceVersionSelect + `
		WHERE v.canonical_id = $1 AND ($2 = '' OR v.billing_mode = $2)
		ORDER BY v.effective_from DESC
		LIMIT $3`
	rows, err := s.pool.Query(ctx, q, canonicalID, billingMode, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectModelPriceVersions(rows)
}

// ListModelPriceChanges returns recent price periods across all models,
// newest first — the 「订价变化模式」 feed (the UI derives deltas from
// consecutive periods of the same model+mode).
func (s *Service) ListModelPriceChanges(ctx context.Context, days int, limit int) ([]ModelPriceVersionRow, error) {
	if !s.Enabled() {
		return nil, errors.New("maas service disabled")
	}
	if days <= 0 || days > 365 {
		days = 30
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, modelPriceVersionSelect+`
		WHERE v.effective_from > now() - make_interval(days => $1)
		ORDER BY v.effective_from DESC
		LIMIT $2`, days, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectModelPriceVersions(rows)
}

func collectModelPriceVersions(rows pgx.Rows) ([]ModelPriceVersionRow, error) {
	out := make([]ModelPriceVersionRow, 0)
	for rows.Next() {
		var r ModelPriceVersionRow
		if err := rows.Scan(
			&r.CanonicalID, &r.DisplayName, &r.CanonicalName,
			&r.BillingMode, &r.EffectiveFrom, &r.EffectiveTo,
			&r.CreditsPer1MIn, &r.CreditsPer1MOut,
			&r.CreditsPer1MCacheIn, &r.CreditsPer1MCacheOut,
			&r.CreditsPer1MImage, &r.CreditsPer1MAudio, &r.CreditsPer1MVideo,
			&r.Source, &r.Note,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
