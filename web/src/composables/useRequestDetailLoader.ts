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
import { getRequestJourney, type RequestJourney, type RequestJourneyEvent } from '../api/request-journeys'

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

export type RequestDetailAttempt = WaterfallAttempt & {
  source?: 'journey' | 'waterfall' | 'synthesized'
}

interface CacheEntry {
  at: number
  log: RequestLogDetail | null
  unified: UnifiedRequestDetail | null
  bodiesLoaded: boolean
  waterfall: WaterfallRequest | null
  waterfallSource: string
  journey: RequestJourney | null
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
  // Merge rule for each cache field: prefer patch.X when defined, otherwise
  // fall back to prev?.X, otherwise to the field's type-zero default.
  //
  // Why the explicit per-field table instead of `{ ...prev, ...patch, at }`:
  // object-spread cannot distinguish "patch did not pass this key" from
  // "patch explicitly set this key to the type-zero default" — which is
  // fine for most fields, but for `bodiesLoaded: false` and
  // `waterfallSource: ''` an empty/false patch would silently resurrect the
  // previous value. The schema co-located below makes it impossible to add
  // a new CacheEntry field without declaring its default.
  const next: CacheEntry = {
    at: Date.now(),
    log: patch.log !== undefined ? patch.log : (prev?.log ?? null),
    unified: patch.unified !== undefined ? patch.unified : (prev?.unified ?? null),
    bodiesLoaded: patch.bodiesLoaded !== undefined ? patch.bodiesLoaded : (prev?.bodiesLoaded ?? false),
    waterfall: patch.waterfall !== undefined ? patch.waterfall : (prev?.waterfall ?? null),
    waterfallSource: patch.waterfallSource !== undefined ? patch.waterfallSource : (prev?.waterfallSource ?? ''),
    journey: patch.journey !== undefined ? patch.journey : (prev?.journey ?? null),
    sessionSnap: patch.sessionSnap !== undefined ? patch.sessionSnap : (prev?.sessionSnap ?? null),
  }
  cache.set(id, next)
}

export function mapLogRoutingAttempts(attempts: RoutingAttempt[] | undefined): RequestDetailAttempt[] {
  if (!attempts?.length) return []
  return attempts.map((a, i) => ({
    attempt_id: `log-${a.seq ?? i}`,
    attempt_no: a.seq ?? i + 1,
    model: a.raw_model,
    provider_id: a.provider_id,
    credential_id: a.credential_id,
    outcome: a.result,
    error_kind: a.error_message || undefined,
    source: 'synthesized',
  }))
}

function journeyAttemptEvents(events: RequestJourneyEvent[] | undefined): RequestDetailAttempt[] {
  const byAttempt = new Map<string, WaterfallAttempt>()
  for (const event of [...(events ?? [])].sort((a, b) => a.seq - b.seq)) {
    const ref = event.attempt
    if (!ref) continue
    const key = ref.attempt_id || `attempt-${ref.attempt_no}`
    const current: RequestDetailAttempt = byAttempt.get(key) ?? {
      attempt_id: ref.attempt_id || key,
      attempt_no: ref.attempt_no,
      credential_id: ref.credential_id ?? 0,
      source: 'journey',
    }
    current.model ||= ref.model || event.model || event.resolved_model
    current.provider_id ??= ref.provider_id ?? event.provider_id
    if (current.credential_id === 0) current.credential_id = ref.credential_id ?? event.credential_id ?? 0
    if (event.event_type === 'attempt_started') current.started_at ||= event.occurred_at
    if (event.event_type === 'first_byte') current.first_byte_at ||= event.occurred_at
    if (event.event_type === 'attempt_succeeded' || event.event_type === 'attempt_failed') {
      current.ended_at ||= event.occurred_at
      current.outcome ||= event.outcome || (event.event_type === 'attempt_succeeded' ? 'success' : 'failure')
    }
    current.error_kind ||= event.error_kind
    byAttempt.set(key, current)
  }
  return [...byAttempt.values()].sort((a, b) => a.attempt_no - b.attempt_no)
}

