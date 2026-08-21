// liveStreamStore — module-level singleton EventSource for the
// dashboard swim lane. Multiple Vue components that call
// useLiveStream() share one EventSource; the connection is opened
// on the first acquire() and closed on the last release().
//
// Auth: EventSource does not accept custom headers, so we append
// ?token=<jwt> to the URL (AdminMiddleware extracts it to Authorization).
// The HttpOnly cookie is also sent (credentials: 'include').

import { reactive, computed, type ComputedRef } from 'vue'
import { authBearer } from '../store'
import type { RouteIncidentUpdate } from '../types/routeIncident'

export type LiveStatus = 'in_progress' | 'success' | 'failure' | 'rate_limited'

export type LiveModelCategory = 'openai' | 'anthropic' | 'domestic' | 'oss' | 'other'

export interface LiveRequest {
  type?: 'request' | 'idle_marker'
  ts: string
  request_id?: string
  tenant_id?: string
  gw_session_id?: string
  model?: string
  model_category?: LiveModelCategory
  provider_code?: string
  status?: LiveStatus
  latency_ms?: number | null
  prompt_tokens?: number | null
  completion_tokens?: number | null
  total_tokens?: number | null
  cost_usd?: number | null
  error_kind?: string | null
  failure_stage?: string | null  // "gateway" | "upstream" — failure origin
  client_profile?: string | null
  identity_hash?: string | null
  credits_charged?: number | null
  // 2026-07-27: 客户端感知 (SSE 从后端 request_logs_hot.agent_* 推送)
  agent_name?: string
  agent_type?: string
  client_protocol?: string
  // 2026-07-13: error-triggered probe fields
  is_probe?: boolean
  probe_origin?: 'direct' | 'gateway' | 'scheduled'
  probe_attempt?: number
  // 2026-08-15 (24号 §3): lifecycle extension fields — camelCase on the wire.
  // stage is the §1 state-machine enum the request currently sits in
  // (arriving/routing/queued_model/queued_node/forwarding/first_byte/
  // streaming/done/failed/rejected/...). All optional + backward compatible.
  stage?: string
  retrySeq?: number
  retryReasonClass?: string
  parentRequestId?: string
  requestType?: 'chat' | 'title' | 'summary' | 'sensitive_word' | 'probe' | 'unknown'
}

/**
 * ActionEvent — one row of the 24号 §2 action vocabulary
 * (arrive | route_resolved | model_enqueued | credential_selected |
 *  node_enqueued | node_selected | upstream_request | first_byte | reply |
 *  node_switch | model_switch | no_route).
 *
 * `seq` is monotonically increasing WITHIN a request_id; the store sorts by
 * seq, never by arrival order. Field names keep the store's snake_case wire
 * style. All fields optional + backward compatible (13号 contract principle):
 * absent fields must be treated as "not reported", never rendered as zero.
 */
export interface ActionEvent {
  request_id?: string
  seq?: number
  action?: string
  ts?: string
  model?: string
  credential_id?: number
  error_kind?: string | null
  retry_seq?: number
  retry?: boolean
  stage?: string
  stage_category?: 'routing' | 'llm' | 'retrying' | 'terminal'
  // §2 per-action detail fields (only present on the actions that use them)
  client_protocol?: string
  auto_decision?: string
  queue_depth?: number
  weight?: number
  tier?: string
  sticky?: boolean
  attempt?: number
  ttfb_ms?: number
  status?: string
  latency_ms?: number | null
  from_credential_id?: number
  to_credential_id?: number
  reason?: string
  from_model?: string
  to_model?: string
  blocked_reasons?: string[]
}

export interface LiveStreamStats {
  total: number
  success: number
  failure: number
  in_progress?: number
}

export interface LiveStreamTile {
  request_id: string
  timestamp: string
  model: string
  vendor: string
  provider: string
  status: string
  error_kind?: string | null
  latency_ms?: number | null
  cost_usd?: number | null
  prompt_tokens?: number | null
  completion_tokens?: number | null
  is_probe?: boolean
  probe_origin?: string
  probe_attempt?: number
  stage?: string
  stage_category?: 'routing' | 'llm' | 'retrying' | 'terminal'
}

export interface LiveStreamLane {
  id: string
  name: string
  dimension: 'vendor' | 'provider' | 'model'
  requests: LiveStreamTile[]
  stats: LiveStreamStats
  isOthers: boolean
}

export interface LiveStreamLegendItem {
  key: string
  name: string
  count: number
}

export interface LiveStreamSnapshot {
  summary: LiveStreamStats
  detail_dimensions: Record<'vendor' | 'provider' | 'model', LiveStreamLane[]>
  dimensions: Record<'vendor' | 'provider' | 'model', LiveStreamLane[]>
  dimension_legends: Record<'vendor' | 'provider' | 'model', LiveStreamLegendItem[]>
  status_legends: LiveStreamLegendItem[]
  latest_request_ts?: string
}

export interface LiveStreamDelta {
  summary: LiveStreamStats
  changed_lanes: Record<'vendor' | 'provider' | 'model', LiveStreamLane[]>
  dimension_legends: Record<'vendor' | 'provider' | 'model', LiveStreamLegendItem[]>
  status_legends: LiveStreamLegendItem[]
}

export interface LiveStreamHealth {
  redis_connected: boolean
  redis_error?: string
}

export interface LiveQueueLaneSnapshot {
  model?: string
  credential?: number
  mode?: string
  depth: number
  limit?: number
  full?: boolean
}

// OBS-BE3 (V3.3-OBS, 2026-08-15, 13号 §4 pipeline 层): 全链路 pipeline 聚合
// 口径。字段 optional 语义：waitingMsP50/P95 无样本时缺省（不是 0），
// pipeline 整层在 dispatch 未启用/未接线时缺省 —— 前端必须按缺省隐藏，
// 禁止零值冒充。
export interface LiveQueuePipelineStats {
  depth: number
  waitingMsP50?: number | null
  waitingMsP95?: number | null
  inFlight: number
  degraded: boolean
}

export interface LiveQueueSnapshot {
  enabled: boolean
  wired: boolean
  sourceVersion?: number
  pipeline?: LiveQueuePipelineStats | null
  models: LiveQueueLaneSnapshot[]
  credentials: LiveQueueLaneSnapshot[]
}

