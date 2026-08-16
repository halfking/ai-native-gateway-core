package credential

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
func newWriterWithDB(db DBQuerier) *Writer { //nolint:unused
	return &Writer{dbPool: db}
}

func (w *Writer) Enabled() bool {
	return w != nil && w.dbPool != nil
}

// RestoreOnSuccess clears cooling / rate_limited / unreachable
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
		  AND availability_state IN ('cooling', 'rate_limited', 'unreachable')
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
			    unavailable_recover_at = NULL,
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
		// model_offers is a VIEW over credential_model_bindings, so it
		// automatically reflects the update above. No separate UPDATE needed.
	} else {
		if _, err = tx.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at     = NULL,
			    unavailable_recover_at = NULL,
			    updated_at         = now()
			FROM provider_models pm
			WHERE pm.id = cmb.provider_model_id
			  AND cmb.credential_id = $1
			  AND pm.canonical_raw_name = $2
			  AND cmb.available = FALSE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, credentialID, rawModel); err != nil {
			return err
		}
		// model_offers is a VIEW over credential_model_bindings, so it
		// automatically reflects the update above. No separate UPDATE needed.
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
		recoverAt := inferQuotaRecoverAt(failure.Detail)
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET quota_state             = 'periodic_exhausted',
			    quota_recover_at        = $1,
			    availability_state      = 'suspended',
			    availability_recover_at = $1,
			    state_reason_code       = $2,
			    state_reason_detail     = $3,
			    state_updated_at        = now()
			WHERE id = $4
			  AND lifecycle_status = 'active'
			  AND quota_state NOT IN ('balance_exhausted', 'permanently_exhausted')
		`, recoverAt, string(failure.Kind), detail, credentialID)
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
		// 2026-07-22 fix (BUG #2): availability_recover_at must be set
		// to a future timestamp so credential_recovery.go's 60s ticker
		// can flip availability_state back to 'ready' once the cooling
		// period elapses. Previously this column was written as NULL,
		// which made the recovery ticker's `AND availability_recover_at
		// IS NOT NULL` guard impossible to satisfy — auth_failed
		// credentials were stuck until manual admin intervention.
		//
		// 15 minutes mirrors the existing breaker.go:103 intent
		// ("permanent" auth recovery now redesigned in BUG #1 to
		// exponential backoff), and matches the 15-minute first probe
		// cadence used by credstate's active_probe trigger.
		recoverAt := time.Now().UTC().Add(15 * time.Minute)
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET availability_state      = 'auth_failed',
			    availability_recover_at = $4,
			    state_reason_code       = $1,
			    state_reason_detail     = $2,
			    state_updated_at        = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
			  AND availability_state NOT IN ('suspended')
		`, string(failure.Kind), detail, credentialID, recoverAt)
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
	case errorsx.KindUpstreamOverloaded:
		// Relay capacity pressure is transient and must not remove the
		// credential-model binding from routing. Keep the real upstream detail
		// for operators while retry/backoff is handled by the executor.
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET state_reason_code   = $1,
			    state_reason_detail = $2,
			    state_updated_at    = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
		`, string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindConcurrent, errorsx.KindRateLimit, errorsx.KindStreamTimeout, errorsx.KindNoAvailableChannel:
		// Per-model kind. Update the specific (credential, model) binding
		// in BOTH cmb (production router) and model_offers (/api/routing/
		// resolve "test route" + admin UI). Sibling models on the same
		// credential are NOT touched — that was the 2026-06-22 audit bug.
		//
		// 2026-06-23 fix: Use writeModelLevelFailureOnly to avoid polluting
		// credentials.availability_state, which would incorrectly mark ALL
		// models on this credential as unavailable (cross-model pollution bug).
		recoverAt := time.Now().UTC().Add(coolingDuration(failure.Kind, failure.RetryAfter))
		return w.writeModelLevelFailureOnly(ctx, credentialID, rawModel, "auto_"+string(failure.Kind), recoverAt, detail)
	case errorsx.KindNetwork, errorsx.KindTimeout, errorsx.KindUpstreamDown:
		// 2026-08-11 soft-degrade: transient network blips, single-request
		// timeouts, and short-lived 5xx (UpstreamDown) are reported by an
		// otherwise-healthy upstream and clear in seconds. Previously these
		// flipped cmb.available=FALSE for 30–120s, removing the node from
		// v_routable for that window — which surfaced as "the gateway
		// excludes an accessible provider node". Per KindUpstreamOverloaded
		// above, record the real detail for operators but do NOT remove the
		// binding from routing. Transient load is absorbed by the executor's
		// per-attempt retry, the circuit breaker (e.Circuit), and the URSM v2
		// fail_streak path; sustained failures still escalate there.
		//
		// KindStreamTimeout (no feedback at all on a live stream) is kept on
		// the hard-degrade path above because it indicates a genuinely stuck
		// node that should be cooled.
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET state_reason_code   = $1,
			    state_reason_detail = $2,
			    state_updated_at    = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
		`, string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindModelNotFound:
		// 2026-07-03 fix: Bug #10 - model_not_found should write state
		// (removed from IsClientBug). When upstream deprecates a model,
		// mark it unavailable with a long cooling period (7 days) so we
		// don't repeatedly try it, but allow eventual retry in case the
		// model is restored.
		recoverAt := time.Now().UTC().Add(7 * 24 * time.Hour)
		return w.writeModelLevelFailureOnly(ctx, credentialID, rawModel, "auto_model_not_found", recoverAt, detail)
	case errorsx.KindModelDeprecated:
		// 2026-08-05 fix: upstream has permanently end-of-lifed the model
		// (HTTP 410 Gone + "end of life" body, or 404/422 "has been
		// deprecated"). Distinct from model_not_found: deprecation is an
		// authoritative statement that the model will NOT come back, so we
		// cool the per-(credential,model) binding for 30 days (vs 7 for
		// model_not_found). Per-model scope only — the credential may serve
		// other models fine, so we must NOT pollute credentials.
		// availability_state (see writeModelLevelFailureOnly rationale).
		recoverAt := time.Now().UTC().Add(30 * 24 * time.Hour)
		return w.writeModelLevelFailureOnly(ctx, credentialID, rawModel, "auto_model_deprecated", recoverAt, detail)
	case errorsx.KindContextLength, errorsx.KindUnsupportedFeature,
		errorsx.KindToolCallIdMismatch, errorsx.KindClientBug,
		errorsx.KindContentFilter, errorsx.KindCanceled:
		// Request shape, context-window, capability, tool-history, and
		// content-policy rejections describe this request, not credential
		// health. They must never cool a binding or change credential state.
		return nil

	default:
		// Unknown error kinds: do not write state to avoid false negatives.
		// We intentionally do not log here either — credential.WriteOnError is
		// on the request hot path, and any unknown kind will already surface
		// from the upstream response in the caller's logs/metrics. Adding a
		// log here would multiply noise during incidents without adding signal.
		return nil
	}
}

// writeModelLevelFailureOnly applies a per-(credential, model) failure ONLY to
// the model-level state surfaces (cmb and model_offers), without touching
// credentials.availability_state. This prevents cross-model pollution where a
// single model's failure incorrectly marks the entire credential as unavailable.
//
// Use this for model-specific errors (network, timeout, concurrent, etc.).
// Credential-wide errors (quota, auth) are handled by the inline SQL UPDATEs
// in WriteOnError's switch — they write directly to credentials.availability_state
// because the entire credential becomes unreachable (not just one model).
//
// 2026-06-23: Originally extracted alongside a legacy writeModelLevelFailure
// helper that also wrote credentials.availability_state. The legacy helper was
// removed in 2026-06-23 (PR-3 T3) after a code audit found zero remaining
// callers; the bug it caused (minimax-m3 failing marking minimax-01
// unavailable too) is now structurally impossible because no code path writes
// to credentials.availability_state from a per-model error.
func (w *Writer) writeModelLevelFailureOnly(
	ctx context.Context,
	credentialID int,
	rawModel, reason string,
	recoverAt time.Time,
	detail *string,
) error {
	// 1. Per-binding state on cmb (the production router's source of truth)
	if rawModel == "" {
		if _, err := w.dbPool.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available              = FALSE,
			    unavailable_reason     = $1,
			    unavailable_at         = now(),
			    unavailable_recover_at = $2,
			    updated_at             = now()
			WHERE cmb.credential_id = $3
			  AND cmb.available = TRUE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, reason, recoverAt, credentialID); err != nil {
			return err
		}
	} else {
		if _, err := w.dbPool.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = FALSE,
			    unavailable_reason = $1,
			    unavailable_at     = now(),
			    unavailable_recover_at = $2,
			    updated_at         = now()
			FROM provider_models pm
			WHERE pm.id = cmb.provider_model_id
			  AND cmb.credential_id = $3
			  AND pm.canonical_raw_name = $4
			  AND cmb.available = TRUE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, reason, recoverAt, credentialID, rawModel); err != nil {
			return err
		}
	}

	// 2. model_offers is a VIEW over credential_model_bindings, so it
	//    automatically reflects the update above. No separate UPDATE needed.
	//    (Previously we tried to UPDATE model_offers directly, but views
	//    cannot be updated and caused "cannot update view" errors.)

	// 3. ✅ DO NOT update credentials.availability_state.
	//    Model-level failures must not pollute the credential-level state,
	//    which would incorrectly block all other healthy models on this
	//    credential. The legacy writeModelLevelFailure helper that did this
	//    was removed in 2026-06-23 (PR-3 T3); this function is now the only
	//    per-model error write path.
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
		// 2026-08-09: 调整为 3 分钟（原 15 分钟过长）。rate_limit 通常是
		// 短期限流（如每分钟配额耗尽），3 分钟后配额窗口通常已滚动。上游
		// 提供 Retry-After 时优先用其值（coolingDuration 首行已处理）。
		// 参考：OpenAI 的 rate_limit_exceeded 常见 Retry-After 为 1-60s；
		// 部分中转的 quota 窗口为 1-5 分钟；3 分钟覆盖大部分场景。
		return 3 * time.Minute
	case errorsx.KindNoAvailableChannel:
		// 2026-08-09: distributor group has no channel for this model. 15
		// minutes gives the group time to restore its channel without holding
		// the (credential, model) binding out of v_routable for the repeated
		// 5-minute concurrent window that re-hammered the same broken node
		// (server-154 incident). credentialstate's active probe (consecutive
		// >= 2) flips the binding back sooner once the channel is healthy.
		return 15 * time.Minute
	case errorsx.KindStreamTimeout:
		// 2026-07-09 修正（问题2 - NVIDIA NIM 流式无反馈长时间未熔断）：
		// 流式无反馈（first_byte_timeout / stream_timeout / EOF-without-DONE）
		// 归为 KindStreamTimeout。之前与 Transient/Timeout 共用 30s cooling，
		// 凭据 30s 后即恢复又被选中、再次无反馈，形成"前端长时间无响应但凭据
		// 一直在线"的循环。提升到 5 分钟，让 cmb/model_offers 的
		// unavailable_recover_at 跨实例一致地把该 (凭据,模型) 挡在 v_routable
		// 视图之外足够久，配合 credentialstate 的立即下线，彻底停止接收请求。
		return 5 * time.Minute
	case errorsx.KindTransient, errorsx.KindTimeout:
		return 30 * time.Second
	case errorsx.KindUpstreamDown:
		return 60 * time.Second
	case errorsx.KindUpstreamOverloaded:
		// 2026-08-08: an upstream that answers "currently overloaded, try
		// again later" is reporting a seconds-scale load spike, not an
		// outage. 60s matches KindUpstreamDown rather than KindConcurrent's
		// 5 minutes: holding the binding out of v_routable for five minutes
		// over a transient spike needlessly shrinks the candidate pool. A
		// provider-supplied Retry-After still wins (handled above).
		return 60 * time.Second
	case errorsx.KindNetwork:
		return 120 * time.Second
	default:
		return 30 * time.Second
	}
}

func inferQuotaRecoverAt(detail string) time.Time {
	now := time.Now().UTC()
	if t, ok := parseQuotaResetTimestamp(detail); ok {
		return t.UTC()
	}
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

// parseQuotaResetTimestamp scans the body for a YYYY-MM-DD HH:MM:SS
// timestamp and returns it. Returns false when no parseable timestamp
// is found or when the timestamp is already in the past (caller falls
// back to day-based heuristic).
func parseQuotaResetTimestamp(detail string) (time.Time, bool) {
	lower := strings.ToLower(detail)
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006/01/02 15:04:05",
		"2006/01/02T15:04:05",
	} {
		const window = 19
		if len(lower) < window {
			continue
		}
		for i := 0; i+window <= len(lower); i++ {
			candidate := lower[i : i+window]
			if !looksLikeTimestamp(candidate, layout) {
				continue
			}
			t, err := time.ParseInLocation(layout, candidate, time.UTC)
			if err != nil {
				continue
			}
			if t.Before(time.Now().Add(-1 * time.Minute)) {
				return time.Time{}, false
			}
			return t, true
		}
	}
	return time.Time{}, false
}

func looksLikeTimestamp(s, layout string) bool {
	if len(s) != len(layout) {
		return false
	}
	for i, r := range layout {
		switch r {
		case '2', '1', '0', '6', '5', '4':
			if !isDigit(s[i]) {
				return false
			}
		default:
			if r == ' ' {
				if s[i] != ' ' && s[i] != 'T' && s[i] != 't' {
					return false
				}
			} else if byte(s[i]) != byte(r) {
				return false
			}
		}
	}
	return true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

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
