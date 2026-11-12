// provider/candidate_empty_diag.go —— ★「候选为空」时的**归因**诊断（2026-11-12 48h 审计第五轮）
//
// # 为什么要有这个文件
//
// ★ 现象（211 真机 6h 窗取证）：`/v1/audio/transcriptions` 35 次 503，日志里只有
// ★ `[candidate_diag] db_empty {plan_count:0, candidate_count:0}` —— ★ 这条信号
// ★ **无法区分**下面两种完全不同、处置方式也完全不同的局面：
//
// ★① **模型没有登记**（真·没这个模型）⇒ 处置＝登记模型/换模型名；
// ★② **模型登记了、但候选被状态闸滤空** ⇒ 处置＝**把那批凭据/供应商的状态打开**。
//
// ★ 实测踩到的是②：whisper ASR 的 offer 在 `model_offers` 里**有**（canonical_id 3346055），
// ★ 但它挂的凭据 `credentials.status='disabled'`（**遗留 v1 健康列**，2026-11-05 写的，
// ★ 当前状态机 `lifecycle_status='active'` / `health_status` 另说）
// ★ ⇒ 候选 SQL 的 `COALESCE(c.status,'active') = 'active'` 把它滤掉
// ★ ⇒ 上游 whisper-cpp **其实活着**（192.168.31.13:8081 实测可达），
// ★ ★ 但运维在日志里只看到「db_empty」⇒ **无法定位到真正的开关**。
//
// ★⇒ 本文件把「空」的**归因**做成一次**只在空路径发生**的查询：
// ★⇒ 代价只付在已经失败的时候（成功路径零开销），收益＝日志直接给出可执行结论。
//
// # 口径（必须与 provider/client.go 的候选 SQL 逐条对齐）
//
// ★ 这里数的是「**同一条模型、同一批状态闸**」下的分流计数：
// ★ `offers_total` 是**忽略状态闸**的模型级 offer 数（= 模型登记了没有）；
// ★ 后面每个 `blocked_*` 是**被哪一道闸滤掉的**。
// ★ ★★ 因此本文件**不是**新的一套口径：闸的集合与顺序取自候选 SQL 本身（见下方注释），
// ★ ★★ 口径分叉的风险＝「诊断说滤掉了、实际没滤」⇒ ★ 故这里逐条写出闸，
// ★ ★★ 且**只统计本模型自己的 offer**，不引入新语义。
package provider

import (
	"context"
	"fmt"
	"strings"
)

// EmptyCandidateBreakdown 是「候选为空」的**分流归因**。
//
// ★ 每个字段都是「被该闸滤掉的 offer 数」；`OffersTotal` 是模型级 offer 总数
// （= 忽略全部状态闸时的候选上限）。
type EmptyCandidateBreakdown struct {
	// OffersTotal：模型登记了多少 offer（忽略全部状态闸）。
	OffersTotal int
	// BlockedByCredentialStatus：被 `credentials.status` 闸滤掉
	// （遗留 v1 健康列；仓内另有 5 处查询用 `status IN ('active','cooling','degraded')`
	//  当作可用 ⇒ 此闸比仓内其它口径**更严**）。
	BlockedByCredentialStatus int
	// BlockedByCredentialManualDisabled：被 `credentials.manual_disabled` 闸滤掉
	// （2026-11-12 批判审计 P2 补：候选 SQL 有此闸而诊断漏数 ⇒ 该闸单独
	//  触发时旧版报「状态闸全开 ⇒ 状态不是原因」——恰是要消灭的误导形态）。
	BlockedByCredentialManualDisabled int
	// BlockedByCredentialLifecycle：被 `credentials.lifecycle_status` 闸滤掉。
	BlockedByCredentialLifecycle int
	// BlockedByCredentialQuota：被 `credentials.quota_state` 闸滤掉。
	BlockedByCredentialQuota int
	// BlockedByProviderDisabled：被 `providers.enabled/manual_disabled` 闸滤掉。
	BlockedByProviderDisabled int
	// BlockedByProviderTenant：被 `providers.tenant_id` 闸滤掉
	// （2026-11-12 批判审计 P2 补：候选 SQL 限定 `p.tenant_id=$2` 而诊断
	//  无租户谓词 ⇒ 计数跨租户）。
	BlockedByProviderTenant int
	// BlockedByUnavailableReason：被 `model_offers.unavailable_reason LIKE 'manual%'` 闸滤掉。
	BlockedByUnavailableReason int
	// BlockedByCredentialMissing：凭据行不存在/被删（LEFT JOIN 为空）。
	BlockedByCredentialMissing int
}