export interface LiveNodeStatus {
  credential_id: number
  provider_id?: number
  provider_code?: string
  circuit_state?: string
  availability_state?: string
  quota_state?: string
  health_status?: string
  manual_disabled: boolean
  in_flight?: number
  last_latency_ms?: number
  last_error?: string
  // OBS-BE4 (V3.3-OBS, 2026-08-15): fpslot/降级投影。全部 optional：
  // 健康节点缺省；fp_disabled 仅在 true 时上报；disable_kind ∈
  // manual | system（缺省 = ok）；时间为 RFC3339 字符串，未上报不渲染。
  fp_disabled?: boolean
  fp_disabled_until?: string
  disable_kind?: 'manual' | 'system' | ''
  system_recover_at?: string
  last_error_at?: string
  // 2026-08-17 (OBS-UI model-grouped nodes): 该凭据当前路由可见的原始
  // 模型名列表（credential_model_bindings 投影）。未上报时缺省，
  // 前端不得用空数组冒充"无绑定"。
  raw_models?: string[]
}

export interface LiveStreamEnvelope {
  type: 'initial_data' | 'request' | 'idle_marker' | 'health_update' | 'incident_update' | 'snapshot_refresh' | 'queue_snapshot' | 'node_update' | 'request_lifecycle' | 'child_request'
  ts: string
  request?: LiveRequest
  requests?: LiveRequest[]
  snapshot?: LiveStreamSnapshot
  delta?: LiveStreamDelta
  health?: LiveStreamHealth
  incident?: RouteIncidentUpdate
  lane_ids?: string[]
  queue?: LiveQueueSnapshot
  nodes?: LiveNodeStatus[]
  // 2026-08-15 (24号 §3): request_lifecycle payload. The backend normally
  // sends one action object, but an aggregated batch frame may carry an array
  // — both shapes are accepted. `actions` is a defensive alias.
  action?: ActionEvent | ActionEvent[]
  actions?: ActionEvent[]
  // 2026-08-15 (24号 §3): child_request payload key.
  parent_request_id?: string
}

export type ConnectionState = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed' | 'unsupported'

export const liveStreamState = reactive({
  requests: [] as LiveRequest[],
  snapshot: null as LiveStreamSnapshot | null,
  connection: 'idle' as ConnectionState,
  paused: false,
  lastEventAt: 0,
  redisHealthy: true,
  redisError: '',
  queue: null as LiveQueueSnapshot | null,
  nodes: [] as LiveNodeStatus[],
  // 2026-08-15 (24号 §7): per-request action timeline + parent→children
  // index. Both Maps live inside the reactive state so Vue tracks
  // get/set/iteration on them.
  actions: new Map<string, ActionEvent[]>(),
  children: new Map<string, LiveRequest[]>(),
})

// Action timeline caps (24号 §7): 50 events per request, 2000 globally
// (oldest evicted; the Map is re-inserted on touch so eviction is LRU by
// last-activity, falling back to insertion order).
export const ACTIONS_PER_REQUEST_CAP = 50
export const ACTIONS_GLOBAL_CAP = 2000
export const CHILDREN_PER_PARENT_CAP = 50
export const CHILDREN_GLOBAL_CAP = 2000

let actionsTotal = 0
let childrenTotal = 0

// Page visibility state
const visibilityState = reactive({
  isVisible: typeof document !== 'undefined' ? !document.hidden : true,
  lastVisibleAt: Date.now(),
  missedWhileHidden: false,
})

let needsFullRefresh = false

// Forward declaration - actual implementation is below
let _requestSnapshotRefresh: () => void = () => {}

// Track page visibility changes
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    const wasHidden = !visibilityState.isVisible
    visibilityState.isVisible = !document.hidden

    if (!document.hidden && wasHidden) {
      // Page became visible after being hidden
      const hiddenDuration = Date.now() - visibilityState.lastVisibleAt
      console.log(`[LiveStream] Page visible after ${Math.round(hiddenDuration / 1000)}s`)
      
      if (visibilityState.missedWhileHidden) {
        console.log('[LiveStream] Missed updates while hidden, reconnecting for authoritative replay')
        // A snapshot_refresh only repairs request tiles. Reconnect so the
        // server also replays request_lifecycle and child_request frames.
        // The existing connection is intentionally closed first; openConnection
        // will perform the normal initial_data + lifecycle replay handshake.
        if (refCount > 0) {
          closeConnection()
          openConnection()
        }
        visibilityState.missedWhileHidden = false
      }
    } else if (document.hidden) {
      visibilityState.lastVisibleAt = Date.now()
      console.log('[LiveStream] Page hidden, marking updates as missed')
    }
  })
}

// Vue auto-unwraps `ref` and `reactive` proxies in templates.
// Exposing the reactive properties as ComputedRef keeps the
// type-safety of the composable's public API while letting
// `useLiveStream()` return a stable interface. We deliberately
// wrap the reactive property in a computed() so consumers do not
// need to know whether the underlying state is a ref or a
// reactive object property.
export const requestsRef: ComputedRef<LiveRequest[]> = computed(() => liveStreamState.requests)
export const snapshotRef: ComputedRef<LiveStreamSnapshot | null> = computed(() => liveStreamState.snapshot)
export const connectionRef: ComputedRef<ConnectionState> = computed(() => liveStreamState.connection)
export const pausedRef: ComputedRef<boolean> = computed(() => liveStreamState.paused)
export const lastEventAtRef: ComputedRef<number> = computed(() => liveStreamState.lastEventAt)
export const redisHealthyRef: ComputedRef<boolean> = computed(() => liveStreamState.redisHealthy)
export const redisErrorRef: ComputedRef<string> = computed(() => liveStreamState.redisError)
export const queueRef: ComputedRef<LiveQueueSnapshot | null> = computed(() => liveStreamState.queue)
export const nodesRef: ComputedRef<LiveNodeStatus[]> = computed(() => liveStreamState.nodes)
export const actionsRef: ComputedRef<Map<string, ActionEvent[]>> = computed(() => liveStreamState.actions)
export const childrenRef: ComputedRef<Map<string, LiveRequest[]>> = computed(() => liveStreamState.children)

