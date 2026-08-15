// probeStreamStore.ts — singleton SSE client for the self-check / node-probe
// stream (需求 6 bullet 8: 首页自检队列显示与"实时请求流"类似，从右侧加入新数据，
// 通过 SSE 与 Redis 队列数据打通).
//
// Mirrors composables/liveStreamStore.ts: one ref-counted EventSource against
// /api/admin/probe/stream. Tiles are appended from the right and collapsed in
// place by stable task ID across the pending → in-flight → ok|fail lifecycle.
// Backend delivers two forms: initial_data/snapshot_refresh (bulk) and
// submitted/started/completed/failed/idle_marker (incremental).
//
// SelfCheckPanel uses this for live updates while still calling the REST
// endpoints for the first paint and as a fallback when SSE is unavailable.
import { ref } from 'vue'
import { authBearer } from '../store'

// OBS-BE5 (25 号 §6 / 26 号 §4): tri-state origin badge carried by every task
// object. Old payloads without `origin` keep rendering (callers derive a
// fallback from `source`).
export type ProbeOrigin = 'scheduled' | 'error' | 'manual'

// SSE connection state — mirrors liveStreamStore's ConnectionState so panels
// can render a reconnecting chip instead of silently going stale.
export type ProbeConnectionState = 'connecting' | 'open' | 'reconnecting' | 'closed'

export interface ProbeStreamTile {
  id: string
  task_type: string
  source: string
  status: string // pending | in-flight | ok | fail
  credential_id: number
  provider_id?: number
  provider_code?: string
  raw_model?: string
  attempt?: number
  latency_ms?: number
  http_status?: number
  err_code?: string
  err_detail?: string
  scheduled?: boolean
  reason?: string
  // OBS-BE5: optional tri-state extras. next_retry_at_ms is only present on
  // pending re-arm rows — absent means "none", never zero-fake it.
  origin?: ProbeOrigin
  next_retry_at_ms?: number
  ts: number
}

const ENDPOINT = '/api/admin/probe/stream'
const MAX_VISIBLE = 200

const tiles = ref<ProbeStreamTile[]>([])
// Connection state for the reconnecting chip (26 号 §4: SSE 断线重连态展示).
const connection = ref<ProbeConnectionState>('closed')
let es: EventSource | null = null
let refCount = 0
let reconnectTimer: ReturnType<typeof setTimeout> | null = null

// Page-visibility gate (2026-08-15, mirrors liveStreamStore): while the tab
// is hidden we keep the SSE connection alive (cheap) but SKIP all state
// writes — no findIndex/push/slice churn, no reactive triggers, so hidden
// dashboards cost ~zero CPU. When the tab becomes visible again we clear the
// (now stale) tiles and reconnect so the backend's authoritative
// initial_data snapshot rebuilds the view instead of replaying a gap.
const visibility = {
  hidden: typeof document !== 'undefined' ? document.hidden : false,
  missed: false,
}
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && visibility.hidden) {
      visibility.hidden = false
      if (visibility.missed && refCount > 0) {
        visibility.missed = false
        tiles.value = []
        teardownEs()
        open()
      }
    } else if (document.hidden) {
      visibility.hidden = true
    }
  })
}

function buildUrl(): string {
  let url = ENDPOINT
  try {
    const token = authBearer()
    if (token) url += `?token=${encodeURIComponent(token)}`
  } catch { /* cookie auth fallback */ }
  return url
}

function collapseTile(tile: ProbeStreamTile) {
  // Right-append + collapse in place by id so pending→in-flight→terminal is
  // one moving tile (mirrors liveStreamStore.pushOrQueue).
  const idx = tiles.value.findIndex(t => t.id === tile.id)
  if (idx >= 0) {
    tiles.value[idx] = { ...tiles.value[idx], ...tile }
  } else {
    tiles.value.push(tile)
    if (tiles.value.length > MAX_VISIBLE) tiles.value = tiles.value.slice(-MAX_VISIBLE)
  }
}

function applyInitial(items: ProbeStreamTile[]) {
  if (!items?.length) return
  // Replace (bulk form = authoritative snapshot).
  tiles.value = items.slice(-MAX_VISIBLE)
}

function handleEvent(type: string, data: unknown) {
  // Hidden page: no rendering, no state churn — just remember we fell behind.
  if (visibility.hidden) {
    visibility.missed = true
    return
  }
  try {
    const payload = (data ?? {}) as Record<string, unknown>
    if (type === 'initial_data' || type === 'snapshot_refresh') {
      // OBS-BE5 wire form: ProbeStreamEnvelope { type, ts, initial: [...] }.
      // Keep the legacy `tasks`/`items` keys for older gateways.
      const items = (payload.initial ?? payload.tasks ?? payload.items ?? []) as ProbeStreamTile[]
      applyInitial(items)
      return
    }
    // submitted/started/completed/failed/idle_marker → one incremental tile.
    // The envelope nests the task under `task`; bare task payloads (older
    // wire form) are still accepted.
    const tile = (payload.task ?? payload) as unknown as ProbeStreamTile
    if (!tile || !tile.id) return
    collapseTile(tile)
  } catch { /* swallow malformed event */ }
}

function open() {
  if (es) return
  connection.value = 'connecting'
  try {
    es = new EventSource(buildUrl(), { withCredentials: true })
  } catch {
    es = null
    connection.value = 'reconnecting'
    scheduleReconnect()
    return
  }
  es.onopen = () => { connection.value = 'open' }
  es.onerror = () => {
    // Browser auto-reconnects; fall back to a manual retry if it gives up.
    connection.value = 'reconnecting'
    scheduleReconnect()
  }
  // Subscribe to all named event types the hub emits.
  const types = ['initial_data', 'snapshot_refresh', 'submitted', 'started', 'completed', 'failed', 'idle_marker']
  for (const t of types) {
    es.addEventListener(t, (ev: Event) => {
      const me = ev as MessageEvent
      if (!me.data) return
      try { handleEvent(t, JSON.parse(me.data)) } catch { /* ignore */ }
    })
  }
}

function scheduleReconnect() {
  if (reconnectTimer || refCount === 0) return
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null
    if (refCount === 0) return
    teardownEs()
    open()
  }, 5000)
}

function teardownEs() {
  if (es) {
    try { es.close() } catch { /* ignore */ }
    es = null
  }
  connection.value = 'closed'
}

export function useProbeStream() {
  return { tiles, connection }
}

export function acquireProbeStream() {
  refCount++
  if (refCount === 1) open()
  return () => releaseProbeStream()
}

export function releaseProbeStream() {
  refCount = Math.max(0, refCount - 1)
  if (refCount === 0) {
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null }
    teardownEs()
    tiles.value = []
  }
}

// resetProbeTiles clears the local cache (used when the panel is re-mounted
// after a long absence so the next initial_data is authoritative).
export function resetProbeTiles() {
  tiles.value = []
}