export function mergeRequestAttempts(
  journey: Pick<RequestJourney, 'events'> | null | undefined,
  waterfallAttempts: WaterfallAttempt[] | undefined,
  routingAttempts: RoutingAttempt[] | undefined,
): RequestDetailAttempt[] {
  const merged = new Map<number, RequestDetailAttempt>()
  const sourceRank = (source?: RequestDetailAttempt['source']) =>
    source === 'journey' ? 3 : source === 'waterfall' ? 2 : 1
  const add = (attempt: RequestDetailAttempt) => {
    if (!Number.isFinite(attempt.attempt_no)) return
    const existing = merged.get(attempt.attempt_no)
    if (!existing) {
      merged.set(attempt.attempt_no, attempt)
      return
    }
    const preferred = sourceRank(attempt.source) >= sourceRank(existing.source) ? attempt : existing
    const fallback = preferred === attempt ? existing : attempt
    merged.set(attempt.attempt_no, {
      ...fallback,
      ...preferred,
      attempt_id: preferred.attempt_id || fallback.attempt_id,
      attempt_no: preferred.attempt_no,
      model: preferred.model ?? fallback.model,
      provider_id: preferred.provider_id ?? fallback.provider_id,
      credential_id: preferred.credential_id ?? fallback.credential_id,
      started_at: preferred.started_at ?? fallback.started_at,
      first_byte_at: preferred.first_byte_at ?? fallback.first_byte_at,
      ended_at: preferred.ended_at ?? fallback.ended_at,
      outcome: preferred.outcome ?? fallback.outcome,
      error_kind: preferred.error_kind ?? fallback.error_kind,
      source: preferred.source || fallback.source,
    })
  }
  for (const attempt of mapLogRoutingAttempts(routingAttempts)) add(attempt)
  for (const attempt of waterfallAttempts ?? []) {
    const detailAttempt = attempt as RequestDetailAttempt
    add({ ...detailAttempt, source: detailAttempt.source || 'waterfall' })
  }
  for (const attempt of journeyAttemptEvents(journey?.events)) add(attempt)
  return [...merged.values()].sort((a, b) => a.attempt_no - b.attempt_no)
}

/** Test helper — clear module cache between tests. */
export function clearRequestDetailCache() {
  cache.clear()
}

