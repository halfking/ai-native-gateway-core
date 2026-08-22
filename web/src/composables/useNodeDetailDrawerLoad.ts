import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { isDefaultTenant, isSuperAdmin } from '../store'
import type { RoutingCandidate } from '../api/routing'
import type { CredentialLifecycleStatus } from '../api/providers'
import type {
  CredentialModelStatus, CredentialMonitorSummary, CredentialRoutingDecision,
  ModelHistoryEvent, WindowStats,
} from '../api/credential-monitor'
import { nodesRef, type LiveNodeStatus } from '../composables/liveStreamStore'
import { createNodeDetailFetchers } from './nodeDetailDrawerFetch'

export type NodeDetailTab = 'detail' | 'availability' | 'requests' | 'other-models' | 'settings'
export type NodeDetailDrawerProps = {
  modelValue: boolean
  node: LiveNodeStatus | null
  model?: string
  initialTab?: NodeDetailTab
  seedCandidate?: RoutingCandidate | null
  requireSuperAdminEdit?: boolean
}

export function useNodeDetailDrawerLoad(
  props: NodeDetailDrawerProps,
  emit: { (e: 'update:modelValue', value: boolean): void },
) {
  const activeTab = ref<NodeDetailTab>('detail')
  const otherModelsLoaded = ref(false)
  const otherModelsLoading = ref(false)
  const loading = ref(false)
  const detailLoaded = ref(false); const detailLoading = ref(false)
  const requestsLoaded = ref(false); const requestsLoading = ref(false)
  const settingsLoaded = ref(false); const settingsLoading = ref(false)
  const loadError = ref('')
  const candidate = ref<RoutingCandidate | null>(null)
  const candidateLoading = ref(false)
  const monitor = ref<CredentialMonitorSummary | null>(null)
  const selectedModel = ref('')
  const windowEntries = ref<Array<{ rid: string; ts: number; ok: boolean; lat: number; err?: string }>>([])
  const windowStats = ref<WindowStats | null>(null)
  const windowSource = ref('')
  const history = ref<ModelHistoryEvent[]>([])
  const decisions = ref<CredentialRoutingDecision[]>([])
  const saving = ref(false)
  const actionMessage = ref(''); const actionError = ref('')
  const pingResult = ref<{ status: string; latency_ms: number; tested_at: string; error?: string } | null>(null)
  const lifecycle = ref<CredentialLifecycleStatus>('active')
  const manualPriority = ref(99); const routingTier = ref(2); const weight = ref(100)
  const modelActionReason = ref('')
  const coreLoaded = ref(false); const coreLoading = ref(false)
  const detailRequestId = ref<string | null>(null)
  const sequence = { n: 0 }
  const loadController = { c: null as AbortController | null }
  const requestsController = { c: null as AbortController | null }
  const coreController = { c: null as AbortController | null }
  const coreTask = { p: null as Promise<boolean> | null }
  const coreCandidateTask = { p: null as Promise<boolean> | null }

  const visible = computed({
    get: () => props.modelValue,
    set: (v: boolean) => emit('update:modelValue', v),
  })
  const canEdit = computed(() => {
    if (!isDefaultTenant()) return false
    return props.requireSuperAdminEdit ? isSuperAdmin() : true
  })
  const editGateHint = computed(() => {
    if (props.requireSuperAdminEdit && !isSuperAdmin()) return '仅超级管理员可以维护节点；当前以只读方式展示。'
    if (!isDefaultTenant()) return '仅 default 租户可以维护节点；当前以只读方式展示。'
    return ''
  })
  const currentNode = computed<LiveNodeStatus | null>(() => {
    if (!props.node) return null
    return nodesRef.value.find(n => n.credential_id === props.node?.credential_id) ?? props.node
  })
  const scopedModel = computed(() => props.model?.trim() || '')
  const allModels = computed(() => monitor.value?.models ?? [])
  const models = computed(() => {
    const scope = scopedModel.value.toLowerCase()
    if (!scope) return allModels.value
    return allModels.value.filter(m => m.raw_model_name.toLowerCase() === scope)
  })
  const selectedModelStatus = computed(() => allModels.value.find(m => m.raw_model_name === selectedModel.value) ?? null)
  const resolvedProviderId = computed(() => currentNode.value?.provider_id ?? candidate.value?.provider_id ?? monitor.value?.provider_id ?? null)
  const headlineState = computed(() => {
    const n = currentNode.value
    if (!n) return '未知'
    if (n.manual_disabled || n.disable_kind === 'manual') return '手工禁用'
    if (n.circuit_state === 'open') return '熔断'
    if (n.health_status === 'unreachable' || n.availability_state === 'unreachable') return '不可达'
    if (n.availability_state === 'cooling' || n.circuit_state === 'half_open') return '恢复中'
    return '可用'
  })
  const errorKinds = computed(() => Object.entries(windowStats.value?.error_kinds ?? {}))
  const failedWindowEntries = computed(() => windowEntries.value.filter(e => !e.ok).slice(0, 40))
  const failedDecisions = computed(() => decisions.value.filter(d => !d.success).slice(0, 40))
  const otherModelsNeedRefresh = computed(() => (monitor.value?.models?.length ?? 0) <= 1)

  const { loadCandidate, loadDecisionsOnly, startCorePreload, loadNode } = createNodeDetailFetchers({
    sequence, loadController, requestsController, coreController, coreTask, coreCandidateTask,
    visible, currentNode, scopedModel, loading, loadError, candidate, candidateLoading, monitor,
    selectedModel, windowEntries, windowStats, windowSource, history, decisions, lifecycle,
    manualPriority, routingTier, weight, coreLoaded, coreLoading,
  })

  function resetDrawerData() {
    loadController.c?.abort(); requestsController.c?.abort(); coreController.c?.abort()
    loadController.c = null; requestsController.c = null; coreController.c = null
    coreTask.p = null; coreCandidateTask.p = null; sequence.n++
    loading.value = false; coreLoaded.value = false; coreLoading.value = false
    detailLoaded.value = false; detailLoading.value = false
    requestsLoaded.value = false; requestsLoading.value = false
    settingsLoaded.value = false; settingsLoading.value = false
    loadError.value = ''; candidate.value = null; candidateLoading.value = false
    monitor.value = null; selectedModel.value = ''
    windowEntries.value = []; windowStats.value = null; windowSource.value = ''
    history.value = []; decisions.value = []; pingResult.value = null
    actionMessage.value = ''; actionError.value = ''; detailRequestId.value = null
    otherModelsLoaded.value = false; otherModelsLoading.value = false
  }
  function detailEntriesReset() {
    windowEntries.value = []; windowStats.value = null; windowSource.value = ''; history.value = []
  }

  function mergeMonitorModels(incoming: CredentialModelStatus[]) {
    if (!monitor.value) {
      monitor.value = {
        id: currentNode.value?.credential_id ?? 0, provider_id: currentNode.value?.provider_id ?? 0,
        provider_name: '', label: '', status: '', availability_state: '', health_status: '', quota_state: '',
        concurrency_limit: null, concurrency_limit_auto: null, effective_concurrency: 0, manual_disabled: false,
        consecutive_failures: 0, availability_recover_at: null, state_reason_code: null, state_reason_detail: null,
        health_checked_at: null, total_requests: 0, model_total: incoming.length,
        model_available: incoming.filter(m => m.effective_state === 'available').length,
        broken_model_count: incoming.filter(m => m.effective_state === 'probe_broken').length, models: incoming,
      }
      return
    }
    monitor.value = { ...monitor.value, models: incoming }
  }

  async function ensureTabLoaded(tab: NodeDetailTab) {
    if (!visible.value || !currentNode.value) return
    if (tab === 'detail' && !detailLoaded.value && !detailLoading.value) {
      detailEntriesReset(); detailLoading.value = true
      detailLoaded.value = await loadNode(); detailLoading.value = false
    } else if (tab === 'availability') {
      if (!candidate.value && props.seedCandidate) candidate.value = props.seedCandidate
      if (!detailLoaded.value && !detailLoading.value) {
        detailEntriesReset(); detailLoading.value = true
        detailLoaded.value = await loadNode(); detailLoading.value = false
      } else if (!candidate.value && !candidateLoading.value) {
        const model = scopedModel.value || selectedModel.value
        if (model) await loadCandidate(model, sequence.n, new AbortController().signal)
      }
    } else if (tab === 'requests' && !requestsLoaded.value && !requestsLoading.value) {
      decisions.value = []; requestsLoading.value = true
      requestsLoaded.value = await loadDecisionsOnly(); requestsLoading.value = false
    } else if (tab === 'other-models' && !otherModelsLoaded.value && !otherModelsLoading.value) {
      otherModelsLoading.value = true
      if (!detailLoaded.value) {
        if (!detailLoading.value) {
          detailEntriesReset(); detailLoading.value = true
          detailLoaded.value = await loadNode(); detailLoading.value = false
        } else {
          await new Promise<void>(resolve => {
            const timer = setInterval(() => {
              if (!detailLoading.value) { clearInterval(timer); resolve() }
            }, 20)
          })
        }
      }
      otherModelsLoaded.value = true
      otherModelsLoading.value = false
    } else if (tab === 'settings' && !settingsLoaded.value && !settingsLoading.value) {
      settingsLoading.value = true
      const loaded = await startCorePreload()
      settingsLoading.value = false
      settingsLoaded.value = loaded === true || coreLoaded.value || candidate.value != null
    }
  }

  function bootstrapAllTabs() {
    // Only consume `seedCandidate` when the user hasn't yet touched the
    // form fields. Otherwise we'd overwrite live edits with stale values
    // from a previous open cycle (the parent may pass a snapshot that
    // has not been refreshed).
    const formIsPristine =
      lifecycle.value === 'active'
      && manualPriority.value === 99
      && routingTier.value === 2
      && weight.value === 100
    if (props.seedCandidate && formIsPristine) {
      candidate.value = props.seedCandidate
      lifecycle.value = (props.seedCandidate.lifecycle_status ?? 'active') as CredentialLifecycleStatus
      manualPriority.value = props.seedCandidate.manual_priority ?? 99
      routingTier.value = props.seedCandidate.tier ?? 2
      weight.value = props.seedCandidate.weight ?? 100
    }
    void ensureTabLoaded('detail')
    void ensureTabLoaded('availability')
    void ensureTabLoaded('requests')
    void ensureTabLoaded('settings')
  }

  function chooseModel(model: string) {
    if (model === selectedModel.value || !detailLoaded.value) return
    selectedModel.value = model; detailLoaded.value = false
    void ensureTabLoaded('detail')
  }

  async function refreshCurrentTab() {
    if (activeTab.value === 'requests') { requestsLoaded.value = false; await ensureTabLoaded('requests'); return }
    if (activeTab.value === 'other-models') { otherModelsLoaded.value = false; await ensureTabLoaded('other-models'); return }
    if (activeTab.value === 'settings') {
      settingsLoaded.value = false; coreLoaded.value = false; coreLoading.value = false; coreTask.p = null
      await ensureTabLoaded('settings'); return
    }
    detailLoaded.value = false; await ensureTabLoaded('detail')
  }

  let lastWatchKey = ''
  watch(() => [props.modelValue, props.node?.credential_id, props.model] as const, ([open, credentialId, model]) => {
    if (!open) {
      lastWatchKey = ''
      resetDrawerData()
      return
    }
    const key = `${credentialId ?? ''}|${model ?? ''}`
    // Even when the key hasn't changed (same credential re-opened after
    // the parent briefly flipped modelValue), reset state so cached
    // settings/edits don't leak across open cycles.
    if (key !== lastWatchKey) {
      lastWatchKey = key
      resetDrawerData()
      bootstrapAllTabs()
    } else {
      resetDrawerData()
      bootstrapAllTabs()
    }
    activeTab.value = props.initialTab ?? 'detail'
  }, { immediate: true })
  watch(() => props.initialTab, (tab) => { if (props.modelValue && tab) activeTab.value = tab })
  watch(activeTab, (tab) => { if (visible.value) void ensureTabLoaded(tab) })
  onBeforeUnmount(() => { loadController.c?.abort(); requestsController.c?.abort(); coreController.c?.abort() })

  return {
    activeTab, otherModelsLoaded, otherModelsLoading, loading, detailLoaded, detailLoading, requestsLoaded, requestsLoading,
    settingsLoaded, settingsLoading, loadError, candidate, candidateLoading, monitor, selectedModel,
    windowEntries, windowStats, windowSource, history, decisions, saving, actionMessage, actionError,
    pingResult, lifecycle, manualPriority, routingTier, weight, modelActionReason, coreLoaded, coreLoading,
    detailRequestId, visible, canEdit, editGateHint, currentNode, allModels, models, selectedModelStatus,
    resolvedProviderId, headlineState, errorKinds, failedWindowEntries, failedDecisions, otherModelsNeedRefresh,
    mergeMonitorModels, chooseModel, refreshCurrentTab, ensureTabLoaded,
  }
}

export type NodeDetailDrawerLoad = ReturnType<typeof useNodeDetailDrawerLoad>
