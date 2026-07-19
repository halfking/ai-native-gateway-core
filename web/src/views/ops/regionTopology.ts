import type { CenterInstance, RegionStats } from '../../api/ops'

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
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const diff = Date.now() - d.getTime()
  // Guard against epoch / far-future / multi-year garbage (e.g. "739816d ago")
  if (diff < -60_000 || diff > 366 * 24 * 3600_000) return ''
  if (diff < 60_000) return labels.justNow
  const mins = Math.round(diff / 60_000)
  if (mins < 60) return labels.minutesAgo(mins)
  const hours = Math.round(mins / 60)
  if (hours < 24) return labels.hoursAgo(hours)
  return labels.daysAgo(Math.round(hours / 24))
}

export function successRate(ok: number, total: number): number | null {
  if (!total || total < 0) return null
  return Math.round((1000 * Math.max(0, ok)) / total) / 10
}

export function memPressurePct(usedMb: number, totalMb: number): number | null {
  if (!totalMb || totalMb <= 0) return null
  return Math.min(100, Math.round((100 * usedMb) / totalMb))
}
