package bg

import (
	"context"
	"log/slog"
)

// reconcileHealthyConfirmedBindings 是 reconcileBrokenConfirmedBindings 的反向操作。
// 当 model_probe_state.state = 'healthy_confirmed' 时，恢复 binding.available = TRUE。
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
		    unavailable_at     = NULL
		FROM provider_models pm
		WHERE cmb.provider_model_id = pm.id
		  AND cmb.available = FALSE
		  AND cmb.unavailable_reason = 'model_probe_broken'
		  AND EXISTS (
		      SELECT 1 FROM model_probe_state mps
		      WHERE mps.credential_id = cmb.credential_id
		        AND mps.raw_model_name = pm.raw_model_name
		        AND mps.state = 'healthy_confirmed'
		  )
	`)
	if err != nil {
		slog.Warn("model probe: healthy_confirmed reconciliation failed", "error", err)
	} else if tag.RowsAffected() > 0 {
		slog.Info("model probe: reconciled healthy_confirmed bindings to available",
			"count", tag.RowsAffected())
	}
}
