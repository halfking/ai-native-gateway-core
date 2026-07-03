// liveStreamStore — module-level singleton EventSource for the
// dashboard swim lane. Multiple Vue components that call
// useLiveStream() share one EventSource; the connection is opened
// on the first acquire() and closed on the last release().
//
// Cookie auth (rule 20 §6.1): the JWT lives in an HttpOnly
// "llmgw_session" cookie. EventSource does not accept custom
// headers, but it does honour `credentials: 'include'` for same-
// origin requests — which is exactly what we want, no token shim.

import { reactive, computed, type ComputedRef } from 'vue'

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
}

export interface LiveStreamEnvelope {
  type: 'initial_data' | 'request' | 'idle_marker'
  ts: string
  request?: LiveRequest
  requests?: LiveRequest[]
}

export type ConnectionState = 'idle' | 'connecting' | 'open' | 'reconnecting' | 'closed' | 'unsupported'

export const liveStreamState = reactive({
  requests: [] as LiveRequest[],
  connection: 'idle' as ConnectionState,
  paused: false,
  lastEventAt: 0,
})

// Vue auto-unwraps `ref` and `reactive` proxies in templates.
// Exposing the reactive properties as ComputedRef keeps the
// type-safety of the composable's public API while letting
// `useLiveStream()` return a stable interface. We deliberately
// wrap the reactive property in a computed() so consumers do not
// need to know whether the underlying state is a ref or a
// reactive object property.
export const requestsRef: ComputedRef<LiveRequest[]> = computed(() => liveStreamState.requests)
export const connectionRef: ComputedRef<ConnectionState> = computed(() => liveStreamState.connection)
export const pausedRef: ComputedRef<boolean> = computed(() => liveStreamState.paused)
export const lastEventAtRef: ComputedRef<number> = computed(() => liveStreamState.lastEventAt)

export const MAX_VISIBLE = 60
export const ENDPOINT = '/api/admin/live-stream'

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
    if (idIndex.has(item.request_id)) return
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
}

function openConnection() {
  if (es) return
  if (typeof EventSource === 'undefined') {
    liveStreamState.connection = 'unsupported'
    return
  }
  try {
    es = new EventSource(ENDPOINT, { withCredentials: true })
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
  resetStream,
  refCount: () => refCount,
  es: () => es,
  MAX_VISIBLE,
}
