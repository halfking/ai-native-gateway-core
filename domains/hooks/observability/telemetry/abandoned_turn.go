// ============================================================================
// 「请求开始了却从没有终态」在**会话族内**的落点（迁移 820，审计 §9.92）
//
// # 为什么是「会话族补一类状态」而不是另建小表
//
// 迁移 819 的做法是新建 `public.request_abandoned`，t0 插行、终态 DELETE。
// 2026-10-03 用户拍板改选「会话族补一类状态」（会话族补状态 / 接受丢失 /
// 另建小表，三选一）。本文件是那个选择的实现。
//
// # 决定形态的那条硬约束（读代码 + 真库实测，不是推测）
//
// `session_turns` 按 `(tenant_id, request_id, partition_date)` 幂等，且
// **首次插入后不可更新**：`turn_writer.go` 的 `appendTurnInLockedTx` 用
// `ON CONFLICT DO NOTHING`，冲突时靠 `RowsAffected()==0` 事后回读 turn_no
// （注释在 :257-265）。
//
// ⇒ 「t0 插一行 in_progress 占位、t1 再 UPDATE 它」**走不通**：第二次写入
// 会被 DO NOTHING 静默吞掉，那行会永久停在占位值上，把 success=false
// 写死并吞掉后续富化——这正是 mirror hook 首道门存在的理由
// （`hook.go:68-72` 的注释自陈了同一件事）。
//
// ⇒ 所以本落点**不插占位行**，改为在**终态事件到达时**打一个标记：
//
//	is_abandoned = TRUE ⇔ 这是一行终态 turn，而它的请求在 v1 侧
//	                      从来没有过 t0 记录。
//
// # 判据在哪（§9.92.2）
//
// `updateRequestLog` 的 `updated.RowsAffected() == 0` 分支里，紧跟着的
// `SELECT EXISTS (… WHERE request_id = $1)` **本来就在跑**（upsert 竞态回落）。
// `exists == false` 就是「v1 侧根本没有这个 request_id」。
//
// 为什么这等价于「t0 从未落库」而不是「已落但被覆盖」：
// `request_logs_hot` 上 `request_id` 是**唯一键**（真库实测：唯一索引
// `request_logs_hot_pkey USING btree (request_id)`），且实测每 id 恒 1 行
// （`SELECT n, count(*) FROM (… GROUP BY request_id)` → 全是 n=1）。
// ⇒ 「不存在」只可能意味着「从来没插过」。
//
// # 已知能力缺口（如实登记，不假装覆盖）
//
// 本列**覆盖不到**「只有 t0、之后彻底静默、连终态事件都没发」的那一类——
// 而那正是 §9.66 实测 19 条的主要形态。原因是结构性的：要在 turns 上覆盖它，
// 只能恢复「插 t0 占位行」，而那会被 ON CONFLICT DO NOTHING 写死
// success=false。要覆盖它必须让 session_turns 支持 UPDATE（另一个量级的改动）。
//
// ⇒ 本列的定位是**有界的子集**：「有终态事件、但 v1 侧无 t0」。
// 它的价值是**零新增写放大**：不加任何 INSERT、不改 mirror hook 的任何门，
// 只在「UPDATE 命中 0 行」这个本来就会发生的分支上多一次 UPDATE。
// ============================================================================

