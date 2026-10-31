package credential

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/modelbinding"
	"github.com/kaixuan/llm-gateway-go/settings"
)

var ErrNoDatabase = errors.New("credential state database not configured")

// DBQuerier is the subset of pgxpool.Pool that Writer needs. Defined here
// (instead of imported from credentialhealth) to avoid a cyclic import.
// RestoreOnSuccess uses Begin() too, so callers that need it must supply
// a *pgxpool.Pool (or a stub with both methods).
type DBQuerier interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
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
	// 2026-09-13 closeout (P2): real-traffic success also resets the probe
	// backoff ladder, so a credential the vendor has unblocked recovers its
	// fast probe cadence immediately instead of waiting out a long
	// availability_recover_at rung from earlier failed probes.
	if _, err = tx.Exec(ctx, `
		UPDATE credentials
		SET probe_consecutive_failures = 0
		WHERE id = $1
		  AND COALESCE(probe_consecutive_failures, 0) > 0
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
		rawModel, err = modelbinding.ResolveRawBinding(ctx, tx, credentialID, rawModel)
		if err != nil {
			return err
		}
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
			  AND pm.raw_model_name = $2
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

// SetCredentialUnavailable applies a credential-wide failure only after the
// caller has established that the credential itself is at fault. It updates
// every non-manual binding so the credential and binding routing gates cannot
// disagree.
func (w *Writer) SetCredentialUnavailable(ctx context.Context, credentialID int, failure Failure) error {
	if !w.Enabled() {
		return ErrNoDatabase
	}
	// P0-C：PenaltyNone（client_bug / shape / content_filter…）永不冷却凭据。
	if errorsx.PenaltyOf(failure.Kind).Target == errorsx.PenaltyNone {
		// 防归责矩阵被改导致静默失效：留下 debug 级轨迹，便于事后
		// grep 凭据冷却缺位的根因（按口径应是 client-side 错误）。
		slog.Debug("credential/writer: skipping cool for PenaltyNone",
			"kind", failure.Kind, "credential_id", credentialID)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	recoverAt := time.Now().UTC().Add(coolingDuration(failure.Kind, failure.RetryAfter))
	detail := trimDetail(failure.Detail)
	tx, err := w.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	//nolint:errcheck // deferred rollback, best-effort
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		UPDATE credentials
		SET availability_state      = 'cooling',
		    availability_recover_at = $1,
		    state_reason_code       = $2,
		    state_reason_detail     = $3,
		    state_updated_at        = now()
		WHERE id = $4
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND availability_state NOT IN ('suspended', 'auth_failed')
	`, recoverAt, string(failure.Kind), detail, credentialID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE credential_model_bindings cmb
		SET available              = FALSE,
		    unavailable_reason     = 'auto_credential_' || $1,
		    unavailable_at         = now(),
		    unavailable_recover_at = $2,
		    updated_at             = now()
		WHERE cmb.credential_id = $3
		  AND cmb.available = TRUE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
	`, string(failure.Kind), recoverAt, credentialID); err != nil {
		return err
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
	// 2026-10-24 magpie 19 类 rest 词表轮（§6-P0-4）：首字节超时的归责是
	// 「**第一次放过**（长 prompt 哪儿都慢）→ 反复发生才罚 → **罚在模型粒度**
	// 而不是账号 → 退避递增」。静态表判 PenaltyNone（第一次绝不罚），
	// 「反复才罚」由慢启动休息器承担 ⇒ 必须**在 PenaltyNone 短路之前**接管，
	// 否则这个 kind 会被当成"不罚"直接放行，休息器永远收不到事件。
	if failure.Kind == errorsx.KindFirstByteTimeout {
		return w.writeSlowRest(ctx, credentialID, rawModel, failure)
	}
	// P0-C：归责 none 的失败不写冷却/配额态（避免形状 400 级联摘节点）。
	if errorsx.PenaltyOf(failure.Kind).Target == errorsx.PenaltyNone {
		// 与 SetCredentialUnavailable 同样的防静默失效轨迹：
		// 万一 errorsx.PenaltyOf 误判 kind 归 none，本路径不会被日志暴露。
		slog.Debug("credential/writer: skipping WriteOnError cool for PenaltyNone",
			"kind", failure.Kind, "credential_id", credentialID, "raw_model", rawModel)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	detail := trimDetail(failure.Detail)
	switch failure.Kind {
	case errorsx.KindQuotaPeriodic:
		// 2026-10-31 A1（magpie 四域对比刷新轮 §五 A1）：恢复点来源链
		// 「body 词表 reset 时间 → Retry-After（8d 信任上限，本轮新增消费）
		// → 窗口启发式」，来源写进 state_reason_detail（运维可读「这次冷却
		// 信了谁」）。既有窗口启发式（5h/周/月/零点）行为逐字节不变——
		// 它整体退为来源 3。
		recovery := inferQuotaRecoveryAtNow(failure.Detail, failure.RetryAfter, time.Now())
		detail := quotaRecoverySuffix(failure.Detail, recovery)
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
		`, recovery.at, string(failure.Kind), detail, credentialID)
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
			  -- 2026-09-13: 不夺走 balance_floor guard 摘出的行（reason 守卫
			  -- 保持所有权，避免 BalanceQuotaProbe 豁免失效）。
			  AND COALESCE(state_reason_code, '') <> 'balance_floor'
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
			  -- 2026-09-13: floor 摘出的行本就是 balance_exhausted，重打
			  -- reason 会丢所有权（guard 恢复与 probe 豁免都依赖它）。
			  AND COALESCE(state_reason_code, '') <> 'balance_floor'
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
			  -- 2026-09-13 closeout (P6): never clobber the reason trail of a
			  -- suspended/auth_failed credential with a transient note — the
			  -- audit found suspended rows whose only remaining evidence was
			  -- "passive_probe_review_failed: transient ..." (prod creds 9/21),
			  -- which reads as "why is this stuck?" and forces manual recovery.
			  AND availability_state NOT IN ('suspended', 'auth_failed')
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
			  AND availability_state NOT IN ('suspended', 'auth_failed')
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
		//
		// 2026-10-31 A2（magpie 四域对比刷新轮 §五 A2）: KindRateLimit 从
		// 固定 3min 改为「Retry-After 优先（信任上限 1h）→ 累计升级
		// （3min→6min→12min→24min→30min 封顶，遗忘窗 30min）」。
		// 红线不动：429 仍不进断路器（breaker.go Wave 1 A4 裁决），升级
		// 只作用于绑定级冷却时长；计数在 ratebackoff.go（进程内，与
		// slowrest 同款已知局限：多实例下退避起点偏保守）。Concurrency/
		// StreamTimeout/NoAvailableChannel 三个同臂 kind 走原 coolingDuration
		// 路径，零行为变化。
		if failure.Kind == errorsx.KindRateLimit {
			recoverAt := time.Now().UTC().Add(rateLimitedCoolingDuration(failure.RetryAfter, DefaultRateBackoff(), RateBackoffKey(credentialID, rawModel)))
			reason := "auto_rate_limit"
			return w.writeModelLevelFailureOnly(ctx, credentialID, rawModel, reason, recoverAt, detail)
		}
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
		//
		// 2026-09-13 closeout (P6): same suspended/auth_failed reason guard
		// as KindTransient — passive observer writes must not overwrite the
		// suspension evidence of a stuck credential.
		_, err := w.dbPool.Exec(ctx, `
			UPDATE credentials
			SET state_reason_code   = $1,
			    state_reason_detail = $2,
			    state_updated_at    = now()
			WHERE id = $3
			  AND lifecycle_status = 'active'
			  AND availability_state NOT IN ('suspended', 'auth_failed')
		`, string(failure.Kind), detail, credentialID)
		return err
	case errorsx.KindModelNotFound:
		// 2026-07-03 fix: Bug #10 - model_not_found should write state
		// (removed from IsClientBug). When upstream deprecates a model,
		// mark it unavailable with a cooling period so we don't repeatedly
		// try it, but allow eventual retry in case the model is restored.
		//
		// 2026-09-22 Wave 2 C4 三级化: a FIRST request-path 404 takes the
		// 30-minute initial tier (aggregator routing blip / temporarily
		// removed upstream model must not pin the pair for a week); a
		// second 404 landing while the binding is already auto_model_not_
		// found-cooled escalates to the 7-day sustained tier. The
		// node_probe 5-minute exclusion window makes such a mid-cooling
		// 404 strong evidence of real unavailability, and the probe ladder
		// (≤6h cap) stays the authoritative self-heal for a mistaken
		// escalation. Tiers are settings-hot-reloadable.
		return w.writeModelNotFoundTiered(ctx, credentialID, rawModel, detail)
	case errorsx.KindModelDeprecated:
		// 2026-08-05 fix: upstream has permanently end-of-lifed the model
		// (HTTP 410 Gone + "end of life" body, or 404/422 "has been
		// deprecated"). Distinct from model_not_found: deprecation is an
		// authoritative statement that the model will NOT come back, so we
		// cool the per-(credential,model) binding for 30 days (vs 7 for
		// model_not_found). Per-model scope only — the credential may serve
		// other models fine, so we must NOT pollute credentials.
		// availability_state (see writeModelLevelFailureOnly rationale).
		recoverAt := time.Now().UTC().Add(time.Duration(settings.ModelDeprecatedCooldownSeconds()) * time.Second)
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

