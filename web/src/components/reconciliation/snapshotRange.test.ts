import { describe, expect, it } from 'vitest'
import { defaultSnapshotRange, snapshotDatePresets } from './snapshotRange'

const now = Date.parse('2026-09-30T15:00:00Z')

describe('snapshotDatePresets', () => {
  it('ends on yesterday UTC and does not offer today', () => {
    const presets = snapshotDatePresets(now)
    expect(presets.map((item) => item.id)).toEqual([
      'yesterday', 'last7d', 'last30d', 'thisMonth', 'lastMonth',
    ])
    expect(presets.find((item) => item.id === 'yesterday')?.resolve()).toEqual({
      start: '2026-09-29',
      end: '2026-09-29',
    })
    expect(presets.find((item) => item.id === 'last7d')?.resolve()).toEqual({
      start: '2026-09-23',
      end: '2026-09-29',
    })
    expect(defaultSnapshotRange(now)).toEqual(['2026-09-23', '2026-09-29'])
  })

  it('keeps this month inside yesterday when today is the first UTC day', () => {
    const first = Date.parse('2026-10-01T00:30:00Z')
    const presets = snapshotDatePresets(first)
    expect(presets.find((item) => item.id === 'thisMonth')?.resolve()).toEqual({
      start: '2026-09-01',
      end: '2026-09-30',
    })
    expect(presets.find((item) => item.id === 'lastMonth')?.resolve()).toEqual({
      start: '2026-08-01',
      end: '2026-08-31',
    })
  })
})
