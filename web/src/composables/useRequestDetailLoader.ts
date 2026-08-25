// useRequestDetailLoader — phased async load for fullscreen request detail.
// Phase A: omit_body meta (blocks shell). Phase B/C: tab-triggered, cancellable.
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

export type DetailSection =
  | 'overview'
  | 'chat'
  | 'waterfall'
  | 'attempts'
  | 'flow'
  | 'compress'
  | 'attachments'
  | 'raw'

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

export function useRequestDetailLoader() {
  const loadSeq = ref(0)
  let abort: AbortController | null = null

  const metaLoading = ref(false)
  const metaError = ref('')
  const log = ref<RequestLogDetail | null>(null)
  const unified = ref<UnifiedRequestDetail | null>(null)

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

  function resetPayload() {
    log.value = null
    unified.value = null
    waterfall.value = null
    waterfallSource.value = ''
    waterfallError.value = ''
    metaError.value = ''
    bodiesLoaded.value = false
  }

  /** Phase A — parallel omit_body. Does not await bodies/compare/tree. */
  async function loadMeta(requestId: string) {
    const seq = bumpSeq()
    resetPayload()
    metaLoading.value = true
    try {
      const signal = abort?.signal
      const [u, meta] = await Promise.all([
        getUnifiedRequestDetail(requestId, { omitBody: true }).catch(() => null),
        getRequestLogDetail(requestId, { omitBody: true }).catch(() => null),
      ])
      if (seq !== loadSeq.value) return
      void signal
      unified.value = u
      log.value = meta
      if (!u && !meta) {
        metaError.value = '请求详情未找到'
      }
    } catch (e: unknown) {
      if (seq !== loadSeq.value) return
      metaError.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (seq === loadSeq.value) metaLoading.value = false
    }
  }

  /** Phase B — bodies only when chat/raw need them. */
  async function ensureBodies(requestId: string) {
    if (bodiesLoaded.value || unified.value?.bodies) {
      bodiesLoaded.value = true
      return
    }
    const seq = loadSeq.value
    bodiesLoading.value = true
    try {
      const full = await getRequestLogDetail(requestId)
      if (seq !== loadSeq.value) return
      if (log.value) {
        log.value = { ...log.value, ...full }
      } else {
        log.value = full
      }
      bodiesLoaded.value = true
    } catch {
      /* meta-only ok */
    } finally {
      if (seq === loadSeq.value) bodiesLoading.value = false
    }
  }

  /** Phase B — waterfall-by-id. */
  async function ensureWaterfall(requestId: string) {
    if (waterfall.value?.request_id === requestId) return
    const seq = loadSeq.value
    waterfallLoading.value = true
    waterfallError.value = ''
    try {
      const res = await fetchWaterfallByRequestId(requestId, { signal: abort?.signal })
      if (seq !== loadSeq.value) return
      waterfall.value = res.request
      waterfallSource.value = res.source || ''
    } catch (e: unknown) {
      if (seq !== loadSeq.value) return
      waterfall.value = null
      waterfallError.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (seq === loadSeq.value) waterfallLoading.value = false
    }
  }

  async function onSectionNeed(requestId: string, section: DetailSection) {
    if (section === 'chat' || section === 'raw' || section === 'compress' || section === 'attachments') {
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