/** Action timeline of one request, ordered by seq ASC. Empty when unknown. */
export function getRequestActions(requestId: string): ActionEvent[] {
  return liveStreamState.actions.get(requestId) ?? []
}

/** Child requests of one parent request_id, collapsed by request_id. */
export function getRequestChildren(parentRequestId: string): LiveRequest[] {
  return liveStreamState.children.get(parentRequestId) ?? []
}

const TILE_WIDTH = 80
const TILE_GAP = 6
const TILES_PER_LANE_ESTIMATE = 12 // average lanes visible in a typical viewport

function computeMaxVisible(): number {
  if (typeof window === 'undefined') return 60
  const viewportWidth = window.innerWidth
  const tilesPerRow = Math.floor((viewportWidth + TILE_GAP) / (TILE_WIDTH + TILE_GAP))
  // Keep 2x viewport-width of tiles to allow smooth scroll buffer
  return Math.max(20, Math.min(200, tilesPerRow * 2 * TILES_PER_LANE_ESTIMATE))
}

export let MAX_VISIBLE = computeMaxVisible()

export function recomputeMaxVisible() {
  MAX_VISIBLE = computeMaxVisible()
}

export const ENDPOINT = '/api/admin/live-stream'

// localStorage 中允许管理员写入一个自定义 SSE endpoint（reverse proxy / 隧道）
// 读取时若为空字符串 / 不可达 / 同源默认则走 ENDPOINT。
function readCustomEndpoint(): string {
  try {
    const v = localStorage.getItem('llmgw_sse_endpoint')
    return (v || '').trim()
  } catch {
    return ''
  }
}

function buildUrl(endpoint: string): string {
  let url = endpoint
  try {
    const token = authBearer()
    if (token) {
      const sep = url.includes('?') ? '&' : '?'
      url = `${url}${sep}token=${encodeURIComponent(token)}`
    }
  } catch {
    /* SSR or storage disabled — fall back to cookie auth */
  }
  return url
}

// 用户层想要使用的最终 URL（可能被管理员通过弹窗覆盖）
let customEndpoint = readCustomEndpoint()

export function setCustomEndpoint(url: string) {
  customEndpoint = (url || '').trim()
  try {
    if (customEndpoint) localStorage.setItem('llmgw_sse_endpoint', customEndpoint)
    else localStorage.removeItem('llmgw_sse_endpoint')
  } catch {
    /* ignore */
  }
}

export function getCustomEndpoint(): string {
  if (!customEndpoint) {
    customEndpoint = readCustomEndpoint()
  }
  return customEndpoint
}

const idIndex = new Set<string>()
const pending: LiveRequest[] = []
const PENDING_CAP = MAX_VISIBLE * 4

// maxSeenTs tracks the maximum request timestamp the frontend has ever
// received (from snapshots, deltas, or initial_data). The backend's
// snapshot_refresh carries LatestRequestTs; the frontend rejects any
// snapshot whose timestamp ≤ maxSeenTs to prevent stale Redis data from
// overwriting newer deltas already applied in local state.
let maxSeenTs = ''

let es: EventSource | null = null
let refCount = 0

let onEvictCb: ((id: string) => void) | null = null
const terminalListeners = new Set<(req: LiveRequest) => void>()

function notifyTerminalRequest(req: LiveRequest) {
  if (req.type === 'idle_marker' || !req.request_id) return
  if (req.status !== 'success' && req.status !== 'failure' && req.status !== 'rate_limited') return
  for (const fn of terminalListeners) fn(req)
}

// ---------------------------------------------------------------------------
// 2026-08-15 (24号 §2/§3/§7): request_lifecycle action timeline + child_request
// index. Store layer only — rendering is done by consumers (RequestDetail /
// RequestProcessingTrail upgrade).
// ---------------------------------------------------------------------------

function seqOf(a: ActionEvent): number {
  return a.seq ?? 0
}

// 2026-08-17 (OBS-UI model-grouped nodes): 派生索引 request_id → 当前
// credential_id。ActionEvent 在 credential_selected / node_selected /
// upstream_request / node_switch / reply 等动作上携带 credential_id（同
// request 内以 seq 递增）。仅取该请求最近一条带 credential_id 的动作作为
// "当前凭据"——支持故障转移（node_switch.from→to）只保留最新。
//
// 窗口语义：仅覆盖当前 SSE 回放窗口内（≈最近 200 条 + 实时流入）的请求。
// 与"实时请求流"栏目本身的窗口语义一致，不承诺全量历史。
type RequestCredentialIndex = Map<string, { credentialId: number; seq: number; ts: number }>
const requestCredential: RequestCredentialIndex = new Map()

// Carry-credential actions: ActionEvent 中带 credential_id 且表示"请求
// 绑定到该凭据"的子集。node_switch 携带 from_credential_id +
// to_credential_id；reply 携带 credential_id；其余只携带 credential_id。
function extractCredentialFromAction(action: ActionEvent): { credentialId: number; seq: number; ts: number } | null {
  const seq = action.seq ?? 0
  const ts = action.ts ? Date.parse(action.ts) : 0
  if (action.action === 'node_switch') {
    // 故障转移目标优先；缺省时退回到原凭据（让索引至少有一个非空值）。
    if (typeof action.to_credential_id === 'number') {
      return { credentialId: action.to_credential_id, seq, ts }
    }
    if (typeof action.from_credential_id === 'number') {
      return { credentialId: action.from_credential_id, seq, ts }
    }
    return null
  }
  if (typeof action.credential_id === 'number') {
    return { credentialId: action.credential_id, seq, ts }
  }
  return null
}

function applyRequestCredentialIndex(requestId: string, action: ActionEvent) {
  const next = extractCredentialFromAction(action)
  if (!next) return
  const prev = requestCredential.get(requestId)
  // 用 (seq, ts) 比较保证：① seq 大的胜出；② seq 缺失/相同时 ts 大的胜出
  // —— 避免 node_switch 乱序造成"旧凭据"覆盖"新凭据"。
  if (prev && prev.seq > next.seq) return
  if (prev && prev.seq === next.seq && prev.ts > next.ts) return
  requestCredential.set(requestId, next)
}

function rebuildRequestCredentialIndex(requestId: string, timeline: ActionEvent[]) {
  requestCredential.delete(requestId)
  for (const action of timeline) {
    applyRequestCredentialIndex(requestId, action)
  }
}

