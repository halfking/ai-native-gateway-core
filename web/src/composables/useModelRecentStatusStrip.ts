/**
 * useModelRecentStatusStrip — 从按模型泳道 snapshot 取最近 N 条 tile，
 * 供「按模型分组的可用节点」标题行右侧的输入/输出双队列条使用。
 *
 * 数据与 useSwimLane(groupBy=model) 同源：snapshot.dimensions.model。
 *
 * 2026-09-01: 泳道 tile 语义 = 每客户端请求一条，终态只写一次
 * （后端 RequestLogContext.SetTerminal CAS），重试发生在请求内部。
 * 因此：
 *   - 终态 tile（success/failure/rate_limited）= 客户端最终结果 →「输出」
 *   - in_progress tile = 当前在途请求          →「输入」（当前请求队列）
 * 上游每次尝试的质量仍由节点卡片的 credential 滑窗（✓/✗）呈现，不受影响。
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

/** 合并匹配 lane 的 tiles：同 request_id 只保留最后一次出现（last write
 *  wins，delta 重放可能重复），按 timestamp 升序，取尾部 limit 条。
 * 拆分（输入/输出）必须发生在合并去重之后，否则同一请求的
 * in_progress→success 更新会同时出现在两条队列里。 */
function mergedTilesForGroup(
  group: ModelGroupStripKey,
  lanes: LiveStreamLane[] | null | undefined,
  limit: number,
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

/** 终态集合：客户端已经拿到最终结果的请求（含 rate_limited——客户端
 *  实际收到了 429，属于输出侧可见结果）。idle/未知状态不属于任何一侧。 */
const TERMINAL_STATUSES = new Set(['success', 'failure', 'rate_limited'])

/** 探针是合成流量，不是客户端请求，两侧都排除。 */
function isClientTile(tile: LiveStreamTile): boolean {
  return !tile.is_probe
}

export interface ModelGroupRecentIO {
  /** 输入：当前在途请求（in_progress）= 该模型的当前请求队列。 */
  inflight: LiveStreamTile[]
  /** 输出：客户端最终结果（success / failure / rate_limited）。 */
  terminal: LiveStreamTile[]
  /** 输出侧成功数。 */
  success: number
  /** 输出侧失败数（failure + rate_limited，均为客户端可见的非成功结果）。 */
  failed: number
  /** 输出成功率 = success / terminal.length；terminal 为空时为 null
   *  （数据未上报语义，禁止渲染成 0% / 100% 冒充）。 */
  successRate: number | null
}

/**
 * 输入/输出双队列视图：合并去重后按终态/在途拆分。
 * 上游有错但重试成功的请求只保留最终 success tile → 输出侧为绿。
 */
export function recentIOForGroup(
  group: ModelGroupStripKey,
  lanes: LiveStreamLane[] | null | undefined,
  limit: number = RECENT_STATUS_STRIP_LIMIT,
): ModelGroupRecentIO {
  const merged = mergedTilesForGroup(group, lanes, limit)
  const inflight: LiveStreamTile[] = []
  const terminal: LiveStreamTile[] = []
  let success = 0
  for (const tile of merged) {
    if (!isClientTile(tile)) continue
    if (TERMINAL_STATUSES.has(tile.status)) {
      terminal.push(tile)
      if (tile.status === 'success') success += 1
    } else if (tile.status === 'in_progress') {
      inflight.push(tile)
    }
    // idle / 未知状态：两侧都不进（不冒充）
  }
  return {
    inflight,
    terminal,
    success,
    failed: terminal.length - success,
    successRate: terminal.length > 0 ? success / terminal.length : null,
  }
}
