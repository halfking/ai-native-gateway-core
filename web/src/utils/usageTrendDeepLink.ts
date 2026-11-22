// usageTrendDeepLink.ts —— 深链「钉住维度」纯函数（2026-11-23 深链钉住轮）
//
// ★ 需求（本轮 owner 原话）：在会话分析「Top5 供应商/模型」里点一行跳到
//   /admin/usage-trends 时，**页面里的条件应当自动选成那一项**（整个页面的数据
//   都是它的），**并且把对应维度的列表收起来**（不要在下拉框里摆一个"全部"，
//   那会让用户以为筛选没生效）。
//
// ★ 为什么要有"钉住"这个概念（而不是"深链带了就生效"）：
//   深链参数在 URL 里 ⇒ 说明这个维度是**入口上下文**（用户是从那个供应商/模型的
//   排行跳过来的）⇒ 页面该把它当既成事实展示，而不是当成一个可随手改的筛选条件。
//   ⇒ 于是：**参数存在 = 该维度被钉住** ⇒ 选择器隐藏 + 只读徽标 + 一个解除按钮。
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
// ★ 这里只放**纯函数**：页面组件（Vue/jsdom）测不动 echarts 与路由，
//   把口径抽出来才能被单测钉住。

/** 可被深链钉住的维度。 */
export type PinnedDimension = 'provider' | 'tenant' | 'apikey' | 'model'

/** 深链 query 的最小形状（vue-router 的 query 兼容它）。 */
export type DeepLinkQuery = Record<string, unknown>

/** 各维度是否被深链钉住（参数存在即钉住）。 */
export type PinnedMap = Record<PinnedDimension, boolean>

const PINNED_DIMENSIONS: PinnedDimension[] = ['provider', 'tenant', 'apikey', 'model']

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
    out[dim] = firstQueryString(query, dim) !== ''
  }
  return out
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
