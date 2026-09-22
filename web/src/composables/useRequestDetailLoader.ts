// useRequestDetailLoader — phased async load + short TTL cache per requestId.
import { computed, ref, shallowRef } from 'vue'
import { ApiError } from '../api/_core'
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
import { getRequestJourney, type RequestJourney, type RequestJourneyEvent, type RequestJourneyEventType } from '../api/request-journeys'

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

// Sentinel stored in metaError when both detail endpoints return nothing for
// a request. Consumers match on this constant to render a localized message
// (e.g. the drawer maps it to t('requestDetail.drawer.notFound')) instead of
// string-matching a hardcoded Chinese literal.
export const REQUEST_DETAIL_NOT_FOUND = '请求详情未找到'

// 2026-09-05 (F2-#5): partial endpoint failures used to be silently swallowed
// by the drawer's local loader while the drawer showed a warnings list. The
// recorder moved here so every consumer of the composable gets the same
// degradation visibility without re-implementing per-endpoint bookkeeping.
function formatEndpointFailure(endpoint: string, e: unknown): string {
  const detail = e instanceof ApiError
    ? `HTTP ${e.status} ${e.message}`
    : e instanceof Error
      ? e.message
      : String(e)
  return `${endpoint}: ${detail}`
}

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

function eventTime(events: RequestJourneyEvent[] | undefined, type: RequestJourneyEventType): string | undefined {
  return events?.find((event) => event.event_type === type)?.occurred_at
}

function toMs(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, value) : 0
}