export function getRequestCredentialId(requestId: string): number | null {
  return requestCredential.get(requestId)?.credentialId ?? null
}

/** Return the requests in the current SSE replay window that are bound to
 *  the given credential_id. Filter runs over the flat `liveStreamState.requests`
 *  buffer, no extra data is kept. */
export function getRequestsForCredential(credentialId: number): LiveRequest[] {
  if (!Number.isFinite(credentialId)) return []
  const out: LiveRequest[] = []
  for (const r of liveStreamState.requests) {
    if (!r || r.type === 'idle_marker' || !r.request_id) continue
    if (getRequestCredentialId(r.request_id) === credentialId) {
      out.push(r)
    }
  }
  return out
}

/** Reverse index for model-grouped nodes: model name → nodes that
 *  route-serve that model. Backed solely by the wire `raw_models` field. */
export function getNodesForModel(model: string): LiveNodeStatus[] {
  if (!model) return []
  const out: LiveNodeStatus[] = []
  for (const n of liveStreamState.nodes) {
    if (!n) continue
    if (Array.isArray(n.raw_models) && n.raw_models.includes(model)) {
      out.push(n)
    }
  }
  return out
}

export function clearRequestCredentialIndex() {
  requestCredential.clear()
}

function applyStageToRequest(requestId: string, action: ActionEvent) {
  if (!requestId || (!action.stage && !action.stage_category)) return
  const patch: Partial<LiveStreamTile> = {}
  if (action.stage) patch.stage = action.stage
  if (action.stage_category) patch.stage_category = action.stage_category
  for (const request of liveStreamState.requests) {
    if (request.request_id === requestId) Object.assign(request, patch)
  }
  const snapshot = liveStreamState.snapshot
  if (!snapshot) return
  for (const dim of ['vendor', 'provider', 'model'] as const) {
    for (const lane of snapshot.dimensions[dim] || []) {
      const tile = lane.requests.find((item) => item.request_id === requestId)
      if (tile) Object.assign(tile, patch)
    }
    for (const lane of snapshot.detail_dimensions[dim] || []) {
      const tile = lane.requests.find((item) => item.request_id === requestId)
      if (tile) Object.assign(tile, patch)
    }
  }
}

/** Insert one action into its request's timeline, kept sorted by seq ASC.
 *  Re-delivering an existing seq (Redis replay dedupe) replaces in place. */
function recordAction(action: ActionEvent) {
  const requestId = action.request_id
  if (!requestId) return
  let list = liveStreamState.actions.get(requestId)
  if (!list) {
    list = []
    liveStreamState.actions.set(requestId, list)
  } else {
    // Refresh LRU order: delete + re-insert so the least recently active
    // request is evicted first when the global cap is hit.
    liveStreamState.actions.delete(requestId)
    liveStreamState.actions.set(requestId, list)
  }

  const existingIndex = list.findIndex((a) => seqOf(a) === seqOf(action))
  if (existingIndex >= 0) {
    // Same seq replays (snapshot refresh / reconnect): last write wins.
    list[existingIndex] = action
    // 2026-08-17 (audit P1): timeline 用同 seq last-write-wins；索引必须
    // 从替换后的 timeline 重建，不能让旧 action 的时间戳覆盖该语义。
    rebuildRequestCredentialIndex(requestId, list)
    return
  }

  list.push(action)
  // Array#sort is stable, so equal-seq events keep arrival order and the
  // overall order depends only on seq — never on message arrival history.
  list.sort((a, b) => seqOf(a) - seqOf(b))
  actionsTotal += 1
  applyRequestCredentialIndex(requestId, action)

  // Per-request cap: drop the OLDEST actions (smallest seq).
  while (list.length > ACTIONS_PER_REQUEST_CAP) {
    list.shift()
    actionsTotal -= 1
  }
  enforceGlobalActionsCap()
}

/** Global cap: evict the oldest actions of the least recently active request
 *  (Map insertion order front) until the total is back under the cap. */
function enforceGlobalActionsCap() {
  while (actionsTotal > ACTIONS_GLOBAL_CAP && liveStreamState.actions.size > 0) {
    const oldestKey = liveStreamState.actions.keys().next().value as string | undefined
    if (oldestKey === undefined) {
      actionsTotal = 0
      break
    }
    const list = liveStreamState.actions.get(oldestKey)
    if (!list || list.length === 0) {
      liveStreamState.actions.delete(oldestKey)
      continue
    }
    list.shift()
    actionsTotal -= 1
    if (list.length === 0) liveStreamState.actions.delete(oldestKey)
  }
}

/** request_lifecycle frame: `action` may be a single object or an aggregated
 *  batch array; `actions` is accepted as a defensive alias. */
function applyLifecycleActions(payload: ActionEvent | ActionEvent[] | undefined) {
  if (!payload) return
  const batch = Array.isArray(payload) ? payload : [payload]
  for (const action of batch) {
    if (!action || typeof action !== 'object') continue
    recordAction(action)
    applyStageToRequest(action.request_id || '', action)
  }
}

/** child_request frame: attach the child request to its parent's index and
 *  refresh the child's card in the flat replay buffer when it is visible. */
function applyChildRequest(parentRequestId: string, child: LiveRequest) {
  const enriched: LiveRequest = { ...child, parentRequestId: child.parentRequestId ?? parentRequestId }
  if (!enriched.request_id) return

  let list = liveStreamState.children.get(parentRequestId)
  if (!list) {
    list = []
    liveStreamState.children.set(parentRequestId, list)
  } else {
    liveStreamState.children.delete(parentRequestId)
    liveStreamState.children.set(parentRequestId, list)
  }

  const existingIndex = list.findIndex((c) => c.request_id === enriched.request_id)
  if (existingIndex >= 0) {
    // Lifecycle update of a known child: collapse in place, keep list order.
    list[existingIndex] = { ...list[existingIndex], ...enriched }
  } else {
    list.push(enriched)
    childrenTotal += 1
    while (list.length > CHILDREN_PER_PARENT_CAP) {
      list.shift()
      childrenTotal -= 1
    }
    enforceGlobalChildrenCap()
  }

  // Update the corresponding request card if the child is present in the
  // flat replay buffer (children may never enter the main swim lane).
  if (idIndex.has(enriched.request_id)) {
    const cardIndex = liveStreamState.requests.findIndex((r) => r.request_id === enriched.request_id)
    if (cardIndex >= 0) {
      liveStreamState.requests[cardIndex] = { ...liveStreamState.requests[cardIndex], ...enriched }
    }
  }
}