export function useRequestDetailLoader() {
  const loadSeq = ref(0)
  const activeRequestId = ref('')
  let abort: AbortController | null = null

  const metaLoading = ref(false)
  const metaError = ref('')
  const log = ref<RequestLogDetail | null>(null)
  const unified = ref<UnifiedRequestDetail | null>(null)
  const sessionSnap = ref<Record<string, unknown> | null>(null)
  const journey = shallowRef<RequestJourney | null>(null)

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
  const attempts = computed((): RequestDetailAttempt[] => mergeRequestAttempts(
    journey.value,
    waterfall.value?.attempts,
    log.value?.routing_attempts?.attempts,
  ))

  function bumpSeq(): number {
    abort?.abort()
    abort = new AbortController()
    loadSeq.value += 1
    return loadSeq.value
  }

  function resetTransientState() {
    // 2026-08-30: when the route navigates to a new request (or returns
    // from a session-mode back to a different single-request), previous
    // waterfall loading / error state must not bleed into the new view.
    // We deliberately keep the meta cache so a short back-and-forth
    // navigation still hits the in-memory entry, but drop every
    // section-specific signal so the UI doesn't show "加载失败" for a
    // request that hasn't even started loading yet.
    journey.value = null
    waterfall.value = null
    waterfallSource.value = ''
    waterfallError.value = ''
    bodiesLoaded.value = false
    bodiesLoading.value = false
    waterfallLoading.value = false
  }

  function applyEntry(e: CacheEntry) {
    log.value = e.log
    unified.value = e.unified
    if (e.journey) journey.value = e.journey
    else journey.value = null
    bodiesLoaded.value = e.bodiesLoaded
    if (e.waterfall) {
      waterfall.value = e.waterfall
      waterfallSource.value = e.waterfallSource
    } else {
      waterfall.value = null
      waterfallSource.value = ''
    }
    waterfallError.value = ''
    sessionSnap.value = e.sessionSnap
  }

  async function loadMeta(requestId: string) {
    activeRequestId.value = requestId
    resetTransientState()
    const cached = cacheGet(requestId)
    if (cached?.log || cached?.unified) {
      bumpSeq()
      applyEntry(cached)
      metaLoading.value = false
      metaError.value = ''
      if (sessionId.value && !sessionSnap.value) {
        void ensureSessionSnap(sessionId.value, abort?.signal)
      }
      return
    }

    const seq = bumpSeq()
    log.value = null
    unified.value = null
    sessionSnap.value = null
    metaLoading.value = true
    try {
      const [u, meta, durableJourney] = await Promise.all([
        getUnifiedRequestDetail(requestId, { omitBody: true }).catch(() => null),
        getRequestLogDetail(requestId, { omitBody: true }).catch(() => null),
        getRequestJourney(requestId).catch(() => null),
      ])
      if (seq !== loadSeq.value) return
      unified.value = u
      log.value = meta
      journey.value = durableJourney
      if (!u && !meta) {
        metaError.value = '请求详情未找到'
        return
      }
      cachePut(requestId, { log: meta, unified: u, journey: durableJourney, bodiesLoaded: false })
      const sid = meta?.gw_session_id || u?.meta.gw_session_id
      if (sid) void ensureSessionSnap(sid, abort?.signal)
    } catch (e: unknown) {
      if (seq !== loadSeq.value) return
      metaError.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (seq === loadSeq.value) metaLoading.value = false
    }
  }

  async function ensureSessionSnap(sid: string, signal?: AbortSignal) {
    // The session-snap fetch is fire-and-forget at every call site, so a
    // late-resolving snap can otherwise land in sessionSnap.value after the
    // user has already switched to a different request — polluting the new
    // view with the previous request's snapshot. The seq check below caught
    // most cases but not the case where the snapshot fires before bumpSeq
    // gets called (e.g. the cache-hit branch). Explicitly aborting on
    // signal.aborted guarantees we never resolve the snap into the wrong
    // request.
    if (signal?.aborted) return
    const seq = loadSeq.value
    try {
      const snap = await getSessionSnapshot(sid, { signal })
      if (seq !== loadSeq.value) return
      sessionSnap.value = snap
      const rid = log.value?.request_id || unified.value?.meta.request_id
      if (rid) cachePut(rid, { sessionSnap: snap })
    } catch {
      /* snapshot optional */
    }
  }

  async function ensureBodies(requestId: string) {
    if (requestId !== activeRequestId.value) return
    if (bodiesLoaded.value || unified.value?.bodies) {
      bodiesLoaded.value = true
      return
    }
    const hit = cacheGet(requestId)
    if (hit?.bodiesLoaded && hit.log && requestId === activeRequestId.value) {
      log.value = hit.log
      unified.value = hit.unified
      bodiesLoaded.value = true
      return
    }
    const seq = loadSeq.value
    bodiesLoading.value = true
    try {
      // The unified endpoint is the only source for in-flight details and
      // remains available when /api/logs/:id is denied or temporarily absent.
      // Load both projections and merge whichever body-bearing response
      // succeeds, rather than assuming the request-log endpoint is always
      // authoritative.
      const [fullLog, fullUnified] = await Promise.all([
        getRequestLogDetail(requestId).catch(() => null),
        getUnifiedRequestDetail(requestId).catch(() => null),
      ])
      if (seq !== loadSeq.value || requestId !== activeRequestId.value) return
      const merged = fullLog
        ? (log.value ? { ...log.value, ...fullLog } : fullLog)
        : log.value
      if (merged) log.value = merged
      if (fullUnified) unified.value = fullUnified
      if (merged || fullUnified) {
        bodiesLoaded.value = true
        cachePut(requestId, { log: merged, unified: fullUnified || unified.value, bodiesLoaded: true })
      }
    } catch {
      /* meta-only ok */
    } finally {
      if (seq === loadSeq.value) bodiesLoading.value = false
    }
  }

  async function ensureWaterfall(requestId: string) {
    if (requestId !== activeRequestId.value) return
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
