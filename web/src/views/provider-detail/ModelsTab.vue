<script setup lang="ts">
import { ref, reactive, computed, onBeforeUnmount, watch, onMounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useFormat } from '../../i18n/useFormat'
import { useChart, createTimeSeriesConfig } from '../../composables/useChart'
import {
  getProviderModels,
  refreshProviderModels,
  clearProviderModels,
  getProviderRefreshStatus,
  toggleModelOfferState,
  getModelOfferSuggestions,
  updateModelOffer,
  getRoutableSummary,
  getRoutingBlockedDiagnostic,
  fixRoutingBlocked,
  triggerProviderProbeAll,
  resetNodeProbeState,
  getProviderCredentials,
  checkCredential,
  diagnoseProvider,
  type ModelOffer,
  type ModelOfferSuggestion,
  type ProbeAllResult,
  type ProviderRefreshRun,
  type RoutingBlockedDiagnostic,
} from '../../api'
import { getModelIQHistory, triggerModelIQTest, type IQHistoryPoint } from '../../api/model-iq'

const { t: td } = useI18n()
const pm = (k: string, params?: Record<string, unknown>): string =>
  td(`providerDetail.models.${k}` as never, params as never)
const { fmtDateTime } = useFormat()

const props = defineProps<{
  providerId: number
  /**
   * When set, the drawer for the matching offer opens as soon as offers
   * are loaded.  Used to deep-link from a `endpoint_id_required` probe
   * error so the operator lands directly on the outbound_model_name editor.
   * Cleared once consumed.
   */
  focusOffer?: { credential_id: number; raw_model_name: string } | null
}>()

const offers = ref<ModelOffer[]>([])
const loading = ref(false)
const error = ref('')

const refreshing = ref(false)
const clearing = ref(false)
const refreshRun = ref<ProviderRefreshRun | null>(null)
const refreshError = ref('')
let pollTimer: ReturnType<typeof setInterval> | null = null

const routable = ref<{
  total_bindings: number
  routable_bindings: number
  unavailable_bindings: number
  unavailable_breakdown: Record<string, number>
  routable_ratio: number
} | null>(null)
const routableLoading = ref(false)

const routingDiag = ref<RoutingBlockedDiagnostic | null>(null)
const routingDiagLoading = ref(false)
const routingDiagFixing = ref(false)
const routingDiagFixed = ref(false)
const routingDiagErr = ref('')

const probeAllLoading = ref(false)
const probeAllResults = ref<ProbeAllResult[]>([])
const probeAllSummary = ref<{ ok: number; model_unavailable: number; provider_error: number; skipped: number } | null>(null)

const selected = ref<ModelOffer | null>(null)

// Phase 3.2: Model check across credentials
const checkingModel = ref(false)
const modelCheckResults = ref<Array<{
  credential_id: number
  credential_label: string
  phase1_status?: 'ok' | 'warning' | 'error' | 'unavailable'
  phase1_message?: string
  phase2_status?: 'ok' | 'warning' | 'error' | null
  phase2_message?: string | null
  status: 'ok' | 'unavailable' | 'error' | 'warning'
  error: string | null
}> | null>(null)

interface EditDraft {
  standardized_name: string
  canonical_id: number | null
  /**
   * Upstream-side model identifier.  For Volcano Ark and similar providers
   * this is the deployment endpoint ID (e.g. "ep-20241227XXXX") that the
   * gateway must send in the request body instead of raw_model_name.
   * Empty string == use raw_model_name.
   */
  outbound_model_name: string
  /**
   * 2026-08-17: context window override for this credential-model binding.
   *
   * Runtime type widens beyond `number | null` because `<input type="number">`
   * with `v-model.number` yields `''` (empty string) when the user clears
   * the field — Vue only falls back to `null` if the input is removed
   * entirely.  We accept all three and normalize at dispatch time.
   *
   * Backend contract: JSON `null` is treated by Go as "field omitted" (no-op
   * write); `0` or any non-positive number clears the override; a positive
   * number sets the override.  We therefore never send JSON `null` for
   * intent-to-clear — we send `0`.  And we only include the field in the
   * PATCH body when the draft differs from the initial value, so that
   * editing other fields (e.g. `standardized_name`) does not silently
   * clobber an existing override.
   */
  context_window: number | '' | null
  saving: boolean
  toggling: boolean
  loadingSuggest: boolean
  suggest: ModelOfferSuggestion | null
  suggestErr: string
  saveErr: string
}
const draft = reactive<Partial<EditDraft>>({})

// 2026-08-17: snapshot of the binding's initial context-window override
// captured when the drawer opens.  Used by saveEdit() to detect whether
// the user actually edited the field, so we can omit the field from the
// PATCH body (preserving an existing override) when the user only edits
// unrelated fields like standardized_name.
const initialContextWindow = ref<number | null>(null)

// 2026-08-11: node IQ history for the drawer (time-series chart) + on-demand test.
const iqHistory = ref<IQHistoryPoint[]>([])
const iqHistoryLoading = ref(false)
const iqTestLoading = ref(false)
const iqTestError = ref('')

// IQ history trend chart (chart.js via useChart). History arrives newest-first;
// reverse for left-to-right time order.
const iqChartRef = ref<HTMLCanvasElement | null>(null)
const iqChartLabels = computed(() =>
  [...iqHistory.value].reverse().map(h => {
    const d = new Date(h.tested_at)
    return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }),
)
const iqChartConfig = computed(() =>
  createTimeSeriesConfig('line', iqChartLabels.value, [
    {
      label: pm('iqColScore'),
      data: [...iqHistory.value].reverse().map(h => h.overall_score),
      borderColor: '#58a6ff',
      backgroundColor: 'rgba(88,166,255,0.12)',
      tension: 0.3,
    },
    {
      label: pm('iqColAccuracy'),
      data: [...iqHistory.value].reverse().map(h => h.accuracy),
      borderColor: '#3fb950',
      backgroundColor: 'rgba(63,185,80,0.12)',
      tension: 0.3,
    },
  ], {
    scales: { y: { beginAtZero: true, max: 100 } },
    plugins: { legend: { display: true, labels: { boxWidth: 12, font: { size: 11 } } } },
  }),
)
const { initChart: iqInitChart, destroyChart: iqDestroyChart, isDisposed: iqChartDisposed } = useChart(iqChartRef, iqChartConfig)
let iqChartAlive = true
async function iqRefreshChart() {
  if (!iqChartAlive || iqChartDisposed()) return
  if (!iqHistory.value.length) {
    iqDestroyChart()
    return
  }
  await nextTick()
  if (!iqChartAlive || iqChartDisposed()) return
  iqInitChart()
}
watch(iqChartConfig, () => void iqRefreshChart(), { deep: true })
watch(() => iqHistory.value.length, () => void iqRefreshChart())
onMounted(() => void iqRefreshChart())
onBeforeUnmount(() => { iqChartAlive = false; iqDestroyChart() })

function iqBadgeClass(iq: number): string {
  if (iq >= 80) return 'iq-good'
  if (iq >= 60) return 'iq-ok'
  if (iq >= 40) return 'iq-warn'
  return 'iq-bad'
}

