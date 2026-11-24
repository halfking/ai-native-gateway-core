package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrInvalidSupplierRate  = errors.New("invalid supplier contract rate")
	ErrSupplierRateConflict = errors.New("rate effective time already has different evidence")
)

type SupplierRateInput struct {
	ProviderID    int64         `json:"provider_id"`
	CredentialID  int64         `json:"credential_id"`
	Model         string        `json:"model"`
	Reference     string        `json:"reference"`
	Currency      string        `json:"currency"`
	EffectiveFrom time.Time     `json:"effective_from"`
	EffectiveTo   *time.Time    `json:"effective_to,omitempty"`
	Terms         SupplierTerms `json:"terms"`
}

type SupplierTerms struct {
	SubtractFromInput []string            `json:"subtract_from_input,omitempty"`
	Components        []SupplierComponent `json:"components"`
	AmountScale       *int                `json:"amount_scale,omitempty"`
}

type SupplierComponent struct {
	Meter string         `json:"meter"`
	Unit  int64          `json:"unit"`
	Tiers []SupplierTier `json:"tiers"`
}

// Through is the cumulative inclusive limit within one attempt. The last tier has no limit.
type SupplierTier struct {
	Through *int64 `json:"through,omitempty"`
	Price   string `json:"price"`
}

type SupplierRateImport struct {
	RateID  uuid.UUID `json:"rate_id"`
	Created bool      `json:"created"`
}

var supportedSupplierMeters = [...]string{"confirmed_send", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
	"audio_input_duration_us", "audio_billed_duration_us", "audio_output_duration_us", "generated_images", "video_output_duration_us"}

func normalizeSupplierRate(in SupplierRateInput) (SupplierRateInput, error) {
	in.Model, in.Reference = strings.TrimSpace(in.Model), strings.TrimSpace(in.Reference)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.ProviderID <= 0 || in.CredentialID <= 0 || !validInvoiceText(in.Model, 256) || !validInvoiceText(in.Reference, 160) ||
		!invoiceCurrencyRE.MatchString(in.Currency) || in.EffectiveFrom.IsZero() ||
		in.EffectiveFrom.Nanosecond()%1000 != 0 || in.EffectiveTo != nil &&
		(!in.EffectiveTo.After(in.EffectiveFrom) || in.EffectiveTo.Nanosecond()%1000 != 0) {
		return in, fmt.Errorf("%w: provider, credential, exact model, reference, currency and effective period are required", ErrInvalidSupplierRate)
	}
	in.EffectiveFrom = in.EffectiveFrom.UTC()
	if in.EffectiveTo != nil {
		end := in.EffectiveTo.UTC()
		in.EffectiveTo = &end
	}
	if len(in.Terms.Components) < 1 || len(in.Terms.Components) > len(supportedSupplierMeters) {
		return in, fmt.Errorf("%w: expected one component per billed meter", ErrInvalidSupplierRate)
	}
	if scale := in.Terms.AmountScale; scale != nil && (*scale < 0 || *scale > 24) {
		return in, fmt.Errorf("%w: amount_scale must be between 0 and 24", ErrInvalidSupplierRate)
	}
	seen := make(map[string]bool, len(in.Terms.Components))
	in.Terms.Components = slices.Clone(in.Terms.Components)
	for i := range in.Terms.Components {
		c := &in.Terms.Components[i]
		if seen[c.Meter] || !slices.Contains(supportedSupplierMeters[:], c.Meter) ||
			(c.Unit != 1 && c.Unit != 1000 && c.Unit != 1_000_000 && !(c.Unit == 60_000_000 && strings.HasSuffix(c.Meter, "_duration_us"))) ||
			len(c.Tiers) < 1 || len(c.Tiers) > 20 || c.Meter == "confirmed_send" && (c.Unit != 1 || len(c.Tiers) != 1) {
			return in, fmt.Errorf("%w: invalid meter, unit or tier count", ErrInvalidSupplierRate)
		}
		seen[c.Meter] = true
		c.Tiers = slices.Clone(c.Tiers)
		var previous int64
		for j := range c.Tiers {
			tier := &c.Tiers[j]
			price, err := parseInvoiceAmount(tier.Price)
			if err != nil || price.IsNegative() || (tier.Through == nil) != (j == len(c.Tiers)-1) ||
				tier.Through != nil && *tier.Through <= previous {
				return in, fmt.Errorf("%w: tier prices must be nonnegative with increasing limits and an open last tier", ErrInvalidSupplierRate)
			}
			tier.Price = price.String()
			if tier.Through != nil {
				previous = *tier.Through
			}
		}
	}
	in.Terms.SubtractFromInput = slices.Clone(in.Terms.SubtractFromInput)
	slices.Sort(in.Terms.SubtractFromInput)
	for i, meter := range in.Terms.SubtractFromInput {
		if !seen["input_tokens"] || (meter != "cache_read_tokens" && meter != "cache_write_tokens") ||
			i > 0 && meter == in.Terms.SubtractFromInput[i-1] {
			return in, fmt.Errorf("%w: invalid input cache adjustment", ErrInvalidSupplierRate)
		}
	}
	slices.SortFunc(in.Terms.Components, func(a, b SupplierComponent) int { return strings.Compare(a.Meter, b.Meter) })
	return in, nil
}

