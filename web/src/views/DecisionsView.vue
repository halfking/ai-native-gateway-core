<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, watch, onMounted, onUnmounted } from 'vue'
import { getDecisions, type RoutingDecision } from '../api'
import ModelPicker from '../components/ModelPicker.vue'

const { t } = useI18n()


const rows = ref<RoutingDecision[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const sinceMinutes = ref(30)
const filterModel = ref('')
const filterSuccess = ref<'' | 'true' | 'false'>('')
const limit = ref(50)
const offset = ref(0)
const total = ref(0)
const autoRefresh = ref(true)

// Detail panel
const selectedRow = ref<RoutingDecision | null>(null)

function openDetail(row: RoutingDecision) {
  selectedRow.value = row
}
function closeDetail() {
  selectedRow.value = null
}

let timer: ReturnType<typeof setInterval> | null = null

function startAutoRefresh() {
  stopAutoRefresh()
  if (autoRefresh.value) {
    timer = setInterval(load, 5000)
  }
}

function stopAutoRefresh() {
  if (timer) {
    clearInterval(timer)
    timer = null
  }
}

watch(autoRefresh, (newVal) => {
  if (newVal) {
    startAutoRefresh()
  } else {
    stopAutoRefresh()
  }
})

async function load() {
  loading.value = true
  error.value = null
  try {
    const params: Record<string, unknown> = {
      since_minutes: sinceMinutes.value,
      limit: limit.value,
    }
    if (filterModel.value.trim()) params.model = filterModel.value.trim()
    if (filterSuccess.value !== '') params.success = filterSuccess.value === 'true'
    params.offset = offset.value
    const resp = await getDecisions(params)
    rows.value = resp.decisions
    total.value = resp.total
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function fmtTs(ts: string) {
  return new Date(ts).toLocaleTimeString('zh-CN', { hour12: false })
}

function traceList(v: unknown): string {
  if (!Array.isArray(v) || !v.length) return t('decisions.dash')
  return v
    .map((item) => {
      if (!item || typeof item !== 'object') return String(item)
      const row = item as Record<string, unknown>
      const provider = row.provider_id ?? t('decisions.dash')
      const credential = row.credential_id ?? t('decisions.dash')
      const reason = row.reason ?? row.raw_model ?? ''
      return `p${provider}/c${credential} ${reason}`.trim()
    })
    .join(' | ')
}

function resetAndLoad() {
  offset.value = 0
  load()
}

onMounted(() => {
  load()
  startAutoRefresh()
})
onUnmounted(() => {
  stopAutoRefresh()
})
</script>

<template>
  <div>
    <div class="page-header" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px">
      <h2 style="margin:0">{{ t('decisionsView.title') }}</h2>
      <label style="display:flex;align-items:center;gap:6px;font-size:12px;color:var(--muted);cursor:pointer;user-select:none">
        <input type="checkbox" v-model="autoRefresh" style="cursor:pointer;margin:0">
        <span>{{ t('decisionsView.autoRefresh') }}</span>
      </label>
    </div>

    <div class="compact-filter-bar compact-filter-bar--stacked">
      <div class="cf-row">
        <select v-model="filterSuccess" class="cf-select cf-status" :title="t('decisionsView.filter.status')" @change="resetAndLoad">
          <option value="">{{ t('decisionsView.filter.statusAll') }}</option>
          <option value="true">{{ t('decisionsView.filter.statusSuccess') }}</option>
          <option value="false">{{ t('decisionsView.filter.statusFailed') }}</option>
        </select>
        <select v-model="sinceMinutes" class="cf-select cf-hours" :title="t('decisionsView.filter.timeRange')" @change="resetAndLoad">
          <option :value="10">{{ t('decisionsView.filter.time10m') }}</option>
          <option :value="30">{{ t('decisionsView.filter.time30m') }}</option>
          <option :value="60">{{ t('decisionsView.filter.time1h') }}</option>
          <option :value="360">{{ t('decisionsView.filter.time6h') }}</option>
          <option :value="1440">{{ t('decisionsView.filter.time24h') }}</option>
        </select>
        <select v-model="limit" class="cf-select" style="width:72px" :title="t('decisionsView.filter.limit')" @change="resetAndLoad">
          <option :value="20">{{ t('decisionsView.filter.limit20') }}</option>
          <option :value="50">{{ t('decisionsView.filter.limit50') }}</option>
          <option :value="100">{{ t('decisionsView.filter.limit100') }}</option>
          <option :value="200">{{ t('decisionsView.filter.limit200') }}</option>
        </select>
        <button class="btn btn-ghost btn-sm" @click="load">{{ t('decisionsView.filter.refresh') }}</button>
        <span class="cf-meta">{{ t('decisionsView.filter.totalCount', { n: total }) }}</span>
      </div>
      <div class="cf-row cf-row--secondary">
        <div class="cf-field cf-field--grow">
          <span class="cf-label">{{ t('decisionsView.filter.modelLabel') }}</span>
          <div class="decisions-model-picker">
            <ModelPicker
              v-model="filterModel"
              :placeholder="t('decisionsView.filter.modelPlaceholder')"
              :title="t('decisionsView.filter.modelTitle')"
              @update:model-value="resetAndLoad"
            />
          </div>
        </div>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <!-- Top Pagination -->
    <div v-if="total > 0" class="card" style="margin-bottom:12px;display:flex;justify-content:space-between;align-items:center;font-size:13px">
      <div style="color:var(--muted)" v-html="t('decisionsView.pagination.summary', { total, start: offset + 1, end: Math.min(offset + limit, total) })">
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn btn-ghost btn-sm" :disabled="offset === 0" @click="offset = Math.max(0, offset - limit); load()">{{ t('decisionsView.pagination.prev') }}</button>
        <button class="btn btn-ghost btn-sm" :disabled="offset + limit >= total" @click="offset = offset + limit; load()">{{ t('decisionsView.pagination.next') }}</button>
      </div>
    </div>

    <div class="card" style="overflow:auto">
      <table class="data-table" style="min-width:1500px">
        <thead>
          <tr>
            <th>{{ t('decisionsView.table.time') }}</th>
            <th>{{ t('decisionsView.table.status') }}</th>
            <th>{{ t('decisionsView.table.model') }}</th>
            <th>{{ t('decisionsView.table.interpretation') }}</th>
            <th>Tier</th>
            <th>{{ t('decisionsView.table.latency') }}</th>
            <th>{{ t('decisionsView.table.provider') }}</th>
            <th>{{ t('decisionsView.table.outboundModel') }}</th>
            <th>prompt_t</th>
            <th>comp_t</th>
            <th>{{ t('decisionsView.table.cost') }}</th>
            <th>{{ t('decisionsView.table.candidateChain') }}</th>
            <th>{{ t('decisionsView.table.blockReason') }}</th>
            <th>{{ t('decisionsView.table.error') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="!rows.length && !loading">
            <td colspan="13" style="text-align:center;padding:32px;color:var(--muted)">
              {{ t('decisionsView.table.noData') }}
            </td>
          </tr>
          <tr v-for="r in rows" :key="r.request_id + r.ts" :class="{ 'row-fail': !r.success }" class="row-clickable" @click="openDetail(r)">
            <td style="white-space:nowrap;font-size:12px">{{ fmtTs(r.ts) }}</td>
            <td>
              <span :class="r.success ? 'badge-ok' : 'badge-err'">
                {{ r.success ? t('decisions.stickyHitOk') : t('decisions.stickyHitNo') }}
              </span>
            </td>
            <td style="font-size:12px;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{ r.model }}</td>
            <td style="font-size:11px;max-width:220px;overflow:hidden;text-overflow:ellipsis">
              <div>{{ r.resolution_path ?? t('decisions.dash') }} / {{ r.canonical_model ?? t('decisions.dash') }}</div>
              <div style="color:var(--muted)">{{ (r.resolution_raw_models || []).join(', ') || t('decisions.dash') }}</div>
            </td>
            <td style="text-align:center">{{ r.tier ?? t('decisions.dash') }}</td>
            <td style="text-align:right">{{ r.latency_ms != null ? r.latency_ms + t('decisions.msUnit') : t('decisions.dash') }}</td>
            <td style="font-size:12px">{{ r.chosen_provider_id ?? t('decisions.dash') }}</td>
            <td style="font-size:12px;max-width:160px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{ r.outbound_model ?? t('decisions.dash') }}</td>
            <td style="text-align:right">{{ r.prompt_tokens ?? t('decisions.dash') }}</td>
            <td style="text-align:right">{{ r.completion_tokens ?? t('decisions.dash') }}</td>
            <td style="text-align:right;font-size:12px">
              {{ r.cost_usd != null ? t('decisions.costUnit') + Number(r.cost_usd).toFixed(5) : t('decisions.dash') }}
            </td>
            <td style="font-size:11px;max-width:260px;overflow:hidden;text-overflow:ellipsis">
              {{ traceList((r.decision_trace || {}).planned_candidates) }}
            </td>
            <td style="font-size:11px;max-width:260px;overflow:hidden;text-overflow:ellipsis;color:var(--warning)">
              {{ traceList((r.decision_trace || {}).blocked_candidates) }}
            </td>
            <td style="font-size:11px;color:var(--danger);max-width:140px;overflow:hidden;text-overflow:ellipsis">
              {{ r.failure_detail_code ?? r.error_class ?? '' }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <div v-if="loading" style="text-align:center;padding:8px;font-size:12px;color:var(--muted)">{{ t('decisionsView.loading') }}</div>

    <!-- Pagination -->
    <div v-if="total > 0" class="card" style="margin-top:12px;display:flex;justify-content:space-between;align-items:center;font-size:13px">
      <div style="color:var(--muted)" v-html="t('decisionsView.pagination.summary', { total, start: offset + 1, end: Math.min(offset + limit, total) })">
      </div>
      <div style="display:flex;gap:8px;align-items:center">
        <button class="btn btn-ghost btn-sm" :disabled="offset === 0" @click="offset = Math.max(0, offset - limit); load()">{{ t('decisionsView.pagination.prev') }}</button>
        <button class="btn btn-ghost btn-sm" :disabled="offset + limit >= total" @click="offset = offset + limit; load()">{{ t('decisionsView.pagination.next') }}</button>
      </div>
    </div>

    <!-- Row detail modal -->
    <Teleport to="body">
      <div v-if="selectedRow" class="drawer-backdrop" @click="closeDetail">
        <div class="drawer-panel card" @click.stop>
          <div class="drawer-header">
            <span style="font-size:14px;font-weight:600">{{ t('decisionsView.detail.title') }}</span>
            <button class="btn btn-ghost btn-sm" @click="closeDetail">{{ t('decisionsView.detail.close') }}</button>
          </div>
          <div class="detail-body">

            <!-- Basic -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.basicInfo') }}</div>
              <div class="detail-grid">
                <span class="dk">{{ t('decisionsView.detail.time') }}</span><span class="dv">{{ selectedRow.ts }}</span>
                <span class="dk">Request ID</span><span class="dv mono">{{ selectedRow.request_id }}</span>
                <span class="dk">Idempotency Key</span><span class="dv mono">{{ selectedRow.idempotency_key ?? t('decisions.dash') }}</span>
                <span class="dk">Tenant</span><span class="dv mono">{{ selectedRow.tenant_id }}</span>
                <span class="dk">{{ t('decisionsView.detail.status') }}</span>
                <span class="dv">
                  <span :class="selectedRow.success ? 'badge-ok' : 'badge-err'">
                    {{ selectedRow.success ? t('decisions.successOk') : t('decisions.successFail') }}
                  </span>
                </span>
                <span class="dk">{{ t('decisionsView.detail.latency') }}</span><span class="dv">{{ selectedRow.latency_ms != null ? selectedRow.latency_ms + t('decisions.latencyUnit') : t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.clientModel') }}</span><span class="dv mono">{{ selectedRow.client_model ?? selectedRow.model }}</span>
                <span class="dk">{{ t('decisionsView.detail.outboundModel') }}</span><span class="dv mono">{{ selectedRow.outbound_model ?? t('decisions.dash') }}</span>
                <span class="dk">Request Mode</span><span class="dv">{{ selectedRow.request_mode ?? t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.protocol') }}</span><span class="dv">{{ selectedRow.egress_protocol ?? t('decisions.dash') }}</span>
                <span class="dk">Sticky Hit</span><span class="dv">{{ selectedRow.sticky_hit ? t('decisions.stickyHitOk') : t('decisions.stickyHitNo') }}</span>
              </div>
            </div>

            <!-- Resolution -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.modelResolution') }}</div>
              <div class="detail-grid">
                <span class="dk">Resolution Path</span><span class="dv mono">{{ selectedRow.resolution_path ?? t('decisions.dash') }}</span>
                <span class="dk">Canonical Model</span><span class="dv mono">{{ selectedRow.canonical_model ?? t('decisions.dash') }}</span>
                <span class="dk">Raw Models</span>
                <span class="dv mono">{{ (selectedRow.resolution_raw_models || []).join(', ') || t('decisions.dash') }}</span>
                <span class="dk">Client Profile</span><span class="dv">{{ selectedRow.client_profile ?? t('decisions.dash') }}</span>
                <span class="dk">Transform Rule</span><span class="dv mono">{{ selectedRow.transform_rule_id ?? t('decisions.dash') }}</span>
              </div>
            </div>

            <!-- Routing -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.routingDecision') }}</div>
              <div class="detail-grid">
                <span class="dk">{{ t('decisionsView.detail.providerId') }}</span><span class="dv">{{ selectedRow.chosen_provider_id ?? t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.credentialId') }}</span><span class="dv">{{ selectedRow.chosen_credential_id ?? t('decisions.dash') }}</span>
                <span class="dk">Tier</span><span class="dv">{{ selectedRow.tier ?? t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.candidatesCount') }}</span><span class="dv">{{ selectedRow.candidates_tried }}</span>
              </div>
            </div>

            <!-- Tokens & Cost -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.usage') }}</div>
              <div class="detail-grid">
                <span class="dk">Prompt Tokens</span><span class="dv">{{ selectedRow.prompt_tokens ?? t('decisions.dash') }}</span>
                <span class="dk">Completion Tokens</span><span class="dv">{{ selectedRow.completion_tokens ?? t('decisions.dash') }}</span>
                <span class="dk">{{ t('decisionsView.detail.costCalc') }}</span>
                <span class="dv">{{ selectedRow.cost_usd != null ? t('decisions.costUnit') + Number(selectedRow.cost_usd).toFixed(6) : t('decisions.dash') }}</span>
                <span class="dk">Request Size</span><span class="dv">{{ selectedRow.request_bytes != null ? selectedRow.request_bytes + t('decisions.bytesUnit') : t('decisions.dash') }}</span>
                <span class="dk">Response Size</span><span class="dv">{{ selectedRow.response_bytes != null ? selectedRow.response_bytes + t('decisions.bytesUnit') : t('decisions.dash') }}</span>
              </div>
            </div>

            <!-- Error -->
            <div v-if="!selectedRow.success" class="drawer-section">
              <div class="drawer-section-title" style="color:var(--danger)">{{ t('decisionsView.detail.errorInfo') }}</div>
              <div class="detail-grid">
                <span class="dk">Error Class</span><span class="dv" style="color:var(--danger)">{{ selectedRow.error_class ?? t('decisions.dash') }}</span>
                <span class="dk">Failure Stage</span><span class="dv">{{ selectedRow.failure_stage ?? t('decisions.dash') }}</span>
                <span class="dk">Failure Code</span><span class="dv mono">{{ selectedRow.failure_detail_code ?? t('decisions.dash') }}</span>
              </div>
            </div>

            <!-- Decision Trace -->
            <div class="drawer-section">
              <div class="drawer-section-title">{{ t('decisionsView.detail.trace') }}</div>
              <pre class="trace-json">{{ JSON.stringify(selectedRow.decision_trace, null, 2) }}</pre>
            </div>

          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.data-table { width: 100%; border-collapse: collapse; }
.data-table th {
  text-align: left;
  padding: 8px 12px;
  font-size: 12px;
  color: var(--muted);
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.data-table td {
  padding: 7px 12px;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.row-fail td { background: rgba(239,68,68,.05); }
.badge-ok  { color: #22c55e; font-weight: 600; }
.badge-err { color: #ef4444; font-weight: 600; }
.error-banner {
  background: rgba(239,68,68,.15);
  border: 1px solid #ef4444;
  border-radius: 8px;
  padding: 12px 16px;
  color: #ef4444;
  margin-bottom: 16px;
}
.row-clickable { cursor: pointer; }
.row-clickable:hover td { background: rgba(var(--accent-rgb, 99,102,241), .06); }

.detail-body {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.drawer-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.drawer-section-title {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: .06em;
  text-transform: uppercase;
  color: var(--muted);
  padding-bottom: 4px;
  border-bottom: 1px solid var(--border);
}
.detail-grid {
  display: grid;
  grid-template-columns: 140px 1fr;
  gap: 4px 12px;
  font-size: 13px;
}
.dk {
  color: var(--muted);
  font-size: 12px;
  padding: 2px 0;
  white-space: nowrap;
}
.dv {
  word-break: break-all;
  padding: 2px 0;
}
.mono { font-family: monospace; font-size: 12px; }
.trace-json {
  font-family: monospace;
  font-size: 11px;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--bg, #13131f);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 12px;
  margin: 0;
  max-height: 320px;
  overflow-y: auto;
  color: var(--text, #cdd6f4);
}
</style>