function nodeIqTitle(o: ModelOffer): string {
  const parts: string[] = []
  if (o.node_iq_avg != null) parts.push(pm('iqAvg', { v: o.node_iq_avg.toFixed(1) }))
  if (o.node_iq_sample_count) parts.push(pm('iqSamples', { n: o.node_iq_sample_count }))
  if (o.node_iq_tested_at) parts.push(pm('iqTestedAt', { t: timeText(o.node_iq_tested_at) }))
  return parts.join(' · ')
}

async function loadIQHistory() {
  const o = selected.value
  if (!o) return
  iqHistoryLoading.value = true
  iqTestError.value = ''
  try {
    iqHistory.value = await getModelIQHistory(o.credential_id, o.raw_model_name, 50)
  } catch (e: unknown) {
    iqHistory.value = []
  } finally {
    iqHistoryLoading.value = false
  }
}

async function runIQTest() {
  const o = selected.value
  if (!o || iqTestLoading.value) return
  iqTestLoading.value = true
  iqTestError.value = ''
  try {
    await triggerModelIQTest(o.credential_id, o.raw_model_name)
    await loadIQHistory()
    // Refresh the offers list so the new node_iq reflects in the table.
    await load()
    const refreshed = offers.value.find(x => x.id === o.id)
    if (refreshed) selected.value = refreshed
  } catch (e: unknown) {
    iqTestError.value = e instanceof Error ? e.message : pm('iqTestFailed')
  } finally {
    iqTestLoading.value = false
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    offers.value = await getProviderModels(props.providerId)
    maybeOpenFocusedOffer()
    routableLoading.value = true
    try {
      routable.value = await getRoutableSummary(props.providerId)
    } catch {
      routable.value = null
    } finally {
      routableLoading.value = false
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pm('loadFailed')
  } finally {
    loading.value = false
  }
}

async function loadRoutingDiagnostics() {
  routingDiagLoading.value = true
  routingDiagErr.value = ''
  try {
    routingDiag.value = await getRoutingBlockedDiagnostic(props.providerId)
  } catch (e: unknown) {
    routingDiagErr.value = e instanceof Error ? e.message : '诊断失败'
  } finally {
    routingDiagLoading.value = false
  }
}

async function handleFixBlocked() {
  if (!confirm('确定强制恢复该供应商所有被阻断的绑定？这将重置所有凭据状态、模型绑定和探测状态。')) return
  routingDiagFixing.value = true
  routingDiagErr.value = ''
  try {
    await fixRoutingBlocked(props.providerId)
    routingDiagFixed.value = true
    // Reload both views
    await Promise.all([load(), loadRoutingDiagnostics()])
    setTimeout(() => { routingDiagFixed.value = false }, 5000)
  } catch (e: unknown) {
    routingDiagErr.value = e instanceof Error ? e.message : '修复失败'
  } finally {
    routingDiagFixing.value = false
  }
}

function stopPolling() {
  if (pollTimer != null) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

async function pollRefreshStatus() {
  try {
    const resp = await getProviderRefreshStatus(props.providerId)
    const run = resp.running ?? resp.latest
    if (!run) return
    refreshRun.value = run
    if (run.status !== 'running') {
      stopPolling()
      refreshing.value = false
      if (run.status === 'succeeded') {
        await load()
      } else if (run.status === 'failed') {
        refreshError.value = run.message || run.errors?.join('; ') || pm('refreshFailed')
      }
    }
  } catch (e: unknown) {
    stopPolling()
    refreshing.value = false
    refreshError.value = e instanceof Error ? e.message : pm('refreshStatusFailed')
  }
}

async function clearModels() {
  if (clearing.value || refreshing.value) return
  if (!confirm(pm('clearConfirm'))) return
  clearing.value = true
  refreshError.value = ''
  try {
    const resp = await clearProviderModels(props.providerId)
    refreshRun.value = null
    await load()
    refreshError.value = ''
    refreshRun.value = {
      run_id: 'clear',
      provider_id: props.providerId,
      status: 'succeeded',
      started_at: new Date().toISOString(),
      finished_at: new Date().toISOString(),
      heartbeat_at: null,
      credentials_scanned: 0,
      models_upserted: 0,
      credentials_failed: 0,
      errors: [],
      message: pm('clearMessage', { n: resp.deleted }),
    }
  } catch (e: unknown) {
    refreshError.value = e instanceof Error ? e.message : pm('clearFailed')
  } finally {
    clearing.value = false
  }
}

async function refreshFromProvider() {
  if (refreshing.value) return
  refreshing.value = true
  refreshError.value = ''
  refreshRun.value = null
  stopPolling()
  try {
    const start = await refreshProviderModels(props.providerId)
    refreshRun.value = start.run
    if (start.run.status === 'running') {
      // Poll every 1.5s while the backend run is in flight; the
      // "正在从供应商读取数据…" hint is bound to the `refreshing`
      // ref and will clear once status flips to succeeded/failed.
      pollTimer = setInterval(pollRefreshStatus, 1500)
    } else if (start.run.status === 'succeeded') {
      refreshing.value = false
      await load()
    } else if (start.run.status === 'failed') {
      refreshing.value = false
      refreshError.value = start.run.message || pm('refreshFailed')
    }
  } catch (e: unknown) {
    refreshing.value = false
    refreshError.value = e instanceof Error ? e.message : pm('refreshFailed')
  }
}

const refreshSummary = computed(() => {
  const r = refreshRun.value
  if (!r || r.status === 'running') return ''
  const parts: string[] = []
  if (r.models_upserted > 0) {
    parts.push(pm('refreshSummaryUpserted', { n: r.models_upserted }))
  }
  if (r.credentials_failed > 0) {
    parts.push(pm('refreshSummaryFailed', { n: r.credentials_failed }))
  }
  if (parts.length === 0) {
    return r.message || pm('refreshSummaryEmpty')
  }
  return parts.join(' · ')
})

async function triggerAllProbes() {
  if (probeAllLoading.value) return
  probeAllLoading.value = true
  probeAllResults.value = []
  probeAllSummary.value = null
  try {
    const result = await triggerProviderProbeAll(props.providerId)
    probeAllResults.value = result.results
    probeAllSummary.value = {
      ok: result.ok,
      model_unavailable: result.model_unavailable,
      provider_error: result.provider_error,
      skipped: result.skipped,
    }
    // 2026-07-21 P0: probeAllSummary.ok>0 implies TriggerAllSync wrote
    // node_probe_state for those bindings. Surface a clear hint so
    // operators know the next reload should reflect new routing.
    if (result.ok > 0) {
      probeAllHint.value = pm('probeAllRecoveredHint', { n: result.ok })
    }
  } catch (e: unknown) {
    probeAllSummary.value = null
    alert(e instanceof Error ? e.message : pm('probeFailed'))
  } finally {
    probeAllLoading.value = false
  }
}

const resetNodeProbeLoading = ref(false)
const probeAllHint = ref('')

// 2026-07-21 P0: clear failed node_probe_state rows so the routing
// view v_routable_credential_models immediately re-admits bindings
// without waiting for NodeProbeWorker's 5s/30s/.../24h backoff.
// Useful when "全面探测" was run before this fix landed and stale
// failed rows still block routing.
async function resetFailedNodeProbes() {
  if (resetNodeProbeLoading.value) return
  if (!confirm(pm('resetNodeProbeConfirm'))) return
  resetNodeProbeLoading.value = true
  try {
    const r = await resetNodeProbeState(props.providerId)
    probeAllHint.value = pm('resetNodeProbeDone', { n: r.rows_updated })
    // Re-fetch the routable summary so the operator sees the
    // effect of the reset without having to reload the page.
    try {
      routable.value = await getRoutableSummary(props.providerId)
    } catch {
      routable.value = null
    }
  } catch (e: unknown) {
    alert(e instanceof Error ? e.message : pm('probeFailed'))
  } finally {
    resetNodeProbeLoading.value = false
  }
}

function probeResultBadge(category: string) {
  if (category === 'ok') return 'badge-green'
  if (category === 'model_unavailable') return 'badge-red'
  if (category === 'provider_error') return 'badge-amber'
  return 'badge-gray'
}

function probeResultLabel(category: string) {
  if (category === 'ok') return pm('probeCategory.ok')
  if (category === 'model_unavailable') return pm('probeCategory.unavailable')
  if (category === 'provider_error') return pm('probeCategory.providerError')
  if (category === 'skipped') return pm('probeCategory.skipped')
  return category
}

onBeforeUnmount(stopPolling)

function sourceLabel(v?: string | null) {
  if (v === 'auto') return pm('source.auto')
  if (v === 'manual') return pm('source.manual')
  return pm('source.never')
}

function timeText(v?: string | null) {
  if (!v) return '—'
  return fmtDateTime(v)
}

function statusBadge(status: string): string {
  if (status === 'active') return 'badge-green'
  if (status === 'degraded') return 'badge-amber'
  if (status === 'cooling') return 'badge-blue'
  return 'badge-red'
}

function resetDraft(o: ModelOffer) {
  draft.standardized_name = o.standardized_name ?? ''
  draft.canonical_id = o.canonical_id ?? null
  draft.outbound_model_name = o.outbound_model_name ?? ''
  // 2026-08-17: initialize the override input from the binding-level
  // override if set, otherwise leave it null/empty so the operator can
  // type a fresh value.  We deliberately do NOT pre-fill from the
  // effective (COALESCE'd) value — the read-only "effective" line below
  // already shows it, so pre-filling the input would invite accidental
  // "set as binding override" submissions that match the catalog value
  // and shadow any future catalog changes.  `initialContextWindow` is
  // captured for delta-detection at save time.
  draft.context_window = o.context_window_override ?? null
  initialContextWindow.value = o.context_window_override ?? null
  draft.saving = false
  draft.toggling = false
  draft.loadingSuggest = false
  draft.suggest = null
  draft.suggestErr = ''
  draft.saveErr = ''
}

async function openDrawer(o: ModelOffer) {
  selected.value = o
  resetDraft(o)
  iqHistory.value = []
  iqTestError.value = ''
  loadIQHistory()  // fire-and-forget; chart fills in when ready
  draft.loadingSuggest = true
  try {
    draft.suggest = await getModelOfferSuggestions(props.providerId, o.id)
  } catch (e: unknown) {
    draft.suggestErr = e instanceof Error ? e.message : pm('suggestionFailed')
  } finally {
    draft.loadingSuggest = false
  }
}

function closeDrawer() {
  selected.value = null
  modelCheckResults.value = null  // Clear check results when closing
  iqHistory.value = []
  iqTestError.value = ''
}

// Phase 3.2: Check model availability across all credentials with 2-phase validation
async function checkModelAcrossCredentials() {
  if (!selected.value) return

  checkingModel.value = true
  modelCheckResults.value = null

  try {
    const credentials = await getProviderCredentials(props.providerId)
    const modelName = selected.value.raw_model_name

    // Check each credential with 2-phase validation
    const results = await Promise.all(
      credentials.map(async (cred) => {
        // Phase 1: Static check - does the credential have this model in offers?
        const offerMatch = offers.value.find(
          offer => offer.credential_id === cred.id &&
                   offer.raw_model_name.toLowerCase() === modelName.toLowerCase()
        )

        if (!offerMatch) {
          // Phase 1 failed: Not in offers list
          return {
            credential_id: cred.id,
            credential_label: cred.label || cred.name || pm('creds.labelFallback', { id: cred.id }),
            phase1_status: 'unavailable' as const,
            phase1_message: pm('phase1Missing'),
            phase2_status: null,
            phase2_message: null,
            status: 'unavailable' as const,
            error: pm('offerNotInList')
          }
        }

        // Phase 1 passed: Model exists in offers
        // Phase 2: Dynamic check - run checkCredential for health check
        try {
          const result = await checkCredential(props.providerId, cred.id, modelName)

          // Analyze Phase 1: models_ok (ability to fetch model list)
          let phase1Status: 'ok' | 'warning' | 'error'
          let phase1Message: string

          if (result.models_ok) {
            phase1Status = 'ok'
            phase1Message = pm('phase1ModelsOk')
          } else if (result.effective_source === 'manifest' || result.effective_source === 'manifest_only') {
            phase1Status = 'warning'
            phase1Message = pm('phase1ManifestFallback')
          } else {
            phase1Status = 'error'
            const reason = result.models_failure_reason || result.models_error || ''
            phase1Message = pm('phase1Failed', { reason })
          }

          // Analyze Phase 2: probe_ok (actual chat test)
          let phase2Status: 'ok' | 'error' | null = null
          let phase2Message: string | null = null
          let finalStatus: 'ok' | 'error' | 'warning'
          let finalError: string | null = null

          if (result.probe_ok) {
            phase2Status = 'ok'
            phase2Message = pm('phase2ChatOk')
            finalStatus = 'ok'
          } else if (result.probe_error) {
            phase2Status = 'error'

            // Classify error types
            const error = result.probe_error.toLowerCase()
            const statusCode = result.probe_http_status

            if (statusCode === 401 || error.includes('unauthorized') || error.includes('invalid') || error.includes('api key')) {
              phase2Message = pm('phase2Auth', { msg: result.probe_error })
            } else if (statusCode === 402 || statusCode === 429 || error.includes('quota') || error.includes('insufficient') || error.includes('balance')) {
              phase2Message = pm('phase2Quota', { msg: result.probe_error })
            } else if (statusCode === 404 || error.includes('not found') || error.includes('does not exist')) {
              phase2Message = pm('phase2NotFound', { msg: result.probe_error })
            } else if (statusCode && statusCode >= 500) {
              phase2Message = pm('phase2Server', { msg: result.probe_error })
            } else if (error.includes('timeout') || error.includes('network')) {
              phase2Message = pm('phase2Network', { msg: result.probe_error })
            } else {
              phase2Message = pm('phase2Generic', { msg: result.probe_error })
            }

            if (statusCode) {
              phase2Message += ` (HTTP ${statusCode})`
            }

            finalStatus = 'error'
            finalError = phase2Message
          } else {
            // No probe result
            phase2Status = null
            phase2Message = pm('phase2Skipped')
            finalStatus = 'warning'
            finalError = pm('phase2Skipped')
          }

          return {
            credential_id: cred.id,
            credential_label: cred.label || cred.name || pm('creds.labelFallback', { id: cred.id }),
            phase1_status: phase1Status,
            phase1_message: phase1Message,
            phase2_status: phase2Status,
            phase2_message: phase2Message,
            status: finalStatus,
            error: finalError
          }
        } catch (e: unknown) {
          return {
            credential_id: cred.id,
            credential_label: cred.label || cred.name || pm('creds.labelFallback', { id: cred.id }),
            phase1_status: 'ok' as const,
            phase1_message: pm('phase2InOfferOnly'),
            phase2_status: 'error' as const,
            phase2_message: pm('phase2Generic', { msg: e instanceof Error ? e.message : String(e) }),
            status: 'error' as const,
            error: pm('checkFailedPrefix', { msg: e instanceof Error ? e.message : String(e) })
          }
        }
      })
    )

    modelCheckResults.value = results
  } catch (e: unknown) {
    alert(pm('checkFailedPrefix', { msg: e instanceof Error ? e.message : String(e) }))
  } finally {
    checkingModel.value = false
  }
}

function applyRuleBased() {
  if (!draft.suggest) return
  draft.standardized_name = draft.suggest.rule_based || draft.standardized_name || ''
  const match = draft.suggest.canonical_options.find(
    c => (c.canonical_name || '').toLowerCase() === (draft.standardized_name || '').toLowerCase()
  )
  draft.canonical_id = match ? match.id : null
}

function applyCanonical(canonicalId: number | null) {
  draft.canonical_id = canonicalId
  if (canonicalId != null && draft.suggest) {
    const match = draft.suggest.canonical_options.find(c => c.id === canonicalId)
    if (match) draft.standardized_name = match.canonical_name
  }
}

async function saveEdit() {
  const o = selected.value
  if (!o) return
  draft.saving = true
  draft.saveErr = ''
  // 2026-08-17: context window override payload.
  //
  // The Go backend uses a `*int` pointer to distinguish three states:
  //   1. field omitted (JSON null OR not present) → no-op (no DB write)
  //   2. present, value <= 0                     → clear override
  //   3. present, value > 0                      → set override
  //
  // Frontend behavior must match:
  //   • user untouched the field          → OMIT (preserve existing value)
  //   • user typed a positive integer      → SEND positive (set override)
  //   • user cleared / typed 0 / typed ""  → SEND 0 (clear override)
  //
  // We detect "untouched" by comparing draft.context_window to the
  // snapshot captured in resetDraft (initialContextWindow).  This is
  // critical: without it, editing only standardized_name would silently
  // clobber an existing override.
  //
  // We normalize "" to 0 because `<input type="number">` with
  // `v-model.number` yields "" when the user clears it — sending ""
  // would cause Go's json decoder to reject with HTTP400 "cannot
  // unmarshal string into int".
  const rawCw = draft.context_window
  const isTouched = rawCw !== initialContextWindow.value
  const body: {
    standardized_name?: string | null
    canonical_id?: number | null
    outbound_model_name?: string | null
    context_window?: number | null
  } = {
    standardized_name: (draft.standardized_name ?? '').trim() || null,
    canonical_id: draft.canonical_id ?? null,
    outbound_model_name: (draft.outbound_model_name ?? '').trim() || null,
  }
  if (isTouched) {
    const cw: number =
      rawCw == null || rawCw === '' || (typeof rawCw === 'number' && rawCw <= 0)
        ? 0
        : (rawCw as number)
    body.context_window = cw
  }
  try {
    const updated = await updateModelOffer(props.providerId, o.id, body)
    const idx = offers.value.findIndex(x => x.id === o.id)
    if (idx >= 0) {
      offers.value[idx] = {
        ...offers.value[idx],
        standardized_name: updated.standardized_name ?? '',
        canonical_id: updated.canonical_id,
        outbound_model_name: updated.outbound_model_name ?? null,
        context_window: updated.context_window,
        context_window_override: updated.context_window_override,
      }
      selected.value = offers.value[idx]
      // Re-sync the initial snapshot so a subsequent re-edit of the drawer
      // uses the post-save value as its baseline.
      initialContextWindow.value = updated.context_window_override ?? null
    }
  } catch (e: unknown) {
    draft.saveErr = e instanceof Error ? e.message : pm('saveFailed')
  } finally {
    draft.saving = false
  }
}

async function toggleAvailable() {
  const o = selected.value
  if (!o) return
  draft.toggling = true
  try {
    await toggleModelOfferState(props.providerId, o.id, { available: !o.available })
    await load()
    const refreshed = offers.value.find(x => x.id === o.id)
    if (refreshed) selected.value = refreshed
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : pm('operationFailed')
  } finally {
    draft.toggling = false
  }
}

function maybeOpenFocusedOffer() {
  const target = props.focusOffer
  if (!target) return
  const match = offers.value.find(
    o => o.credential_id === target.credential_id && o.raw_model_name === target.raw_model_name,
  )
  if (match) {
    openDrawer(match)
  }
}

// Watch for focusOffer changes so deep-links land on the right drawer
// even when offers are already loaded (e.g. when the user navigates
// probe → models → probe → models without a full page refresh).
watch(() => props.focusOffer, () => {
  if (offers.value.length > 0) {
    maybeOpenFocusedOffer()
  }
})

load()
</script>

<template>
  <div>
    <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px;flex-wrap:wrap;gap:8px">
      <h4 style="margin:0;display:flex;align-items:center;gap:10px;flex-wrap:wrap">
        <span>{{ pm('title', { n: offers.length }) }}</span>
        <span v-if="refreshing" class="refresh-hint refresh-hint--loading" role="status" aria-live="polite">
          <span class="refresh-spinner" aria-hidden="true"></span>
          {{ pm('refreshing') }}
        </span>
        <span
          v-else-if="refreshError"
          class="refresh-hint refresh-hint--error"
          role="status"
          aria-live="polite"
        >{{ refreshError }}</span>
        <span
          v-else-if="refreshRun && refreshRun.status !== 'running'"
          class="refresh-hint"
          :class="refreshRun.status === 'succeeded' ? 'refresh-hint--success' : 'refresh-hint--error'"
          role="status"
          aria-live="polite"
        >{{ refreshSummary }}</span>
      </h4>
      <div style="display:flex;gap:6px">
        <button
          class="btn btn-sm"
          :disabled="refreshing || clearing"
          :title="pm('refreshTitle')"
          @click="refreshFromProvider"
        >
          {{ refreshing ? pm('refreshLoading') : pm('refreshBtn') }}
        </button>
        <button
          class="btn btn-sm btn-ghost"
          :disabled="loading || refreshing || clearing || offers.length === 0"
          :title="pm('clearTitle')"
          @click="clearModels"
        >{{ clearing ? pm('clearLoading') : pm('clearBtn') }}</button>
        <button
          class="btn btn-sm btn-ghost"
          :disabled="loading || refreshing || clearing"
          :title="pm('refreshLocalTitle')"
          @click="load"
        >{{ loading ? pm('refreshLocalLoading') : pm('refreshLocalBtn') }}</button>
        <button
          class="btn btn-sm"
          :disabled="probeAllLoading || offers.length === 0"
          :title="pm('probeAllTitle')"
          @click="triggerAllProbes"
        >{{ probeAllLoading ? pm('probeAllLoading') : pm('probeAllBtn') }}</button>
        <!-- 2026-07-21 P0: companion to "全面探测". When a stale
             node_probe_state row is blocking a binding's routing
             (NodeProbeWorker backoff 5s..24h), the operator can
             force-clear it here without waiting for the ladder to
             naturally roll over. -->
        <button
          class="btn btn-sm btn-ghost"
          :disabled="resetNodeProbeLoading"
          :title="pm('resetNodeProbeTitle')"
          @click="resetFailedNodeProbes"
        >{{ resetNodeProbeLoading ? pm('resetNodeProbeLoading') : pm('resetNodeProbeBtn') }}</button>
      </div>
      <div v-if="probeAllLoading" class="probe-all-loading">
        <span class="refresh-spinner" aria-hidden="true"></span>
        {{ pm('probingModels') }}
      </div>
      <div v-else-if="probeAllSummary" class="probe-all-summary">
        <div class="probe-summary-stats">
          <span class="stat stat-ok">{{ pm('probeResultOk', { n: probeAllSummary.ok }) }}</span>
          <span class="stat stat-error">{{ pm('probeResultUnavailable', { n: probeAllSummary.model_unavailable }) }}</span>
          <span class="stat stat-warn">{{ pm('probeResultProviderErr', { n: probeAllSummary.provider_error }) }}</span>
          <span class="stat stat-skip">{{ pm('probeResultSkipped', { n: probeAllSummary.skipped }) }}</span>
        </div>
        <div v-if="probeAllHint" class="probe-all-hint">{{ probeAllHint }}</div>
        <details class="probe-results-details">
          <summary>{{ pm('probeResultsDetails', { n: probeAllResults.length }) }}</summary>
          <table class="data-table probe-results-table">
            <thead>
              <tr>
                <th>{{ pm('tableCol.credential') }}</th>
                <th>{{ pm('tableCol.model') }}</th>
                <th>{{ pm('tableCol.status') }}</th>
                <th>{{ pm('tableCol.category') }}</th>
                <th>{{ pm('tableCol.http') }}</th>
                <th>{{ pm('tableCol.error') }}</th>
                <th>{{ pm('tableCol.latency') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in probeAllResults" :key="`${r.credential_id}-${r.raw_model_name}`">
                <td>#{{ r.credential_id }}</td>
                <td><code>{{ r.raw_model_name }}</code></td>
                <td><span class="badge" :class="probeResultBadge(r.category)">{{ r.status }}</span></td>
                <td><span class="badge" :class="probeResultBadge(r.category)">{{ probeResultLabel(r.category) }}</span></td>
                <td>{{ r.http_status ?? '—' }}</td>
                <td class="err-cell">{{ r.error_message || '—' }}</td>
                <td>{{ r.latency_ms }}ms</td>
              </tr>
            </tbody>
          </table>
        </details>
      </div>
    </div>

    <div v-if="routable" class="card" style="margin-bottom:12px;background:color-mix(in srgb, var(--accent) 4%, transparent)">
      <h5 style="margin:0 0 8px 0">可路由性摘要 (v_routable_credential_models)</h5>
      <div class="metric-grid" style="grid-template-columns:repeat(4,1fr);gap:8px">
        <div class="metric">
          <b>{{ routable.routable_bindings }} / {{ routable.total_bindings }}</b>
          <span>可路由 (routable_ratio: {{ (routable.routable_ratio * 100).toFixed(0) }}%)</span>
        </div>
        <div class="metric">
          <b>{{ routable.unavailable_bindings }}</b>
          <span>不可路由</span>
        </div>
        <div class="metric" v-if="Object.keys(routable.unavailable_breakdown).length > 0">
          <b>细分</b>
          <div class="routable-breakdown">
            <div v-for="(count, code) in routable.unavailable_breakdown" :key="code">
              <code>{{ code }}</code>: {{ count }}
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 路由阻塞诊断 (2026-07-24) -->
    <div class="card" style="margin-bottom:12px;border-left:3px solid var(--warning)">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
        <h5 style="margin:0">路由阻塞诊断</h5>
        <div style="display:flex;gap:8px">
          <button class="btn btn-sm" @click="loadRoutingDiagnostics" :disabled="routingDiagLoading">
            {{ routingDiagLoading ? '诊断中…' : '诊断' }}
          </button>
          <button
            v-if="routingDiag && routingDiag.bindings_blocked > 0"
            class="btn btn-sm btn-danger"
            :disabled="routingDiagFixing"
            @click="handleFixBlocked"
          >
            {{ routingDiagFixing ? '修复中…' : '检测并修复全部' }}
          </button>
        </div>
      </div>
      <div v-if="routingDiagErr" class="alert alert-danger" style="margin-bottom:8px">{{ routingDiagErr }}</div>
      <div v-if="routingDiagFixed" class="alert alert-success" style="margin-bottom:8px">✅ 修复完成，请观察路由是否恢复</div>
      <div v-if="routingDiagLoading" class="text-muted">加载中…</div>
      <div v-else-if="!routingDiag" class="text-muted">点击"诊断"查看每个凭据-模型的路由状态</div>
      <div v-else>
        <div class="metric-grid" style="grid-template-columns:repeat(4,1fr);gap:8px;margin-bottom:8px">
          <div class="metric">
            <b :class="routingDiag.bindings_blocked === 0 ? 'text-success' : 'text-danger'">
              {{ routingDiag.bindings_routable }} / {{ routingDiag.bindings_total }}
            </b>
            <span>可路由</span>
          </div>
          <div class="metric">
            <b :class="routingDiag.bindings_blocked > 0 ? 'text-danger' : ''">{{ routingDiag.bindings_blocked }}</b>
            <span>被阻断</span>
          </div>
          <div class="metric" v-if="Object.keys(routingDiag.block_reason_breakdown).length > 0">
            <b>阻断原因</b>
            <div>
              <div v-for="(cnt, reason) in routingDiag.block_reason_breakdown" :key="reason" style="font-size:12px">
                <code>{{ reason }}</code>: {{ cnt }}
              </div>
            </div>
          </div>
        </div>

        <!-- Per-credential breakdown -->
        <div v-for="cred in routingDiag.credentials" :key="cred.credential_id" style="margin-bottom:6px;padding:6px 8px;border:1px solid var(--border);border-radius:4px">
          <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:4px">
            <div>
              <strong>#{{ cred.credential_id }} {{ cred.credential_label }}</strong>
              <span class="badge" :class="statusBadge(cred.status)" style="margin-left:6px">{{ cred.status }}</span>
            </div>
            <div style="font-size:12px">
              {{ cred.bindings_routable }}/{{ cred.bindings_total }} 可路由 ·
              <span :class="cred.bindings_blocked > 0 ? 'text-danger' : ''">{{ cred.bindings_blocked }} 被阻断</span>
              <span v-if="cred.manual_disabled" class="badge badge-red" style="margin-left:4px">已禁用</span>
            </div>
          </div>
          <div v-if="cred.bindings_blocked > 0" style="margin-top:4px;font-size:12px">
            <div v-for="b in cred.bindings.filter(b => !b.is_routable)" :key="b.raw_model_name" style="display:flex;gap:8px;padding:2px 0">
              <code>{{ b.raw_model_name }}</code>
              <span class="badge badge-red">阻断</span>
              <span class="text-muted">{{ b.unavailable_reason || '未知' }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>
    <div class="card" style="overflow-x:auto">
      <table class="data-table model-table">
        <thead>
          <tr>
            <th>{{ pm('table.rawModel') }}</th>
            <th>{{ pm('table.standardName') }}</th>
            <th>{{ pm('table.credential') }}</th>
            <th>{{ pm('table.available') }}</th>
            <th>{{ pm('table.source') }}</th>
            <th>{{ pm('table.contextWindow') }}</th>
            <th>{{ pm('table.latencyP95') }}</th>
            <th>{{ pm('table.successRate') }}</th>
            <th>{{ pm('table.standardIq') }}</th>
            <th>{{ pm('table.nodeIq') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td colspan="10">{{ pm('tableLoading') }}</td></tr>
          <tr v-else-if="!offers.length"><td colspan="10">{{ pm('tableEmpty') }}</td></tr>
          <tr
            v-for="o in offers"
            :key="o.id"
            class="model-row"
            tabindex="0"
            @click="openDrawer(o)"
            @keydown.enter="openDrawer(o)"
          >
            <td><code>{{ o.raw_model_name }}</code></td>
            <td>
              <code v-if="o.standardized_name">{{ o.standardized_name }}</code>
              <span v-else class="cell-muted">—</span>
            </td>
            <td>#{{ o.credential_id }} {{ o.credential_label }}</td>
            <td>
              <span class="avail-badge" :class="o.available ? 'on' : 'off'">
                {{ o.available ? pm('overview.chipAvailable') : pm('overview.chipUnavailable') }}
              </span>
            </td>
            <td>
              <span class="badge" :class="o.availability_source === 'auto' ? 'badge-amber' : o.availability_source === 'manual' ? 'badge-blue' : ''">
                {{ sourceLabel(o.availability_source) }}
              </span>
            </td>
            <td>
              <span v-if="o.context_window_override != null" class="cw-cell" :title="pm('contextWindowOverrideHint')">
                <code>{{ o.context_window_override }}</code>
              </span>
              <span v-else-if="o.context_window != null" class="cell-muted">
                {{ o.context_window }}
              </span>
              <span v-else class="cell-muted">—</span>
            </td>
            <td>{{ o.p95_latency_ms != null ? o.p95_latency_ms + 'ms' : '—' }}</td>
            <td>{{ o.success_rate != null ? (o.success_rate * 100).toFixed(1) + '%' : '—' }}</td>
            <td>
              <span v-if="o.canonical_standard_iq != null" class="iq-cell" :title="pm('standardIqHint')">
                {{ o.canonical_standard_iq.toFixed(1) }}
              </span>
              <span v-else class="cell-muted">—</span>
            </td>
            <td>
              <span
                v-if="o.node_iq != null"
                class="iq-cell"
                :class="iqBadgeClass(o.node_iq)"
                :title="nodeIqTitle(o)"
              >{{ o.node_iq.toFixed(1) }}<span v-if="o.node_iq_avg != null" class="iq-avg"> / avg {{ o.node_iq_avg.toFixed(1) }}</span></span>
              <span v-else class="cell-muted">—</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Model detail drawer -->
    <div v-if="selected" class="drawer-backdrop" @click="closeDrawer">
      <div class="drawer-panel card drawer-panel-wide" @click.stop>
        <div class="drawer-header">
          <div>
            <h3 style="margin:0"><code>{{ selected.raw_model_name }}</code></h3>
            <div class="drawer-sub">{{ pm('drawTitle', { id: selected.id, credId: selected.credential_id, credLabel: selected.credential_label }) }}</div>
          </div>
          <button type="button" class="btn btn-ghost btn-sm" @click="closeDrawer">{{ pm('drawerClose') }}</button>
        </div>

        <div class="drawer-body">
          <div class="drawer-section">
            <div class="drawer-section-title">{{ pm('drawerSectionAvailable') }}</div>
            <div class="avail-row">
              <span class="avail-badge lg" :class="selected.available ? 'on' : 'off'">
                {{ selected.available ? pm('overview.chipAvailable') : pm('overview.chipUnavailable') }}
              </span>
              <span class="cell-muted">{{ sourceLabel(selected.availability_source) }}</span>
              <span v-if="selected.unavailable_at" class="cell-muted">{{ timeText(selected.unavailable_at) }}</span>
            </div>
            <button
              class="btn btn-sm"
              :disabled="draft.toggling"
              style="margin-top:10px"
              @click="toggleAvailable"
            >
              {{ draft.toggling ? pm('drawToggleLoading') : (selected.available ? pm('drawToggleDisable') : pm('drawToggleEnable')) }}
            </button>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">
              {{ pm('drawerSectionStandard') }}
            </div>
            <input
              v-model="draft.standardized_name"
              class="field-input"
              :placeholder="pm('standardizedPlaceholder')"
            />
            <div v-if="draft.saveErr" class="alert alert-danger" style="margin:8px 0;padding:6px 10px">{{ draft.saveErr }}</div>
            <div class="suggest-block">
              <div class="suggest-row">
                <span class="suggest-label">{{ pm('ruleBased') }}</span>
                <span v-if="draft.loadingSuggest" class="suggest-loading">{{ pm('ruleBasedLoading') }}</span>
                <button
                  v-else-if="draft.suggest?.rule_based"
                  type="button"
                  class="suggest-chip"
                  @click="applyRuleBased"
                >{{ draft.suggest?.rule_based }}</button>
                <span v-else class="suggest-empty">—</span>
              </div>
              <div class="suggest-row">
                <span class="suggest-label">{{ pm('canonicalLabel') }}</span>
                <select
                  :value="draft.canonical_id ?? ''"
                  class="field-input"
                  style="margin:0"
                  @change="(ev) => applyCanonical((ev.target as HTMLSelectElement).value === '' ? null : Number((ev.target as HTMLSelectElement).value))"
                >
                  <option value="">{{ pm('canonicalEmpty') }}</option>
                  <option
                    v-for="c in (draft.suggest?.canonical_options ?? [])"
                    :key="c.id"
                    :value="c.id"
                  >{{ c.canonical_name }}</option>
                </select>
              </div>
              <div v-if="draft.suggestErr" class="suggest-err">{{ draft.suggestErr }}</div>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">
              {{ pm('drawerSectionOutbound') }}
              <span
                class="hint"
                :title="pm('drawerOutboundHint')"
              >?</span>
            </div>
            <input
              v-model="draft.outbound_model_name"
              class="field-input"
              :placeholder="selected?.raw_model_name || pm('drawerOutboundPlaceholder')"
            />
            <div class="cell-sub" style="margin-top:6px">
              <span v-if="draft.outbound_model_name">
                {{ pm('drawerOutboundSend') }}<code>{{ draft.outbound_model_name }}</code>
              </span>
              <span v-else class="cell-muted">
                {{ pm('drawerOutboundUnset') }} <code>{{ selected?.raw_model_name }}</code>
              </span>
            </div>
          </div>

          <!-- 2026-08-17: context window override (binding-level).
               Resolution chain: cmb.context_window_override > mc.context_window_override > mc.context_window.
               Empty / 0 in the input clears the override; positive integer saves it.
               We omit the field from the PATCH body when the draft equals the
               initial snapshot, so editing unrelated fields does not clobber
               an existing override. -->
          <div class="drawer-section">
            <div class="drawer-section-title">
              {{ pm('drawerSectionContextWindow') }}
              <span
                class="hint"
                :title="pm('drawerContextWindowHint')"
              >?</span>
            </div>
            <div style="display:flex;gap:8px;align-items:center">
              <input
                v-model.number="draft.context_window"
                type="number"
                min="0"
                step="1"
                class="field-input"
                :placeholder="pm('drawerContextWindowPlaceholder')"
                style="flex:1;min-width:0"
              />
              <button
                type="button"
                class="btn btn-sm btn-outline"
                :disabled="draft.context_window === '' || draft.context_window == null || draft.context_window === 0"
                :title="pm('drawerContextWindowClearTitle')"
                @click="draft.context_window = 0"
              >{{ pm('drawerContextWindowClearBtn') }}</button>
            </div>
            <div class="cell-sub" style="margin-top:6px">
              <span v-if="selected.context_window_override != null">
                {{ pm('drawerContextWindowOverride') }}<code>{{ selected.context_window_override }}</code>
              </span>
              <span v-else-if="selected.context_window != null" class="cell-muted">
                {{ pm('drawerContextWindowEffective') }}<code>{{ selected.context_window }}</code>
              </span>
              <span v-else class="cell-muted">—</span>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">{{ pm('drawerSectionMetrics') }}</div>
            <div class="metric-row">
              <span>{{ pm('metricP95') }}</span>
              <b>{{ selected.p95_latency_ms != null ? selected.p95_latency_ms + 'ms' : '—' }}</b>
            </div>
            <div class="metric-row">
              <span>{{ pm('metricSuccessRate') }}</span>
              <b>{{ selected.success_rate != null ? (selected.success_rate * 100).toFixed(1) + '%' : '—' }}</b>
            </div>
          </div>

          <!-- 2026-08-11: Model IQ — standard + node history + on-demand test -->
          <div class="drawer-section">
            <div class="drawer-section-title">
              {{ pm('drawerSectionIq') }}
              <button
                class="btn btn-sm btn-outline"
                style="margin-left:auto"
                :disabled="iqTestLoading"
                :title="pm('iqTestBtnTitle')"
                @click="runIQTest"
              >{{ iqTestLoading ? pm('iqTestRunning') : pm('iqTestBtn') }}</button>
            </div>
            <div class="iq-summary">
              <div class="iq-summary-item">
                <span class="cell-sub">{{ pm('standardIqLabel') }}</span>
                <b>{{ selected.canonical_standard_iq != null ? selected.canonical_standard_iq.toFixed(1) : '—' }}</b>
              </div>
              <div class="iq-summary-item">
                <span class="cell-sub">{{ pm('nodeIqLabel') }}</span>
                <b>{{ selected.node_iq != null ? selected.node_iq.toFixed(1) : '—' }}</b>
              </div>
              <div class="iq-summary-item">
                <span class="cell-sub">{{ pm('nodeIqAvgLabel') }}</span>
                <b>{{ selected.node_iq_avg != null ? selected.node_iq_avg.toFixed(1) : '—' }}</b>
              </div>
            </div>
            <div v-if="iqTestError" class="alert alert-danger" style="margin:8px 0;padding:6px 10px">{{ iqTestError }}</div>
            <div v-if="iqHistoryLoading" class="cell-muted" style="margin-top:8px">{{ pm('iqHistoryLoading') }}</div>
            <div v-else-if="!iqHistory.length" class="cell-muted" style="margin-top:8px">{{ pm('iqHistoryEmpty') }}</div>
            <div v-else class="iq-history" style="margin-top:8px">
              <canvas ref="iqChartRef" class="iq-history-chart"></canvas>
              <details class="iq-history-details">
                <summary>{{ pm('iqHistoryDetails') }}</summary>
                <table class="data-table iq-history-table">
                  <thead>
                    <tr>
                      <th>{{ pm('iqColTestedAt') }}</th>
                      <th>{{ pm('iqColScore') }}</th>
                      <th>{{ pm('iqColGrade') }}</th>
                      <th>{{ pm('iqColAccuracy') }}</th>
                      <th>{{ pm('iqColTrigger') }}</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="(h, i) in iqHistory" :key="i">
                      <td>{{ timeText(h.tested_at) }}</td>
                      <td><span class="iq-cell" :class="iqBadgeClass(h.overall_score)">{{ h.overall_score.toFixed(1) }}</span></td>
                      <td>{{ h.grade || '—' }}</td>
                      <td>{{ h.accuracy != null ? h.accuracy.toFixed(1) + '%' : '—' }}</td>
                      <td><span class="badge" :class="h.trigger_kind === 'on_demand' ? 'badge-blue' : h.trigger_kind === 'anomaly' ? 'badge-red' : ''">{{ h.trigger_kind }}</span></td>
                    </tr>
                  </tbody>
                </table>
              </details>
            </div>
          </div>

          <!-- Phase 3.2: Batch credential check results -->
          <div v-if="modelCheckResults" class="drawer-section">
            <div class="drawer-section-title">
              {{ pm('drawerSectionCheck') }}
              <span class="cell-sub">{{ pm('drawerCheckSuffix', { n: modelCheckResults.length }) }}</span>
            </div>

            <div class="check-results">
              <div
                v-for="result in modelCheckResults"
                :key="result.credential_id"
                class="check-result-row"
              >
                <div class="result-header">
                  <span class="credential-label">{{ result.credential_label }}</span>
                  <span class="status-badge" :class="result.status">
                    {{
                      result.status === 'ok' ? pm('checkStatusOk') :
                      result.status === 'unavailable' ? pm('checkStatusUnavailable') :
                      result.status === 'warning' ? pm('checkStatusWarning') :
                      pm('checkStatusError')
                    }}
                  </span>
                </div>

                <!-- Two-phase validation details -->
                <div class="phase-details">
                  <!-- Phase 1: Model availability -->
                  <div class="phase-row">
                    <span class="phase-label">{{ pm('phaseLabel.phase1') }}</span>
                    <span class="phase-status" :class="result.phase1_status">
                      {{ result.phase1_message }}
                    </span>
                  </div>

                  <!-- Phase 2: Chat test (if applicable) -->
                  <div v-if="result.phase2_message" class="phase-row">
                    <span class="phase-label">{{ pm('phaseLabel.phase2') }}</span>
                    <span class="phase-status" :class="result.phase2_status">
                      {{ result.phase2_message }}
                    </span>
                  </div>
                </div>

                <!-- Overall error message (if different from phase messages) -->
                <div v-if="result.error && result.status === 'unavailable'" class="result-error">
                  {{ result.error }}
                </div>
              </div>
            </div>
          </div>
        </div>

        <div class="drawer-footer">
          <div class="btn-row btn-row--space-between">
            <!-- Left: Check all credentials button -->
            <button
              class="btn btn-outline"
              :disabled="checkingModel"
              @click="checkModelAcrossCredentials"
            >
              {{ checkingModel ? pm('drawCheckAllLoading') : pm('drawCheckAllBtn') }}
            </button>

            <!-- Right: Save and cancel buttons -->
            <div class="btn-row btn-row--end">
              <button class="btn btn-ghost" @click="closeDrawer">{{ pm('drawCancel') }}</button>
              <button class="btn btn-primary" :disabled="draft.saving" @click="saveEdit">
                {{ draft.saving ? pm('drawSaving') : pm('drawSave') }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.model-table {
  width: 100%;
  font-size: 12px;
}
/* 2026-08-11: model IQ cells + drawer history */
.iq-cell {
  display: inline-block;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11px;
  background: color-mix(in srgb, var(--accent) 10%, transparent);
  color: var(--text);
}
.iq-cell.iq-good { background: rgba(34, 197, 94, 0.14); color: #16a34a; }
.iq-cell.iq-ok   { background: rgba(59, 130, 246, 0.14); color: #2563eb; }
.iq-cell.iq-warn { background: rgba(245, 158, 11, 0.14); color: #d97706; }
.iq-cell.iq-bad  { background: rgba(239, 68, 68, 0.14); color: #dc2626; }
.iq-avg {
  font-weight: 400;
  color: var(--muted);
  font-size: 10px;
}
.iq-summary {
  display: flex;
  gap: 18px;
  flex-wrap: wrap;
  margin-top: 4px;
}
.iq-summary-item {
  display: flex;
  flex-direction: column;
}
.iq-summary-item .cell-sub {
  font-size: 11px;
}
.iq-summary-item b {
  font-size: 16px;
  font-variant-numeric: tabular-nums;
}
.iq-history-table {
  width: 100%;
  font-size: 11px;
}
.iq-history-table th {
  font-weight: 500;
}
.iq-history-chart {
  width: 100% !important;
  height: 200px !important;
  margin-bottom: 8px;
}
.iq-history-details {
  margin-top: 4px;
}
.iq-history-details > summary {
  cursor: pointer;
  font-size: 11px;
  color: var(--muted);
  user-select: none;
  margin-bottom: 6px;
}
.drawer-section-title {
  display: flex;
  align-items: center;
}
.refresh-hint {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 400;
  color: var(--muted);
  padding: 2px 10px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--accent) 8%, transparent);
  border: 1px solid color-mix(in srgb, var(--accent) 20%, transparent);
  white-space: nowrap;
}
.refresh-hint--loading {
  color: var(--accent);
  border-color: color-mix(in srgb, var(--accent) 40%, transparent);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
}
.refresh-hint--success {
  color: #16a34a;
  border-color: rgba(34, 197, 94, 0.3);
  background: rgba(34, 197, 94, 0.1);
}
.refresh-hint--error {
  color: #ef4444;
  border-color: rgba(239, 68, 68, 0.3);
  background: rgba(239, 68, 68, 0.1);
}
.refresh-hint--idle {
  color: var(--muted);
}
.refresh-spinner {
  display: inline-block;
  width: 10px;
  height: 10px;
  border: 2px solid color-mix(in srgb, var(--accent) 30%, transparent);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: refresh-spin 0.8s linear infinite;
}
@keyframes refresh-spin {
  to {
    transform: rotate(360deg);
  }
}
.model-row {
  cursor: pointer;
}
.model-row:hover td {
  background: color-mix(in srgb, var(--accent) 6%, transparent);
}
.model-row:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
.cell-muted {
  color: var(--muted);
}
.avail-badge {
  display: inline-block;
  border-radius: 999px;
  padding: 2px 10px;
  font-size: 11px;
}
.avail-badge.on {
  background: rgba(34, 197, 94, 0.15);
  color: #22c55e;
}
.avail-badge.off {
  background: rgba(239, 68, 68, 0.12);
  color: #ef4444;
}
.avail-badge.lg {
  font-size: 13px;
  padding: 4px 14px;
}
.drawer-sub {
  font-size: 12px;
  color: var(--muted);
  margin-top: 4px;
}
.drawer-body {
  flex: 1;
  overflow-y: auto;
}
.drawer-footer {
  margin-top: auto;
  padding-top: 16px;
  border-top: 1px solid var(--border);
}
.avail-row {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.field-input {
  width: 100%;
  padding: 8px 10px;
  font-size: 13px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
}
.field-input:focus {
  border-color: var(--accent);
  outline: none;
}
.suggest-block {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 10px 12px;
  margin-top: 10px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.suggest-row {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
}
.suggest-label {
  color: var(--muted);
  white-space: nowrap;
  min-width: 110px;
}
.suggest-chip {
  border: 1px solid var(--accent);
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  color: var(--text);
  border-radius: 999px;
  padding: 4px 12px;
  font-size: 12px;
  font-family: monospace;
  cursor: pointer;
}
.suggest-chip:hover {
  background: var(--accent);
  color: #fff;
}
.suggest-loading,
.suggest-empty {
  color: var(--muted);
  font-size: 11px;
}
.suggest-err {
  color: var(--danger);
  font-size: 11px;
}
.metric-row {
  display: flex;
  justify-content: space-between;
  padding: 6px 0;
  font-size: 13px;
  border-bottom: 1px solid var(--border);
}
.btn-row {
  display: flex;
  gap: 8px;
}
.btn-row--end {
  justify-content: flex-end;
}
.probe-all-loading {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
  font-size: 12px;
  color: var(--accent);
}
.probe-all-summary {
  margin-top: 8px;
  padding: 10px 12px;
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: 8px;
}
.probe-all-hint {
  margin-top: 8px;
  padding: 6px 10px;
  background: rgba(34, 197, 94, 0.12);
  border-left: 3px solid var(--ok);
  color: var(--ok);
  font-size: 12px;
  border-radius: 4px;
}
.probe-summary-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  font-size: 13px;
}
.stat-ok { color: #22c55e; }
.stat-error { color: #ef4444; }
.stat-warn { color: #f59e0b; }
.stat-skip { color: var(--muted); }
.probe-results-details {
  margin-top: 10px;
}
.probe-results-details > summary {
  cursor: pointer;
  font-size: 12px;
  color: var(--muted);
  user-select: none;
}
.probe-results-details[open] > summary {
  margin-bottom: 8px;
}
.probe-results-table {
  width: 100%;
  font-size: 11px;
  margin-top: 8px;
}
.err-cell {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Phase 3.2: Model check results styles */
.check-results {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: 8px;
}
.check-result-row {
  padding: 8px 12px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-secondary);
}
.result-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
}
.credential-label {
  font-size: 13px;
  font-weight: 500;
  color: var(--text);
}
.status-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
}
.status-badge.ok {
  background: rgba(34, 197, 94, 0.1);
  color: rgb(34, 197, 94);
}
.status-badge.unavailable,
.status-badge.error {
  background: rgba(239, 68, 68, 0.1);
  color: rgb(239, 68, 68);
}
.status-badge.warning {
  background: rgba(251, 191, 36, 0.1);
  color: rgb(251, 191, 36);
}
.result-error {
  margin-top: 4px;
  font-size: 11px;
  color: var(--muted);
  line-height: 1.4;
}
.phase-details {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 8px;
  padding: 8px;
  background: var(--bg-tertiary, rgba(0, 0, 0, 0.02));
  border-radius: 4px;
}
.phase-row {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  font-size: 12px;
  line-height: 1.5;
}
.phase-label {
  flex-shrink: 0;
  font-weight: 500;
  color: var(--text-secondary);
  min-width: 110px;
}
.phase-status {
  flex: 1;
  color: var(--text);
}
.phase-status.ok {
  color: rgb(34, 197, 94);
}
.phase-status.error {
  color: rgb(239, 68, 68);
}
.phase-status.warning {
  color: rgb(251, 191, 36);
}
.phase-status.unavailable {
  color: var(--muted);
}
.btn-row--space-between {
  display: flex;
  justify-content: space-between;
  align-items: center;
  width: 100%;
}
.routable-breakdown {
  font-size: 10px;
  text-align: start;
  max-height: 80px;
  overflow-y: auto;
}
</style>
