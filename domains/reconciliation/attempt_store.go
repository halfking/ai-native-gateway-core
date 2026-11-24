package reconciliation

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Attempt is the durable identity and routing context for one outbound call.
// It deliberately contains no request/response bodies or credentials.
type Attempt struct {
	ID, TenantID, RequestID, JobID, TrafficClass, ClientID, Source string
	AttemptNo, ProviderID, CredentialID                            int
	Model                                                          string
	PreparedAt                                                     time.Time
	RateSnapshot                                                   map[string]any
}

type UsageEvidence struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens *int64
	Source                                                       string
	ReportedAmount                                               *string
	ReportedCurrency, ReportedSemantics                          string
	EstimatedAmount                                              *string
	EstimatedCurrency                                            string
	RateSnapshot                                                 map[string]any
	Media                                                        MediaUsageEvidence `json:",omitzero"`
}

type MediaUsageEvidence struct {
	AudioInputDurationUS  *int64
	AudioBilledDurationUS *int64
	AudioOutputDurationUS *int64
	GeneratedImages       *int64
	VideoOutputDurationUS *int64
	Source                string
}

// AttemptStore is injected at transport boundaries. Implementations must be
// idempotent by attempt ID; evidence updates never cause a transport replay.
type AttemptStore interface {
	Prepare(context.Context, Attempt) error
	MarkDispatched(context.Context, string) error
	MarkNotSent(context.Context, string) error
	MarkSent(context.Context, string, time.Time) error
	RecordHTTP(context.Context, string, int, string, time.Time) error
	RecordUsage(context.Context, string, UsageEvidence) error
	Finish(context.Context, string, string) error
}

// RecordTransportFailure uses only definitive pre-request network errors to
// mark an attempt as unsent. Partial writes and response-side errors may be billed.
func RecordTransportFailure(ctx context.Context, store AttemptStore, id string, transportErr error, sent time.Time) error {
	outcome := "unknown"
	var deliveryErr error
	if !sent.IsZero() {
		deliveryErr = store.MarkSent(ctx, id, sent)
	} else if failedBeforeRequest(transportErr) {
		deliveryErr = store.MarkNotSent(ctx, id)
		outcome = "failure"
	}
	return errors.Join(deliveryErr, store.Finish(ctx, id, outcome))
}

func failedBeforeRequest(err error) bool {
	var dns *net.DNSError
	var transport *net.OpError
	var certificate *tls.CertificateVerificationError
	return errors.As(err, &dns) || (errors.As(err, &transport) && transport != nil && transport.Op == "dial") || errors.As(err, &certificate)
}

type AttemptRecorder struct{ pool *pgxpool.Pool }

func NewAttemptRecorder(pool *pgxpool.Pool) *AttemptRecorder { return &AttemptRecorder{pool: pool} }

func NewAttempt(a Attempt) Attempt {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.PreparedAt.IsZero() {
		a.PreparedAt = time.Now().UTC()
	}
	return a
}

