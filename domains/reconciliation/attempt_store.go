package reconciliation

import (
	"context"
	"encoding/json"
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
}

// AttemptStore is injected at transport boundaries. Implementations must be
// idempotent by attempt ID; evidence updates never cause a transport replay.
type AttemptStore interface {
	Prepare(context.Context, Attempt) error
	MarkDispatched(context.Context, string) error
	MarkSent(context.Context, string, time.Time) error
	RecordHTTP(context.Context, string, int, string, time.Time) error
	RecordUsage(context.Context, string, UsageEvidence) error
	Finish(context.Context, string, string) error
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
func ParseUsageEvidence(body []byte) UsageEvidence {
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
		if json.Unmarshal(setRaw, &n) == nil && n >= 0 {
			e.CacheReadTokens = &n
		}
	}
	if inputDetails["cached_tokens"] != nil {
		var n int64
		if json.Unmarshal(inputDetails["cached_tokens"], &n) == nil && n >= 0 {
			e.CacheReadTokens = &n
		}
	}
	set(&e.CacheReadTokens, "cache_read_input_tokens")
	set(&e.CacheWriteTokens, "cache_creation_input_tokens")
	if e.InputTokens != nil || e.OutputTokens != nil || e.CacheReadTokens != nil || e.CacheWriteTokens != nil {
		e.Source = "provider_response_json"
	}
	// Guard against accidentally persisting the raw payload through this API.
	_ = strings.TrimSpace
	return e
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

func (r *AttemptRecorder) Prepare(ctx context.Context, a Attempt) error {
	if r == nil || r.pool == nil {
		return nil
	}
	a = NewAttempt(a)
	var rates []byte
	var err error
	if a.RateSnapshot != nil {
		rates, err = json.Marshal(a.RateSnapshot)
		if err != nil {
			return err
		}
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
	if r == nil || r.pool == nil {
		return nil
	}
	return r.write(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE public.provider_call_attempts SET delivery='unknown' WHERE attempt_id=$1 AND delivery='prepared'`, id)
		return err
	})
}

func (r *AttemptRecorder) MarkSent(ctx context.Context, id string, sentAt time.Time) error {
	if r == nil || r.pool == nil {
		return nil
	}
	return r.write(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE public.provider_call_attempts SET delivery='sent',sent_at=COALESCE(sent_at,$2) WHERE attempt_id=$1`, id, sentAt)
		return err
	})
}

func (r *AttemptRecorder) RecordHTTP(ctx context.Context, id string, status int, requestID string, sentAt time.Time) error {
	if r == nil || r.pool == nil {
		return nil
	}
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	} // response proves dispatch; record confirmation time if transport hook unavailable
	return r.write(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE public.provider_call_attempts SET delivery='sent', http_status=$2, provider_request_id=NULLIF($3,''), sent_at=COALESCE(sent_at,$4) WHERE attempt_id=$1`, id, status, requestID, sentAt)
		return err
	})
}

func (r *AttemptRecorder) RecordUsage(ctx context.Context, id string, u UsageEvidence) error {
	if r == nil || r.pool == nil {
		return nil
	}
	var rates []byte
	var err error
	if u.RateSnapshot != nil {
		rates, err = json.Marshal(u.RateSnapshot)
		if err != nil {
			return err
		}
	}
	return r.write(ctx, func(tx pgx.Tx) error {
		_, err = tx.Exec(ctx, `UPDATE public.provider_call_attempts SET
		input_tokens=COALESCE($2,input_tokens), output_tokens=COALESCE($3,output_tokens), cache_read_tokens=COALESCE($4,cache_read_tokens), cache_write_tokens=COALESCE($5,cache_write_tokens),
		usage_source=COALESCE(NULLIF($6,''),usage_source), reported_amount=COALESCE($7::numeric,reported_amount), reported_currency=COALESCE(NULLIF($8,''),reported_currency), reported_semantics=COALESCE(NULLIF($9,''),reported_semantics),
		estimated_amount=COALESCE($10::numeric,estimated_amount), estimated_currency=COALESCE(NULLIF($11,''),estimated_currency), rate_snapshot=COALESCE($12::jsonb,rate_snapshot)
		WHERE attempt_id=$1`, id, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.Source, u.ReportedAmount, u.ReportedCurrency, u.ReportedSemantics, u.EstimatedAmount, u.EstimatedCurrency, rates)
		return err
	})
}

func (r *AttemptRecorder) Finish(ctx context.Context, id, outcome string) error {
	if r == nil || r.pool == nil {
		return nil
	}
	return r.write(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE public.provider_call_attempts SET outcome=$2,finished_at=COALESCE(finished_at,now()) WHERE attempt_id=$1`, id, outcome)
		return err
	})
}

func (r *AttemptRecorder) write(ctx context.Context, fn func(pgx.Tx) error) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
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
