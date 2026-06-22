package credentialstate

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

var ErrNoDatabase = errors.New("credential state database not configured")

// DBQuerier is the subset of pgxpool.Pool that Writer needs. Defined here
// (instead of imported from credentialhealth) to avoid a cyclic import.
// RestoreOnSuccess uses Begin() too, so callers that need it must supply
// a *pgxpool.Pool (or a stub with both methods).
type DBQuerier interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Writer struct {
	dbPool DBQuerier
}

type Failure struct {
	Kind       errorsx.ErrorKind
	Detail     string
	RetryAfter time.Duration
}

func NewWriter(pool *pgxpool.Pool) *Writer {
	return &Writer{dbPool: pool}
}

// newWriterWithDB builds a Writer against an arbitrary DBQuerier. Used by
// tests (pgxmock) and by callers that already have a Tx-bound DBQuerier.
func newWriterWithDB(db DBQuerier) *Writer {
	return &Writer{dbPool: db}
}

func (w *Writer) Enabled() bool {
	return w != nil && w.dbPool != nil
}

// RestoreOnSuccess clears cooling / rate_limited / unreachable / degraded
// state on the credential. The (credential, model) bindings are restored
// in lock-step so production routing (cmb) and /api/routing/resolve
// (model_offers) agree on which bindings are live.
//
// rawModel is the model that just succeeded. If empty, every binding on
// the credential is restored (legacy behaviour for callers that don't
// know the model — only used by tests).
func (w *Writer) RestoreOnSuccess(ctx context.Context, credentialID int, rawModel string) error {
	if !w.Enabled() {
		return ErrNoDatabase
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := w.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	//nolint:errcheck // deferred rollback, best-effort
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
	// Restore the specific (credential, model) binding. Skip rows that
	// are admin-pinned. If rawModel is empty, restore every binding on
	// the credential (legacy path).
	if rawModel == "" {
		if _, err = tx.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at     = NULL,
			    updated_at         = now()
			FROM provider_models pm
			WHERE pm.id = cmb.provider_model_id
			  AND cmb.credential_id = $1
			  AND cmb.available = FALSE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, credentialID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `
			UPDATE model_offers mo
			SET available          = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at     = NULL,
			    updated_at         = now()
			WHERE mo.credential_id = $1
			  AND mo.available = FALSE
			  AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(mo.admin_protected, FALSE) = FALSE
		`, credentialID); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at     = NULL,
			    updated_at         = now()
			FROM provider_models pm
			WHERE pm.id = cmb.provider_model_id
			  AND cmb.credential_id = $1
			  AND COALESCE(pm.outbound_model_name, pm.raw_model_name) = $2
			  AND cmb.available = FALSE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, credentialID, rawModel); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `
			UPDATE model_offers mo
			SET available          = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at     = NULL,
			    updated_at         = now()
			FROM provider_models pm
			WHERE pm.raw_model_name = mo.raw_model_name
			  AND pm.id = (
			      SELECT cmb.provider_model_id
			      FROM credential_model_bindings cmb
			      WHERE cmb.credential_id = $1
			        AND COALESCE(pm.outbound_model_name, pm.raw_model_name) = $2
			      LIMIT 1
			  )
			  AND mo.credential_id = $1
			  AND mo.available = FALSE
			  AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(mo.admin_protected, FALSE) = FALSE
		`, credentialID, rawModel); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// WriteOnError records a (credential, model) failure and updates the
