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

export type LiveStatus = 'in_progress' | 'success' | 'failure'

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
  // 2026-07-13: error-triggered probe fields
  is_probe?: boolean
  probe_origin?: 'direct' | 'gateway' | 'scheduled'
  probe_attempt?: number
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

export interface LiveStreamEnvelope {
  type: 'initial_data' | 'request' | 'idle_marker' | 'health_update' | 'incident_update'
  ts: string
  request?: LiveRequest
  requests?: LiveRequest[]
  snapshot?: LiveStreamSnapshot
  delta?: LiveStreamDelta
  health?: LiveStreamHealth
  incident?: RouteIncidentUpdate
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
})

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

export const MAX_VISIBLE = 60
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

let es: EventSource | null = null
let refCount = 0

let onEvictCb: ((id: string) => void) | null = null

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
  if (item.type !== 'idle_marker' && item.request_id) {
    // 2026-07-04: if request_id already exists, UPDATE in place instead of ignore.
    // This way in_progress -> success/failure transitions update the live tile.
    if (idIndex.has(item.request_id)) {
      const existingIndex = liveStreamState.requests.findIndex(
        r => r.request_id === item.request_id
      )
      if (existingIndex >= 0) {
        liveStreamState.requests[existingIndex] = item
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
  for (const r of kept) {
    if (r.type !== 'idle_marker' && r.request_id) idIndex.add(r.request_id)
  }
  liveStreamState.requests = kept
}

function handleEnvelope(env: LiveStreamEnvelope) {
  liveStreamState.lastEventAt = Date.now()
  // Update Redis health from any envelope that carries it.
  if (env.health) {
    liveStreamState.redisHealthy = env.health.redis_connected
    liveStreamState.redisError = env.health.redis_error || ''
  }
  if (env.snapshot) {
    liveStreamState.snapshot = env.snapshot
  } else if (env.delta) {
    mergeDelta(env.delta)
  }
  if (env.type === 'initial_data' && Array.isArray(env.requests)) {
    applyInitialData(env.requests)
    return
  }
  if (env.type === 'request' && env.request) {
    pushOrQueue(env.request)
    return
  }
  if (env.type === 'idle_marker') {
    pushOrQueue({ type: 'idle_marker', ts: env.ts })
    return
  }
  if (env.type === 'health_update') {
    // Health-only envelope; state already updated above.
    return
  }
  if (env.type === 'incident_update' && env.incident) {
    // 2026-07-13: route-incident diagnostic updates. Forwarded
    // to the useRouteIncidents reducer which maintains the per-
    // lane index. We use a dynamic import to avoid a hard cycle
    // between the live stream store and the route-incident
    // composable.
    void import('./useRouteIncidents').then((mod) => {
      mod.applyIncidentUpdate(env.incident!)
    })
    return
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
  if (!liveStreamState.snapshot) {
    liveStreamState.snapshot = {
      summary: delta.summary,
      detail_dimensions: { vendor: [], provider: [], model: [] },
      dimensions: { vendor: [], provider: [], model: [] },
      dimension_legends: delta.dimension_legends || { vendor: [], provider: [], model: [] },
      status_legends: delta.status_legends,
    }
    return
  }
  const s = liveStreamState.snapshot
  s.summary = delta.summary
  s.status_legends = delta.status_legends
  for (const dim of ['vendor', 'provider', 'model'] as const) {
    if (delta.changed_lanes[dim]) {
      s.dimensions[dim] = mergeLaneList(s.dimensions[dim] || [], delta.changed_lanes[dim])
      s.detail_dimensions[dim] = mergeLaneList(s.detail_dimensions[dim] || [], delta.changed_lanes[dim])
    }
    if (delta.dimension_legends && delta.dimension_legends[dim]) {
      s.dimension_legends[dim] = delta.dimension_legends[dim]
    }
  }
}

function openConnection() {
  if (es) return
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
  idIndex.clear()
  pending.length = 0
}
export function reconnectStream() {
  closeConnection()
  openConnection()
}
export function setOnRequestEvicted(cb: ((id: string) => void) | null) {
  onEvictCb = cb
}

export const __testing = {
  state: liveStreamState,
  idIndex,
  pending,
  pushOrQueue,
  handleEnvelope,
  applyInitialData,
  mergeDelta,
  mergeLaneList,
  laneDataEqual,
  resetStream,
  refCount: () => refCount,
  es: () => es,
  MAX_VISIBLE,
}
