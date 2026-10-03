// useDegradationMarker.ts —— 降级标记的合并逻辑（2026-10-03）
//
// 后端在「schema 落后于代码」时返回 200 + 空载荷。这个状态必须和
// 「真的没有数据」在页面上区分开，否则用户会看到「本月没花钱」而实际有数据。
//
// 存在的原因是一次真实事故：period-compare 把 2026-09 的 1139.62 美元
// 显示成 0，cache-economics 一次渲染 6 个指标全是 0，页面上没有任何提示。
//
// 三态，缺一不可：
//   undefined → 没调用过 / 没这个字段（**不可解释为健康**）
//   false     → 服务端确认过，这是真实测量值
//   true      → 服务端算不出来，下面的数字是占位
//
// ⚠️ 别把 undefined 和 false 合并成同一个「健康」语义。那是本标记要解决的
// 问题自己长回来。
//
// 顺带纠正一条我自己写过的过度声明：这里用 `=== true` 而不是真值判断，
// 对当前 API **并没有可观察的差别**（JSON 只产出真布尔值）。真正的承重点
// 在下面 isDegraded 的注释里 —— 是「缺失项不得触发降级提示」，不是「=== true
// 比真值判断更严」。别把后一个当成理由。

export interface DegradationPayload {
  degraded?: boolean
  degraded_reason?: string
}

/**
 * DegradationMarker 是后端在**每一个**会降级的响应上恒发的契约字段。
 * 放在本模块而不是某个 api 文件里：api/admin.ts 与 api/usage.ts 都要用它，
 * 契约的归属地应当是最底层的那个模块，否则每加一个降级端点就要复制一份。
 */
export interface DegradationMarker {
  degraded: boolean
  degraded_reason?: string
}

export interface DegradationState {
  /** 是否有任一数据源处于降级态。 */
  active: boolean
  /** 逐源的原因，形如 "缓存经济学: missing column: xxx.yyy"。 */
  reasons: string[]
}

export function emptyDegradation(): DegradationState {
  return { active: false, reasons: [] }
}

/**
 * isDegraded 判定一个响应是否处于降级态。
 *
 * 只有**显式** true 才算降级：字段缺失（undefined）与 false 都不是降级，
 * 但它们也都不能被当作「已确认健康」—— 那由调用方看 active 的语义区分。
 */
export function isDegraded(payload: DegradationPayload | null | undefined): boolean {
  return payload?.degraded === true
}

/**
 * mergeDegradation 把一个响应并入累计状态。
 *
 * 刻意不原地修改：返回新对象，便于单元测试断言，且避免 Vue 深层响应
 * 代理下「引用相同却内容变了」导致 UI 不更新。
 *
 * @param state  现有累计状态
 * @param source 数据源的可读名（用于让用户知道**哪个**指标不可信）
 * @param payload 该源的响应
 */
export function mergeDegradation(
  state: DegradationState,
  source: string,
  payload: DegradationPayload | null | undefined,
): DegradationState {
  if (!isDegraded(payload)) return state
  const reason = payload?.degraded_reason?.trim()
  const entry = `${source}: ${reason || 'schema 落后于代码'}`
  if (state.reasons.includes(entry)) return state
  return { active: true, reasons: [...state.reasons, entry] }
}