function enforceGlobalChildrenCap() {
  while (childrenTotal > CHILDREN_GLOBAL_CAP && liveStreamState.children.size > 0) {
    const oldestKey = liveStreamState.children.keys().next().value as string | undefined
    if (oldestKey === undefined) {
      childrenTotal = 0
      break
    }
    const list = liveStreamState.children.get(oldestKey)
    if (!list || list.length === 0) {
      liveStreamState.children.delete(oldestKey)
      continue
    }
    list.shift()
    childrenTotal -= 1
    if (list.length === 0) liveStreamState.children.delete(oldestKey)
  }
}

function trimOldest() {
  if (liveStreamState.requests.length < MAX_VISIBLE) return
  const dropped = liveStreamState.requests.shift()
  if (!dropped) return
  if (dropped.type !== 'idle_marker' && dropped.request_id) {
    idIndex.delete(dropped.request_id)
    if (onEvictCb) onEvictCb(dropped.request_id)
  }
}

function pushOrQueue(item: LiveRequest) {
  if (liveStreamState.paused) {
    pending.push(item)
    if (pending.length > PENDING_CAP) {
      const dropped = pending.splice(0, pending.length - PENDING_CAP)
      for (const d of dropped) {
        if (d.type !== 'idle_marker' && d.request_id) {
          idIndex.delete(d.request_id)
          if (onEvictCb) onEvictCb(d.request_id)
        }
      }
    }
    return
  }
  if (item.type === 'idle_marker' && item.request_id) {
    const existingIndex = liveStreamState.requests.findIndex(
      (r) => r.request_id === item.request_id,
    )
    if (existingIndex >= 0) {
      // Idle markers keep a stable request_id; update payload in place so
      // the tile stays in chronological order (pushed left by newer requests).
      liveStreamState.requests[existingIndex] = item
      return
    }
  }
  if (item.type !== 'idle_marker' && item.request_id) {
    if (idIndex.has(item.request_id)) {
      const existingIndex = liveStreamState.requests.findIndex(
        r => r.request_id === item.request_id
      )
      if (existingIndex >= 0) {
        // A lifecycle update keeps the same request identity. Replacing the
        // item in place preserves both queue order and the tile's Vue key,
        // preventing a routine in-progress -> terminal transition from
        // looking like a remove/reinsert flicker.
        liveStreamState.requests[existingIndex] = item
      } else {
        // The index can be stale after a snapshot reconcile. Recover by
        // inserting the request instead of silently dropping the update.
        liveStreamState.requests.push(item)
      }
      return
    }
    idIndex.add(item.request_id)
  }
  liveStreamState.requests.push(item)
  while (liveStreamState.requests.length > MAX_VISIBLE) {
    trimOldest()
  }
}

function flushPending() {
  if (pending.length === 0) return
  const drained = pending.splice(0, pending.length)
  for (const item of drained) {
    pushOrQueue(item)
  }
}

function applyInitialData(items: LiveRequest[]) {
  const sorted = [...items].sort((a, b) => (a.ts || '').localeCompare(b.ts || ''))
  const kept = sorted.slice(-MAX_VISIBLE)
  const newIds = new Set<string>()
  for (const r of kept) {
    if (r.type !== 'idle_marker' && r.request_id) newIds.add(r.request_id)
  }
  if (onEvictCb) {
    for (const oldId of idIndex) {
      if (!newIds.has(oldId)) onEvictCb(oldId)
    }
  }
  idIndex.clear()
  liveStreamState.children.clear()
  childrenTotal = 0
  for (const r of kept) {
    if (r.type !== 'idle_marker' && r.request_id) idIndex.add(r.request_id)
  }
  liveStreamState.requests = kept
  for (const r of kept) {
    if (r.parentRequestId) applyChildRequest(r.parentRequestId, r)
  }
}

function handleEnvelope(env: LiveStreamEnvelope) {
  liveStreamState.lastEventAt = Date.now()
  if (env.queue) {
    liveStreamState.queue = env.queue
  }
  if (env.nodes) {
    liveStreamState.nodes = env.nodes
  }
  if (env.health) {
    liveStreamState.redisHealthy = env.health.redis_connected
    liveStreamState.redisError = env.health.redis_error || ''
  }

  if (env.type === 'initial_data' && Array.isArray(env.requests)) {
    if (env.snapshot) {
      mergeSnapshotFromServer(env.snapshot)
      if (env.snapshot.latest_request_ts && env.snapshot.latest_request_ts > maxSeenTs) {
        maxSeenTs = env.snapshot.latest_request_ts
      }
    } else if (env.delta) {
      mergeDelta(env.delta)
    }
    applyInitialData(env.requests)
    for (const r of env.requests) {
      if (r.ts && r.ts > maxSeenTs) maxSeenTs = r.ts
    }
    return
  }

  // A periodic snapshot reconciles lane aggregates. Keep the independent
  // flat replay buffer intact so the UI never briefly renders an empty queue.
  // 2026-07-26: Fixed the always-skip guard. Previously `incomingTs <= maxSeenTs`
  // rejected EVERY periodic snapshot once any delta had raised maxSeenTs above
  // the snapshot's ts — so reconciliation never ran and local state drifted
  // until some rare strictly-newer snapshot force-applied everything at once
  // (the "page flip"). Now: skip only when STRICTLY older (`<`); when equal,
  // apply it — deterministic sorting (mergeTilesById) makes an equal-timed
  // snapshot idempotent, so applying it produces no visual change. The
  // regression guard against genuinely-stale snapshots is preserved.
  if (env.type === 'snapshot_refresh' && env.snapshot) {
    const incomingTs = env.snapshot.latest_request_ts
    if (incomingTs && maxSeenTs && incomingTs < maxSeenTs) {
      console.debug('[LiveStream] Skipping stale snapshot', { incomingTs, maxSeenTs })
      return
    }
    if (incomingTs && incomingTs > maxSeenTs) {
      maxSeenTs = incomingTs
    }
    mergeSnapshotFromServer(env.snapshot)
    return
  }

  if (env.delta) {
    mergeDelta(env.delta)
    for (const dim of ['vendor', 'provider', 'model'] as const) {
      const lanes = env.delta.changed_lanes[dim]
      if (!lanes) continue
      for (const lane of lanes) {
        for (const tile of lane.requests) {
          if (tile.timestamp && tile.timestamp > maxSeenTs) {
            maxSeenTs = tile.timestamp
          }
        }
      }
    }
  } else if (env.snapshot) {
    mergeSnapshotFromServer(env.snapshot)
  }

  if (env.type === 'request' && env.request) {
    pushOrQueue(env.request)
    if (env.request.ts && env.request.ts > maxSeenTs) {
      maxSeenTs = env.request.ts
    }
    notifyTerminalRequest(env.request)
    return
  }
  if (env.type === 'idle_marker') {
    // Backend now writes idle markers to Redis and includes them in the
    // envelope's delta payload (see admin/live_stream_sse.go
    // maybeEmitIdleMarker). The delta was merged above, so do not merge it
    // again here: the five-minute heartbeat must not trigger a second lane
    // array replacement and unnecessary repaint.
    return
  }
  if (env.type === 'request_lifecycle') {
    applyLifecycleActions(env.action ?? env.actions)
    return
  }
  if (env.type === 'child_request' && env.request && env.parent_request_id) {
    applyChildRequest(env.parent_request_id, env.request)
    return
  }

  if (env.type === 'health_update') {
    return
  }
  if (env.type === 'incident_update' && env.incident) {
    void import('./useRouteIncidents').then((mod) => {
      mod.applyIncidentUpdate(env.incident!)
    })
    return
  }
}

