<script setup lang="ts">
import { ref, reactive, computed, onBeforeUnmount, watch } from 'vue'
import {
  getProviderModels,
  refreshProviderModels,
  clearProviderModels,
  getProviderRefreshStatus,
  toggleModelOfferState,
  getModelOfferSuggestions,
  updateModelOffer,
  getRoutableSummary,
  triggerProviderProbeAll,
  getProviderCredentials,
  checkCredential,
  diagnoseProvider,
  type ModelOffer,
  type ModelOfferSuggestion,
  type ProviderRefreshRun,
} from '../../api'

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

const probeAllLoading = ref(false)
const probeAllResults = ref<ProbeAllResult[]>([])
const probeAllSummary = ref<{ ok: number; model_unavailable: number; provider_error: number; skipped: number } | null>(null)

const selected = ref<ModelOffer | null>(null)

// Phase 3.2: Model check across credentials
const checkingModel = ref(false)
const modelCheckResults = ref<Array<{
  credential_id: number
  credential_label: string
  status: 'ok' | 'unavailable' | 'error'
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
  saving: boolean
  toggling: boolean
  loadingSuggest: boolean
  suggest: ModelOfferSuggestion | null
  suggestErr: string
  saveErr: string
}
const draft = reactive<Partial<EditDraft>>({})

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
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
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
        refreshError.value = run.message || run.errors?.join('; ') || '从供应商读取失败'
      }
    }
  } catch (e: unknown) {
    stopPolling()
    refreshing.value = false
    refreshError.value = e instanceof Error ? e.message : '查询刷新状态失败'
  }
}

async function clearModels() {
  if (clearing.value || refreshing.value) return
  if (!confirm('确定清空当前供应商的全部模型绑定？清空后可重新从供应商读取。')) return
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
      message: `已清空 ${resp.deleted} 条模型绑定`,
    }
  } catch (e: unknown) {
    refreshError.value = e instanceof Error ? e.message : '清空失败'
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
      refreshError.value = start.run.message || '从供应商读取失败'
    }
  } catch (e: unknown) {
    refreshing.value = false
    refreshError.value = e instanceof Error ? e.message : '从供应商读取失败'
  }
}

const refreshSummary = computed(() => {
  const r = refreshRun.value
  if (!r || r.status === 'running') return ''
  const parts: string[] = []
  if (r.models_upserted > 0) {
    parts.push(`新增/更新 ${r.models_upserted}`)
  }
  if (r.credentials_failed > 0) {
    parts.push(`失败 ${r.credentials_failed}`)
  }
  if (parts.length === 0) {
    return r.message || '无变化'
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
  } catch (e: unknown) {
    probeAllSummary.value = null
    alert(e instanceof Error ? e.message : '探测失败')
  } finally {
    probeAllLoading.value = false
  }
}

function probeResultBadge(category: string) {
  if (category === 'ok') return 'badge-green'
  if (category === 'model_unavailable') return 'badge-red'
  if (category === 'provider_error') return 'badge-amber'
  return 'badge-gray'
}

function probeResultLabel(category: string) {
  if (category === 'ok') return '成功'
  if (category === 'model_unavailable') return '模型不可用'
  if (category === 'provider_error') return '供应商问题'
  if (category === 'skipped') return '已跳过'
  return category
}

onBeforeUnmount(stopPolling)

function sourceLabel(v?: string | null) {
  if (v === 'auto') return '自动'
  if (v === 'manual') return '手动'
  return '从未'
}

function timeText(v?: string | null) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { hour12: false })
}

function resetDraft(o: ModelOffer) {
  draft.standardized_name = o.standardized_name ?? ''
  draft.canonical_id = o.canonical_id ?? null
  draft.outbound_model_name = o.outbound_model_name ?? ''
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
  draft.loadingSuggest = true
  try {
    draft.suggest = await getModelOfferSuggestions(props.providerId, o.id)
  } catch (e: unknown) {
    draft.suggestErr = e instanceof Error ? e.message : '加载推荐失败'
  } finally {
    draft.loadingSuggest = false
  }
}

function closeDrawer() {
  selected.value = null
  modelCheckResults.value = null  // Clear check results when closing
}

