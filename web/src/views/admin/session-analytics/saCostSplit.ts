// saCostSplit.ts —— 会话分析族的「内部成本（美元）/ 外部成本（积分）」双单位呈现口径。
//
// ★ 为什么不能把两个单位相加后再加 $ 前缀：
//   后端的 total_cost = internal_cost(美元) + external_cost(积分) ⇒ **跨单位相加**
//   （统计标准 framework §3.2 明令「两种单位不同，禁止相加」）⇒ 给这个合计数
//   加一个 $，等于凭空造出一个**不存在的美元金额**。
//   ⇒ 渲染口径：**分列两个单位**，各带自己的单位标签与数值（与已删总览页的
//     「两张卡」写法同构：sessionAnalytics.metric.internalCost +
//     sessionAnalytics.unit.usd / sessionAnalytics.metric.externalCost +
//     sessionAnalytics.unit.credits）。
//
// ★★「外部成本为 0 ⇒ 渲染 '—'」这条规则（本轮新增，理由必须写清楚）：
//   会话分析族的 external_cost **当前恒为 0** —— 积分写侧尚未落地，
//   物化视图的 external_cost 列还没有真实数据。于是
//     · 渲染「外部成本（积分）0」= 把「**没采到**」讲成「**测得是 0**」，
//       这正是本轮要消灭的那一类失实断言；
//     · 而 external_cost 是 Go 的 float64（无 omitempty）⇒ 字段**永远在**，
//       「0」既可能是「真零」也可能是「没写侧」⇒ 前端**无法**区分二者。
//   ⇒ 规则（本文件是唯一真源，所有调用点一致）：
//     · external 为 null / undefined / NaN ⇒ '—'（字段没来 ⇒ 不知道）；
//     · external 为 0（字段在位）⇒ '—' + 标题说明「本视图不携带积分」；
//     · external 非 0 ⇒ 渲染真实数值。
//   ★ internal（美元）**不适用**这条规则：它是真被测到的量（物化视图
//     internal_cost 列），0 是一个合法的测量值 ⇒ 照常渲染。
//
// ★ 「不写这一列」与「写 0」是两件事：前端能给的只有「不知道」，
//   所以缺值一律渲染中性占位符 '—'，绝不渲染 0。

/** 中性占位符（缺值 / 未采集 ⇒ 渲染它，而不是 0）。 */
export const COST_PLACEHOLDER = '—'

/** 数值是否「缺」（null / undefined / NaN ⇒ 不可解释）。 */
export function isMissingCost(v: number | null | undefined): boolean {
  return v === null || v === undefined || Number.isNaN(v)
}

/**
 * 内部成本（美元）文本：两位小数 + 千分位；缺值 ⇒ '—'。
 * 口径：internal_cost 是真被测到的量，0 是合法测量值 ⇒ **不做**占位符处理。
 */
export function formatUsdAmount(v: number | null | undefined): string {
  if (isMissingCost(v)) return COST_PLACEHOLDER
  return Number(v).toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  })
}

/**
 * 外部成本（积分）文本：千分位；缺值或 0 ⇒ '—'（见文件头规则）。
 */
export function formatCreditsAmount(v: number | null | undefined): string {
  if (isMissingCost(v)) return COST_PLACEHOLDER
  if (Number(v) === 0) return COST_PLACEHOLDER
  return Number(v).toLocaleString()
}

/**
 * 外部成本是否落在「占位符」形态（⇒ 调用方应挂上「未采集 / 不携带」的标题说明）。
 */
export function externalCostIsPlaceholder(v: number | null | undefined): boolean {
  return isMissingCost(v) || Number(v) === 0
}

/** 翻译函数签名（vue-i18n 的 `t` 与此兼容）。 */
export type Translate = (key: string, named?: Record<string, unknown>) => string

/**
 * 内部成本（美元）的**完整呈现文本**（含单位）。
 * 缺值 ⇒ 纯占位符 '—'（不拼单位后缀，避免把「不知道」说成「0 美元」）。
 */
export function usdCostText(v: number | null | undefined, t: Translate): string {
  if (isMissingCost(v)) return COST_PLACEHOLDER
  return t('sessionAnalytics.unit.usd', { value: formatUsdAmount(v) })
}

/**
 * 外部成本（积分）的**完整呈现文本**（含单位）。
 * 缺值或 0 ⇒ 纯占位符 '—'（规则见文件头）。
 */
export function creditsCostText(v: number | null | undefined, t: Translate): string {
  if (isMissingCost(v)) return COST_PLACEHOLDER
  if (Number(v) === 0) return COST_PLACEHOLDER
  return t('sessionAnalytics.unit.credits', { value: formatCreditsAmount(v) })
}