/** 
 * Handle idle_marker envelope: backend broadcasts all known lane IDs every 5 min.
 * For each lane, check if it has recent (<5min) non-idle activity.
 * If idle → create/update an idle tile in the snapshot lane's request list.
 * If active → remove any existing idle tile.
 * 
 * NOTE: This function is currently NOT CALLED. Idle markers are handled via
 * delta.changed_lanes by the backend. Keeping for reference if needed later.
 */
function handleLaneIdleCheck_UNUSED(laneIds: string[], backendTs: string) {
  const snap = liveStreamState.snapshot
  if (!snap) return

  const now = Date.now()
  const idleThresholdMs = 5 * 60 * 1000

  for (const laneId of laneIds) {
    const colonIdx = laneId.indexOf(':')
    if (colonIdx === -1) continue
    const dim = laneId.substring(0, colonIdx) as 'vendor' | 'provider' | 'model'
    const key = laneId.substring(colonIdx + 1)

    const lanes = snap.dimensions[dim]
    if (!lanes) continue
    const lane = lanes.find(l => l.id === key)
    if (!lane) continue

    // Check for recent non-idle tiles
    const hasRecentActivity = lane.requests.some(tile => {
      if (tile.status === 'idle') return false
      const tileTs = new Date(tile.timestamp).getTime()
      return !Number.isNaN(tileTs) && (now - tileTs) < idleThresholdMs
    })

    const stableId = `idle-${dim}-${key.replace(/[:/]/g, '_')}`

    // Remove existing idle tile if lane is active
    if (hasRecentActivity) {
      const idx = lane.requests.findIndex(t => t.request_id === stableId)
      if (idx >= 0) lane.requests.splice(idx, 1)
      continue
    }

    // Find the last non-idle tile's timestamp to calculate idle duration
    const sorted = [...lane.requests]
      .filter(t => t.status !== 'idle')
      .sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime())
    const idleSince = sorted.length > 0 ? sorted[0].timestamp : backendTs

    const idleTile: LiveStreamTile = {
      request_id: stableId,
      timestamp: idleSince,
      model: dim === 'model' ? key : '空闲',
      vendor: dim === 'vendor' ? key : '__idle__',
      provider: dim === 'provider' ? key : '系统心跳',
      status: 'idle',
    }

    // Replace existing idle tile or add
    const existing = lane.requests.findIndex(t => t.request_id === stableId)
    if (existing >= 0) {
      lane.requests[existing] = idleTile
    } else {
      lane.requests.push(idleTile)
    }
  }
}

function normalizeLaneTiles(lane: LiveStreamLane) {
  const tiles = [...lane.requests]
  mergeTilesById(lane.requests, tiles)
}

function mergeSnapshotFromServer(incoming: LiveStreamSnapshot) {
  if (!liveStreamState.snapshot) {
    // The backend serializes lane tiles newest-first for its own replay/cap
    // semantics. The UI contract is the opposite: FIFO, oldest on the left
    // and newest on the right. Normalize the first snapshot too; incremental
    // merges already pass through mergeTilesById below.
    for (const dim of ['vendor', 'provider', 'model'] as const) {
      for (const lane of incoming.dimensions[dim] || []) {
        normalizeLaneTiles(lane)
      }
      for (const lane of incoming.detail_dimensions[dim] || []) {
        normalizeLaneTiles(lane)
      }
    }
    liveStreamState.snapshot = incoming
    return
  }
  const s = liveStreamState.snapshot
  s.summary = incoming.summary
  s.status_legends = incoming.status_legends
  for (const dim of ['vendor', 'provider', 'model'] as const) {
    if (incoming.dimensions[dim]) {
      if (!s.dimensions[dim]) s.dimensions[dim] = []
      mergeLanesById(s.dimensions[dim], incoming.dimensions[dim])
    }
    if (incoming.detail_dimensions[dim]) {
      if (!s.detail_dimensions[dim]) s.detail_dimensions[dim] = []
      mergeLanesById(s.detail_dimensions[dim], incoming.detail_dimensions[dim])
    }
    if (incoming.dimension_legends[dim]) {
      if (!s.dimension_legends[dim]) s.dimension_legends[dim] = []
      mergeLegendsByKey(s.dimension_legends[dim], incoming.dimension_legends[dim])
    }
  }
}

function tilesEqual(a: LiveStreamTile[], b: LiveStreamTile[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    const x = a[i]
    const y = b[i]
    if (
      x.request_id !== y.request_id ||
      x.timestamp !== y.timestamp ||
      x.status !== y.status ||
      x.model !== y.model ||
      x.vendor !== y.vendor ||
      x.provider !== y.provider
    ) {
      return false
    }
  }
  return true
}

