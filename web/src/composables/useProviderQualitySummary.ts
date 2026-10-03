import { ref } from 'vue'
import { getQualitySummary, type QualitySummaryItem } from '../api/quality'

/** Sort keys for /providers list quality columns. */
export type QualitySortKey =
  | 'default'
  | 'name'
  | 'usage'
  | 'quality_score'
  | 'availability_score'
  | 'performance_score'

export type ProviderWithQuality<T extends { id: number }> = T & {
  quality?: QualitySummaryItem | null
}

/**
 * 2026-10-03：供应商列表的「按名称排序」（老板点名要的查找性排序）。
 * 名称取 display_name → vendor_name → catalog_code 依次回退，任何一行都能落到
 * 一个非空字符串上；用 localeCompare 而不是 < / >，否则中文名会按码点排，
 * 看起来就是乱的（"阿里云" 排在 "OpenAI" 后面这类）。
 */
/** 名称字段：display_name → vendor_name → catalog_code 依次回退。 */
export type ProviderNameFields = {
  display_name?: string | null
  vendor_name?: string | null
  catalog_code?: string | null
}

export function providerDisplayName(p: ProviderNameFields): string {
  return p.display_name?.trim() || p.vendor_name?.trim() || p.catalog_code?.trim() || ''
}

export function sortProvidersByQuality<T extends { id: number } & ProviderNameFields>(
  providers: ProviderWithQuality<T>[],
  sortKey: QualitySortKey,
): ProviderWithQuality<T>[] {
  if (sortKey === 'default') return providers

  // 按名称：与质量数据无关，不存在「缺数据沉底」，纯字典序。
  if (sortKey === 'name') {
    return [...providers].sort((a, b) => {
      const an = providerDisplayName(a)
      const bn = providerDisplayName(b)
      // 空名称沉底：查找性排序里一行无名供应商不该占住字母表最前面。
      // （空串是任何串的前缀，直接 localeCompare 会让它排到第一。）
      if (!an && bn) return 1
      if (an && !bn) return -1
      const byName = an.localeCompare(bn, undefined, { numeric: true, sensitivity: 'base' })
      // 同名时用 id 兜底，保证排序稳定（否则每次刷新行序会跳）。
      return byName !== 0 ? byName : a.id - b.id
    })
  }

  const metric = (q: QualitySummaryItem | null | undefined): number | null => {
    if (!q) return null
    switch (sortKey) {
      case 'usage':
        return q.total_requests_24h
      case 'quality_score':
        return q.quality_score
      case 'availability_score':
        return q.availability_score
      case 'performance_score':
        return q.performance_score
      default:
        return null
    }
  }

  return [...providers].sort((a, b) => {
    const av = metric(a.quality)
    const bv = metric(b.quality)
    // No quality data sinks to the bottom.
    if (av == null && bv == null) return 0
    if (av == null) return 1
    if (bv == null) return -1
    return bv - av
  })
}

export function useProviderQualitySummary() {
  const qualityByProviderId = ref<Map<number, QualitySummaryItem>>(new Map())
  const qualityLoading = ref(false)
  const qualityError = ref('')

  async function loadQualitySummary() {
    qualityLoading.value = true
    qualityError.value = ''
    try {
      const rows = await getQualitySummary()
      const map = new Map<number, QualitySummaryItem>()
      for (const row of rows) {
        map.set(row.provider_id, row)
      }
      qualityByProviderId.value = map
    } catch (e: unknown) {
      qualityError.value = e instanceof Error ? e.message : 'quality summary failed'
      qualityByProviderId.value = new Map()
    } finally {
      qualityLoading.value = false
    }
  }

  function enrichProviders<T extends { id: number }>(providers: T[]): ProviderWithQuality<T>[] {
    return providers.map((p) => ({
      ...p,
      quality: qualityByProviderId.value.get(p.id) ?? null,
    }))
  }

  return {
    qualityByProviderId,
    qualityLoading,
    qualityError,
    loadQualitySummary,
    enrichProviders,
    sortProvidersByQuality,
  }
}