// ImportSupplierRate appends a normalized supplier contract version.
func ImportSupplierRate(ctx context.Context, pool *pgxpool.Pool, input SupplierRateInput, actor string) (SupplierRateImport, error) {
	input, err := normalizeSupplierRate(input)
	if err != nil {
		return SupplierRateImport{}, err
	}
	if !validInvoiceText(actor, 160) {
		return SupplierRateImport{}, ErrInvalidSupplierRate
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return SupplierRateImport{}, err
	}
	hash := sha256.Sum256(encoded)
	checksum := hex.EncodeToString(hash[:])
	var result SupplierRateImport
	err = writeReconciliationEvidence(ctx, pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO public.supplier_contract_rates
			(rate_id,provider_id,credential_id,model,reference,currency,effective_from,effective_to,terms,content_sha256,imported_by)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb->'terms',$10,$11
			WHERE EXISTS (SELECT 1 FROM public.credentials WHERE id=$3 AND provider_id=$2)
			ON CONFLICT (provider_id,credential_id,model,effective_from) DO NOTHING RETURNING rate_id`,
			uuid.New(), input.ProviderID, input.CredentialID, input.Model, input.Reference, input.Currency,
			input.EffectiveFrom, input.EffectiveTo, string(encoded), checksum, actor).Scan(&result.RateID)
		result.Created = err == nil
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var existing string
		err = tx.QueryRow(ctx, `SELECT rate_id, content_sha256 FROM public.supplier_contract_rates
			WHERE provider_id=$1 AND credential_id=$2 AND model=$3 AND effective_from=$4`,
			input.ProviderID, input.CredentialID, input.Model, input.EffectiveFrom).Scan(&result.RateID, &existing)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: credential must belong to the supplier", ErrInvalidSupplierRate)
		}
		if err != nil {
			return err
		}
		if existing != checksum {
			return ErrSupplierRateConflict
		}
		return nil
	})
	if err != nil {
		return SupplierRateImport{}, err
	}
	return result, nil
}

func ListSupplierRates(ctx context.Context, pool *pgxpool.Pool, providerID, credentialID int64, model string, limit, offset int) ([]json.RawMessage, error) {
	if providerID <= 0 || credentialID <= 0 || !validInvoiceText(model, 256) {
		return nil, ErrInvalidSupplierRate
	}
	tx, err := beginPlatformEvidenceRead(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var items []json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY effective_from DESC),'[]'::jsonb)
		FROM (SELECT rate_id,reference,currency,effective_from,effective_to,terms,content_sha256,imported_by,imported_at
		  FROM public.supplier_contract_rates WHERE provider_id=$1 AND credential_id=$2 AND model=$3
		  ORDER BY effective_from DESC LIMIT $4 OFFSET $5) r`, providerID, credentialID, model, limit, offset).Scan(&items); err != nil {
		return nil, err
	}
	return items, tx.Commit(ctx)
}

