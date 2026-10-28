// availableModels —— 把 /api/routing/available-models 的响应投影成「模型条目」列表。
//
// 2026-10-28：移动端此前直接把 `popular`（Go popularModelEntry **对象数组**）
// 当 string[] 消费 ⇒ 界面整列渲染成 JSON 字样（用户报的「路由检查选模型是一列
// JSON」）。桌面端早有 `web/src/utils/availableModels.ts` 的
// `projectAvailableModels` 作为正确实现，本文件是它的移动端同形件：
// **一个模型只出现一次、显示友好名、选中时产出 canonical_name**。
import type {
  AvailableModelsResponse,
  AvailableVersion,
  ModelFamily,
  PopularModel,
} from '@/api/models'

/** 展示名：后端可能给空串 ⇒ 回落 canonical_name。 */
export function modelDisplayName(model: PopularModel): string {
  return model.display_name?.trim() || model.canonical_name
}

/**
 * 常用模型（popular）条目 —— 归一成「一项一个模型」的形状。
 *
 * ★ 后端契约是对象数组 `{canonical_name, display_name, source}`；
 * 这里同时兼容 string（纯防御），但**主路径是对象**。
 */
export function projectPopularModels(data: AvailableModelsResponse): PopularModel[] {
  const byCanonical = new Map<string, PopularModel>()
  for (const entry of data.popular ?? []) {
    // 旧形状（string）兼容：包成对象再走同一条路。
    const model: PopularModel = typeof entry === 'string'
      ? { canonical_name: entry, display_name: entry, source: 'unknown' }
      : entry
    const key = (model.canonical_name ?? '').trim().toLowerCase()
    if (!key || byCanonical.has(key)) continue
    byCanonical.set(key, model)
  }
  return Array.from(byCanonical.values())
}

/** popular 里已出现的 canonical（去重用，大小写不敏感）。 */
export function popularKeys(models: PopularModel[]): Set<string> {
  return new Set(models.map((m) => m.canonical_name.trim().toLowerCase()))
}

/**
 * 家族分组 —— 过滤掉**已经在常用模型里出现过**的模型。
 *
 * 与桌面 `projectAvailableModels` 的合并语义一致：一个模型只出现一次。
 * 不去重的话，同一个模型会在「常用模型」和它所属的家族里各出现一次，
 * 用户看到的是两条内容相同的条目。
 */
export function projectFamilies(
  data: AvailableModelsResponse,
  exclude: Set<string> = new Set(),
): ModelFamily[] {
  return (data.families ?? []).map((family) => ({
    ...family,
    versions: (family.versions ?? []).filter((version) => {
      const key = (version.canonical_name ?? '').trim().toLowerCase()
      if (!key) return false
      return !exclude.has(key)
    }),
  })).filter((family) => family.versions.length > 0)
}

/** 家族分组的展示名（与 popular同一口径：空 display_name 回落 canonical）。 */
export function versionDisplayName(version: AvailableVersion): string {
  return version.display_name?.trim() || version.canonical_name
}
