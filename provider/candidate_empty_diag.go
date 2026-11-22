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
	"sort"
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

// emptyCandidateGate 是一道状态闸在归因里的全部表述：分类、人读标签、
// 处置方向，以及**单闸独占时的整句**（逐字沿用 2026-11-12 版文案）。
type emptyCandidateGate struct {
	class  string
	label  string
	action string
	single func(offers, n int) string
}

// gates 按既有优先序返回**全部**闸（含计数为 0 的），调用方自行过滤。
//
// ★ 顺序是**刻意的**：先说「模型压根没登记」（那是根因），再说状态闸（那是
// 运维开关）；结构性的问题（凭据缺失 / 遗留 v1 健康列）排在运营开关之前。
// 多闸命中数**打平**时沿用这个顺序定主因。
func (b EmptyCandidateBreakdown) gates() []emptyCandidateGate {
	return []emptyCandidateGate{
		{CauseClassCredentialMissing, "已删除/缺失的凭据", "修绑或改绑该 offer",
			func(offers, n int) string {
				return fmt.Sprintf("模型已登记（%d offer），但 %d 条挂在已删除/缺失的凭据上 ⇒ 处置：修绑或改绑该 offer", offers, n)
			}},
		{CauseClassCredentialStatus, "credentials.status 闸", "核对并打开该凭据的 status",
			func(offers, n int) string {
				return fmt.Sprintf("模型已登记（%d offer），但 %d 条被 credentials.status 闸滤掉（遗留 v1 健康列，与 lifecycle_status/health_status 不同源）⇒ 处置：核对并打开该凭据的 status", offers, n)
			}},
		{CauseClassCredentialManualDisabled, "credentials.manual_disabled 闸", "解除该凭据的人工停用标记",
			func(offers, n int) string {
				return fmt.Sprintf("模型已登记（%d offer），但 %d 条被 credentials.manual_disabled 闸滤掉 ⇒ 处置：解除该凭据的人工停用标记", offers, n)
			}},
		{CauseClassCredentialLifecycle, "credentials.lifecycle_status 闸", "在凭据生命周期面启用/解除 retired",
			func(offers, n int) string {
				return fmt.Sprintf("模型已登记（%d offer），但 %d 条被 credentials.lifecycle_status 闸滤掉 ⇒ 处置：在凭据生命周期面启用/解除 retired", offers, n)
			}},
		{CauseClassCredentialQuota, "凭据配额态", "处置配额（充值/解封）",
			func(offers, n int) string {
				return fmt.Sprintf("模型已登记（%d offer），但 %d 条被凭据配额态滤掉 ⇒ 处置：处置配额（充值/解封）", offers, n)
			}},
		{CauseClassProviderDisabled, "供应商停用", "启用供应商/换供应商",
			func(offers, n int) string {
				return fmt.Sprintf("模型已登记（%d offer），但 %d 条的供应商被停用 ⇒ 处置：启用供应商/换供应商", offers, n)
			}},
		{CauseClassUnavailableReason, "model_offers.unavailable_reason(manual*) 下线", "解除该 offer 的人工下线标记",
			func(offers, n int) string {
				return fmt.Sprintf("模型已登记（%d offer），但 %d 条被 model_offers.unavailable_reason(manual*) 滤掉 ⇒ 处置：解除该 offer 的人工下线标记", offers, n)
			}},
		{CauseClassProviderTenant, "其他租户的供应商", "为当前租户登记模型/凭据，或核对请求租户",
			func(offers, n int) string {
				return fmt.Sprintf("模型已登记（%d offer），但 %d 条属于其他租户的供应商（候选 SQL 限定 p.tenant_id=$tenant）⇒ 处置：为当前租户登记模型/凭据，或核对请求租户", offers, n)
			}},
	}
}

// activeGates 返回命中数 > 0 的闸，保持 gates() 的优先序。
func (b EmptyCandidateBreakdown) activeGates() []rankedGate {
	counts := []int{
		b.BlockedByCredentialMissing, b.BlockedByCredentialStatus, b.BlockedByCredentialManualDisabled,
		b.BlockedByCredentialLifecycle, b.BlockedByCredentialQuota, b.BlockedByProviderDisabled,
		b.BlockedByUnavailableReason, b.BlockedByProviderTenant,
	}
	all := b.gates()
	out := make([]rankedGate, 0, len(all))
	for i := range all {
		if counts[i] > 0 {
			out = append(out, rankedGate{emptyCandidateGate: all[i], count: counts[i], order: i})
		}
	}
	return out
}

// rankedGate 是带命中数的闸；order 是优先序下标（打平时据此定先后）。
type rankedGate struct {
	emptyCandidateGate
	count int
	order int
}

// rankedGates 把命中的闸按「命中 offer 数降序、同数按优先序」排好。
//
// ★ 为什么要按命中数排（2026-11-22 211 事故）：一条 offer 可被**多道闸同时**
// 挡住，各闸计数是重叠的。旧实现按固定顺序报「第一道有命中的闸」——
// claude-sonnet-5-5 的 11 条 offer 里只有 1 条沾到 credentials.manual_disabled，
// 却被它抢了头条（「解除人工停用标记」），而真正挡住 8 条的「凭据配额态
// （上游 INSUFFICIENT_BALANCE）」被埋没 ⇒ 运维照着日志去解一个不相干的
// 标记，模型照样 503。按命中数排，头条才是「处理它能恢复最多 offer」的那道闸。
func (b EmptyCandidateBreakdown) rankedGates() []rankedGate {
	active := b.activeGates()
	sort.SliceStable(active, func(i, j int) bool { return active[i].count > active[j].count })
	return active
}

