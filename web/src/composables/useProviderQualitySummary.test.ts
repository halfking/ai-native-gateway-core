import { describe, expect, it } from 'vitest'
import {
  providerDisplayName,
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

describe('providerDisplayName', () => {
  it('falls back display_name → vendor_name → catalog_code → empty', () => {
    expect(providerDisplayName({ display_name: '阿里云' })).toBe('阿里云')
    expect(providerDisplayName({ display_name: '   ', vendor_name: '阿里云' })).toBe('阿里云')
    expect(providerDisplayName({ catalog_code: 'aliyun' })).toBe('aliyun')
    expect(providerDisplayName({})).toBe('')
  })
})

describe('sortProvidersByQuality: name（老板点名要的查找性排序）', () => {
  const named: ProviderWithQuality<{ id: number; display_name: string }>[] = [
    { id: 3, display_name: 'OpenAI' },
    { id: 1, display_name: '阿里云' },
    { id: 2, display_name: 'anthropic' },
  ]

  it('按名称排序，且与码点序不同 —— 证明用的是 localeCompare 而不是 < / >', () => {
    const sorted = sortProvidersByQuality(named.map((n) => ({ ...n, quality: null })), 'name')
    const got = sorted.map((r) => r.display_name)

    // 性质 1：与独立算出的 localeCompare 序一致。
    const expected = [...named].sort((a, b) =>
      a.display_name.localeCompare(b.display_name, undefined, { numeric: true, sensitivity: 'base' }),
    ).map((r) => r.display_name)
    expect(got).toEqual(expected)

    // 性质 2（鉴别力）：朴素码点序必须与它不同，否则这条判据恒绿没有意义。
    // 小写 anthropic 与大写 OpenAI 保证 en-US 下也能区分码点序；
    // 不能假设所有宿主默认中文区域，靠「阿里云」排首位区分。
    const codePointOrder = [...named].sort((a, b) => (a.display_name < b.display_name ? -1 : 1)).map((r) => r.display_name)
    expect(codePointOrder).not.toEqual(expected)
  })

  it('三行全部参与排序（不因缺质量数据沉底）', () => {
    const mixed: ProviderWithQuality<{ id: number; display_name: string }>[] = [
      { id: 1, display_name: 'Zeta' },
      { id: 2, display_name: 'Alpha', quality: null },
      { id: 3, display_name: 'Mu' },
    ]
    const got = sortProvidersByQuality(mixed, 'name').map((r) => r.display_name)
    expect(got).toHaveLength(3)
    expect(got).toEqual(['Alpha', 'Mu', 'Zeta'])
  })

  it('同名时用 id 兜底，保证顺序稳定（不随输入顺序抖动）', () => {
    const dup: ProviderWithQuality<{ id: number; display_name: string }>[] = [
      { id: 9, display_name: '同一家' },
      { id: 4, display_name: '同一家' },
    ]
    expect(sortProvidersByQuality(dup, 'name').map((r) => r.id)).toEqual([4, 9])
    expect(sortProvidersByQuality([...dup].reverse(), 'name').map((r) => r.id)).toEqual([4, 9])
  })

  it('空名称沉底，不占住列表最前面', () => {
    const withEmpty: ProviderWithQuality<{ id: number; display_name?: string }>[] = [
      { id: 1, display_name: '' },
      { id: 2, display_name: 'Bing' },
    ]
    expect(sortProvidersByQuality(withEmpty, 'name').map((r) => r.id)).toEqual([2, 1])
  })
})
