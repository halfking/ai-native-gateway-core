package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

var (
	ErrInvalidInvoice  = errors.New("invalid invoice")
	ErrInvoiceConflict = errors.New("invoice reference already has different evidence")
	invoiceAmountRE    = regexp.MustCompile(`^[+-]?[0-9]{1,30}(\.[0-9]{1,18})?$`)
	invoiceCurrencyRE  = regexp.MustCompile(`^[A-Z]{3}$`)
)

const DefaultMonthTimezone = "Asia/Shanghai"

// NaturalMonth returns the exact half-open window for a calendar month.
func NaturalMonth(month, timezone string) (start, end time.Time, err error) {
	if timezone == "" || timezone == "Local" {
		return start, end, errors.New("timezone must be an IANA name or UTC")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return start, end, errors.New("invalid IANA timezone")
	}
	label, err := time.Parse("2006-01", month)
	if err != nil || label.Format("2006-01") != month || label.Year() < 1 || label.Year() > 9998 {
		return time.Time{}, end, errors.New("month must be in YYYY-MM format")
	}
	start, startOK := firstInstantAtLocalDate(label, loc)
	end, endOK := firstInstantAtLocalDate(label.AddDate(0, 1, 0), loc)
	if !startOK || !endOK || !end.After(start) || start.Month() != label.Month() {
		return time.Time{}, time.Time{}, errors.New("could not resolve the natural month boundaries")
	}
	return start, end, nil
}

// NaturalDay returns the exact half-open window for a local calendar date.
// A date skipped by a timezone transition has no report window.
func NaturalDay(day, timezone string) (start, end time.Time, err error) {
	if timezone == "" || timezone == "Local" {
		return start, end, errors.New("timezone must be an IANA name or UTC")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return start, end, errors.New("invalid IANA timezone")
	}
	label, err := time.Parse(time.DateOnly, day)
	if err != nil || label.Format(time.DateOnly) != day || label.Year() < 1 || label.Year() > 9998 {
		return time.Time{}, end, errors.New("day must be in YYYY-MM-DD format")
	}
	start, startOK := firstInstantAtLocalDate(label, loc)
	end, endOK := firstInstantAtLocalDate(label.AddDate(0, 0, 1), loc)
	if !startOK || !endOK || start.Format(time.DateOnly) != day || !end.After(start) {
		return time.Time{}, time.Time{}, errors.New("day does not exist in the requested timezone")
	}
	return start, end, nil
}

// Walk offset intervals around nominal midnight. The first actual instant
// on or after the local date is well defined across gaps and repeated times.
func firstInstantAtLocalDate(label time.Time, loc *time.Location) (time.Time, bool) {
	const search = 48 * time.Hour
	end := label.Add(search)
	for cursor := label.Add(-search); cursor.Before(end); {
		local := cursor.In(loc)
		_, offset := local.Zone()
		_, next := local.ZoneBounds()
		if next.IsZero() || next.After(end) {
			next = end
		}
		candidate := label.Add(-time.Duration(offset) * time.Second)
		if candidate.Before(cursor) {
			candidate = cursor
		}
		if candidate.Before(next) {
			return candidate.In(loc), true
		}
		cursor = next
	}
	return time.Time{}, false
}

// InvoiceInput is one normalized bill. CredentialIDs identify the account's
// credentials during the invoice period, including retired credentials.
// Amounts are signed decimal strings in the stated currency.
type InvoiceInput struct {
	ProviderID    int64         `json:"provider_id"`
	AccountRef    string        `json:"account_ref"`
	Currency      string        `json:"currency"`
	InvoiceRef    string        `json:"invoice_ref"`
	Month         string        `json:"month"`
	Day           string        `json:"day,omitempty"`
	Timezone      string        `json:"timezone"`
	CredentialIDs []int64       `json:"credential_ids"`
	StatedTotal   string        `json:"stated_total"`
	Lines         []InvoiceLine `json:"lines"`
}

type InvoiceLine struct {
	LineRef           string              `json:"line_ref"`
	Kind              string              `json:"kind"`
	ProviderRequestID string              `json:"provider_request_id,omitempty"`
	Amount            string              `json:"amount"`
	Model             string              `json:"model,omitempty"`
	Description       string              `json:"description,omitempty"`
	UsageBucket       *InvoiceUsageBucket `json:"usage_bucket,omitempty"`
}

// InvoiceUsageBucket describes a model's total request charges in the invoice window.
// Optional quantities must use the same units and measurement basis as the ledger.
type InvoiceUsageBucket struct {
	Basis                 string `json:"basis"`
	SentAttempts          int64  `json:"sent_attempts"`
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	AudioInputDurationUS  *int64 `json:"audio_input_duration_us,omitempty"`
	AudioBilledDurationUS *int64 `json:"audio_billed_duration_us,omitempty"`
	AudioOutputDurationUS *int64 `json:"audio_output_duration_us,omitempty"`
	GeneratedImages       *int64 `json:"generated_images,omitempty"`
	VideoOutputDurationUS *int64 `json:"video_output_duration_us,omitempty"`
}