// Summarize 把归因压成一句人能直接照做的结论。
//
// ★ 只有一道闸命中 ⇒ 沿用单闸整句（逐字不变）。
// ★ 多道闸同时命中 ⇒ 列出全部闸（按命中数降序）并标明主因与处置方向——
// 只报其中一道会让运维漏掉其余的闸，按固定顺序报则会被小计数闸抢头条。
// ★ 全零 ⇒ 说明模型登记了但闸全开 ⇒ 那就是别的问题（如租户/档位/模态不匹配），
// 这句话会明说「状态不是原因」，免得运营继续朝状态方向查。
func (b EmptyCandidateBreakdown) Summarize() string {
	if b.OffersTotal == 0 {
		return "模型未登记（offers_total=0）⇒ 处置：登记模型 / 核对模型名"
	}
	ranked := b.rankedGates()
	switch len(ranked) {
	case 0:
		return fmt.Sprintf("模型已登记（%d offer）且状态闸全开 ⇒ 状态不是原因 ⇒ 继续查租户/档位/模态/客户端档匹配", b.OffersTotal)
	case 1:
		return ranked[0].single(b.OffersTotal, ranked[0].count)
	}
	parts := make([]string, 0, len(ranked))
	for _, g := range ranked {
		parts = append(parts, fmt.Sprintf("%s %d 条", g.label, g.count))
	}
	return fmt.Sprintf("模型已登记（%d offer），但候选被 %d 道状态闸滤空（一条 offer 可被多道闸同时挡住，计数有重叠）；按命中 offer 数：%s ⇒ 主因=%s ⇒ 处置：%s",
		b.OffersTotal, len(ranked), strings.Join(parts, " > "), ranked[0].label, ranked[0].action)
}

// CauseClass 是归因的**有界分类**（R-76-3 批判审计补，2026-11-17）。
//
// # 为什么要有它
//
// db_empty 的日志冷却闸需要「归因变了就立刻放行」。但**不能**把
// `Summarize()` 的原文塞进键：Summarize() 把 offer **计数**也拼进句子
// （「模型已登记（3 offer），但 2 条…」），计数抖动（3↔4）会让键交替出现
// ⇒ 每条都是「该键首次」⇒ **洪水反而压不住**。
//
// ⇒ 所以键里放的是**有限枚举**：与 Summarize() 的分支一一对应，
// 取值集合固定 ⇒ 键空间有界（类数 × 模型数 × 租户数）。
//
// ★ 与 Summarize() 的分支顺序**必须一致**：Summarize 先说「模型压根没登记」
// （那是根因），再说状态闸。分类若与摘要顺序不同，运营会看到
// 「摘要说配额、分类说未登记」这种自相矛盾的两套结论。
const (
	// CauseClassNotRegistered：模型压根没登记（offers_total=0）。
	CauseClassNotRegistered = "not_registered"
	// CauseClassCredentialMissing：offer 挂在已删除/缺失的凭据上。
	CauseClassCredentialMissing = "cred_missing"
	// CauseClassCredentialStatus：被 credentials.status 闸滤掉。
	CauseClassCredentialStatus = "cred_status"
	// CauseClassCredentialManualDisabled：被 credentials.manual_disabled 闸滤掉。
	CauseClassCredentialManualDisabled = "cred_manual_disabled"
	// CauseClassCredentialLifecycle：被 credentials.lifecycle_status 闸滤掉。
	CauseClassCredentialLifecycle = "cred_lifecycle"
	// CauseClassCredentialQuota：被凭据配额态滤掉。
	CauseClassCredentialQuota = "cred_quota"
	// CauseClassProviderDisabled：供应商被停用。
	CauseClassProviderDisabled = "provider_disabled"
	// CauseClassUnavailableReason：被 model_offers.unavailable_reason(manual*) 滤掉。
	CauseClassUnavailableReason = "unavailable_reason"
	// CauseClassProviderTenant：offer 属于其他租户的供应商。
	CauseClassProviderTenant = "provider_tenant"
	// CauseClassGatesOpen：模型已登记且状态闸全开 ⇒ 状态不是原因。
	CauseClassGatesOpen = "gates_open"
	// CauseClassDiagError：归因查询本身失败（拿不到 breakdown）。
	CauseClassDiagError = "diag_error"
)

// CauseClass 返回归因的**有界分类**。
//
// 与 Summarize() 的主因严格一致：多闸命中时取「命中数最多」的那道闸
// （打平按既有优先序），单闸 / 零闸与旧版相同。
func (b EmptyCandidateBreakdown) CauseClass() string {
	if b.OffersTotal == 0 {
		return CauseClassNotRegistered
	}
	ranked := b.rankedGates()
	if len(ranked) == 0 {
		return CauseClassGatesOpen
	}
	return ranked[0].class
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