// Summarize 把归因压成一句人能直接照做的结论。
//
// ★ 排序是**刻意的**：先说「模型压根没登记」（那是根因），再说状态闸（那是运维开关）。
// ★ 全零 ⇒ 说明模型登记了但闸全开 ⇒ ★ 那就是别的问题（如租户/档位/模态不匹配），
// ★ 这句话会明说「状态闸不是原因」，免得运营继续朝状态方向查。
func (b EmptyCandidateBreakdown) Summarize() string {
	if b.OffersTotal == 0 {
		return "模型未登记（offers_total=0）⇒ 处置：登记模型 / 核对模型名"
	}
	switch {
	case b.BlockedByCredentialMissing > 0:
		return fmt.Sprintf("模型已登记（%d offer），但 %d 条挂在已删除/缺失的凭据上 ⇒ 处置：修绑或改绑该 offer",
			b.OffersTotal, b.BlockedByCredentialMissing)
	case b.BlockedByCredentialStatus > 0:
		return fmt.Sprintf("模型已登记（%d offer），但 %d 条被 credentials.status 闸滤掉（遗留 v1 健康列，与 lifecycle_status/health_status 不同源）⇒ 处置：核对并打开该凭据的 status",
			b.OffersTotal, b.BlockedByCredentialStatus)
	case b.BlockedByCredentialManualDisabled > 0:
		return fmt.Sprintf("模型已登记（%d offer），但 %d 条被 credentials.manual_disabled 闸滤掉 ⇒ 处置：解除该凭据的人工停用标记",
			b.OffersTotal, b.BlockedByCredentialManualDisabled)
	case b.BlockedByCredentialLifecycle > 0:
		return fmt.Sprintf("模型已登记（%d offer），但 %d 条被 credentials.lifecycle_status 闸滤掉 ⇒ 处置：在凭据生命周期面启用/解除 retired",
			b.OffersTotal, b.BlockedByCredentialLifecycle)
	case b.BlockedByCredentialQuota > 0:
		return fmt.Sprintf("模型已登记（%d offer），但 %d 条被凭据配额态滤掉 ⇒ 处置：处置配额（充值/解封）",
			b.OffersTotal, b.BlockedByCredentialQuota)
	case b.BlockedByProviderDisabled > 0:
		return fmt.Sprintf("模型已登记（%d offer），但 %d 条的供应商被停用 ⇒ 处置：启用供应商/换供应商",
			b.OffersTotal, b.BlockedByProviderDisabled)
	case b.BlockedByUnavailableReason > 0:
		return fmt.Sprintf("模型已登记（%d offer），但 %d 条被 model_offers.unavailable_reason(manual*) 滤掉 ⇒ 处置：解除该 offer 的人工下线标记",
			b.OffersTotal, b.BlockedByUnavailableReason)
	case b.BlockedByProviderTenant > 0:
		return fmt.Sprintf("模型已登记（%d offer），但 %d 条属于其他租户的供应商（候选 SQL 限定 p.tenant_id=$tenant）⇒ 处置：为当前租户登记模型/凭据，或核对请求租户",
			b.OffersTotal, b.BlockedByProviderTenant)
	default:
		return fmt.Sprintf("模型已登记（%d offer）且状态闸全开 ⇒ 状态不是原因 ⇒ 继续查租户/档位/模态/客户端档匹配", b.OffersTotal)
	}
}

