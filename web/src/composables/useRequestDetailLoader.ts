// useRequestDetailLoader — phased async load + short TTL cache per requestId.
import { computed, ref, shallowRef } from 'vue'
import { getRequestLogDetail, type RequestLogDetail, type RoutingAttempt } from '../api/logs'
import {
  getUnifiedRequestDetail,
  type UnifiedRequestDetail,
} from '../api/requestDetail'
import {
  fetchWaterfallByRequestId,
  type WaterfallAttempt,
  type WaterfallRequest,
} from '../api/dispatch'
import { getSessionSnapshot } from '../api/sessions_v2'

export type DetailSection =
  | 'overview'
  | 'chat'
  | 'waterfall'
  | 'attempts'
  | 'flow'
  | 'compress'
  | 'attachments'
  | 'raw'

const CACHE_TTL_MS = 45_000

interface CacheEntry {
  at: number
  log: RequestLogDetail | null
  unified: UnifiedRequestDetail | null
  bodiesLoaded: boolean
  waterfall: WaterfallRequest | null
  waterfallSource: string
  sessionSnap: Record<string, unknown> | null
}

const cache = new Map<string, CacheEntry>()

function cacheGet(id: string): CacheEntry | null {
  const e = cache.get(id)
  if (!e) return null
  if (Date.now() - e.at > CACHE_TTL_MS) {
    cache.delete(id)
    return null
  }
  return e
}

function cachePut(id: string, patch: Partial<CacheEntry>) {
  const prev = cache.get(id)
  cache.set(id, {
    at: Date.now(),
    log: patch.log !== undefined ? patch.log : (prev?.log ?? null),
    unified: patch.unified !== undefined ? patch.unified : (prev?.unified ?? null),
    bodiesLoaded: patch.bodiesLoaded !== undefined ? patch.bodiesLoaded : (prev?.bodiesLoaded ?? false),
    waterfall: patch.waterfall !== undefined ? patch.waterfall : (prev?.waterfall ?? null),
    waterfallSource: patch.waterfallSource !== undefined ? patch.waterfallSource : (prev?.waterfallSource ?? ''),
    sessionSnap: patch.sessionSnap !== undefined ? patch.sessionSnap : (prev?.sessionSnap ?? null),
  })
}

export function mapLogRoutingAttempts(attempts: RoutingAttempt[] | undefined): WaterfallAttempt[] {
  if (!attempts?.length) return []
  return attempts.map((a, i) => ({
    attempt_id: `log-${a.seq ?? i}`,
    attempt_no: a.seq ?? i + 1,
    model: a.raw_model,
    provider_id: a.provider_id,
    credential_id: a.credential_id,
    outcome: a.result,
    error_kind: a.error_message || undefined,
  }))
}

/** Test helper — clear module cache between tests. */
export function clearRequestDetailCache() {
  cache.clear()
}