func supplierAmount(terms SupplierTerms, use UsageEvidence) *string {
	one := int64(1)
	metres := map[string]*int64{"confirmed_send": &one, "input_tokens": use.InputTokens, "output_tokens": use.OutputTokens,
		"cache_read_tokens": use.CacheReadTokens, "cache_write_tokens": use.CacheWriteTokens,
		"audio_input_duration_us": use.Media.AudioInputDurationUS, "audio_billed_duration_us": use.Media.AudioBilledDurationUS,
		"audio_output_duration_us": use.Media.AudioOutputDurationUS, "generated_images": use.Media.GeneratedImages,
		"video_output_duration_us": use.Media.VideoOutputDurationUS}
	if len(terms.SubtractFromInput) > 0 {
		if use.InputTokens == nil {
			return nil
		}
		input := *use.InputTokens
		for _, meter := range terms.SubtractFromInput {
			tokens := metres[meter]
			if tokens == nil || *tokens < 0 || *tokens > input {
				return nil
			}
			input -= *tokens
		}
		metres["input_tokens"] = &input
	}
	total := new(big.Rat)
	for _, c := range terms.Components {
		quantity := metres[c.Meter]
		if quantity == nil || *quantity < 0 {
			return nil
		}
		var previous int64
		for _, tier := range c.Tiers {
			end := *quantity
			if tier.Through != nil && *tier.Through < end {
				end = *tier.Through
			}
			price, ok := new(big.Rat).SetString(tier.Price)
			if !ok {
				return nil
			}
			total.Add(total, price.Mul(price, big.NewRat(end-previous, c.Unit)))
			if end == *quantity {
				break
			}
			previous = end
		}
	}
	scale := 24
	if terms.AmountScale != nil {
		scale = *terms.AmountScale
	}
	value := total.FloatString(scale)
	if terms.AmountScale == nil {
		exact, _ := new(big.Rat).SetString(value)
		if exact.Cmp(total) != 0 {
			return nil
		}
	}
	if scale > 0 {
		value = strings.TrimSuffix(strings.TrimRight(value, "0"), ".")
	}
	return &value
}

// projectSupplierEstimate runs after a transport or usage event while its attempt row is locked.
func projectSupplierEstimate(ctx context.Context, tx pgx.Tx, attemptID string) error {
	var snapshot []byte
	var quantity UsageEvidence
	err := tx.QueryRow(ctx, `SELECT COALESCE(a.rate_snapshot->'supplier_contract', applicable.document),
		a.input_tokens, a.output_tokens, a.cache_read_tokens, a.cache_write_tokens,
		a.audio_input_duration_us, a.audio_billed_duration_us, a.audio_output_duration_us, a.generated_images, a.video_output_duration_us
		FROM public.provider_call_attempts a
		LEFT JOIN LATERAL (
		  SELECT jsonb_build_object('rate_id',r.rate_id,'provider_id',r.provider_id,'credential_id',r.credential_id,
		    'model',r.model,'reference',r.reference,'currency',r.currency,'effective_from',r.effective_from,
		    'effective_to',r.effective_to,'terms',r.terms,'imported_at',r.imported_at,'content_sha256',r.content_sha256) AS document
		  FROM (
		    SELECT * FROM public.supplier_contract_rates r
		    WHERE r.provider_id=a.provider_id AND r.credential_id=a.credential_id AND r.model=a.model
		      AND r.effective_from<=a.sent_at AND r.imported_at<=a.sent_at
		    ORDER BY r.effective_from DESC LIMIT 1
		  ) r
		  WHERE r.effective_to IS NULL OR a.sent_at<r.effective_to
		) applicable ON true
		WHERE a.attempt_id=$1 AND a.delivery='sent' AND a.sent_at IS NOT NULL
		  AND a.provider_id IS NOT NULL AND a.credential_id IS NOT NULL AND NULLIF(a.model,'') IS NOT NULL`,
		attemptID).Scan(&snapshot, &quantity.InputTokens, &quantity.OutputTokens, &quantity.CacheReadTokens, &quantity.CacheWriteTokens,
		&quantity.Media.AudioInputDurationUS, &quantity.Media.AudioBilledDurationUS, &quantity.Media.AudioOutputDurationUS,
		&quantity.Media.GeneratedImages, &quantity.Media.VideoOutputDurationUS)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && len(snapshot) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	var rate SupplierRateInput
	if err := json.Unmarshal(snapshot, &rate); err != nil {
		return err
	}
	rate, err = normalizeSupplierRate(rate)
	if err != nil {
		return err
	}
	amount := supplierAmount(rate.Terms, quantity)
	_, err = tx.Exec(ctx, `UPDATE public.provider_call_attempts
		SET rate_snapshot=COALESCE(rate_snapshot,'{}'::jsonb) || jsonb_build_object('supplier_contract',$2::jsonb),
		    estimated_amount=$3::numeric, estimated_currency=CASE WHEN $3::numeric IS NOT NULL THEN $4::text END
		WHERE attempt_id=$1`, attemptID, string(snapshot), amount, rate.Currency)
	return err
}