// per-binding availability. rawModel is the model that failed — leaving
// it empty falls back to the legacy credential-wide update path used
// by tests.
//
// Per-model kinds (network / rate_limit / concurrent / timeout /
// upstream_down / stream_timeout) now write credential_model_bindings
// (the production router's source of truth) AND model_offers (so
// /api/routing/resolve "test route" matches production). Sibling
// models on the same credential are NOT touched.
//
// Credential-wide kinds (quota* / auth* / auth_revoked) continue to
// write credentials.availability_state (which is what the admin UI
// surfaces) and additionally mark every binding on the credential
// unavailable so the binding-level view stays consistent.
func (w *Writer) WriteOnError(ctx context.Context, credentialID int, rawModel string, failure Failure) error {
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
		// Per-model kind. Update the specific (credential, model) binding
		// in BOTH cmb (production router) and model_offers (/api/routing/
		// resolve "test route" + admin UI). Sibling models on the same
		// credential are NOT touched — that was the 2026-06-22 audit bug.
		//
		// credentials.availability_state is updated too so admin UI badges
		// reflect the cooling state, but the production router is driven
		// exclusively by cmb.available via v_routable_credential_models.
		availability := "cooling"
		if failure.Kind == errorsx.KindRateLimit {
			availability = "rate_limited"
		}
		recoverAt := time.Now().UTC().Add(coolingDuration(failure.Kind, failure.RetryAfter))
		return w.writeModelLevelFailure(ctx, credentialID, rawModel, "auto_"+string(failure.Kind), availability, recoverAt, detail)
	case errorsx.KindNetwork:
		recoverAt := time.Now().UTC().Add(coolingDuration(failure.Kind, failure.RetryAfter))
		return w.writeModelLevelFailure(ctx, credentialID, rawModel, "auto_network", "unreachable", recoverAt, detail)
	default:
		return nil
	}
}

// writeModelLevelFailure applies a per-(credential, model) failure to all
// three state surfaces: cmb (production router), model_offers (admin UI
// + test-route endpoint), and credentials.availability_state (legacy
// admin badges). rawModel is matched via COALESCE(pm.outbound_model_name,
// pm.raw_model_name) to handle vendor endpoints with a remap alias.
//
// If rawModel is empty (test path), the cmb / model_offers writes fall
// back to "every binding on the credential" so legacy callers don't
// regress.
func (w *Writer) writeModelLevelFailure(
	ctx context.Context,
	credentialID int,
	rawModel, reason, availability string,
	recoverAt time.Time,
	detail *string,
) error {
	// 1. Per-binding state on cmb (the production router's source of truth)
	if rawModel == "" {
		if _, err := w.dbPool.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = FALSE,
			    unavailable_reason = $1,
			    unavailable_at     = now(),
			    updated_at         = now()
			WHERE cmb.credential_id = $2
			  AND cmb.available = TRUE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, reason, credentialID); err != nil {
			return err
		}
	} else {
		if _, err := w.dbPool.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = FALSE,
			    unavailable_reason = $1,
			    unavailable_at     = now(),
			    updated_at         = now()
			FROM provider_models pm
			WHERE pm.id = cmb.provider_model_id
			  AND cmb.credential_id = $2
			  AND COALESCE(pm.outbound_model_name, pm.raw_model_name) = $3
			  AND cmb.available = TRUE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, reason, credentialID, rawModel); err != nil {
			return err
		}
	}

	// 2. Mirror to model_offers so /api/routing/resolve reflects the same
	//    state. Skip the write if rawModel is empty (no clean JOIN) — in
	//    that case the cmb write above has already affected the binding
	//    the router sees.
	if rawModel != "" {
		if _, err := w.dbPool.Exec(ctx, `
			UPDATE model_offers mo
			SET available          = FALSE,
			    unavailable_reason = $1,
			    unavailable_at     = now()
			WHERE mo.credential_id = $2
			  AND mo.raw_model_name = $3
			  AND mo.available = TRUE
			  AND COALESCE(mo.admin_protected, FALSE) = FALSE
		`, reason, credentialID, rawModel); err != nil {
			return err
		}
	}

	// 3. Legacy credentials.availability_state update (admin badge only;
	//    the router does not read this column).
	if _, err := w.dbPool.Exec(ctx, `
		UPDATE credentials
		SET availability_state      = $1,
		    availability_recover_at = $2,
		    state_reason_code       = $3,
		    state_reason_detail     = $4,
		    state_updated_at        = now()
		WHERE id = $5
		  AND lifecycle_status = 'active'
		  AND availability_state NOT IN ('suspended', 'auth_failed')
	`, availability, recoverAt, reason, detail, credentialID); err != nil {
		return err
	}
	return nil
}

func coolingDuration(kind errorsx.ErrorKind, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	switch kind {
	case errorsx.KindConcurrent:
		// 5 minutes cooling for concurrent-overload errors. Upstream
		// concurrency windows (e.g. MiniMax "engine busy") typically
		// clear on a multi-minute scale; 15s was too short and caused
		// the same credential to be re-selected and re-fail in tight
		// loops. Five minutes lets the upstream clear and lets the
		// executor route to a different candidate.
		return 5 * time.Minute
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
