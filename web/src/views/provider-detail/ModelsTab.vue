<script setup lang="ts">
import { ref, computed, onBeforeUnmount, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useFormat } from '../../i18n/useFormat'
import { useCredentialLabels } from '../../composables/useCredentialLabels'
import {
  getProviderModels,
  refreshProviderModels,
  clearProviderModels,
  getProviderRefreshStatus,
  getRoutableSummary,
  getRoutingBlockedDiagnostic,
  fixRoutingBlocked,
  triggerProviderProbeAll,
  resetNodeProbeState,
  type ModelOffer,
  type ProbeAllResult,
  type ProviderRefreshRun,
  type RoutingBlockedDiagnostic,
} from '../../api'
import ModelOfferDetailDrawer from '../../components/model/ModelOfferDetailDrawer.vue'

const { t: td } = useI18n()
const pm = (k: string, params?: Record<string, unknown>): string =>
  td(`providerDetail.models.${k}` as never, params as never)
const { fmtDateTime } = useFormat()

const { credentialDisplayName, loadCredentialLabels } = useCredentialLabels()
onMounted(() => { void loadCredentialLabels() })

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

function openDrawer(o: ModelOffer) {
  selected.value = o
}

function closeDrawer() {
  selected.value = null
}

function onOfferUpdated(o: ModelOffer) {
  selected.value = o
  const i = offers.value.findIndex(x => x.id === o.id)
  if (i >= 0) offers.value[i] = { ...offers.value[i], ...o }
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
                <td>{{ credentialDisplayName(r.credential_id) }}</td>
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
              <strong>{{ credentialDisplayName(cred.credential_id) }} {{ cred.credential_label }}</strong>
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
            <td>{{ credentialDisplayName(o.credential_id) }} {{ o.credential_label }}</td>
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

    <ModelOfferDetailDrawer
      v-if="selected"
      :provider-id="providerId"
      :offer="selected"
      :sibling-offers="offers"
      @close="closeDrawer"
      @updated="onOfferUpdated"
      @iq-tested="load"
    />
  </div>
</template>