function isoOrUndefined(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function synthesizeWaterfallFromDetail(input: {
  requestId: string
  log: RequestLogDetail | null
  unified: UnifiedRequestDetail | null
  journey: RequestJourney | null
  attempts: RequestDetailAttempt[]
}): WaterfallRequest | null {
  const { requestId, log, unified, journey, attempts } = input
  const events = journey?.events
  const arrivedAt = eventTime(events, 'request_received') || isoOrUndefined(log?.ts)
  const modelEnqueuedAt = eventTime(events, 'model_enqueued')
  const credSelectedAt = eventTime(events, 'credential_selected')
  const nodeEnqueuedAt = eventTime(events, 'node_enqueued')
  const nodeSelectedAt = eventTime(events, 'node_selected')
  const firstAttemptStarted = [...(events ?? [])]
    .sort((a, b) => a.seq - b.seq)
    .find((event) => event.event_type === 'attempt_started')
    ?.occurred_at
  const firstByteAt = eventTime(events, 'first_byte')
  const terminalAt = eventTime(events, 'request_succeeded')
    || eventTime(events, 'request_failed')
    || eventTime(events, 'request_canceled')
  const latencyMs = toMs(log?.latency_ms ?? unified?.meta.latency_ms)
  const totalMs = terminalAt && arrivedAt
    ? Math.max(0, Date.parse(terminalAt) - Date.parse(arrivedAt))
    : latencyMs
  const result = log?.request_status
    || unified?.meta.request_status
    || (log?.success === false || unified?.meta.success === false ? 'failure' : 'success')
  const hasTiming = Boolean(arrivedAt || modelEnqueuedAt || firstAttemptStarted || firstByteAt || terminalAt || totalMs)
  if (!hasTiming && !attempts.length) return null
  return {
    request_id: requestId,
    tenant_id: unified?.meta.tenant_id || undefined,
    session_id: log?.gw_session_id || unified?.meta.gw_session_id || undefined,
    model: log?.outbound_model || log?.client_model || unified?.meta.client_model || undefined,
    credential_id: log?.credential_id ?? undefined,
    result: result || 'unknown',
    arrived_at: arrivedAt,
    model_enqueued_at: modelEnqueuedAt,
    cred_enqueued_at: nodeEnqueuedAt || credSelectedAt,
    cred_dequeued_at: nodeSelectedAt,
    forward_start_at: firstAttemptStarted,
    response_start_at: firstByteAt,
    response_end_at: terminalAt,
    waiting_in_total_ms: 0,
    waiting_in_model_ms: 0,
    waiting_in_node_ms: 0,
    routing_ms: 0,
    acquire_ms: 0,
    upstream_latency_ms: 0,
    streaming_duration_ms: 0,
    queue_wait_ms: 0,
    total_ms: totalMs,
    attempts,
  }
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
  const metaWarnings = ref<string[]>([])
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

  const lastKnownSessionId = ref<string | null>(null)
  const sessionId = computed(
    () => log.value?.gw_session_id || unified.value?.meta.gw_session_id || null,
  )
  const retainedSessionId = computed(() => sessionId.value || lastKnownSessionId.value)

  function rememberSession(sid: string | null | undefined) {
    lastKnownSessionId.value = sid || null
  }
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
    // 2026-09-05 (F2-#5): a metaError from the previous request (and any
    // endpoint-failure warnings) must not survive a request switch — the
    // drawer renders metaError inline even when the new request loads fine.
    metaError.value = ''
    metaWarnings.value = []
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
      rememberSession(cached.log?.gw_session_id || cached.unified?.meta.gw_session_id || null)
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
      // 2026-09-05 (F2-#5): record per-endpoint degradation in metaWarnings
      // (both endpoints failing concurrently degrade to null → notFound;
      // without the warnings list the partial failure would be invisible).
      // 2026-09-05 (round2 audit P2-1): each catch re-checks seq before
      // pushing — the user may have switched to another request while this
      // fetch was still in flight, and a stale endpoint failure must not
      // leak into the new view's freshly-cleared warnings list (same guard
      // as the outer catch / ensureSessionSnap below).
      const [u, meta] = await Promise.all([
        getUnifiedRequestDetail(requestId, { omitBody: true }).catch((e: unknown) => {
          if (seq === loadSeq.value && !(e instanceof ApiError && e.status === 404)) {
            metaWarnings.value.push(formatEndpointFailure('admin/request-detail', e))
          }
          return null
        }),
        getRequestLogDetail(requestId, { omitBody: true }).catch((e: unknown) => {
          if (seq === loadSeq.value && !(e instanceof ApiError && e.status === 404)) {
            metaWarnings.value.push(formatEndpointFailure('/api/logs/:id', e))
          }
          return null
        }),
      ])
      // 2026-09-05 (round2 audit P3-3): the journey read is a cold columnar
      // path and used to gate meta first paint inside the Promise.all above.
      // Fire it out-of-band: it lands seq-guarded once resolved (and is
      // merged into the cache entry so a TTL re-entry still restores it);
      // failures stay silent — same degradation semantics as before, without
      // delaying the drawer's full-panel loading.
      void getRequestJourney(requestId)
        .then((j) => {
          if (seq !== loadSeq.value) return
          journey.value = j
          if (j) cachePut(requestId, { journey: j })
          if (waterfallSource.value === 'derived' || waterfallSource.value === 'miss' || waterfallError.value) {
            const synthesized = synthesizeWaterfallFromDetail({
              requestId,
              log: log.value,
              unified: unified.value,
              journey: j,
              attempts: mergeRequestAttempts(
                j,
                waterfall.value?.attempts,
                log.value?.routing_attempts?.attempts,
              ),
            })
            if (synthesized) {
              waterfall.value = synthesized
              waterfallSource.value = 'derived'
              waterfallError.value = ''
              cachePut(requestId, {
                waterfall: synthesized,
                waterfallSource: 'derived',
              })
            }
          }
        })
        .catch(() => null)
      if (seq !== loadSeq.value) return
      // 2026-09-05 (round2 audit P2-2): a same-seq ensureBodies (e.g. the
      // fullscreen view's viewMode watcher fans out onSectionNeed in
      // parallel with loadMeta) may have landed first and cached a
      // bodiesLoaded entry for this id. The omitBody landing must not
      // clobber those bodies — merge: meta fields come from the fresh
      // responses, body-bearing fields stay from the cached entry.
      const prior = cacheGet(requestId)
      let landedU = u
      let landedLog = meta
      if (prior?.bodiesLoaded) {
        landedU = u
          ? (prior.unified?.bodies ? { ...u, bodies: prior.unified.bodies } : u)
          : prior.unified
        if (meta && prior.log) {
          landedLog = {
            ...prior.log,
            ...meta,
            request_body: meta.request_body ?? prior.log.request_body,
            response_body: meta.response_body ?? prior.log.response_body,
            outbound_body: meta.outbound_body ?? prior.log.outbound_body,
          }
        } else {
          landedLog = meta ?? prior.log
        }
      }
      unified.value = landedU
      log.value = landedLog
      if (!landedU && !landedLog) {
        metaError.value = REQUEST_DETAIL_NOT_FOUND
        return
      }
      cachePut(requestId, {
        log: landedLog,
        unified: landedU,
        // P2-2: never clear the bodiesLoaded marker the parallel
        // ensureBodies just set — the merge above preserved its bodies.
        bodiesLoaded: prior?.bodiesLoaded ?? false,
      })
      const sid = landedLog?.gw_session_id || landedU?.meta.gw_session_id || null
      rememberSession(sid)
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
    } catch (e: unknown) {
      // Snapshot stays optional (sessionSnap keeps its previous/null value),
      // but the drawer surfaces the degradation instead of failing silently.
      if (seq !== loadSeq.value) return
      metaWarnings.value.push(formatEndpointFailure('sessions/:id/snapshot', e))
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
      const synthesized = synthesizeWaterfallFromDetail({
        requestId,
        log: log.value,
        unified: unified.value,
        journey: journey.value,
        attempts: mergeRequestAttempts(
          journey.value,
          undefined,
          log.value?.routing_attempts?.attempts,
        ),
      })
      if (synthesized) {
        waterfall.value = synthesized
        waterfallSource.value = 'derived'
        waterfallError.value = ''
        cachePut(requestId, {
          waterfall: synthesized,
          waterfallSource: 'derived',
        })
        return
      }
      waterfall.value = null
      if (e instanceof ApiError && e.status === 404) {
        waterfallSource.value = 'miss'
        waterfallError.value = ''
      } else {
        waterfallError.value = e instanceof Error ? e.message : String(e)
      }
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
    metaWarnings,
    log,
    unified,
    sessionSnap,
    sessionId: retainedSessionId,
    activeRequestId,
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