// writeSlowRest applies the magpie slow-start (first-byte timeout) rest policy.
//
// Contract (magpie internal/gateway/routing.go:314-318 + :1135-1173):
//
//	① 第一次放过      —— 长 prompt 哪儿都慢，第一次不罚任何人；
//	② 反复才罚        —— 阈值 slowRests=2；
//	③ 罚在模型粒度    —— 休息键 = (凭据, 模型)，**不动账号**；
//	④ 退避递增        —— 1min → 2min → 4min…（30min 遗忘窗后归零）。
//
// ★ 休息落在**模型粒度** ⇒ 与账号级的「凭据不可用」是两回事：
//
//	本函数**不写** credentials.availability_state（同一账号的其它模型照常服务）。
func (w *Writer) writeSlowRest(ctx context.Context, credentialID int, rawModel string, failure Failure) error {
	tracker := DefaultSlowRest()
	if tracker == nil {
		return nil
	}
	key := SlowRestKey(credentialID, rawModel)
	decision := tracker.Observe(key)
	if !decision.Rest {
		// ★ 第一次（及任何未达阈值的次数）：放过 ⇒ **不写任何冷却**。
		// 留 debug 轨迹：万一归责口径被改，这里能看出"本该罚却没罚"。
		slog.Debug("credential/writer: first-byte timeout forgiven (magpie slow: once nobody rests)",
			"credential_id", credentialID, "raw_model", rawModel,
			"slow_count", decision.Count, "threshold", SlowRests)
		return nil
	}
	detail := trimDetail(failure.Detail)
	// reason 里带上退避阶段，便于从 DB 直接看出"这是第几次慢启动"，
	// 与 breaker 的 unavailable_reason 习惯一致（auto_<kind>_<phase>）。
	reason := "auto_" + string(errorsx.KindFirstByteTimeout) + "_" + decision.By
	slog.Info("credential/writer: first-byte timeout rest (model-scoped)",
		"credential_id", credentialID, "raw_model", rawModel,
		"slow_count", decision.Count, "until", decision.Until.UTC().Format(time.RFC3339), "by", decision.By)
	return w.writeModelLevelFailureOnly(ctx, credentialID, rawModel, reason, decision.Until, detail)
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
		resolvedRawModel, err := modelbinding.ResolveRawBinding(ctx, w.dbPool, credentialID, rawModel)
		if err != nil {
			return err
		}
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
			  AND pm.raw_model_name = $4
			  AND cmb.available = TRUE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, reason, recoverAt, credentialID, resolvedRawModel); err != nil {
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

// writeModelNotFoundTiered applies the Wave-2 three-tier MNF semantics
// (audit C4): initialRecoverAt for bindings that are not currently cooled,
// sustainedRecoverAt for bindings already cooling with reason
// auto_model_not_found (a second request-path 404 inside the first
// cooling window). The available=TRUE guard of writeModelLevelFailureOnly
// is relaxed for exactly that same-reason cooled arm so the escalation
// write can land; manual/admin-protected bindings stay untouched in both
// arms, and no other reason's cooling is extended or overwritten.
func (w *Writer) writeModelNotFoundTiered(
	ctx context.Context,
	credentialID int,
	rawModel string,
	detail *string,
) error {
	const reason = "auto_model_not_found"
	now := time.Now().UTC()
	initialRecoverAt := now.Add(time.Duration(settings.ModelNotFoundInitialCooldownSeconds()) * time.Second)
	sustainedRecoverAt := now.Add(time.Duration(settings.ModelNotFoundSustainedCooldownSeconds()) * time.Second)

	// 1. Per-binding state on cmb (the production router's source of truth).
	//    CASE arms: available=FALSE (already MNF-cooled) → sustained tier;
	//    available=TRUE (fresh 404) → initial tier.
	if rawModel == "" {
		if _, err := w.dbPool.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available              = FALSE,
			    unavailable_reason     = $1,
			    unavailable_at         = now(),
			    unavailable_recover_at = CASE
			        WHEN cmb.available = FALSE THEN $2::timestamptz
			        ELSE $3::timestamptz
			    END,
			    updated_at             = now()
			WHERE cmb.credential_id = $4
			  AND (
			        (cmb.available = TRUE
			         AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			         AND COALESCE(cmb.admin_protected, FALSE) = FALSE)
			     OR (cmb.available = FALSE AND cmb.unavailable_reason = $1)
			  )
		`, reason, sustainedRecoverAt, initialRecoverAt, credentialID); err != nil {
			return err
		}
	} else {
		resolvedRawModel, err := modelbinding.ResolveRawBinding(ctx, w.dbPool, credentialID, rawModel)
		if err != nil {
			return err
		}
		if _, err := w.dbPool.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = FALSE,
			    unavailable_reason = $1,
			    unavailable_at     = now(),
			    unavailable_recover_at = CASE
			        WHEN cmb.available = FALSE THEN $2::timestamptz
			        ELSE $3::timestamptz
			    END,
			    updated_at         = now()
			FROM provider_models pm
			WHERE pm.id = cmb.provider_model_id
			  AND cmb.credential_id = $4
			  AND pm.raw_model_name = $5
			  AND (
			        (cmb.available = TRUE
			         AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			         AND COALESCE(cmb.admin_protected, FALSE) = FALSE)
			     OR (cmb.available = FALSE AND cmb.unavailable_reason = $1)
			  )
		`, reason, sustainedRecoverAt, initialRecoverAt, credentialID, resolvedRawModel); err != nil {
			return err
		}
	}

	// 2/3. model_offers reflects cmb via the view; credentials.availability_state
	//      stays untouched (same rationale as writeModelLevelFailureOnly).
	return nil
}

