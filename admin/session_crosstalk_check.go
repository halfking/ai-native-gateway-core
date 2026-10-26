package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// handleSessionCrosstalkCheck returns sessions that have been accessed by
// multiple tenants (cross-talk / 串话).
//
// GET /api/admin/session-crosstalk?hours=24
//
// ★ 2026-10-21（R74）换源：原实现读 `session_tenant_binding`。
//
//	那是一张 **v1 族的侧效应表**：唯一写入者是挂在 v1 `request_logs` 上的
//	触发器 `update_session_tenant_binding()`（domain 链 032 建）。
//	v1 族真删（迁移 890）后该触发器随表消失 ⇒ 表**永远不会再被写入**；
//	而且生产 252 上这张表**从来就没建过**（实测 `to_regclass` = NULL，
//	部署台账 `gateway_db_revision_sequences` 里也没有 032 的记录）⇒
//	本端点长期 **HTTP 500（42P01）** —— 属既有缺陷，不是 v1 退役造成的。
//
//	⇒ 改读**会话族**：`session_turns` 每行带 (session_id, tenant_id)
//	⇒ **同一个 session_id 出现在多个 tenant_id 下** = 串话 ⇒ 直接派生，
//	不需要旁路表，也不需要 v1 侧的写入副作用。
//
//	⚠ 口径变化（必须写清楚）：
//	  · 旧实现读的是**历史累积**的绑定表（一个 session 一旦串话过，
//	    tenant_id 就永久变成 `CONFLICT:t1,t2`），`hours` 参数只过滤
//	    `last_seen_at` ⇒ 串话**发生在窗口之外**也会被列出来。
//	  · 新实现按 `hours` 窗口**实时派生** ⇒ 只报告窗口内出现的串话。
//	    ⇒ `hours` 这个参数第一次真正生效。
//
//	⚠ 为什么走 `withAllTenantReadOnlyTx`（显式 super-admin bypass）：
//	  · `session_turns` 开了 RLS（`session_turns_tenant_isolation` /
//	    `session_turns_owner_filter` / `session_turns_super_admin_bypass`），
//	    而「检测跨租户访问」按定义就要**跨租户**看 ⇒ 必须 bypass。
//	  · 旧实现没有设任何 GUC —— 那张表在生产上根本不存在，所以没暴露；
//	    换源后必须显式设，否则 RLS 会把结果收成 0 行（静默失效）。
//	  · 授权前提：本端点挂在 `h.admin(...)` 之后 ⇒ 调用方已是 admin。
func (h *Handler) handleSessionCrosstalkCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	hoursStr := r.URL.Query().Get("hours")
	hours := 24
	if hoursStr != "" {
		if h, err := strconv.Atoi(hoursStr); err == nil && h > 0 && h <= 720 {
			hours = h
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	type conflict struct {
		SessionID    string `json:"gw_session_id"`
		TenantIDs    string `json:"tenant_ids"` // "CONFLICT:t1,t2"
		FirstSeenAt  string `json:"first_seen_at"`
		LastSeenAt   string `json:"last_seen_at"`
		RequestCount int    `json:"request_count"`
	}

	conflicts := make([]conflict, 0)
	var rowsErr error

	// ★ 派生查询：会话族里「同一个 session 出现在多个 tenant 下」。
	//   - ★★ 【数据来源修正，2026-10-22 12h 审计轮 D1】本查询原先只读
	//     `FROM session_turns`（**分区父表**）⇒ **漏掉了 `session_turns_hot`**。
	//     而所有**活的** turn 都写进 hot（`domains/session/v2/turn_writer.go`
	//     的 `INSERT INTO public.session_turns_hot`），hot 行要等保留窗口过后
	//     才被 promote 进父表（`lifecycle.hot_retention_hours`，默认 8h）。
	//     ⇒ ⇒ **最近 8 小时内的串话一条都看不到** ⇒ 端点返回
	//       `200 {"conflicts":[],"count":0}` ⇒ ★ **静默失明**
	//       （与 892 迁移头自己写的「回填 0 行 ⇒ 安静地失明」同型）。
	//     ⇒ 改读 **hot ∪ parent 两条腿**，与仓内正典
	//       `db.SessionFamilyTurnsSourceSQL()` 的形状一致。
	//   - ⚠ **D4：`ts` 不是 `session_turns` 的分区键**（分区键是
	//     `partition_date`，见 `sql/schema/01-schema.sql`）⇒ `ts >= …`
	//     **不触发分区裁剪**；而仓内正典索引是 `(tenant_id, ts)`，
	//     `ts` 不在前导位 ⇒ 对「无 tenant 条件」的谓词不可用。
	//     ⇒ 原注释里那句「ts 是分区键 ⇒ 分区裁剪 + 前导 ts 索引」**是错的**，
	//     已订正。生产实测 cost=752.53 / buffers=2,383（毫秒级）是真的，
	//     但**不能用那个机制解释**。
	//   - `HAVING count(DISTINCT tenant_id) > 1` 就是串话的定义。
	//   - `LIMIT 100` 与旧实现一致（响应体 contract 不变）。
	const crosstalkSQL = `
		SELECT session_id,
		       array_agg(DISTINCT tenant_id ORDER BY tenant_id) AS tenant_ids,
		       min(ts) AS first_seen_at,
		       max(ts) AS last_seen_at,
		       count(*) AS request_count
		FROM (
			SELECT session_id, tenant_id, ts FROM public.session_turns_hot
			UNION ALL
			SELECT session_id, tenant_id, ts FROM public.session_turns
		) t
		WHERE ts >= NOW() - INTERVAL '1 hour' * $1
		GROUP BY session_id
		HAVING count(DISTINCT tenant_id) > 1
		ORDER BY last_seen_at DESC
		LIMIT 100
	`

	err := withAllTenantReadOnlyTx(ctx, h.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, crosstalkSQL, hours)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c conflict
			var tenantIDs []string
			var firstSeen, lastSeen time.Time
			if err := rows.Scan(&c.SessionID, &tenantIDs, &firstSeen, &lastSeen, &c.RequestCount); err != nil {
				warnRowSkip("sessionCrosstalkCheck", err)
				continue
			}
			c.FirstSeenAt = firstSeen.Format(time.RFC3339)
			c.LastSeenAt = lastSeen.Format(time.RFC3339)
			// ★ 保留旧 contract 的 `CONFLICT:` 前缀约定（旧表的列注释
			//   就是「CONFLICT: 前缀表示跨租户访问」）⇒ 下游识别逻辑不用改。
			c.TenantIDs = "CONFLICT:" + strings.Join(tenantIDs, ",")
			conflicts = append(conflicts, c)
		}
		rowsErr = rows.Err()
		return nil
	})
	// ★ 聚合读端的错误必须走**分类三门**（admin/aggregate_read_guard.go）：
	//   42P01（关系不存在）⇒ 503 + `analytics_view_missing` + 迁移提示；
	//   其它错误 ⇒ 500。⇒ 旧实现用裸 writeInternalErr ⇒ 42P01 只给一个
	//   没有线索的 500（这正是本端点长期 500 却看不出缺哪张表的原因）。
	//   `IsMissingRelationError` 用 errors.As ⇒ 能穿透 withTx 的 %w 包装。
	if writeAggRowsErr(w, "sessionCrosstalkCheck", err) {
		return
	}
	// ★ rows 迭代错误**不能**被当成成功响应吞掉（部分结果 ≠ 成功）。
	if writeAggRowsErr(w, "sessionCrosstalkCheck", rowsErr) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"conflicts": conflicts,
		"count":     len(conflicts),
		"hours":     hours,
	})
}