function statsEqual(a: LiveStreamStats, b: LiveStreamStats): boolean {
  return (
    a.total === b.total &&
    a.success === b.success &&
    a.failure === b.failure &&
    (a.in_progress ?? 0) === (b.in_progress ?? 0)
  )
}

function laneDataEqual(a: LiveStreamLane, b: LiveStreamLane): boolean {
  return (
    a.id === b.id &&
    a.name === b.name &&
    a.dimension === b.dimension &&
    a.isOthers === b.isOthers &&
    statsEqual(a.stats, b.stats) &&
    tilesEqual(a.requests, b.requests)
  )
}

/** Patch lanes by id; reuse unchanged lane objects to avoid SwimLane remounts. */
function mergeLaneList(existing: LiveStreamLane[], incoming: LiveStreamLane[]): LiveStreamLane[] {
  const byId = new Map(existing.map((lane) => [lane.id, lane]))
  return incoming.map((inc) => {
    const prev = byId.get(inc.id)
    if (prev && laneDataEqual(prev, inc)) return prev
    return inc
  })
}

function mergeDelta(delta: LiveStreamDelta) {
  // 2026-07-14: the previous implementation did
  //   s.dimensions[dim] = delta.changed_lanes[dim]
  // which **replaced** the entire lane array per dimension every time
  // ANY single lane changed. That broke Vue's TransitionGroup inside
  // SwimLane.vue: the surrounding keys stayed stable but every lane
  // component was re-rendered as a brand-new child, producing the
  // "swim lane flicker" operators reported.
  //
  // The fix is lane-id keyed merge:
  //   - for each lane in the delta's changed_lanes, look up by
  //     lane.id in the existing snapshot
  //     · hit  → mutate the existing object in place (Stats + Requests)
  //     · miss → append it to the snapshot, preserving current order
  //   - lanes that vanish from the new snapshot are NOT removed here
  //     (we never had a "removed_lane_ids" channel — the backend
  //     simply omits them in the next snapshot, so a follow-up
  //     computeScopeDelta path or a periodic reconcile takes care
  //     of it. See `pruneStaleLanes` below.)
  if (!liveStreamState.snapshot) {
    liveStreamState.snapshot = {
      summary: delta.summary,
      detail_dimensions: { vendor: [], provider: [], model: [] },
      dimensions: { vendor: [], provider: [], model: [] },
      dimension_legends: delta.dimension_legends || { vendor: [], provider: [], model: [] },
      status_legends: delta.status_legends,
    }
  }
  const s = liveStreamState.snapshot
  s.summary = delta.summary
  s.status_legends = delta.status_legends
  for (const dim of ['vendor', 'provider', 'model'] as const) {
    if (delta.changed_lanes[dim]) {
      // mergeLanesById updates in place and preserves lane order so
      // backend rank changes do not reshuffle the whole swim-lane row.
      if (!s.dimensions[dim]) s.dimensions[dim] = []
      mergeLanesById(s.dimensions[dim], delta.changed_lanes[dim])
      if (!s.detail_dimensions[dim]) s.detail_dimensions[dim] = []
      mergeLanesById(s.detail_dimensions[dim], delta.changed_lanes[dim])
    }
    if (delta.dimension_legends && delta.dimension_legends[dim]) {
      if (!s.dimension_legends[dim]) s.dimension_legends[dim] = []
      mergeLegendsByKey(s.dimension_legends[dim], delta.dimension_legends[dim])
    }
  }
}

// mergeLanesById merges an incoming lanes list into the existing one
// without changing order or component identity. For each incoming
// lane:
//
//   - If an existing lane has the same id, update its Stats and
//     Requests in place (Vue sees the same component, just with new
//     props — no remount, no TransitionGroup flicker).
//   - Otherwise append the new lane to the tail of the existing
//     list. The Tailwind/TransitionGroup enter animation runs once.
//
// We deliberately do NOT re-sort the existing array. The backend
// already sorts lanes by stats.Total DESC + lane.id ASC tie-breaker
// (admin/live_stream_redis_store.go:buildLiveStreamLanes), and a
// position swap of an existing lane would re-run the leave/enter
// animation. If a lane's rank changes (because a newer request
// landed on a previously-silent lane), the backend's lanesChanged
// check will include that lane in the next changed_lanes batch and
// the new ordering arrives whole — but the existing lanes that did
// NOT change rank stay where they are. This is what the operator
// wants: "no flicker" + "newest lane visible".
function mergeLanesById(existing: LiveStreamLane[], incoming: LiveStreamLane[]) {
  const byId = new Map(existing.filter((lane) => lane.id).map((lane) => [lane.id, lane]))
  const next: LiveStreamLane[] = []
  const seen = new Set<string>()
  for (const lane of incoming) {
    if (!lane.id || seen.has(lane.id)) continue
    seen.add(lane.id)
    const target = byId.get(lane.id)
    if (target) {
      target.name = lane.name
      target.dimension = lane.dimension
      target.isOthers = lane.isOthers
      target.stats = lane.stats
      mergeTilesById(target.requests, lane.requests)
      next.push(target)
    } else {
      const normalized = { ...lane, requests: [...lane.requests] }
      normalizeLaneTiles(normalized)
      next.push(normalized)
    }
  }
  existing.splice(0, existing.length, ...next)
}

function mergeTilesById(existing: LiveStreamTile[], incoming: LiveStreamTile[]) {
  const byId = new Map(existing.filter((tile) => tile.request_id).map((tile) => [tile.request_id, tile]))
  const seen = new Set<string>()
  const next: LiveStreamTile[] = []
  for (const tile of incoming) {
    if (!tile.request_id || seen.has(tile.request_id)) continue
    seen.add(tile.request_id)
    const target = byId.get(tile.request_id)
    if (target) {
      Object.assign(target, tile)
      next.push(target)
    } else {
      next.push({ ...tile })
    }
  }
  next.sort((a, b) => {
    const timestamp = (a.timestamp || '').localeCompare(b.timestamp || '')
    return timestamp || (a.request_id || '').localeCompare(b.request_id || '')
  })
  existing.splice(0, existing.length, ...next.slice(-20))
}