// ParseUsageEvidence extracts only explicit supplier usage fields. Missing
// fields remain NULL; zero values remain explicit zero. It never estimates money.
func ParseUsageEvidence(body []byte, protocol ...string) UsageEvidence {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return UsageEvidence{}
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(root["usage"], &usage) != nil {
		return UsageEvidence{}
	}
	e := UsageEvidence{}
	set := func(dst **int64, keys ...string) {
		for _, key := range keys {
			if raw, ok := usage[key]; ok {
				if strings.TrimSpace(string(raw)) == "null" {
					continue
				}
				var n int64
				if json.Unmarshal(raw, &n) == nil && n >= 0 {
					*dst = &n
					return
				}
			}
		}
	}
	set(&e.InputTokens, "prompt_tokens", "input_tokens")
	set(&e.OutputTokens, "completion_tokens", "output_tokens")
	var promptDetails, inputDetails map[string]json.RawMessage
	_ = json.Unmarshal(usage["prompt_tokens_details"], &promptDetails)
	_ = json.Unmarshal(usage["input_tokens_details"], &inputDetails)
	if promptDetails["cached_tokens"] != nil {
		setRaw := promptDetails["cached_tokens"]
		var n int64
		if strings.TrimSpace(string(setRaw)) != "null" && json.Unmarshal(setRaw, &n) == nil && n >= 0 {
			e.CacheReadTokens = &n
		}
	}
	if inputDetails["cached_tokens"] != nil {
		var n int64
		if strings.TrimSpace(string(inputDetails["cached_tokens"])) != "null" && json.Unmarshal(inputDetails["cached_tokens"], &n) == nil && n >= 0 {
			e.CacheReadTokens = &n
		}
	}
	set(&e.CacheReadTokens, "cache_read_input_tokens")
	set(&e.CacheWriteTokens, "cache_creation_input_tokens")
	if e.InputTokens != nil || e.OutputTokens != nil || e.CacheReadTokens != nil || e.CacheWriteTokens != nil {
		e.Source = "provider_response_json"
		if len(protocol) > 0 && protocol[0] != "" {
			e.Source += ":" + strings.ToLower(protocol[0])
		}
	}
	return e
}

// ParseProviderResponseEvidence extracts explicit usage and any supported
// supplier-reported amount from a complete JSON response.
func ParseProviderResponseEvidence(body []byte, protocol, endpoint string) UsageEvidence {
	evidence := ParseUsageEvidence(body, protocol)
	if amount, currency, semantics, ok := ParseReportedCost(body, endpoint); ok {
		evidence.ReportedAmount, evidence.ReportedCurrency, evidence.ReportedSemantics = &amount, currency, semantics
	}
	return evidence
}

// EstimateAmount computes a routing reference from the legacy candidate price format.
// The supplier ledger projects contract estimates through projectSupplierEstimate.
func EstimateAmount(u UsageEvidence, rates map[string]any) (amount, currency string, ok bool) {
	if u.InputTokens == nil || u.OutputTokens == nil || rates == nil {
		return "", "", false
	}
	currency, _ = rates["currency"].(string)
	if strings.TrimSpace(currency) == "" {
		return "", "", false
	}
	getRate := func(name string) (*big.Rat, bool) {
		v, exists := rates[name]
		if !exists || v == nil {
			return nil, false
		}
		var f float64
		switch n := v.(type) {
		case float64:
			f = n
		case float32:
			f = float64(n)
		case int:
			f = float64(n)
		case int64:
			f = float64(n)
		default:
			return nil, false
		}
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !boundedDecimal(s) {
			return nil, false
		}
		r, good := new(big.Rat).SetString(s)
		return r, good
	}
	inRate, hasIn := getRate("input")
	outRate, hasOut := getRate("output")
	if !hasIn || !hasOut {
		return "", "", false
	}
	in := new(big.Int).SetInt64(*u.InputTokens)
	cacheRead := int64(0)
	if u.CacheReadTokens != nil {
		cacheRead = *u.CacheReadTokens
	}
	cacheWrite := int64(0)
	if u.CacheWriteTokens != nil {
		cacheWrite = *u.CacheWriteTokens
	}
	proto, _ := rates["protocol"].(string)
	if strings.HasPrefix(strings.ToLower(proto), "openai") || strings.Contains(strings.ToLower(proto), "responses") {
		// OpenAI input counts include cached tokens. When either split is
		// explicit, remove it from normal input pricing before applying its rate.
		if u.CacheReadTokens != nil || u.CacheWriteTokens != nil {
			normal := new(big.Int).Add(big.NewInt(cacheRead), big.NewInt(cacheWrite))
			if normal.Cmp(big.NewInt(*u.InputTokens)) > 0 {
				return "", "", false
			}
			in.Sub(in, normal)
		} else if strings.Contains(u.Source, "openai") {
			return "", "", false
		}
	} else if strings.HasPrefix(strings.ToLower(proto), "anthropic") {
		// Anthropic input_tokens excludes cached reads/creation; do not subtract.
	} else if cacheRead > 0 || cacheWrite > 0 {
		return "", "", false
	}
	total := new(big.Rat)
	add := func(tokens int64, rate *big.Rat) {
		term := new(big.Rat).Mul(new(big.Rat).SetInt64(tokens), rate)
		term.Quo(term, big.NewRat(1_000_000, 1))
		total.Add(total, term)
	}
	add(in.Int64(), inRate)
	add(*u.OutputTokens, outRate)
	if cacheRead > 0 {
		r, ok := getRate("cache_read")
		if !ok {
			return "", "", false
		}
		add(cacheRead, r)
	}
	if cacheWrite > 0 {
		r, ok := getRate("cache_write")
		if !ok {
			return "", "", false
		}
		add(cacheWrite, r)
	}
	return total.FloatString(12), currency, true
}