package telemetry

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// markAbandonedTurn 在 turn 上打 abandoned 标记。
//
// 由 `updateRequestLog` 的 upsert 竞态回落分支调用，且**只在
// exists == false（v1 侧确无该 request_id）时**调用——判据的方向在调用点
// 的注释里写明，反向会让每一次正常完成都被标成遗弃。
//
// fail-open（理由与 819 同源，但这次更轻）：失败只 slog.Warn + 计指标，
// **绝不回滚 request_logs 主事务**。它跑在 `tx.Rollback(ctx)` 之后的
// 回落路径上；这里若因 820 未应用而让语句失败，整条请求日志就废了，
// 而丢的只是一个诊断标记。
func markAbandonedTurn(ctx context.Context, tx pgx.Tx, entry *RequestLogEntry) {
	if tx == nil || entry == nil || entry.RequestID == "" {
		return
	}
	// 无会话头的行没有 session_turns 对应物（mirror 会合成 sys:* 会话，
	// 但那是统计口径，不是这个请求的身份）。标了也无处可查。
	if entry.GwSessionID == nil || *entry.GwSessionID == "" {
		return
	}

	// 820 未应用时的 fail-open：savepoint 保住外层事务。
	//nolint:errcheck // best-effort marker; failure must not fail request logging
	if _, err := tx.Exec(ctx, `SAVEPOINT gw_abandoned_turn_mark`); err != nil {
		recordAbandonedTurnOp("mark_failed")
		slog.Warn("abandoned_turn: mark savepoint failed, skipping marker (request log write unaffected)",
			"request_id", entry.RequestID, "error", err)
		return
	}

	// 母表 + hot 两张都试：写方到底命中哪一张取决于该行落在 hot 还是已
	// promote 的月分区。**只试一张**就是「同族只改一半」——命中另一张时
	// 静默 0 行，而这个 0 行与「没有遗弃」不可区分。
	var marked int64
	for _, tbl := range []string{"public.session_turns_hot", "public.session_turns"} {
		//nolint:errcheck // best-effort marker; failure must not fail request logging
		tag, err := tx.Exec(ctx, `
			UPDATE `+tbl+`
			   SET is_abandoned = TRUE
			 WHERE request_id = $1
			   AND tenant_id  = $2
			   AND is_abandoned IS NOT TRUE
		`, entry.RequestID, nonEmpty(entry.TenantID, "default"))
		if err != nil {
			recordAbandonedTurnOp("mark_failed")
			slog.Warn("abandoned_turn: mark failed on "+tbl+" (request log write unaffected)",
				"request_id", entry.RequestID, "error", err)
			//nolint:errcheck // best-effort
			_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT gw_abandoned_turn_mark`)
			return
		}
		marked += tag.RowsAffected()
	}

	//nolint:errcheck // releasing a savepoint never needs to fail the request
	_, _ = tx.Exec(ctx, `RELEASE SAVEPOINT gw_abandoned_turn_mark`)

	if marked == 0 {
		// 落点没命中任何一行。两种可能，**不可区分**：
		//   ① 终态行还没被 mirror 写入（本函数在 updateRequestLog 里，
		//      而 mirror 走 outbox 事件，**异步**）；
		//   ② 那一行已经带着终态写进 hot 了，但 UPDATE 的租户/键不匹配。
		//
		// ⚠ ① 是**常态而非异常**：outbox 是异步的，t1 的 UPDATE 常常先于
		// mirror 落行。所以本函数**不能**在这里报错，那会把正常流量打成告警。
		// 这个缺口是真实的：落点存在竞态窗口，窗口内的遗弃会漏标。
		// 记在 §9.92 的遗留里，不假装覆盖。
		recordAbandonedTurnOp("mark_no_row")
		return
	}
	recordAbandonedTurnOp("mark")
}

// settleAbandonedTurn 是 markAbandonedTurn 的另一半。
//
// 存在意义要说清楚：当前形态下**终态行由 mirror hook 照常镜像**，
// 落点是在那一行上打标，而不是像 819 那样删除一行。所以这里**不需要**
// 「收口」动作——`is_abandoned` 一旦为 TRUE 就是最终状态。
//
// 为什么仍保留这个函数与它的调用点：它是**终态证据才允许动落点**这条
// 不变量的显式表达点。将来若落点演进成需要清理的形态（例如把遗弃行
// 移出 hot），这里是唯一正确的接线处；而把判据写在这里而不是散进
// 业务分支，是 §9.92.3 的同族纪律（承重判据要有单点）。
func settleAbandonedTurn(ctx context.Context, entry *RequestLogEntry) {
	// 今天刻意是 no-op：本形态下没有需要收口的资源。
	// 保留签名与调用点是为了让「终态才动落点」这条不变式有位置可钉，
	// 而不是留一句注释（注释不被任何门检查，见 §9.59.7 装饰门那一节）。
	_ = ctx
	_ = entry
}
