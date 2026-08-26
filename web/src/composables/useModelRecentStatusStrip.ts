/**
 * useModelRecentStatusStrip — 从按模型泳道 snapshot 取最近 N 条 tile，
 * 供「按模型分组的可用节点」标题行右侧色块条使用。
 *
 * 数据与 useSwimLane(groupBy=model) 同源：snapshot.dimensions.model。
 */
import type { LiveStreamLane, LiveStreamTile } from './liveStreamStore'

export const RECENT_STATUS_STRIP_LIMIT = 50

export interface ModelGroupStripKey {
  model: string
  displayName?: string
  aliases?: string[]
}

function normalizeKey(value: string | undefined | null): string {
  return (value || '').trim().toLowerCase()
}

function groupKeys(group: ModelGroupStripKey): Set<string> {
  const keys = new Set<string>()
  const add = (v: string | undefined | null) => {
    const k = normalizeKey(v)
    if (k) keys.add(k)
  }
  add(group.model)
  add(group.displayName)
  for (const alias of group.aliases || []) add(alias)
  return keys
}

function laneMatches(lane: LiveStreamLane, keys: Set<string>): boolean {
  if (keys.size === 0) return false
  return keys.has(normalizeKey(lane.id)) || keys.has(normalizeKey(lane.name))
}

/**
 * 合并匹配到的 model lane 的 requests，按 timestamp 升序后取尾部 limit 条。
 * 同 request_id 只保留最后一次出现（delta 刷新可能重复）。
 */
export function recentTilesForGroup(
  group: ModelGroupStripKey,
  lanes: LiveStreamLane[] | null | undefined,
  limit: number = RECENT_STATUS_STRIP_LIMIT,
): LiveStreamTile[] {
  if (!lanes?.length || limit <= 0) return []
  const keys = groupKeys(group)
  if (keys.size === 0) return []

  const matched = lanes.filter(lane => laneMatches(lane, keys))
  if (matched.length === 0) return []

  const byId = new Map<string, LiveStreamTile>()
  const order: string[] = []
  for (const lane of matched) {
    for (const tile of lane.requests || []) {
      const id = tile.request_id || ''
      const key = id || `${tile.timestamp}|${tile.model}|${tile.status}`
      if (!byId.has(key)) order.push(key)
      byId.set(key, tile)
    }
  }

  const merged = order.map(k => byId.get(k)!).sort((a, b) => {
    const ta = a.timestamp || ''
    const tb = b.timestamp || ''
    if (ta !== tb) return ta < tb ? -1 : 1
    return (a.request_id || '').localeCompare(b.request_id || '')
  })

  return merged.length <= limit ? merged : merged.slice(-limit)
}