export function useRequestDetailLoader() {
  const loadSeq = ref(0)
  let abort: AbortController | null = null

  const metaLoading = ref(false)
  const metaError = ref('')
  const log = ref<RequestLogDetail | null>(null)
  const unified = ref<UnifiedRequestDetail | null>(null)
  const sessionSnap = ref<Record<string, unknown> | null>(null)

  const bodiesLoading = ref(false)
  const waterfallLoading = ref(false)
  const waterfallError = ref('')
  const waterfall = shallowRef<WaterfallRequest | null>(null)
  const waterfallSource = ref('')
  const bodiesLoaded = ref(false)

  const sessionId = computed(
    () => log.value?.gw_session_id || unified.value?.meta.gw_session_id || null,
  )
  const requestBody = computed(
    () => unified.value?.bodies?.request_body ?? log.value?.request_body ?? null,
  )
  const responseBody = computed(
    () => unified.value?.bodies?.response_body ?? log.value?.response_body ?? null,
  )
  const outboundBody = computed(
    () => unified.value?.bodies?.outbound_body ?? log.value?.outbound_body ?? null,
  )
  const attempts = computed((): WaterfallAttempt[] => {
    if (waterfall.value?.attempts?.length) return waterfall.value.attempts
    return mapLogRoutingAttempts(log.value?.routing_attempts?.attempts)
  })

  function bumpSeq(): number {
    abort?.abort()
    abort = new AbortController()
    loadSeq.value += 1
    return loadSeq.value
  }

  function applyEntry(e: CacheEntry) {
    log.value = e.log
    unified.value = e.unified
    bodiesLoaded.value = e.bodiesLoaded
    waterfall.value = e.waterfall
    waterfallSource.value = e.waterfallSource
    sessionSnap.value = e.sessionSnap
  }

  async function loadMeta(requestId: string) {
    const cached = cacheGet(requestId)
    if (cached?.log || cached?.unified) {
      bumpSeq()
      applyEntry(cached)
      metaLoading.value = false
      metaError.value = ''
      if (sessionId.value && !sessionSnap.value) void ensureSessionSnap(sessionId.value)
      return
    }

    const seq = bumpSeq()
    log.value = null
    unified.value = null
    waterfall.value = null
    waterfallSource.value = ''
    waterfallError.value = ''
    metaError.value = ''
    bodiesLoaded.value = false
    sessionSnap.value = null
    metaLoading.value = true
    try {
      const [u, meta] = await Promise.all([
        getUnifiedRequestDetail(requestId, { omitBody: true }).catch(() => null),
        getRequestLogDetail(requestId, { omitBody: true }).catch(() => null),
      ])
      if (seq !== loadSeq.value) return
      unified.value = u
      log.value = meta
      if (!u && !meta) {
        metaError.value = '请求详情未找到'
        return
      }
      cachePut(requestId, { log: meta, unified: u, bodiesLoaded: false })
      const sid = meta?.gw_session_id || u?.meta.gw_session_id
      if (sid) void ensureSessionSnap(sid)
    } catch (e: unknown) {
      if (seq !== loadSeq.value) return
      metaError.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (seq === loadSeq.value) metaLoading.value = false
    }
  }

  async function ensureSessionSnap(sid: string) {
    const seq = loadSeq.value
    try {
      const snap = await getSessionSnapshot(sid, { signal: abort?.signal })
      if (seq !== loadSeq.value) return
      sessionSnap.value = snap
      const rid = log.value?.request_id || unified.value?.meta.request_id
      if (rid) cachePut(rid, { sessionSnap: snap })
    } catch {
      /* snapshot optional */
    }
  }

  async function ensureBodies(requestId: string) {
    if (bodiesLoaded.value || unified.value?.bodies) {
      bodiesLoaded.value = true
      return
    }
    const hit = cacheGet(requestId)
    if (hit?.bodiesLoaded && hit.log) {
      log.value = hit.log
      unified.value = hit.unified
      bodiesLoaded.value = true
      return
    }
    const seq = loadSeq.value
    bodiesLoading.value = true
    try {
      const full = await getRequestLogDetail(requestId)
      if (seq !== loadSeq.value) return
      const merged = log.value ? { ...log.value, ...full } : full
      log.value = merged
      bodiesLoaded.value = true
      cachePut(requestId, { log: merged, unified: unified.value, bodiesLoaded: true })
    } catch {
      /* meta-only ok */
    } finally {
      if (seq === loadSeq.value) bodiesLoading.value = false
    }
  }

  async function ensureWaterfall(requestId: string) {
    if (waterfall.value?.request_id === requestId) return
    const hit = cacheGet(requestId)
    if (hit?.waterfall?.request_id === requestId) {
      waterfall.value = hit.waterfall
      waterfallSource.value = hit.waterfallSource
      return
    }
    const seq = loadSeq.value
    waterfallLoading.value = true
    waterfallError.value = ''
    try {
      const res = await fetchWaterfallByRequestId(requestId, { signal: abort?.signal })
      if (seq !== loadSeq.value) return
      waterfall.value = res.request
      waterfallSource.value = res.source || ''
      cachePut(requestId, {
        waterfall: res.request,
        waterfallSource: res.source || '',
      })
    } catch (e: unknown) {
      if (seq !== loadSeq.value) return
      waterfall.value = null
      waterfallError.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (seq === loadSeq.value) waterfallLoading.value = false
    }
  }

  async function onSectionNeed(requestId: string, section: DetailSection) {
    // Overview needs bodies for turn Q&A card (preview fields are fallback).
    if (
      section === 'overview'
      || section === 'chat'
      || section === 'raw'
      || section === 'compress'
      || section === 'attachments'
    ) {
      await ensureBodies(requestId)
    }
    if (section === 'waterfall' || section === 'attempts') {
      await ensureWaterfall(requestId)
    }
  }

  function dispose() {
    abort?.abort()
    abort = null
  }

  return {
    metaLoading,
    metaError,
    log,
    unified,
    sessionSnap,
    sessionId,
    requestBody,
    responseBody,
    outboundBody,
    bodiesLoading,
    bodiesLoaded,
    waterfallLoading,
    waterfallError,
    waterfall,
    waterfallSource,
    attempts,
    loadMeta,
    ensureBodies,
    ensureWaterfall,
    onSectionNeed,
    dispose,
    loadSeq,
  }
}