// mergeLegendsByKey is the same idea but for the legend strips —
// merge by legend.key instead of replacing the whole array.
function mergeLegendsByKey(existing: LiveStreamLegendItem[], incoming: LiveStreamLegendItem[]) {
  const byKey = new Map<string, number>()
  for (let i = 0; i < existing.length; i++) {
    byKey.set(existing[i].key, i)
  }
  for (const item of incoming) {
    const idx = byKey.get(item.key)
    if (idx === undefined) {
      byKey.set(item.key, existing.length)
      existing.push(item)
    } else {
      const target = existing[idx]
      target.name = item.name
      target.count = item.count
    }
  }
}

function openConnection() {
  if (es) return
  // Recompute MAX_VISIBLE on resize so the replay buffer stays at 2× viewport.
  recomputeMaxVisible()
  const onResize = () => recomputeMaxVisible()
  window.addEventListener('resize', onResize)
  // Store reference for cleanup
  ;(openConnection as any)._resizeHandler = onResize

  if (typeof EventSource === 'undefined') {
    liveStreamState.connection = 'unsupported'
    return
  }
  // Browser EventSource cannot set Authorization headers, and the
  // project uses HttpOnly cookies that some reverse-proxy / dev
  // setups do not propagate to the EventSource request (e.g. a
  // vite dev-server reverse-proxy, or a third-party iframe host).
  // To stay robust in BOTH the production cookie path and the
  // legacy api_key path, we promote the admin api_key (read from
  // localStorage) to a `?token=` query parameter when present.
  //
  // The backend (admin/live_stream_sse.go) accepts this only as a
  // fallback when neither the Bearer header nor the cookie is set,
  // so the security profile is unchanged.
  let url = buildUrl(getCustomEndpoint() || ENDPOINT)
  try {
    es = new EventSource(url, { withCredentials: true })
  } catch (err) {
    console.warn('[liveStream] EventSource construct failed', err)
    liveStreamState.connection = 'closed'
    return
  }
  liveStreamState.connection = 'connecting'

  es.onopen = () => {
    liveStreamState.connection = 'open'
  }
  es.onmessage = (ev) => {
    try {
      const env = JSON.parse(ev.data) as LiveStreamEnvelope
      
      // Skip updates when page is hidden (keep connection alive, don't write state)
      if (!visibilityState.isVisible) {
        console.debug('[LiveStream] Message received but page hidden, marking as missed')
        visibilityState.missedWhileHidden = true
        return
      }
      
      // Handle full refresh after page becomes visible
      if (needsFullRefresh && env.type === 'snapshot_refresh') {
        console.log('[LiveStream] Applying full snapshot after visibility change')
        needsFullRefresh = false
      }
      
      handleEnvelope(env)
    } catch (err) {
      console.warn('[liveStream] bad envelope', err)
    }
  }
  es.onerror = () => {
    if (es && es.readyState === 2) {
      liveStreamState.connection = 'closed'
    } else {
      liveStreamState.connection = 'reconnecting'
    }
  }
}

function closeConnection() {
  if (!es) return
  try { es.close() } catch { /* ignore */ }
  es = null
  // Remove the correct resize listener reference to prevent leak
  const handler = (openConnection as any)._resizeHandler
  if (handler) {
    window.removeEventListener('resize', handler)
    delete (openConnection as any)._resizeHandler
  }
  liveStreamState.connection = 'closed'
}

export function acquireLiveStream(): () => void {
  refCount += 1
  if (refCount === 1) openConnection()
  return () => {
    refCount -= 1
    if (refCount <= 0) {
      refCount = 0
      closeConnection()
    }
  }
}

// Request full snapshot refresh from backend
export function requestSnapshotRefresh() {
  if (liveStreamState.connection !== 'open') {
    console.warn('[LiveStream] Cannot refresh: connection not open')
    return
  }

  // Trigger backend snapshot push via HTTP endpoint
  const token = authBearer()
  const url = '/api/admin/live-stream/trigger-snapshot'
  
  fetch(url, {
    method: 'POST',
    headers: token ? { 'Authorization': `Bearer ${token}` } : {},
    credentials: 'include',
  })
    .then(res => {
      if (res.ok) {
        console.log('[LiveStream] Snapshot refresh triggered successfully')
        needsFullRefresh = true
      } else {
        console.warn('[LiveStream] Snapshot refresh failed:', res.status)
      }
    })
    .catch(err => {
      console.warn('[LiveStream] Snapshot refresh error:', err)
    })
}

// Set the forward reference so the visibility listener can call this
_requestSnapshotRefresh = requestSnapshotRefresh

export function pauseStream() {
  liveStreamState.paused = true
}
export function resumeStream() {
  if (!liveStreamState.paused) return
  liveStreamState.paused = false
  flushPending()
}
export function togglePause() {
  if (liveStreamState.paused) resumeStream()
  else pauseStream()
}
export function resetStream() {
  liveStreamState.requests = []
  liveStreamState.snapshot = null
  liveStreamState.actions.clear()
  liveStreamState.children.clear()
  requestCredential.clear()
  actionsTotal = 0
  childrenTotal = 0
  idIndex.clear()
  pending.length = 0
  maxSeenTs = ''
}
export function reconnectStream() {
  closeConnection()
  openConnection()
}
export function setOnRequestEvicted(cb: ((id: string) => void) | null) {
  onEvictCb = cb
}

/** Subscribe to completed requests (success/failure) for board stat deltas. */
export function subscribeTerminalRequests(listener: (req: LiveRequest) => void): () => void {
  terminalListeners.add(listener)
  return () => terminalListeners.delete(listener)
}

export const __testing = {
  state: liveStreamState,
  idIndex,
  pending,
  pushOrQueue,
  handleEnvelope,
  applyInitialData,
  mergeDelta,
  mergeSnapshotFromServer,
  mergeLaneList,
  mergeTilesById,
  laneDataEqual,
  recordAction,
  applyLifecycleActions,
  applyChildRequest,
  actionsTotal: () => actionsTotal,
  childrenTotal: () => childrenTotal,
  requestCredential,
  getRequestCredentialId,
  getRequestsForCredential,
  getNodesForModel,
  clearRequestCredentialIndex,
  resetStream,
  refCount: () => refCount,
  es: () => es,
  MAX_VISIBLE,
  maxSeenTs: () => maxSeenTs,
}
