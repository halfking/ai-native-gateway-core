import { ref } from 'vue'
import { getQualitySummary, type QualitySummaryItem } from '../api/quality'

/** Sort keys for /providers list quality columns. */
export type QualitySortKey =
  | 'default'
  | 'usage'
  | 'quality_score'
  | 'availability_score'
  | 'performance_score'

export type ProviderWithQuality<T extends { id: number }> = T & {
  quality?: QualitySummaryItem | null
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

  function sortProvidersByQuality<T extends { id: number }>(
    providers: ProviderWithQuality<T>[],
    sortKey: QualitySortKey,
  ): ProviderWithQuality<T>[] {
    if (sortKey === 'default') return providers

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

  return {
    qualityByProviderId,
    qualityLoading,
    qualityError,
    loadQualitySummary,
    enrichProviders,
    sortProvidersByQuality,
  }
}