// emptyCandidateBreakdownSQL 是归因查询。
//
// ★★ 闸的集合**逐条对齐** provider/client.go 的候选 SQL（loadCandidatesByModalityDB）：
// ★   · credentials.status
// ★   · credentials.lifecycle_status
// ★   · credentials.manual_disabled（2026-11-12 批判审计 P2 补数：此前 CTE
// ★     算了没用 ⇒ 该闸触发时报「状态不是原因」假归因）
// ★   · credentials.quota_state
// ★   · providers.enabled / providers.manual_disabled / base_url / protocol
// ★   · model_offers.unavailable_reason LIKE 'manual%'
// ★ ★★ 「模型匹配」这一段也用**同一套 4 路矩阵**（canonical_raw_name / standardized_name /
// ★ ★★ models_canonical.canonical_name / model_aliases）—— ★★ 否则会出现
// ★ ★★ 「诊断说模型没登记、实际模型登记在别名上」的假归因。
const emptyCandidateBreakdownSQL = `
WITH matched AS (
    SELECT mo.id AS offer_id,
           mo.credential_id,
           c.id IS NULL AS credential_missing,
           COALESCE(c.status, 'active')                AS c_status,
           COALESCE(c.lifecycle_status, 'active')      AS c_lifecycle,
           COALESCE(c.quota_state, 'ok')               AS c_quota,
           COALESCE(c.manual_disabled, FALSE)          AS c_manual_disabled,
           COALESCE(p.enabled, FALSE)                  AS p_enabled,
           COALESCE(p.manual_disabled, FALSE)          AS p_manual_disabled,
           COALESCE(p.base_url, '')                    AS p_base_url,
           COALESCE(p.protocol, '')                    AS p_protocol,
           p.tenant_id                                 AS p_tenant_id,
           COALESCE(mo.unavailable_reason, '')         AS unavailable_reason
    FROM model_offers mo
    LEFT JOIN credentials c ON c.id = mo.credential_id
    LEFT JOIN providers  p ON p.id = c.provider_id
    LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
    WHERE (
        mo.canonical_raw_name = $1
        OR mo.standardized_name = $1
        OR mc.canonical_name = $1
        OR EXISTS (
            SELECT 1 FROM model_aliases ma
            WHERE ma.raw_name = $1
              AND COALESCE(ma.status, 'active') = 'active'
              AND (mo.canonical_id IS NOT NULL AND ma.canonical_id = mo.canonical_id)
        )
    )
)
SELECT
    COUNT(*)::int AS offers_total,
    COUNT(*) FILTER (WHERE credential_missing)::int AS blocked_by_credential_missing,
    COUNT(*) FILTER (WHERE NOT credential_missing AND c_status <> 'active')::int AS blocked_by_credential_status,
    COUNT(*) FILTER (WHERE NOT credential_missing AND c_manual_disabled)::int AS blocked_by_credential_manual_disabled,
    COUNT(*) FILTER (WHERE NOT credential_missing AND c_lifecycle <> 'active')::int AS blocked_by_credential_lifecycle,
    COUNT(*) FILTER (WHERE NOT credential_missing AND c_quota IN ('permanently_exhausted','balance_exhausted','periodic_exhausted'))::int AS blocked_by_credential_quota,
    COUNT(*) FILTER (WHERE NOT credential_missing AND (NOT p_enabled OR p_manual_disabled OR p_base_url = '' OR p_protocol = ''))::int AS blocked_by_provider_disabled,
    COUNT(*) FILTER (WHERE NOT credential_missing AND p_tenant_id IS DISTINCT FROM $2)::int AS blocked_by_provider_tenant,
    COUNT(*) FILTER (WHERE NOT credential_missing AND unavailable_reason LIKE 'manual%%')::int AS blocked_by_unavailable_reason
FROM matched`

// diagnoseEmptyCandidates 给出「候选为空」的归因。
//
// ★ 只在**已经判空**的路径调用 ⇒ 成功路径零开销。
// ★ 查询失败**不改变**调用方的结论（诊断是旁路）⇒ 返回 zero value + err。
func (c *Client) diagnoseEmptyCandidates(ctx context.Context, model, tenantID string) (EmptyCandidateBreakdown, error) {
	var b EmptyCandidateBreakdown
	if c.dbPool == nil {
		return b, fmt.Errorf("routing DB not configured")
	}
	lookup := strings.ToLower(strings.TrimSpace(model))
	if lookup == "" {
		return b, fmt.Errorf("empty model")
	}
	// $2 = tenantID：与候选 SQL 的 p.tenant_id=$2 同参（审计 P2 补）。
	row := c.dbPool.QueryRow(ctx, emptyCandidateBreakdownSQL, lookup, tenantID)
	if err := row.Scan(emptyCandidateScanTargets(&b)...); err != nil {
		return EmptyCandidateBreakdown{}, err
	}
	return b, nil
}

// emptyCandidateScanTargets 返回与 emptyCandidateBreakdownSQL 终端 SELECT
// 列序**逐位同序**的 Scan 目标切片。§695 列序钉（2026-11-12）：这条平行
// 数组此前靠人眼对位（批判审计正是抓出「列了但没数」的 manual_disabled），
// 收敛到单一构造点后由 TestEmptyCandidateBreakdownScanOrderPinned 机器钉住
// ——加列不改这里 / 改这里不加列 / 调序，任一漂移当场红。
func emptyCandidateScanTargets(b *EmptyCandidateBreakdown) []any {
	return []any{
		&b.OffersTotal,
		&b.BlockedByCredentialMissing,
		&b.BlockedByCredentialStatus,
		&b.BlockedByCredentialManualDisabled,
		&b.BlockedByCredentialLifecycle,
		&b.BlockedByCredentialQuota,
		&b.BlockedByProviderDisabled,
		&b.BlockedByProviderTenant,
		&b.BlockedByUnavailableReason,
	}
}
