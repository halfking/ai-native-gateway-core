package credentialstate

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

var ErrNoDatabase = errors.New("credential state database not configured")

type Writer struct {
	dbPool *pgxpool.Pool
}

type Failure struct {
	Kind       errorsx.ErrorKind
	Detail     string
	RetryAfter time.Duration
}

func NewWriter(pool *pgxpool.Pool) *Writer {
	return &Writer{dbPool: pool}
}

func (w *Writer) Enabled() bool {
	return w != nil && w.dbPool != nil
}

func (w *Writer) RestoreOnSuccess(ctx context.Context, credentialID int) error {
	if !w.Enabled() {
		return ErrNoDatabase
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := w.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		UPDATE credentials
		SET availability_state      = 'ready',
		    availability_recover_at = NULL,
		    state_reason_code       = NULL,
		    state_updated_at        = now()
		WHERE id = $1
		  AND availability_state IN ('cooling', 'rate_limited', 'unreachable', 'degraded')
	`, credentialID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE credentials
		SET circuit_state        = 'closed',
		    consecutive_failures = 0,
		    cooling_until        = NULL
		WHERE id = $1
		  AND consecutive_failures > 0
	`, credentialID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *Writer) WriteOnError(ctx context.Context, credentialID int, failure Failure) error {
	if !w.Enabled() {
		return ErrNoDatabase
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	detail := trimDetail(failure.Detail)
	switch failure.Kind {
	case errorsx.KindQuotaPeriodic:
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET quota_state         = 'periodic_exhausted',
			    quota_recover_at    = $1,
			    state_reason_code   = $2,
			    state_reason_detail = $3,
			    state_updated_at    = now()
			WHERE id = $4
			  AND lifecycle_status = 'active'
			  AND quota_state NOT IN ('balance_exhausted', 'permanently_exhausted')
		`, inferQuotaRecoverAt(failure.Detail), string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindQuotaPermanent:
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET quota_state             = 'permanently_exhausted',
			    quota_recover_at        = NULL,
			    availability_state      = 'suspended',
			    availability_recover_at = NULL,
			    state_reason_code       = $1,
			    state_reason_detail     = $2,
			    state_updated_at        = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
		`, string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindQuota, errorsx.KindQuotaBalance:
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET quota_state             = 'balance_exhausted',
			    quota_recover_at        = NULL,
			    availability_state      = 'suspended',
			    availability_recover_at = NULL,
			    state_reason_code       = $1,
			    state_reason_detail     = $2,
			    state_updated_at        = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
			  AND quota_state NOT IN ('permanently_exhausted')
		`, string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindAuthRevoked:
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET availability_state      = 'suspended',
			    availability_recover_at = NULL,
			    state_reason_code       = $1,
			    state_reason_detail     = $2,
			    state_updated_at        = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
		`, string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindAuth:
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET availability_state      = 'auth_failed',
			    availability_recover_at = NULL,
			    state_reason_code       = $1,
			    state_reason_detail     = $2,
			    state_updated_at        = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
			  AND availability_state NOT IN ('suspended')
		`, string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindTransient:
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET state_reason_code       = $1,
			    state_reason_detail     = $2,
			    state_updated_at        = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
		`, string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindConcurrent, errorsx.KindRateLimit, errorsx.KindTimeout, errorsx.KindUpstreamDown, errorsx.KindStreamTimeout:
		availability := "cooling"
		if failure.Kind == errorsx.KindRateLimit {
			availability = "rate_limited"
		}
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET availability_state      = $1,
			    availability_recover_at = $2,
			    state_reason_code       = $3,
			    state_reason_detail     = $4,
			    state_updated_at        = now()
			WHERE id = $5
			  AND lifecycle_status = 'active'
			  AND availability_state NOT IN ('suspended', 'auth_failed')
		`, availability, time.Now().UTC().Add(coolingDuration(failure.Kind, failure.RetryAfter)), string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindNetwork:
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET availability_state      = 'unreachable',
			    availability_recover_at = $1,
			    state_reason_code       = $2,
			    state_reason_detail     = $3,
			    state_updated_at        = now()
			WHERE id = $4
			  AND lifecycle_status = 'active'
			  AND availability_state NOT IN ('suspended', 'auth_failed')
		`, time.Now().UTC().Add(coolingDuration(failure.Kind, failure.RetryAfter)), string(failure.Kind), detail, credentialID)
		return err
	default:
		return nil
	}
}

func coolingDuration(kind errorsx.ErrorKind, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	switch kind {
	case errorsx.KindConcurrent:
		return 15 * time.Second
	case errorsx.KindRateLimit:
		// 15 minutes cooling for rate limit errors (unless upstream provides retry_after)
		return 900 * time.Second
	case errorsx.KindTransient, errorsx.KindTimeout, errorsx.KindStreamTimeout:
		return 30 * time.Second
	case errorsx.KindUpstreamDown:
		return 60 * time.Second
	case errorsx.KindNetwork:
		return 120 * time.Second
	default:
		return 30 * time.Second
	}
}

func inferQuotaRecoverAt(detail string) time.Time {
	now := time.Now().UTC()
	lower := strings.ToLower(detail)
	if strings.Contains(lower, "week") || strings.Contains(lower, "per week") || strings.Contains(lower, "周") {
		daysUntilMonday := (7 - int(now.Weekday()) + int(time.Monday)) % 7
		if daysUntilMonday == 0 {
			daysUntilMonday = 7
		}
		return midnightUTC(now.AddDate(0, 0, daysUntilMonday))
	}
	if strings.Contains(lower, "month") || strings.Contains(lower, "per month") || strings.Contains(lower, "月") {
		return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	}
	return midnightUTC(now.AddDate(0, 0, 1))
}

func midnightUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func trimDetail(detail string) *string {
	if detail == "" {
		return nil
	}
	if len(detail) > 500 {
		detail = detail[:500]
	}
	return &detail
}