// maxCoolingDuration caps an upstream-supplied Retry-After (probe-recovery
// closeout P7, 2026-09-13). coolingDuration previously adopted Retry-After
// verbatim, so a misbehaving upstream could pin a credential/binding out of
// routing for an unbounded time with no automatic re-check. 24h covers every
// legitimate quota window (daily/monthly boundaries are re-derived by the
// quota paths anyway — Retry-After only reaches the cooling kinds).
const maxCoolingDuration = 24 * time.Hour

func coolingDuration(kind errorsx.ErrorKind, retryAfter time.Duration) time.Duration {
	// P0-C：PenaltyNone 冷却时长为 0（调用方仍应先用 PenaltyOf 短路）。
	if errorsx.PenaltyOf(kind).Target == errorsx.PenaltyNone {
		return 0
	}
	if retryAfter > 0 {
		if retryAfter > maxCoolingDuration {
			return maxCoolingDuration
		}
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

// fiveHourWindowRe detects 5-hour usage-window wording in quota-exhausted
// error bodies. 智谱AI GLM Coding Plan (and Anthropic-compatible relays of
// it) throttle on fixed 5-hour windows; a body mentioning that window is
// periodic by construction and recovers at the next 5-hour boundary, not at
// the next UTC midnight (2026-08-18 fix: the old default stretched a
// 凌晨 5 点重置的 5h 窗口到次日 UTC 零点 = 北京 08:00，白白多挂 3 小时).
// \b 词边界防止误吞 "25 hours"/"15 hours" 或 "25小时" 的尾部子串；
// standalone `5小时`/`每5小时` 仍会匹配，恢复时间再由探活纠偏。
var fiveHourWindowRe = regexp.MustCompile(`(?i)(?:\bfive[_ -]?hours?\b|\b5[_ -]?hours?\b|\bhour[_ -]?5\b|每.{0,3}\b5.{0,3}小时|\b5.{0,3}小时)`)

// cstZone is the UTC+8 fixed zone used to align 5-hour quota windows.
// 智谱AI coding-plan windows roll at 00/05/10/15/20 北京时间.
var cstZone = time.FixedZone("CST", 8*3600)

// nextFiveHourBoundary returns the first 5-hour mark (00/05/10/15/20 in
// UTC+8) strictly after now.
func nextFiveHourBoundary(now time.Time) time.Time {
	local := now.In(cstZone)
	nextHour := 5 * (local.Hour()/5 + 1)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, cstZone)
	if nextHour >= 24 {
		nextHour -= 24
		day = day.AddDate(0, 0, 1)
	}
	return day.Add(time.Duration(nextHour) * time.Hour)
}

func inferQuotaRecoverAt(detail string) time.Time {
	return inferQuotaRecoverAtNow(detail, time.Now())
}

// quotaRecoverySource 标记一次 quota 恢复点的推断来源（A1，2026-10-31
// magpie 四域对比刷新轮 §五 A1）——写进 state_reason_detail 让运维可读
// 「这次冷却信了谁」，并作为验收量具的断言面（变异：删某级来源 ⇒ 该级
// 标记从恢复点断言里消失 ⇒ 红）。
type quotaRecoverySource string

const (
	// quotaSourceBodyReset —— body 词表解析出的上游 reset 时间戳/相对时长
	//（errorsx.ParseQuotaResetAt；信任上限 = 解析器自己的 maxQuotaResetLead 40d）。
	quotaSourceBodyReset quotaRecoverySource = "body_reset"
	// quotaSourceRetryAfter —— 上游 Retry-After 头（信任上限 8d：A1 实施要点
	// 「词表 8d」同值；coolingDuration 的 24h 全局帽只覆盖 cooling 类 kind，
	// quota 类此前根本不消费 Retry-After，这是本轮补上的第二级来源）。
	quotaSourceRetryAfter quotaRecoverySource = "retry_after"
	// quotaSourceWindowHeuristic —— 词表无 reset 时间时的窗口启发式
	//（5h 窗 / 周 / 月 / 次 UTC 零点；既有的 inferQuotaRecoverAtNow 逻辑）。
	quotaSourceWindowHeuristic quotaRecoverySource = "window_heuristic"
)

// quotaRecovery 是恢复点 + 来源的二元组。
type quotaRecovery struct {
	at     time.Time
	source quotaRecoverySource
}

// maxQuotaRetryAfterTrust 是 quota 类对 Retry-After 的信任上限（8d）。
// A1 实施要点的「词表 8d」同值——ParseQuotaResetAt 自己的上限
// maxQuotaResetLead=40d 管的是 body 词表级；header 级此前在 quota 路径
// 无上限（无消费），本轮接入后按 8d 收口，防单个畸形 header 把凭据
// 挂出月度窗口。
const maxQuotaRetryAfterTrust = 8 * 24 * time.Hour

func inferQuotaRecoveryAtNow(detail string, retryAfter time.Duration, nowArg time.Time) quotaRecovery {
	// 来源 1：body 词表的 reset 时间（最精确的证据，优先）。
	if t, ok := parseQuotaResetTimestamp(detail); ok {
		return quotaRecovery{at: t.UTC(), source: quotaSourceBodyReset}
	}
	// 来源 2：Retry-After 头（quota 类此前不消费；8d 信任上限）。
	if retryAfter > 0 {
		d := retryAfter
		if d > maxQuotaRetryAfterTrust {
			d = maxQuotaRetryAfterTrust
		}
		return quotaRecovery{at: nowArg.UTC().Add(d), source: quotaSourceRetryAfter}
	}
	// 来源 3：窗口启发式（5h 窗 / 周 / 月 / 次日零点）。
	return quotaRecovery{at: inferQuotaRecoverAtNow(detail, nowArg), source: quotaSourceWindowHeuristic}
}

// quotaRecoverySuffix 把来源拼进 state_reason_detail 的尾部段。
// 返回 ("", nil) 当 detail 本身为空——保持既有 trimDetail 的 nil 语义。
func quotaRecoverySuffix(detail string, r quotaRecovery) *string {
	if detail == "" {
		return nil
	}
	tagged := detail + " [recover_source=" + string(r.source) + "]"
	return trimDetail(tagged)
}

func inferQuotaRecoverAtNow(detail string, nowArg time.Time) time.Time {
	now := nowArg.UTC()
	if t, ok := parseQuotaResetTimestamp(detail); ok {
		return t.UTC()
	}
	if fiveHourWindowRe.MatchString(detail) {
		return nextFiveHourBoundary(nowArg)
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

// parseQuotaResetTimestamp 是 errorsx.ParseQuotaResetAt 的薄包装（P0-C：
// 单一重置解析器）。旧实现只扫 YYYY-MM-DD HH:MM:SS 且一律按 UTC 解释，
// 会把「北京时间」文案算错；新解析器覆盖相对时长 / 中英文 / \uXXXX /
// 时区提示。无命中时返回 false，由 inferQuotaRecoverAtNow 走日界启发式。
func parseQuotaResetTimestamp(detail string) (time.Time, bool) {
	return errorsx.ParseQuotaResetAt([]byte(detail), time.Now())
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
