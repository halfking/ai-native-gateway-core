import type { Ref, ComputedRef } from 'vue'
import { resolveRouting, type RoutingCandidate } from '../api/routing'
import type { CredentialLifecycleStatus } from '../api/providers'
import {
  getCredentialDecisions, getCredentialMonitorSummary, getModelHistory, getSlidingWindow,
  type CredentialMonitorSummary, type CredentialRoutingDecision, type ModelHistoryEvent, type WindowStats,
} from '../api/credential-monitor'
import type { LiveNodeStatus } from './liveStreamStore'

export type FetchCtx = {
  sequence: { n: number }
  loadController: { c: AbortController | null }
  requestsController: { c: AbortController | null }
  coreController: { c: AbortController | null }
  coreTask: { p: Promise<boolean> | null }
  coreCandidateTask: { p: Promise<boolean> | null }
  visible: ComputedRef<boolean>
  currentNode: ComputedRef<LiveNodeStatus | null>
  scopedModel: ComputedRef<string>
  loading: Ref<boolean>
  loadError: Ref<string>
  candidate: Ref<RoutingCandidate | null>
  candidateLoading: Ref<boolean>
  monitor: Ref<CredentialMonitorSummary | null>
  selectedModel: Ref<string>
  windowEntries: Ref<Array<{ rid: string; ts: number; ok: boolean; lat: number; err?: string }>>
  windowStats: Ref<WindowStats | null>
  windowSource: Ref<string>
  history: Ref<ModelHistoryEvent[]>
  decisions: Ref<CredentialRoutingDecision[]>
  lifecycle: Ref<CredentialLifecycleStatus>
  manualPriority: Ref<number>
  routingTier: Ref<number>
  weight: Ref<number>
  coreLoaded: Ref<boolean>
  coreLoading: Ref<boolean>
}

const isAbort = (e: unknown) => e instanceof DOMException && e.name === 'AbortError'

