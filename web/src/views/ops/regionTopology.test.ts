import { describe, expect, it } from 'vitest'
import {
  buildRegionBuckets,
  formatRelativeTime,
  memPressurePct,
  successRate,
} from './regionTopology'
import type { CenterInstance, RegionStats } from '../../api/ops'

const node = (partial: Partial<CenterInstance> & Pick<CenterInstance, 'instance_id' | 'hostname'>): CenterInstance => ({
  version: '1.0.0',
  status: 'online',
  build_seq: 1,
  started_at: '2026-07-19T00:00:00Z',
  last_heartbeat: '2026-07-19T12:00:00Z',
  region: '245',
  ...partial,
})

describe('buildRegionBuckets', () => {
  it('keeps expected region order and groups nodes', () => {
    const stats: RegionStats[] = [
      { region: '154', total_instances: 1, online_instances: 1, offline_instances: 0, degraded_instances: 0 },
      { region: '245', total_instances: 1, online_instances: 1, offline_instances: 0, degraded_instances: 0 },
      { region: 'local', total_instances: 0, online_instances: 0, offline_instances: 0, degraded_instances: 0, missing: true },
    ]
    const nodes = [
      node({ instance_id: 'a', hostname: 'h-154', region: '154' }),
      node({ instance_id: 'b', hostname: 'h-245', region: '245' }),
    ]
    const buckets = buildRegionBuckets(stats, nodes)
    expect(buckets.map((b) => b.region)).toEqual(['local', '245', '154'])
    expect(buckets[0].nodes).toHaveLength(0)
    expect(buckets[1].nodes.map((n) => n.instance_id)).toEqual(['b'])
    expect(buckets[2].nodes.map((n) => n.instance_id)).toEqual(['a'])
  })

  it('appends unexpected regions after expected ones', () => {
    const buckets = buildRegionBuckets(
      [{ region: '71', total_instances: 1, online_instances: 1, offline_instances: 0, degraded_instances: 0 }],
      [node({ instance_id: 'c', hostname: 'h-71', region: '71' })],
    )
    expect(buckets.map((b) => b.region)).toEqual(['local', '245', '154', '71'])
    expect(buckets[3].nodes).toHaveLength(1)
  })
})

describe('formatRelativeTime', () => {
  const labels = {
    justNow: 'just now',
    minutesAgo: (n: number) => `${n}m`,
    hoursAgo: (n: number) => `${n}h`,
    daysAgo: (n: number) => `${n}d`,
  }

  it('returns empty for absurd timestamps', () => {
    expect(formatRelativeTime('1970-01-01T00:00:00Z', labels)).toBe('')
    expect(formatRelativeTime('not-a-date', labels)).toBe('')
    expect(formatRelativeTime(undefined, labels)).toBe('')
  })

  it('formats recent times', () => {
    const iso = new Date(Date.now() - 5 * 60_000).toISOString()
    expect(formatRelativeTime(iso, labels)).toBe('5m')
  })
})

describe('successRate / memPressurePct', () => {
  it('computes success rate', () => {
    expect(successRate(95, 100)).toBe(95)
    expect(successRate(1, 3)).toBe(33.3)
    expect(successRate(0, 0)).toBeNull()
  })

  it('computes memory pressure', () => {
    expect(memPressurePct(512, 1024)).toBe(50)
    expect(memPressurePct(10, 0)).toBeNull()
  })
})
