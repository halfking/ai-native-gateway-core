package bg

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// healthyBindingSQL restores one (credential, model) binding after a direct
// probe or business success. It only touches rows that are currently
// unavailable and never overrides manual / admin-pinned bindings, so the
// per-request call from the executor is a 0-row no-op on a healthy node.
//
// ★ 2026-11-07（探测浪费根修轮 · R4 竞态收口）：新增**新鲜度守卫** —— 只恢复
// `unavailable_at <= 证据时刻` 的行。此前恢复是无条件的：探测直连轮在 T_c
// 成功、业务失败在 (T_c, 记账) 窗口内拉黑（unavailable_at 晚于探测证据），
// 无条件恢复会**覆盖更新的一条失败证据**；反过来记账（last_attempt_at）晚于
// 恢复时，审计 R4 的时序判据又会把这类行误判为 write-through 缺口。守卫后：
// 更新的失败证据存活（正确），配对的成功 mirror 把 last_attempt_at 写成证据
// 时刻（probe_service.go mirrorNodeProbeState），R4 只在真缺口时亮。
// evidenceAt 语义：证据的发生时刻（探测直连轮完成 / 业务成功返回），不是
// SQL 执行时刻。
func healthyBindingSQL(evidenceAt time.Time) string {
	return `
		UPDATE credential_model_bindings cmb
		SET available = TRUE,
		    unavailable_reason = NULL,
		    unavailable_at = NULL,
		    unavailable_recover_at = NULL,
		    probe_revert_at = NULL,
		    updated_at = now()
		FROM provider_models pm
		WHERE pm.id = cmb.provider_model_id
		  AND cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		  AND COALESCE(cmb.available, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_at, 'epoch') <= $3::timestamptz`
}

// healthyCredentialSQL flips the credential surfaces back to healthy/ready/ok.
// A real upstream success is newer evidence than any stored quota_state, so
// hard quota states are cleared too (same contract as writeHealth's 2026-08-07
// guard). The trailing predicate keeps the UPDATE a no-op when nothing needs
// to change — MarkNodeProbeHealthy runs on every successful request.
//
// 2026-09-13: rows pulled by bg/balance_floor_guard
// (state_reason_code='balance_floor') are exempt — they still have buffer
// quota by design, so an in-flight/stale-cache success here would instantly
// un-pull them and flap against the guard's next sweep. Recovery for those
// rows belongs exclusively to the guard's balance/plan re-checks.
func healthyCredentialSQL() string {
	return `
		UPDATE credentials
		SET health_status = 'healthy',
		    health_error = NULL,
		    health_checked_at = now(),
		    availability_state = 'ready',
		    availability_recover_at = NULL,
		    quota_state = 'ok',
		    quota_recover_at = NULL,
		    state_reason_code = NULL,
		    state_reason_detail = NULL,
		    state_updated_at = now()
		WHERE id = $1
		  AND lifecycle_status = 'active'
		  AND COALESCE(manual_disabled, FALSE) = FALSE
		  AND COALESCE(state_reason_code, '') <> 'balance_floor'
		  AND (
		      health_status IS DISTINCT FROM 'healthy'
		      OR availability_state IS DISTINCT FROM 'ready'
		      OR COALESCE(quota_state, 'ok') <> 'ok'
		      OR quota_recover_at IS NOT NULL
		      OR availability_recover_at IS NOT NULL
		      -- 2026-10-16: the three columns cleared above must also appear
		      -- here. Without them the "nothing needs to change" no-op guard
		      -- skips the whole UPDATE whenever the ONLY dirt is a stale
		      -- error string — so a credential already marked
		      -- healthy/ready/ok kept an old failure message forever.
		      -- Measured on 211: 18 credentials sat in exactly that shape
		      -- (health_status=healthy, availability_state=ready,
		      -- quota_state=ok, both recover_at NULL, health_error and/or
		      -- state_reason_detail non-NULL). That is the "node says 可用 but
		      -- the error line still shows an old 403" inconsistency.
		      --
		      -- A successful upstream call is newer evidence than any stored
		      -- error text, which is the same contract the function comment
		      -- already states for the quota columns — these are just the
		      -- diagnostic half of it.
		      OR health_error IS NOT NULL
		      OR state_reason_code IS NOT NULL
		      OR state_reason_detail IS NOT NULL
		  )`
}

func syncHealthyNodeSurfaces(ctx context.Context, db *pgxpool.Pool, credentialID int, rawModel string) error {
	// 业务成功的证据时刻＝现在（调用点在成功返回路径上）。
	return syncHealthyNodeSurfacesAt(ctx, db, credentialID, rawModel, time.Now())
}

// syncHealthyNodeSurfacesAt is the evidence-timestamped core; the probe paths
// hand their direct-round completion time so a concurrent newer blacklist is
// never clobbered (see healthyBindingSQL).
func syncHealthyNodeSurfacesAt(ctx context.Context, db *pgxpool.Pool, credentialID int, rawModel string, evidenceAt time.Time) error {
	if db == nil {
		return nil
	}
	if _, err := db.Exec(ctx, healthyBindingSQL(evidenceAt), credentialID, rawModel, evidenceAt); err != nil {
		return err
	}
	_, err := db.Exec(ctx, healthyCredentialSQL(), credentialID)
	return err
}
