package bg

import (
	"context"
	"log/slog"
)

// reconcileLegacyModelProbeStates 把 model_probe_state 中残留的
// 旧状态字面量（'available' / 'healthy' / 'unavailable' / 'failing'）
// 一次性映射到当前的状态机。这是 2026-07-04 加的 belt-and-suspenders
// 修复，与 migrations/329 同时提供：
//
//   - migration 是数据库侧幂等的启动期修复；
//   - 这个函数是运行期 idempotent reconciler，万一旧 init 路径
//     （db/db.go）再次被某条未来代码路径触发并通过新 CHECK 约束
//     "绕过" 写入（比如 Postgres <13 的 NOT VALID 约束被复制时
//     暂时失效），下一次 probe cycle 也会自动清理。
//
// 调用位置：cycle() 紧跟在 reconcileHealthyConfirmedBindings 之后，
// 在 selectProbeTargets 之前——确保路由看到的（credential, model）行
// 全部落在合法状态值集合内。
func (r *ModelProbeRunner) reconcileLegacyModelProbeStates(ctx context.Context) {
	// 'available' / 'healthy' 是早期废弃 init 路径（db/db.go）
	// 写入的"已通过探测"字面量。语义上等同 healthy_confirmed。
	promoted, err := r.db.Exec(ctx, `
		UPDATE model_probe_state
		SET    state = 'healthy_confirmed',
		       last_state_change_at = NOW()
		WHERE  state IN ('available', 'healthy')
	`)
	if err != nil {
		slog.Warn("model probe: legacy state reconciliation (promote) failed",
			"error", err)
	} else if promoted.RowsAffected() > 0 {
		slog.Info("model probe: reconciled legacy probe states to healthy_confirmed",
			"count", promoted.RowsAffected())
	}

	// 'unavailable' / 'failing' 等价于"探测未通过"，交给 probe 自然
	// 流程把它们升级为 unknown → healthy_confirmed。
	demoted, err := r.db.Exec(ctx, `
		UPDATE model_probe_state
		SET    state = 'unknown',
		       last_state_change_at = NOW()
		WHERE  state IN ('unavailable', 'failing')
	`)
	if err != nil {
		slog.Warn("model probe: legacy state reconciliation (demote) failed",
			"error", err)
	} else if demoted.RowsAffected() > 0 {
		slog.Info("model probe: reconciled legacy probe states to unknown",
			"count", demoted.RowsAffected())
	}
}