export function createNodeDetailFetchers(ctx: FetchCtx) {
  async function loadModelDetails(model: string, reqSeq: number, signal: AbortSignal) {
    const node = ctx.currentNode.value
    if (!node || !model) return false
    const [windowResult, historyResult] = await Promise.allSettled([
      getSlidingWindow(node.credential_id, model, 60, { signal }),
      getModelHistory(node.credential_id, model, 30, { signal }),
    ])
    if (signal.aborted || reqSeq !== ctx.sequence.n || ctx.currentNode.value?.credential_id !== node.credential_id || ctx.selectedModel.value !== model) return false
    if (windowResult.status === 'fulfilled') {
      ctx.windowEntries.value = windowResult.value.entries
      ctx.windowStats.value = windowResult.value.stats
      ctx.windowSource.value = windowResult.value.source
    } else if (!isAbort(windowResult.reason)) {
      ctx.windowEntries.value = []; ctx.windowStats.value = null; ctx.windowSource.value = ''
    }
    if (historyResult.status === 'fulfilled') ctx.history.value = historyResult.value.events
    else if (!isAbort(historyResult.reason)) ctx.history.value = []
    const failed = [windowResult, historyResult].some(r => r.status === 'rejected' && !isAbort(r.reason))
    if (failed) ctx.loadError.value = '部分近期统计数据未能加载，请重试。'
    return !failed
  }

  async function loadCandidate(model: string, reqSeq: number, signal: AbortSignal) {
    const node = ctx.currentNode.value
    if (!node || !model) { ctx.candidate.value = null; return false }
    ctx.candidateLoading.value = true
    try {
      const result = await resolveRouting(model, undefined, false, { signal })
      if (signal.aborted || reqSeq !== ctx.sequence.n || ctx.currentNode.value?.credential_id !== node.credential_id || ctx.selectedModel.value !== model) return false
      ctx.candidate.value = result.candidates.find(i => i.credential_id === node.credential_id && i.model_name === model) ?? null
      ctx.lifecycle.value = (ctx.candidate.value?.lifecycle_status ?? 'active') as CredentialLifecycleStatus
      ctx.manualPriority.value = ctx.candidate.value?.manual_priority ?? 99
      ctx.routingTier.value = ctx.candidate.value?.tier ?? 2
      ctx.weight.value = ctx.candidate.value?.weight ?? 100
      return true
    } catch (error) {
      if (!isAbort(error) && reqSeq === ctx.sequence.n && ctx.currentNode.value?.credential_id === node.credential_id) {
        ctx.candidate.value = null; ctx.loadError.value = '未能加载路由候选设置，请重试。'
      }
      return false
    } finally {
      if (reqSeq === ctx.sequence.n) ctx.candidateLoading.value = false
    }
  }

  async function loadDecisionsOnly() {
    const node = ctx.currentNode.value
    if (!node || !ctx.visible.value) return false
    ctx.requestsController.c?.abort()
    const controller = new AbortController()
    ctx.requestsController.c = controller
    const reqSeq = ctx.sequence.n
    const scope = ctx.scopedModel.value
    try {
      const result = scope
        ? await getCredentialDecisions(node.credential_id, 50, scope, { signal: controller.signal })
        : await getCredentialDecisions(node.credential_id, 30, undefined, { signal: controller.signal })
      if (controller.signal.aborted || reqSeq !== ctx.sequence.n || ctx.currentNode.value?.credential_id !== node.credential_id) return false
      ctx.decisions.value = result.decisions ?? []
      return true
    } catch (error) {
      if (!isAbort(error) && reqSeq === ctx.sequence.n) ctx.loadError.value = '未能加载路由决策记录，请重试。'
      return false
    } finally {
      if (ctx.requestsController.c === controller) ctx.requestsController.c = null
    }
  }

  function startCorePreload(): Promise<boolean> | null {
    const node = ctx.currentNode.value
    if (!ctx.visible.value || !node || ctx.coreLoading.value || ctx.coreLoaded.value) return ctx.coreTask.p
    const controller = new AbortController()
    const reqSeq = ctx.sequence.n
    ctx.coreController.c = controller
    ctx.coreLoading.value = true
    const initialModel = ctx.scopedModel.value || node.raw_models?.[0] || ''
    ctx.selectedModel.value = initialModel
    const isCurrent = () => !controller.signal.aborted && reqSeq === ctx.sequence.n && ctx.currentNode.value?.credential_id === node.credential_id
    const monitorTask = getCredentialMonitorSummary(
      { credential_id: node.credential_id, mode: 'core' },
      { signal: controller.signal },
    ).then(result => {
      if (!isCurrent()) return false
      const incoming = result.credentials?.[0] ?? null
      if (!incoming) { if (!ctx.monitor.value) ctx.monitor.value = null; return true }
      const existing = ctx.monitor.value
      const keepModels = (existing?.models?.length ?? 0) >= (incoming.models?.length ?? 0) ? existing?.models : incoming.models
      ctx.monitor.value = { ...incoming, models: keepModels ?? incoming.models }
      return true
    }).catch(error => {
      if (!isAbort(error) && isCurrent()) ctx.loadError.value = '节点核心状态加载失败，仍展示实时状态。'
      return false
    })
    const candidateTask = initialModel ? loadCandidate(initialModel, reqSeq, controller.signal) : Promise.resolve(true)
    ctx.coreCandidateTask.p = candidateTask
    ctx.coreTask.p = monitorTask.then(ready => {
      if (isCurrent()) { ctx.coreLoaded.value = ready; ctx.coreLoading.value = false }
      return ready
    }).catch(() => false).finally(() => {
      if (ctx.coreController.c === controller) ctx.coreController.c = null
    })
    void candidateTask.finally(() => { if (ctx.coreCandidateTask.p === candidateTask) ctx.coreCandidateTask.p = null })
    return ctx.coreTask.p
  }

  async function loadNode(): Promise<boolean> {
    const node = ctx.currentNode.value
    if (!node || !ctx.visible.value) return false
    ctx.loadController.c?.abort()
    const controller = new AbortController()
    ctx.loadController.c = controller
    const reqSeq = ctx.sequence.n
    // Only reuse the in-flight candidate task if it belongs to the SAME
    // sequence (i.e. the SAME open cycle). Without this guard, closing and
    // reopening the drawer on a different credential could await a stale
    // task from the previous credential, blocking the new request until it
    // times out. The candidate write inside `loadCandidate` is still
    // gated by `currentNode.credential_id`, but the await would otherwise
    // keep us from showing the new detail until the previous task settles.
    const pendingCoreCandidateTask = ctx.coreCandidateTask.p
    ctx.loading.value = true
    ctx.loadError.value = ''
    const scope = ctx.scopedModel.value
    const initialModel = scope || ctx.selectedModel.value || node.raw_models?.[0] || ''
    const isCurrent = () => !controller.signal.aborted && reqSeq === ctx.sequence.n && ctx.currentNode.value?.credential_id === node.credential_id
    try {
      const monitorResult = await getCredentialMonitorSummary(
        { credential_id: node.credential_id, mode: 'detail' },
        { signal: controller.signal },
      )
      if (!isCurrent()) return false
      const incoming = monitorResult.credentials?.[0] ?? null
      if (incoming) {
        const existing = ctx.monitor.value
        const keepModels = (existing?.models?.length ?? 0) > (incoming.models?.length ?? 0) ? existing?.models : incoming.models
        ctx.monitor.value = { ...incoming, models: keepModels ?? incoming.models }
      }
      const preferred = scope
        ? initialModel
        : ctx.monitor.value?.models?.find(m => m.raw_model_name === initialModel)?.raw_model_name
          ?? ctx.monitor.value?.models?.find(m => m.probe_state === 'broken_confirmed')?.raw_model_name
          ?? ctx.monitor.value?.models?.[0]?.raw_model_name
          ?? initialModel
      ctx.selectedModel.value = preferred
      const candidateTask = ctx.candidate.value || !preferred
        ? Promise.resolve(true)
        : (pendingCoreCandidateTask && pendingCoreCandidateTask !== ctx.coreCandidateTask.p
            ? pendingCoreCandidateTask
            : loadCandidate(preferred, reqSeq, controller.signal))
      const [detailsLoaded, candidateLoaded] = await Promise.all([
        preferred ? loadModelDetails(preferred, reqSeq, controller.signal) : Promise.resolve(true),
        candidateTask,
      ])
      return isCurrent() && detailsLoaded && candidateLoaded
    } catch (error) {
      if (!isAbort(error) && isCurrent()) ctx.loadError.value = '未能加载节点明细；核心实时状态仍可用。'
      return false
    } finally {
      if (ctx.loadController.c === controller) ctx.loadController.c = null
      if (isCurrent()) ctx.loading.value = false
    }
  }

  return { loadCandidate, loadDecisionsOnly, startCorePreload, loadNode }
}
