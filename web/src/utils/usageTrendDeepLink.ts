// usageTrendDeepLink.ts —— 深链「钉住维度」纯函数（2026-11-23 深链钉住轮）
//
// ★ 需求（本轮 owner 原话）：在会话分析「Top5 供应商/模型」里点一行跳到
//   /admin/usage-trends 时，**页面里的条件应当自动选成那一项**（整个页面的数据
//   都是它的），**并且把对应维度的列表收起来**（不要在下拉框里摆一个"全部"，
//   那会让用户以为筛选没生效）。
//
// ★ 为什么要要有"钉住"这个概念（而不是"深链带了就生效"）：
//   深链参数在 URL 里 ⇒ 说明这个维度是**入口上下文**（用户是从那个供应商/模型的
//   排行跳过来的）⇒ 页面该把它当既成事实展示，而不是当成一个可随手改的筛选条件。
//   ⇒ 于是：**参数存在 = 该维度被钉住** ⇒ 选择器自动选中 + 只读徽标 + 一个解除按钮。
//   解除 = 清掉那个参数 ⇒ URL 里参数消失 ⇒ 选择器重新出现（状态自洽，无需额外标记位）。
//
// ★ 供应商参数的两种形态：
//   · 数字 id：`?provider=14`（页面内部的 provider_id 口径）
//   · **code**：`?provider=apiclaude`（会话分析「Top5 供应商」行传的就是
//     `providers.code`）⇒ 页面必须先把它解析成 id 才能过滤。
//   ⚠ `providers.code` 与 `catalog_code` **不是同一列**：自建供应商常把
//     catalog_code 留空、display_name 另起名字（211 实测：code=速云U站 /
//     catalog_code='' / display_name=suyun）⇒ 解析要三列都试。
//
// ★★ 2026-11-23 用量趋势深链轮（扩展）：维度从 4 个扩到 **9 个**。
//
//	实测取证（211 现役，浏览器实点）：
//	  ① `?model=claude-opus-5-5` ⇒ 条件已自动选中 ✔，但**「Top 5 模型」卡片没收起**
//	     ⇒ 顶着「Top 5」标题只有 1 行（实测 13.5K）= **多余的数据**；
//	  ② `?end_user_id=anonymous` ⇒ 页面**根本不认这个条件**（条件区全是「全部」、
//	     Top 10 用户仍是全站 3 个用户）⇒ ★ URL 承诺的条件与页面呈现的数据
//	     **不一致**（"界面显示的 ≠ 数据反映的"）。
//	⇒ 于是本轮把「排行六维」全部纳入本页的深链词汇，并让**每个维度都对应一张
//	  排行卡片** ⇒ 被钉住的维度 ⇒ 那张卡片**收起**（它必然退化成一��� = 多余数据）。
//
// ★ 这里只放**纯函数**：页面组件（Vue/jsdom）测不动 echarts 与路由，
//   把口径抽出来才能被单测钉住。

/**
 * 可被深链钉住的维度。
 *
 * ★ 前四个是**页面自有的条件**（有对应的下拉/选择器 ⇒ 条件区里自动选中该项）。
 * ★ 后五个是**排行六维里页面原本不认识的**（客户端 / 终端用户 / 项目 / 任务 /
 *   API Key 前缀）—— 它们在页面上**没有选择器** ⇒ 条件区用**只读徽标 chip**
 *   呈现（值 + ✕ 解除）。
 */
export type PinnedDimension =
  | 'provider'
  | 'tenant'
  | 'apikey'
  | 'model'
  | 'client'
  | 'user'
  | 'project'
  | 'task'
  | 'apikeyPrefix'

/** 深链 query 的最小形状（vue-router 的 query 兼容它）。 */
export type DeepLinkQuery = Record<string, unknown>

/** 各维度是否被深链钉住（参数存在即钉住）。 */
export type PinnedMap = Record<PinnedDimension, boolean>

/** 六张排行卡片（每张对应一个排行维度）。 */
export type RankingArm = 'models' | 'clients' | 'users' | 'apiKeys' | 'projects' | 'tasks'

/** 条件区里该维度的控件形态。 */
export type DimensionControl = 'picker' | 'chip'

export interface DimensionSpec {
  /** URL query 参数名（★ 与请求日志页逐字同名 ⇒ 两页可互贴链接）。 */
  param: string
  /** 该维度对应的排行卡片；null = 没有对应卡片（供应商/租户没有）。 */
  arm: RankingArm | null
  /** i18n 标签键。 */
  labelKey: string
  /** 条件区控件：picker = 有选择器（自动选中该项）；chip = 只读徽标。 */
  control: DimensionControl
}

/**
 * ★ 维度表：九个维度的**唯一真相**（参数名 / 排行臂 / 标签 / 控件形态）。
 *
 * ★ 排行臂那一列就是「被钉住时要收起哪张卡片」的依据：
 *   model ⇒ Top 5 模型；client ⇒ Top 5 客户端；user ⇒ Top 10 用户；
 *   apikey / apikeyPrefix ⇒ Top 5 API Keys；project ⇒ Top 5 项目；task ⇒ Top 5 任务。
 *   provider / tenant ⇒ **没有对应卡片**（页面不按这两维排行）⇒ 不收起任何卡片。
 */
