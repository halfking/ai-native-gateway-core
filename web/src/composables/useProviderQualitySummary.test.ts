import { describe, expect, it } from 'vitest'
import {
  sortProvidersByQuality,
  type ProviderWithQuality,
  type QualitySortKey,
} from './useProviderQualitySummary'
import type { QualitySummaryItem } from '../api/quality'

function item(partial: Partial<QualitySummaryItem> & { provider_id: number }): QualitySummaryItem {
  return {
    provider_name: `p${partial.provider_id}`,
    best_model_name: null,
    quality_score: 0,
    quality_grade: 'D',
    availability_score: 0,
    performance_score: 0,
    total_requests_24h: 0,
    calculated_at: '2026-07-20T00:00:00Z',
    ...partial,
  }
}

describe('sortProvidersByQuality', () => {
  const rows: ProviderWithQuality<{ id: number }>[] = [
    { id: 1, quality: item({ provider_id: 1, quality_score: 70, total_requests_24h: 100 }) },
    { id: 2, quality: null },
    { id: 3, quality: item({ provider_id: 3, quality_score: 95, total_requests_24h: 10 }) },
  ]

  it('keeps default order', () => {
    const sorted = sortProvidersByQuality(rows, 'default')
    expect(sorted.map((r) => r.id)).toEqual([1, 2, 3])
  })

  it('sorts by quality_score descending and sinks missing data', () => {
    const sorted = sortProvidersByQuality(rows, 'quality_score')
    expect(sorted.map((r) => r.id)).toEqual([3, 1, 2])
  })

  it('sorts by usage descending', () => {
    const sorted = sortProvidersByQuality(rows, 'usage' as QualitySortKey)
    expect(sorted.map((r) => r.id)).toEqual([1, 3, 2])
  })
})
