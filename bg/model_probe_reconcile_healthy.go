package bg

import (
	"context"
	"log/slog"
)

// reconcileHealthyConfirmedBindings 是 reconcileBrokenConfirmedBindings 的反向操作。
// 当 model_probe_state.state = 'healthy_confirmed' 且连续两次探测成功时，
// 恢复 binding.available = TRUE，并清理遗留冷却标记。
//
// 2026-06-29 fix: 解决"常用模型报无可用凭据"问题的关键修复。
// 原有 reconcileBrokenConfirmedBindings 只处理 broken → unavailable 方向，
// 缺少 healthy → available 的反向恢复，导致探测恢复后路由仍认为不可用。
//
// 调用位置: cycle() 开头，与 reconcileBrokenConfirmedBindings 配对执行。
func (r *ModelProbeRunner) reconcileHealthyConfirmedBindings(ctx context.Context) {
	tag, err := r.db.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET available          = TRUE,
			    unavailable_reason = NULL,
			    unavailable_at     = NULL,
			    unavailable_recover_at = NULL,
			    updated_at         = now()
			FROM provider_models pm
			WHERE cmb.provider_model_id = pm.id
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
			  AND EXISTS (
			      SELECT 1 FROM model_probe_state mps
			      WHERE mps.credential_id = cmb.credential_id
			        AND mps.raw_model_name = pm.raw_model_name
			        AND mps.state = 'healthy_confirmed'
			        AND mps.consecutive_successes >= 2
			  )
		`)
	if err != nil {
		slog.Warn("model probe: healthy_confirmed reconciliation failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("model probe: reconciled healthy_confirmed bindings to available",
			"count", tag.RowsAffected())
		// A stale node_probe_state row is an independent hard gate in
		// v_routable_credential_models. Clear it for the same healthy-confirmed
		// pairs even when the binding was already available, because recovery may
		// have repaired only the node state on an earlier pass.
		if nodeTag, nodeErr := r.db.Exec(ctx, `
		UPDATE node_probe_state nps
		SET consecutive_failures = 0,
		    consecutive_successes = GREATEST(nps.consecutive_successes, 1),
		    next_retry_at = now() + interval '1 hour',
		    next_retry_seconds = 3600,
		    paused = FALSE,
		    in_flight_until = NULL,
		    last_direct_ok = TRUE,
		    last_gateway_ok = TRUE,
		    last_err_code = NULL,
		    last_err_detail = NULL,
		    updated_at = now()
		WHERE EXISTS (
			SELECT 1
			FROM credential_model_bindings cmb2
			JOIN provider_models pm2 ON pm2.id = cmb2.provider_model_id
			JOIN model_probe_state mps2
			  ON mps2.credential_id = cmb2.credential_id
			 AND mps2.raw_model_name = pm2.raw_model_name
			WHERE cmb2.credential_id = nps.credential_id
			  AND pm2.raw_model_name = nps.raw_model_name
			  AND cmb2.available = TRUE
			  AND mps2.state = 'healthy_confirmed'
			  AND mps2.consecutive_successes >= 2
		)
	`); nodeErr != nil {
			slog.Warn("model probe: healthy_confirmed node state reconciliation failed", "error", nodeErr)
		} else if nodeTag.RowsAffected() > 0 {
			slog.Info("model probe: cleared stale node probe state for healthy bindings",
				"count", nodeTag.RowsAffected())
		}
	}
}