export const DIMENSION_SPECS: Record<PinnedDimension, DimensionSpec> = {
  provider: { param: 'provider', arm: null, labelKey: 'usageTrend.filterProvider', control: 'picker' },
  tenant: { param: 'tenant', arm: null, labelKey: 'usageTrend.filterTenant', control: 'picker' },
  apikey: { param: 'apikey', arm: 'apiKeys', labelKey: 'usageTrend.filterApiKey', control: 'picker' },
  model: { param: 'model', arm: 'models', labelKey: 'usageTrend.filterModel', control: 'picker' },
  client: { param: 'client_model', arm: 'clients', labelKey: 'requests.list.filter.deepLink.clientModel', control: 'chip' },
  user: { param: 'end_user_id', arm: 'users', labelKey: 'requests.list.filter.deepLink.endUser', control: 'chip' },
  project: { param: 'application_code', arm: 'projects', labelKey: 'requests.list.filter.deepLink.application', control: 'chip' },
  task: { param: 'task_type', arm: 'tasks', labelKey: 'requests.list.filter.deepLink.task', control: 'chip' },
  apikeyPrefix: { param: 'api_key_prefix', arm: 'apiKeys', labelKey: 'requests.list.filter.deepLink.apiKey', control: 'chip' },
}

/** ★ 只有 chip 形态的维度（页面上没有选择器 ⇒ 只能靠只读徽标 + ✕ 解除）。 */
export const CHIP_DIMENSIONS: PinnedDimension[] = (Object.keys(DIMENSION_SPECS) as PinnedDimension[])
  .filter((d) => DIMENSION_SPECS[d].control === 'chip')

const PINNED_DIMENSIONS: PinnedDimension[] = Object.keys(DIMENSION_SPECS) as PinnedDimension[]

/** 取 query 参数里的第一个非空字符串（vue-router 允许同键多值）。 */
export function firstQueryString(query: DeepLinkQuery, key: string): string {
  const raw = query[key]
  if (Array.isArray(raw)) {
    for (const item of raw) {
      if (typeof item === 'string' && item.trim()) return item.trim()
    }
    return ''
  }
  return typeof raw === 'string' ? raw.trim() : ''
}

/**
 * 读出被深链钉住的维度。
 *
 * ★ model 允许多值（`model=a&model=b`）⇒ 只要**有值**就算钉住。
 */
export function readPinnedDimensions(query: DeepLinkQuery): PinnedMap {
  const out = {} as PinnedMap
  for (const dim of PINNED_DIMENSIONS) {
    out[dim] = firstQueryString(query, DIMENSION_SPECS[dim].param) !== ''
  }
  return out
}

/** 取某个维度在深链里的值（chip 徽标要显示它）。 */
export function readDeepLinkValue(query: DeepLinkQuery, dim: PinnedDimension): string {
  return firstQueryString(query, DIMENSION_SPECS[dim].param)
}

/**
 * ★ 被钉住 ⇒ 要**收起**的排行卡片。
 *
 * ★ 为什么收起来：那张卡片排的**就是这个维度** ⇒ 维度被钉住后它必然只剩一行
 *   （实测：`?model=claude-opus-5-5` ⇒「Top 5 模型」只有 1 行 13.5K）
 *   ⇒ 顶着「Top 5」标题摆 1 行 = **多余的数据** ⇒ 收起来。
 *
 * ★ 其余卡片**保留**：它们是当前条件的**下钻拆解**（这个模型都被钉住了，
 *   那「Top 5 客户端 / Top 10 用户 / Top 5 项目…」正是有价值的细分）。
 *
 * ★ provider / tenant 没有对应卡片 ⇒ 不收起任何卡片。
 */
export function hiddenRankingArms(pinned: PinnedMap): RankingArm[] {
  const arms: RankingArm[] = []
  for (const dim of PINNED_DIMENSIONS) {
    if (!pinned[dim]) continue
    const arm = DIMENSION_SPECS[dim].arm
    if (arm && !arms.includes(arm)) arms.push(arm)
  }
  return arms
}

/**
 * 供应商深链参数是不是"数字 id"形态。
 *
 * 只有**纯数字**才算 id（`Number.isInteger` + > 0）；其余一律当 code 处理。
 */
export function isProviderIdParam(raw: string): boolean {
  if (!/^\d+$/.test(raw)) return false
  const n = Number(raw)
  return Number.isInteger(n) && n > 0
}

/** 解析供应商 code 所需的最小 provider 形状。 */
export interface ProviderLike {
  id: number
  code?: string | null
  catalog_code?: string | null
  display_name?: string | null
}

/**
 * 用供应商列表把 **code** 解析成 id。
 *
 * ★ 匹配 code / catalog_code / display_name 三列：会话分析排行给的是
 *   `providers.code`（providers.go 的 MV 口径），而页面上别的深链可能给
 *   catalog_code 或展示名 ⇒ 三者任一命中即算解析成功。
 * ★ 解析不到就返回 null —— 调用方**必须显式披露**（"未找到，筛选未生效"），
 *   不能静默退化成"全量数据"。
 */
export function resolveProviderId(providers: ProviderLike[], code: string): number | null {
  const target = code.trim()
  if (!target) return null
  const hit = providers.find((p) =>
    (p.code && p.code === target) ||
    (p.catalog_code && p.catalog_code === target) ||
    (p.display_name && p.display_name === target),
  )
  return hit ? hit.id : null
}

/** 供应商徽标上的展示名：优先原样显示深链给的 code，其次展示名/catalog_code/code。 */
export function providerDisplayLabel(
  providers: ProviderLike[],
  pinnedCode: string,
  providerId: number | null,
): string {
  if (pinnedCode) return pinnedCode
  if (providerId == null) return ''
  const hit = providers.find((p) => p.id === providerId)
  if (!hit) return `#${providerId}`
  return hit.display_name || hit.catalog_code || hit.code || `#${hit.id}`
}
