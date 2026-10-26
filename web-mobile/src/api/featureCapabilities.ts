import { req, type RequestOptions } from './client'

/**
 * featureCapabilities.ts — 功能能力位（2026-10-21，R72f 轮 / 属主决策项 #10 的后半）。
 *
 * GET /api/admin/capabilities
 *
 * ## ★ 文件名为什么是 `featureCapabilities` 而不是 `capabilities`
 *
 * ★ 仓里**已经有**一个 `src/hyper/capabilities.ts`（**浏览器能力**探测：
 *   `webCapabilities` / `resolveCapabilities`）⇒ ⇒ 两个「capabilities」
 *   语义完全无关。
 * ⇒ ★ 而且这不是洁癖问题：`src/api/orphanLedger.spec.ts` 的导入图用
 *   `from './<name>'` 的**宽松匹配**建边（UI 侧 `./capabilities` 会被记成
 *   「引用了名为 `capabilities` 的 api 模块」）⇒ ⇒ ⇒ 同名会让台账把
 *   `src/hyper/index.ts` 误认成 api 模块的引用方 ⇒ 报出一条**不存在的**
 *   「死文件」漂移。⇒ ⇒ 改用**带限定前缀**的名字，歧义从源头消失。
 *
 * ## 为什么需要它
 *
 * 「费用对账」是**可选装配**的功能：它的端点只在开关打开时才注册
 * （`admin/handler.go:1099` 的 `providerCostReconciliationHandler` 装配判断）。
 * ⇒ 而移动端那一席（`/m/reconciliation`）
 * 此前**无条件可见** ⇒ ⇒ 开关关着时 super_admin 照样看得见、点进去必然 404。
 * ★ 404 与「端点不存在」同形 ⇒ **没有一层会说真话**。
 *
 * ⇒ 所以前端需要**一个能问「这个功能开着吗」的地方** ⇒ 本模块就是它。
 *
 * ## ★ 口径
 *
 * - 能力位名 = `NavItem.requiresCapability` 用的名字（**逐字相等**）。
 * - 后端由「handler 是否装配」**派生**（不是另存一份状态）
 *   ⇒ ⇒ 「能力位说开着」与「端点真的在」不可能不一致。
 * - 端点**无条件注册** ⇒ ⇒ 前端**学得到「关」**（否则「按能力隐藏入口」无从谈起）。
 * - 鉴权是 **admin 档**（不是 super_admin）⇒ 因为抽屉对 tenant_admin 也渲染；
 *   若用 super_admin，tenant_admin 取能力位会拿到 403 ⇒ 前端就会把
 *   403 误当成「全部能力关」。
 */
export interface FeatureCapabilities {
  /** 「费用对账」已装配（端点已注册）。 */
  cost_reconciliation: boolean
}

/**
 * 能力位名清单（**运行时**可枚举）。
 *
 * ★ 为什么要有一份「清单」而不是只用上面的 interface：
 *   interface 的键在编译后**不存在** ⇒ 没法在运行时核对
 *   「nav 里的 `requiresCapability` 名字是不是后端真发的字段」。
 *   ⇒ ★ 而这种不一致是**静默**的：能力位名对不上 ⇒ fail-closed ⇒
 *   **那一席永远不出现**，没有任何报错 ⇒ ⇒ 必须有一道门。
 * ⇒ 这里给一份运行时清单；`featureCapabilities.spec.ts` 用它把
 *   「nav 用的名字 ⊆ 后端发的字段」钉住。
 *
 * ★ 逐字相等于后端 `FeatureCapabilities` 的 JSON 字段名（见
 *   `admin/capabilities.go`）。
 */
export const CAPABILITY_NAMES = ['cost_reconciliation'] as const

/** 能力位名（取值域 = `CAPABILITY_NAMES`）。 */
export type CapabilityName = (typeof CAPABILITY_NAMES)[number]

/**
 * ★ 类型级对账（由 `vue-tsc` 检查，不是运行时断言）：
 *   清单里的每个名字都**必须**是 `FeatureCapabilities` 的键
 *   ⇒ ⇒ 往清单里加名字却忘了加后端字段 ⇒ **编译期**就红。
 */
export const CAPABILITY_NAME_TO_FIELD: Record<CapabilityName, keyof FeatureCapabilities> = {
  cost_reconciliation: 'cost_reconciliation',
}

/**
 * 取能力位。
 *
 * ★ 失败（网络错 / 401 / 403）⇒ **抛错**，由调用方决定降级口径
 *   （store 层按「能力位全关」fail-closed 处理：宁可少显示一席，
 *   也不要显示一个「点了必然失败」的入口）。
 */
export async function fetchCapabilities(options?: RequestOptions): Promise<FeatureCapabilities> {
  return req<FeatureCapabilities>('GET', '/api/admin/capabilities', undefined, options)
}