type InvoiceImportResult struct {
	InvoiceID uuid.UUID `json:"invoice_id"`
	Created   bool      `json:"created"`
}

func normalizeInvoice(input InvoiceInput) (InvoiceInput, time.Time, time.Time, error) {
	var zero time.Time
	input.AccountRef = strings.TrimSpace(input.AccountRef)
	input.InvoiceRef = strings.TrimSpace(input.InvoiceRef)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	if input.ProviderID <= 0 || !validInvoiceText(input.AccountRef, 160) || !validInvoiceText(input.InvoiceRef, 160) || !invoiceCurrencyRE.MatchString(input.Currency) {
		return input, zero, zero, fmt.Errorf("%w: provider, account reference, invoice reference, and a three-letter currency are required", ErrInvalidInvoice)
	}
	if input.Timezone == "" {
		input.Timezone = DefaultMonthTimezone
	}
	if (input.Day == "") == (input.Month == "") {
		return input, zero, zero, fmt.Errorf("%w: provide exactly one day or month", ErrInvalidInvoice)
	}
	period, bounds := input.Month, NaturalMonth
	if input.Day != "" {
		period, bounds = input.Day, NaturalDay
	}
	start, end, err := bounds(period, input.Timezone)
	if err != nil {
		return input, zero, zero, fmt.Errorf("%w: %v", ErrInvalidInvoice, err)
	}
	if len(input.CredentialIDs) == 0 || len(input.CredentialIDs) > 500 || len(input.Lines) == 0 || len(input.Lines) > 5000 {
		return input, zero, zero, fmt.Errorf("%w: expected 1–500 credential IDs and 1–5000 lines", ErrInvalidInvoice)
	}
	input.CredentialIDs = slices.Clone(input.CredentialIDs)
	slices.Sort(input.CredentialIDs)
	if input.CredentialIDs[0] <= 0 || len(slices.Compact(input.CredentialIDs)) != len(input.CredentialIDs) {
		return input, zero, zero, fmt.Errorf("%w: credential IDs must be distinct positive integers", ErrInvalidInvoice)
	}
	stated, err := parseInvoiceAmount(input.StatedTotal)
	if err != nil {
		return input, zero, zero, fmt.Errorf("%w: stated_total must be a decimal string", ErrInvalidInvoice)
	}
	input.StatedTotal = stated.String()
	input.Lines = slices.Clone(input.Lines)
	seen := make(map[string]bool, len(input.Lines))
	var sum decimal.Decimal
	for i := range input.Lines {
		line := &input.Lines[i]
		line.LineRef = strings.TrimSpace(line.LineRef)
		line.ProviderRequestID = strings.TrimSpace(line.ProviderRequestID)
		if !validInvoiceText(line.LineRef, 160) || seen[line.LineRef] || len(line.Model) > 160 || len(line.Description) > 1000 ||
			strings.ContainsRune(line.Model, 0) || strings.ContainsRune(line.Description, 0) {
			return input, zero, zero, fmt.Errorf("%w: invalid or duplicate line reference or oversized fields at line %d", ErrInvalidInvoice, i+1)
		}
		seen[line.LineRef] = true
		amount, err := parseInvoiceAmount(line.Amount)
		if err != nil || !validInvoiceLine(*line, amount) {
			return input, zero, zero, fmt.Errorf("%w: invalid amount, kind, or request reference at line %d", ErrInvalidInvoice, i+1)
		}
		line.Amount = amount.String()
		sum = sum.Add(amount)
	}
	if !sum.Equal(stated) {
		return input, zero, zero, fmt.Errorf("%w: line amounts do not equal stated_total", ErrInvalidInvoice)
	}
	slices.SortFunc(input.Lines, func(a, b InvoiceLine) int { return strings.Compare(a.LineRef, b.LineRef) })
	return input, start, end, nil
}

func validInvoiceText(value string, maxLen int) bool {
	return value != "" && len(value) <= maxLen && !strings.ContainsRune(value, 0)
}

func parseInvoiceAmount(value string) (decimal.Decimal, error) {
	if !invoiceAmountRE.MatchString(value) {
		return decimal.Zero, ErrInvalidInvoice
	}
	return decimal.NewFromString(value)
}

func validInvoiceLine(line InvoiceLine, amount decimal.Decimal) bool {
	if bucket := line.UsageBucket; bucket != nil {
		if line.Kind != "usage_bucket" || bucket.Basis != "request_total" ||
			line.Model == "" || strings.TrimSpace(line.Model) != line.Model || bucket.SentAttempts <= 0 {
			return false
		}
		for _, quantity := range []*int64{bucket.InputTokens, bucket.OutputTokens, bucket.AudioInputDurationUS,
			bucket.AudioBilledDurationUS, bucket.AudioOutputDurationUS, bucket.GeneratedImages, bucket.VideoOutputDurationUS} {
			if quantity != nil && *quantity < 0 {
				return false
			}
		}
	}
	if line.Kind == "request_total" {
		return validInvoiceText(line.ProviderRequestID, 512) && !amount.IsNegative()
	}
	if line.ProviderRequestID != "" {
		return false
	}
	switch line.Kind {
	case "usage_bucket", "fixed_fee", "tax":
		return !amount.IsNegative()
	case "discount", "credit", "refund":
		return !amount.IsPositive()
	case "adjustment":
		return true
	default:
		return false
	}
}

