import type { CenterInstance, RegionStats } from '../../api/ops'
// 审计 R3#10：相对时间实现收敛至 utils/datetime.ts，本模块保留同名导出
// 作为既有调用点/测试的兼容门面。
import { formatRelativeTime as formatRelativeTimeShared } from '../../utils/datetime'

export interface RegionBucket {
  region: string
  stats: RegionStats
  nodes: CenterInstance[]
}

/** Prefer expected region order; append any extra regions alphabetically. */
export function buildRegionBuckets(
  regionStats: RegionStats[],
  nodes: CenterInstance[],
  expectedOrder: string[] = ['local', '245', '154'],
): RegionBucket[] {
  const byRegion = new Map<string, CenterInstance[]>()
  for (const node of nodes) {
    const key = (node.region || 'unknown').trim() || 'unknown'
    const list = byRegion.get(key) ?? []
    list.push(node)
    byRegion.set(key, list)
  }

  const statsMap = new Map(regionStats.map((r) => [r.region, r]))
  const seen = new Set<string>()
  const buckets: RegionBucket[] = []

  const push = (region: string) => {
    if (seen.has(region)) return
    seen.add(region)
    const stats = statsMap.get(region) ?? {
      region,
      total_instances: byRegion.get(region)?.length ?? 0,
      online_instances: (byRegion.get(region) ?? []).filter((n) => n.status === 'online').length,
      offline_instances: (byRegion.get(region) ?? []).filter((n) => n.status === 'offline').length,
      degraded_instances: (byRegion.get(region) ?? []).filter((n) => n.status === 'degraded').length,
      missing: !(byRegion.get(region)?.length),
    }
    buckets.push({
      region,
      stats,
      nodes: byRegion.get(region) ?? [],
    })
  }

  for (const region of expectedOrder) push(region)
  for (const region of [...statsMap.keys()].sort()) push(region)
  for (const region of [...byRegion.keys()].sort()) push(region)

  return buckets
}

/** Relative time; clamps absurd/invalid timestamps to empty. */
export function formatRelativeTime(
  iso: string | undefined,
  labels: {
    justNow: string
    minutesAgo: (n: number) => string
    hoursAgo: (n: number) => string
    daysAgo: (n: number) => string
  },
): string {
  return formatRelativeTimeShared(iso, labels)
}

export function successRate(ok: number, total: number): number | null {
  if (!total || total < 0) return null
  return Math.round((1000 * Math.max(0, ok)) / total) / 10
}

export function memPressurePct(usedMb: number, totalMb: number): number | null {
  if (!totalMb || totalMb <= 0) return null
  return Math.min(100, Math.round((100 * usedMb) / totalMb))
}