// ProviderRequestID extracts a documented/common response identifier without
// retaining the response body.
func ProviderRequestID(headers map[string][]string, body []byte) string {
	for _, name := range []string{"x-request-id", "request-id", "openai-request-id"} {
		for k, values := range headers {
			if strings.EqualFold(k, name) && len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				return strings.TrimSpace(values[0])
			}
		}
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &v) == nil {
		return strings.TrimSpace(v.ID)
	}
	return ""
}

// ParseReportedCost accepts only OpenRouter's documented usage.cost account
// charge and only when the actual upstream endpoint is under openrouter.ai.
func ParseReportedCost(body []byte, endpoint string) (amount, currency, semantics string, ok bool) {
	u, err := url.Parse(endpoint)
	if err != nil || !strings.EqualFold(u.Hostname(), "openrouter.ai") {
		return "", "", "", false
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return "", "", "", false
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(root["usage"], &usage) != nil {
		return "", "", "", false
	}
	raw, exists := usage["cost"]
	if !exists || strings.TrimSpace(string(raw)) == "null" {
		return "", "", "", false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return "", "", "", false
	}
	value := string(n)
	if !boundedDecimal(value) {
		return "", "", "", false
	}
	rat, good := new(big.Rat).SetString(value)
	if !good || rat.Sign() < 0 {
		return "", "", "", false
	}
	return value, "USD", "openrouter_usage_cost_usd", true
}

func boundedDecimal(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	mantissa := s
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		if i == len(s)-1 || strings.IndexAny(s[i+1:], "eE") >= 0 {
			return false
		}
		exp, err := strconv.Atoi(s[i+1:])
		if err != nil || exp < -18 || exp > 18 {
			return false
		}
		mantissa = s[:i]
	}
	if strings.HasPrefix(mantissa, "-") || strings.HasPrefix(mantissa, "+") {
		return false
	}
	digits := 0
	dot := false
	for _, c := range mantissa {
		if c == '.' && !dot {
			dot = true
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
		digits++
	}
	return digits > 0 && digits <= 38
}

func (r *AttemptRecorder) Prepare(ctx context.Context, a Attempt) error {
	if r == nil || r.pool == nil {
		return nil
	}
	a = NewAttempt(a)
	rates, err := rateSnapshotJSON(a.RateSnapshot)
	if err != nil {
		return err
	}
	return r.write(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO public.provider_call_attempts
		(attempt_id,tenant_id,request_id,job_id,traffic_class,client_id,source,attempt_no,provider_id,credential_id,model,prepared_at,delivery,rate_snapshot)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,0),NULLIF($10,0),$11,$12,'prepared',$13::jsonb)
		ON CONFLICT (attempt_id) DO NOTHING`, a.ID, nullable(a.TenantID), nullable(a.RequestID), nullable(a.JobID), a.TrafficClass, nullable(a.ClientID), a.Source, a.AttemptNo, a.ProviderID, a.CredentialID, a.Model, a.PreparedAt, rates)
		return err
	})
}

func (r *AttemptRecorder) MarkDispatched(ctx context.Context, id string) error {
	return r.update(ctx, id, false, `UPDATE public.provider_call_attempts SET
		delivery=CASE WHEN delivery='prepared' THEN 'unknown' ELSE delivery END WHERE attempt_id=$1`)
}

func (r *AttemptRecorder) MarkNotSent(ctx context.Context, id string) error {
	return r.update(ctx, id, false, `UPDATE public.provider_call_attempts SET
		delivery=CASE WHEN delivery IN ('prepared','unknown') THEN 'not_sent' ELSE delivery END WHERE attempt_id=$1`)
}

// providerHTTPStatusMin / providerHTTPStatusMax 是 http_status 的合法值域。
//
// ★ 口径来源：982 迁移的列级约束 `CHECK (http_status BETWEEN 100 AND 599)`
//
//	（sql/migrations/startup/982_provider_call_attempts.sql）⇒ 越界值**写不进库**。
//
// ★ 2026-11-24 真 PG 17 实测（本地 fixture 库，982 真实 DDL）：
//
//	越界值会让**整条 UPDATE** 被 23514 拒收 ⇒ 连 delivery / sent_at /
//	provider_request_id 一起丢（该行停在 prepared、sent_at 为 NULL）★
//	⇒ 所以收敛点必须在**写库之前**，不能指望数据库兜底。
const (
	providerHTTPStatusMin = 100
	providerHTTPStatusMax = 599
)

// providerHTTPStatusForWrite 把 HTTP 状态码收敛成「可入库的值」。
//
// ok=false 表示该状态码不在合法值域内（不是合法 HTTP 状态码）⇒ 调用方
// 应当**跳过这一列**而不是整条拒收——传输证据（delivery/sent_at/
// provider_request_id）比状态码本身更重要，丢不起。
//
// ★ 单一口径在这里（Go 侧），SQL 侧只做 COALESCE($2, 列) ⇒ 不会出现
//
//	「Go 判一个值域、SQL 判另一个值域」这种双口径漂移。
func providerHTTPStatusForWrite(status int) (int, bool) {
	if status < providerHTTPStatusMin || status > providerHTTPStatusMax {
		return 0, false
	}
	return status, true
}

func (r *AttemptRecorder) MarkSent(ctx context.Context, id string, sentAt time.Time) error {
	// ★ 2026-11-24（L5）零值时间会写成 0001-01-01（真 PG 实测）⇒ sent_at
	//   语义被污染，且两个部分索引的谓词含义同时失真
	//   （idx_..._sent 的 `sent_at IS NOT NULL` 会把它算作「已确认发送」，
	//     idx_..._unconfirmed 的 `sent_at IS NULL` 会把它排除出「未确认」）
	//   ⇒ 零值一律落到「现在」。
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	return r.update(ctx, id, true, `UPDATE public.provider_call_attempts SET delivery='sent',sent_at=COALESCE(sent_at,$2) WHERE attempt_id=$1`, sentAt)
}

func (r *AttemptRecorder) RecordHTTP(ctx context.Context, id string, status int, requestID string, sentAt time.Time) error {
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	} // response proves dispatch; record confirmation time if transport hook unavailable
	// ★ 2026-11-24（L5）越界的 http_status **不写这一列**，但其余证据照落：
	//   直接把越界值交给数据库会让**整条 UPDATE** 被 23514 拒收 ⇒
	//   传输证据（delivery/sent_at/provider_request_id）跟着一起丢。
	//   ⇒ 收敛后传 NULL，SQL 侧 COALESCE($2, http_status) 保持原值。
	//   ★ 传 NULL 而不是 0：0 同样落在 CHECK 值域外 ⇒ 会被 23514 拒收。
	var statusValue any
	if s, ok := providerHTTPStatusForWrite(status); ok {
		statusValue = s
	} else {
		slog.WarnContext(ctx, "provider attempt http status outside the writable range; status column skipped",
			"attempt_id", id, "status", status)
	}
	return r.update(ctx, id, true, `UPDATE public.provider_call_attempts SET delivery='sent', http_status=COALESCE($2::integer,http_status),
		provider_request_id=COALESCE(NULLIF($3,''),provider_request_id), sent_at=COALESCE(sent_at,$4) WHERE attempt_id=$1`, statusValue, requestID, sentAt)
}

func (r *AttemptRecorder) RecordUsage(ctx context.Context, id string, u UsageEvidence) error {
	if r == nil || r.pool == nil {
		return nil
	}
	rates, err := rateSnapshotJSON(u.RateSnapshot)
	if err != nil {
		return err
	}
	return r.update(ctx, id, true, `UPDATE public.provider_call_attempts SET
		input_tokens=COALESCE($2,input_tokens), output_tokens=COALESCE($3,output_tokens), cache_read_tokens=COALESCE($4,cache_read_tokens), cache_write_tokens=COALESCE($5,cache_write_tokens),
		usage_source=COALESCE(NULLIF($6,''),usage_source), reported_amount=COALESCE($7::numeric,reported_amount), reported_currency=COALESCE(NULLIF($8,''),reported_currency), reported_semantics=COALESCE(NULLIF($9,''),reported_semantics),
		estimated_amount=CASE WHEN rate_snapshot ? 'supplier_contract' THEN estimated_amount ELSE COALESCE($10::numeric,estimated_amount) END,
		estimated_currency=CASE WHEN rate_snapshot ? 'supplier_contract' THEN estimated_currency ELSE COALESCE(NULLIF($11,''),estimated_currency) END,
		rate_snapshot=COALESCE(rate_snapshot,$12::jsonb),
		audio_input_duration_us=COALESCE($13,audio_input_duration_us), audio_billed_duration_us=COALESCE($14,audio_billed_duration_us),
		audio_output_duration_us=COALESCE($15,audio_output_duration_us), generated_images=COALESCE($16,generated_images),
		video_output_duration_us=COALESCE($17,video_output_duration_us), media_usage_source=COALESCE(NULLIF($18,''),media_usage_source)
		WHERE attempt_id=$1`, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.Source, u.ReportedAmount, u.ReportedCurrency, u.ReportedSemantics, u.EstimatedAmount, u.EstimatedCurrency, rates,
		u.Media.AudioInputDurationUS, u.Media.AudioBilledDurationUS, u.Media.AudioOutputDurationUS, u.Media.GeneratedImages, u.Media.VideoOutputDurationUS, u.Media.Source)
}

func rateSnapshotJSON(snapshot map[string]any) (*string, error) {
	if snapshot == nil {
		return nil, nil
	}
	data, err := json.Marshal(snapshot)
	// Bind JSON as text for both cached statements and simple protocol.
	value := string(data)
	return &value, err
}

func (r *AttemptRecorder) Finish(ctx context.Context, id, outcome string) error {
	return r.finishAt(ctx, id, outcome, time.Now().UTC())
}

func (r *AttemptRecorder) finishAt(ctx context.Context, id, outcome string, at time.Time) error {
	return r.update(ctx, id, false, `UPDATE public.provider_call_attempts SET
		outcome=CASE WHEN $2='unknown' AND outcome IS NOT NULL AND outcome<>'unknown' THEN outcome ELSE $2 END,
		finished_at=COALESCE(finished_at,$3) WHERE attempt_id=$1`, outcome, at)
}

func (r *AttemptRecorder) update(ctx context.Context, id string, estimate bool, query string, args ...any) error {
	if r == nil || r.pool == nil {
		return nil
	}
	return r.write(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, query, append([]any{id}, args...)...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAttemptMissing
		}
		if estimate {
			savepoint, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			if err := projectSupplierEstimate(ctx, savepoint, id); err != nil {
				if rollbackErr := savepoint.Rollback(ctx); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
				slog.WarnContext(ctx, "supplier contract estimate unavailable", "attempt_id", id, "error", err)
			} else if err := savepoint.Commit(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *AttemptRecorder) write(ctx context.Context, fn func(pgx.Tx) error) error {
	return writeReconciliationEvidence(ctx, r.pool, fn)
}

func writeReconciliationEvidence(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollback, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollback)
	}()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.bypass_rls','true',true)`); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