// Phase 3.2: Check model availability across all credentials (2-step)
async function checkModelAcrossCredentials() {
  if (!selected.value) return
  
  checkingModel.value = true
  modelCheckResults.value = null
  
  try {
    const credentials = await getProviderCredentials(props.providerId)
    const modelName = selected.value.raw_model_name
    
    // Step 1: Static check - does the credential have this model in offers?
    const staticResults = credentials.map((cred) => {
      const hasModel = offers.value.some(
        offer => offer.credential_id === cred.id && 
                 offer.raw_model_name.toLowerCase() === modelName.toLowerCase()
      )
      return {
        credential_id: cred.id,
        credential_label: cred.label || cred.name || `凭据 #${cred.id}`,
        hasModel
      }
    })
    
    // Filter credentials that have the model
    const credsWithModel = staticResults.filter(r => r.hasModel)
    
    if (credsWithModel.length === 0) {
      // No credential has this model
      modelCheckResults.value = staticResults.map(r => ({
        credential_id: r.credential_id,
        credential_label: r.credential_label,
        status: 'unavailable' as const,
        error: '该凭据的模型列表中未找到此模型'
      }))
      return
    }
    
    // Step 2: Dynamic check - test chat with those credentials
    // Use diagnoseProvider to test actual chat calls
    const diagnoseResult = await diagnoseProvider(props.providerId, { force: true })
    
    const results = staticResults.map((r) => {
      if (!r.hasModel) {
        return {
          credential_id: r.credential_id,
          credential_label: r.credential_label,
          status: 'unavailable' as const,
          error: '该凭据的模型列表中未找到此模型'
        }
      }
      
      // Find chat probe result for this credential
      const credResult = diagnoseResult.credentials?.find(
        (c: any) => c.credential_id === r.credential_id
      )
      
      if (!credResult) {
        return {
          credential_id: r.credential_id,
          credential_label: r.credential_label,
          status: 'unavailable' as const,
          error: '未找到诊断结果'
        }
      }
      
      // Check chat probe status
      const chatProbe = credResult.chat_probe
      if (chatProbe && chatProbe.status_code === 200) {
        return {
          credential_id: r.credential_id,
          credential_label: r.credential_label,
          status: 'ok' as const,
          error: null
        }
      } else if (chatProbe && chatProbe.error) {
        return {
          credential_id: r.credential_id,
          credential_label: r.credential_label,
          status: 'error' as const,
          error: `调用失败: ${chatProbe.error} (HTTP ${chatProbe.status_code || 'N/A'})`
        }
      } else {
        return {
          credential_id: r.credential_id,
          credential_label: r.credential_label,
          status: 'error' as const,
          error: '调用测试未完成或失败'
        }
      }
    })
    
    modelCheckResults.value = results
  } catch (e: unknown) {
    alert('检查失败: ' + (e instanceof Error ? e.message : String(e)))
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
  try {
    const updated = await updateModelOffer(props.providerId, o.id, {
      standardized_name: (draft.standardized_name ?? '').trim() || null,
      canonical_id: draft.canonical_id ?? null,
      outbound_model_name: (draft.outbound_model_name ?? '').trim() || null,
    })
    const idx = offers.value.findIndex(x => x.id === o.id)
    if (idx >= 0) {
      offers.value[idx] = {
        ...offers.value[idx],
        standardized_name: updated.standardized_name ?? '',
        canonical_id: updated.canonical_id,
        outbound_model_name: updated.outbound_model_name ?? null,
      }
      selected.value = offers.value[idx]
    }
  } catch (e: unknown) {
    draft.saveErr = e instanceof Error ? e.message : '保存失败'
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
    error.value = e instanceof Error ? e.message : '操作失败'
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
        <span>模型清单 ({{ offers.length }})</span>
        <span v-if="refreshing" class="refresh-hint refresh-hint--loading" role="status" aria-live="polite">
          <span class="refresh-spinner" aria-hidden="true"></span>
          正在从供应商读取数据…
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
          :title="refreshing ? '正在从供应商读取模型列表…' : '调用供应商 /v1/models 接口，新增未入库的模型'"
          @click="refreshFromProvider"
        >
          {{ refreshing ? '读取中…' : '从供应商读取' }}
        </button>
        <button
          class="btn btn-sm btn-ghost"
          :disabled="loading || refreshing || clearing || offers.length === 0"
          title="移除当前供应商的全部模型绑定，便于重新拉取"
          @click="clearModels"
        >{{ clearing ? '清空中…' : '清空' }}</button>
        <button
          class="btn btn-sm btn-ghost"
          :disabled="loading || refreshing || clearing"
          title="仅从本地缓存重新加载，不调用供应商接口"
          @click="load"
        >{{ loading ? '加载中…' : '刷新' }}</button>
        <button
          class="btn btn-sm"
          :disabled="probeAllLoading || offers.length === 0"
          title="对列表中所有模型发起探测，验证可用性"
          @click="triggerAllProbes"
        >{{ probeAllLoading ? '探测中…' : '全面探测' }}</button>
      </div>
      <div v-if="probeAllLoading" class="probe-all-loading">
        <span class="refresh-spinner" aria-hidden="true"></span>
        正在探测模型…
      </div>
      <div v-else-if="probeAllSummary" class="probe-all-summary">
        <div class="probe-summary-stats">
          <span class="stat stat-ok">✅ 成功 {{ probeAllSummary.ok }}</span>
          <span class="stat stat-error">❌ 模型不可用 {{ probeAllSummary.model_unavailable }}</span>
          <span class="stat stat-warn">⚠️ 供应商问题 {{ probeAllSummary.provider_error }}</span>
          <span class="stat stat-skip">⏭️ 跳过 {{ probeAllSummary.skipped }}</span>
        </div>
        <details class="probe-results-details">
          <summary>查看详细结果 ({{ probeAllResults.length }})</summary>
          <table class="data-table probe-results-table">
            <thead>
              <tr>
                <th>凭据</th>
                <th>模型</th>
                <th>状态</th>
                <th>分类</th>
                <th>HTTP</th>
                <th>错误</th>
                <th>延迟</th>
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

    <div v-if="routable" class="card" style="margin-bottom:12px;background:rgba(99,102,241,0.04)">
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
          <div style="font-size:10px;text-align:left;max-height:80px;overflow-y:auto">
            <div v-for="(count, code) in routable.unavailable_breakdown" :key="code">
              <code>{{ code }}</code>: {{ count }}
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
            <th>原始模型名</th>
            <th>标准化名</th>
            <th>关联凭据</th>
            <th>可用</th>
            <th>来源</th>
            <th>延迟 P95</th>
            <th>成功率</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading"><td colspan="7">加载中…</td></tr>
          <tr v-else-if="!offers.length"><td colspan="7">暂无模型</td></tr>
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
                {{ o.available ? '可用' : '不可用' }}
              </span>
            </td>
            <td>
              <span class="badge" :class="o.availability_source === 'auto' ? 'badge-amber' : o.availability_source === 'manual' ? 'badge-blue' : ''">
                {{ sourceLabel(o.availability_source) }}
              </span>
            </td>
            <td>{{ o.p95_latency_ms != null ? o.p95_latency_ms + 'ms' : '—' }}</td>
            <td>{{ o.success_rate != null ? (o.success_rate * 100).toFixed(1) + '%' : '—' }}</td>
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
            <div class="drawer-sub">offer #{{ selected.id }} · 凭据 #{{ selected.credential_id }} {{ selected.credential_label }}</div>
          </div>
          <button type="button" class="btn btn-ghost btn-sm" @click="closeDrawer">关闭</button>
        </div>

        <div class="drawer-body">
          <div class="drawer-section">
            <div class="drawer-section-title">可用状态</div>
            <div class="avail-row">
              <span class="avail-badge lg" :class="selected.available ? 'on' : 'off'">
                {{ selected.available ? '可用' : '不可用' }}
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
              {{ draft.toggling ? '处理中…' : (selected.available ? '禁用此绑定' : '启用此绑定') }}
            </button>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">
              标准化名
            </div>
            <input
              v-model="draft.standardized_name"
              class="field-input"
              placeholder="标准化模型名"
            />
            <div v-if="draft.saveErr" class="alert alert-danger" style="margin:8px 0;padding:6px 10px">{{ draft.saveErr }}</div>
            <div class="suggest-block">
              <div class="suggest-row">
                <span class="suggest-label">规则推荐</span>
                <span v-if="draft.loadingSuggest" class="suggest-loading">计算中…</span>
                <button
                  v-else-if="draft.suggest?.rule_based"
                  type="button"
                  class="suggest-chip"
                  @click="applyRuleBased"
                >{{ draft.suggest?.rule_based }}</button>
                <span v-else class="suggest-empty">—</span>
              </div>
              <div class="suggest-row">
                <span class="suggest-label">已认可标准化名</span>
                <select
                  :value="draft.canonical_id ?? ''"
                  class="field-input"
                  style="margin:0"
                  @change="(ev) => applyCanonical((ev.target as HTMLSelectElement).value === '' ? null : Number((ev.target as HTMLSelectElement).value))"
                >
                  <option value="">— 不关联 canonical —</option>
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
              出口模型名（endpoint ID）
              <span
                class="hint"
                title="某些供应商（如火山方舟）需要 endpoint ID（如 ep-20241227XXXX）作为模型字段，而非原始名称。未配置时探针会被跳过（错误码 endpoint_id_required）。"
              >?</span>
            </div>
            <input
              v-model="draft.outbound_model_name"
              class="field-input"
              :placeholder="selected?.raw_model_name || 'ep-XXXXXXXX'"
            />
            <div class="cell-sub" style="margin-top:6px">
              <span v-if="draft.outbound_model_name">
                将发送：<code>{{ draft.outbound_model_name }}</code>
              </span>
              <span v-else class="cell-muted">
                未设置 — 将使用 <code>{{ selected?.raw_model_name }}</code>
              </span>
            </div>
          </div>

          <div class="drawer-section">
            <div class="drawer-section-title">指标</div>
            <div class="metric-row">
              <span>P95 延迟</span>
              <b>{{ selected.p95_latency_ms != null ? selected.p95_latency_ms + 'ms' : '—' }}</b>
            </div>
            <div class="metric-row">
              <span>成功率</span>
              <b>{{ selected.success_rate != null ? (selected.success_rate * 100).toFixed(1) + '%' : '—' }}</b>
            </div>
          </div>

          <!-- Phase 3.2: Batch credential check results -->
          <div v-if="modelCheckResults" class="drawer-section">
            <div class="drawer-section-title">
              凭据检查结果
              <span class="cell-sub">({{ modelCheckResults.length }}个凭据)</span>
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
                    {{ result.status === 'ok' ? '✓ 可用' : result.status === 'unavailable' ? '✗ 不可用' : '✗ 错误' }}
                  </span>
                </div>
                <div v-if="result.error" class="result-error">
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
              {{ checkingModel ? '检查中…' : '检查所有凭据' }}
            </button>
            
            <!-- Right: Save and cancel buttons -->
            <div class="btn-row btn-row--end">
              <button class="btn btn-ghost" @click="closeDrawer">取消</button>
              <button class="btn btn-primary" :disabled="draft.saving" @click="saveEdit">
                {{ draft.saving ? '保存中…' : '保存标准化名' }}
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
.refresh-hint {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 400;
  color: var(--muted);
  padding: 2px 10px;
  border-radius: 999px;
  background: rgba(99, 102, 241, 0.08);
  border: 1px solid rgba(99, 102, 241, 0.2);
  white-space: nowrap;
}
.refresh-hint--loading {
  color: var(--accent, #6366f1);
  border-color: rgba(99, 102, 241, 0.4);
  background: rgba(99, 102, 241, 0.12);
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
  border: 2px solid rgba(99, 102, 241, 0.3);
  border-top-color: var(--accent, #6366f1);
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
  background: rgba(99, 102, 241, 0.06);
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
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
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
  border: 1px solid var(--accent, #6366f1);
  background: rgba(99,102,241,0.12);
  color: var(--text, #e6edf3);
  border-radius: 999px;
  padding: 4px 12px;
  font-size: 12px;
  font-family: monospace;
  cursor: pointer;
}
.suggest-chip:hover {
  background: var(--accent, #6366f1);
  color: #fff;
}
.suggest-loading,
.suggest-empty {
  color: var(--muted);
  font-size: 11px;
}
.suggest-err {
  color: var(--danger, #f85149);
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
  color: var(--accent, #6366f1);
}
.probe-all-summary {
  margin-top: 8px;
  padding: 10px 12px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border);
  border-radius: 8px;
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
.result-error {
  margin-top: 4px;
  font-size: 11px;
  color: var(--muted);
  line-height: 1.4;
}
.btn-row--space-between {
  display: flex;
  justify-content: space-between;
  align-items: center;
  width: 100%;
}
</style>