// ImportInvoice is immutable and transactionally idempotent for a supplier's
// account, currency and invoice reference. Corrections get their own document.
func ImportInvoice(ctx context.Context, pool *pgxpool.Pool, input InvoiceInput, actor string) (InvoiceImportResult, error) {
	input, start, end, err := normalizeInvoice(input)
	if err != nil {
		return InvoiceImportResult{}, err
	}
	if !validInvoiceText(actor, 160) {
		return InvoiceImportResult{}, fmt.Errorf("%w: import actor is required", ErrInvalidInvoice)
	}
	content, err := json.Marshal(input)
	if err != nil {
		return InvoiceImportResult{}, err
	}
	hash := sha256.Sum256(content)
	checksum := hex.EncodeToString(hash[:])
	lines, err := json.Marshal(input.Lines)
	if err != nil {
		return InvoiceImportResult{}, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return InvoiceImportResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_role','super_admin',true), set_config('app.bypass_rls','true',true)`); err != nil {
		return InvoiceImportResult{}, err
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO public.provider_invoices (
		invoice_id, provider_id, account_ref, currency, invoice_ref, month_start, timezone,
		window_start, window_end, credential_ids, stated_total, content_sha256, imported_by)
		VALUES ($1, $2, $3, $4, $5, $6::text::date, $7, $8, $9, $10, $11::text::numeric, $12, $13)
		ON CONFLICT (provider_id, account_ref, currency, invoice_ref) DO NOTHING RETURNING invoice_id`,
		uuid.New(), input.ProviderID, input.AccountRef, input.Currency, input.InvoiceRef, start.Format("2006-01")+"-01",
		input.Timezone, start, end, input.CredentialIDs, input.StatedTotal, checksum, actor).Scan(&id)
	created := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		var existing string
		err = tx.QueryRow(ctx, `SELECT invoice_id, content_sha256 FROM public.provider_invoices
			WHERE provider_id=$1 AND account_ref=$2 AND currency=$3 AND invoice_ref=$4`,
			input.ProviderID, input.AccountRef, input.Currency, input.InvoiceRef).Scan(&id, &existing)
		if err == nil && existing != checksum {
			return InvoiceImportResult{}, ErrInvoiceConflict
		}
	}
	if err != nil {
		return InvoiceImportResult{}, err
	}
	if created {
		var invalidCredentials int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM unnest($1::bigint[]) AS supplied(id)
			WHERE NOT EXISTS (SELECT 1 FROM public.credentials c WHERE c.id=supplied.id AND c.provider_id=$2)
			  AND NOT EXISTS (SELECT 1 FROM public.provider_call_attempts a
			      WHERE a.credential_id=supplied.id AND a.provider_id=$2
			        AND COALESCE(a.sent_at,a.prepared_at)>=$3 AND COALESCE(a.sent_at,a.prepared_at)<$4)`,
			input.CredentialIDs, input.ProviderID, start, end).Scan(&invalidCredentials)
		if err != nil {
			return InvoiceImportResult{}, err
		}
		if invalidCredentials != 0 {
			return InvoiceImportResult{}, fmt.Errorf("%w: %d credential IDs lack evidence for this provider", ErrInvalidInvoice, invalidCredentials)
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.provider_invoice_lines
			(invoice_id, line_ref, kind, provider_request_id, amount, model, description,
			 bucket_sent_attempts, bucket_input_tokens, bucket_output_tokens, bucket_audio_input_duration_us,
			 bucket_audio_billed_duration_us, bucket_audio_output_duration_us, bucket_generated_images, bucket_video_output_duration_us)
			SELECT $1, line_ref, kind, provider_request_id, amount::numeric, model, description,
			 (usage_bucket->>'sent_attempts')::bigint, (usage_bucket->>'input_tokens')::bigint, (usage_bucket->>'output_tokens')::bigint,
			 (usage_bucket->>'audio_input_duration_us')::bigint, (usage_bucket->>'audio_billed_duration_us')::bigint,
			 (usage_bucket->>'audio_output_duration_us')::bigint, (usage_bucket->>'generated_images')::bigint,
			 (usage_bucket->>'video_output_duration_us')::bigint
			FROM jsonb_to_recordset($2::jsonb) AS l(line_ref text, kind text, provider_request_id text,
				amount text, model text, description text, usage_bucket jsonb)`, id, string(lines))
		if err != nil {
			return InvoiceImportResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return InvoiceImportResult{}, err
	}
	return InvoiceImportResult{InvoiceID: id, Created: created}, nil
}
