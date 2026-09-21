<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '../utils/datetime'
import { localeRef } from '../i18n'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  getFormatAnomalies,
  getFormatAnomalySummary,
  resolveFormatAnomaly,
  getRequestAnomalies,
  resolveRequestAnomaly,
  batchResolveRequestAnomalies,
  type FormatAnomalyRecord,
  type FormatAnomalySummary,
  type RequestAnomalyRecord,
  type RequestAnomalyTrigger,
} from '../api'
import { isSuperAdmin } from '../store'
import ModelPicker from '../components/ModelPicker.vue'
import ProviderPicker from '../components/ProviderPicker.vue'
import AnomalyTypePicker, { type AnomalyTypeOption } from '../components/AnomalyTypePicker.vue'
import { refreshRequestAnomalyBadge } from '../composables/useRequestAnomalyBadge'

const { t } = useI18n()


const anomalies = ref<FormatAnomalyRecord[]>([])
const summaries = ref<FormatAnomalySummary[]>([])
const loading = ref(false)
const summaryLoading = ref(false)
const error = ref<string | null>(null)

const providerFilter = ref('')
const modelFilter = ref('')
const anomalyTypeFilter = ref('')
const unresolvedOnly = ref(true)
const page = ref(1)
const pageSize = ref(50)
const total = ref(0)
const summaryHours = ref(24)

const selected = ref<FormatAnomalyRecord | null>(null)
const resolutionNotes = ref('')
const resolving = ref(false)

// ── 请求错误 tab（reqprobe，2026-09-21）──────────────────────────────
type TabKey = 'format' | 'request'
const activeTab = ref<TabKey>('format')

const reqAnomalies = ref<RequestAnomalyRecord[]>([])
const reqLoading = ref(false)
const reqError = ref<string | null>(null)
const reqProviderFilter = ref('')
const reqModelFilter = ref('')
const reqTriggerFilter = ref('')
const reqDayFilter = ref('')
const reqUnresolvedOnly = ref(true)
const reqPage = ref(1)
const reqPageSize = ref(50)
const reqTotal = ref(0)
const reqSelectedIds = ref<number[]>([])
const reqDetail = ref<RequestAnomalyRecord | null>(null)
const reqNotes = ref('')
const reqResolving = ref(false)
const reqBatchResolving = ref(false)

const triggerLabels = computed<Record<string, string>>(() => ({
  param_rejected: t('formatAnomaliesView.requestTab.trigger.param_rejected'),
  mode_mismatch: t('formatAnomaliesView.requestTab.trigger.mode_mismatch'),
  upstream_error: t('formatAnomaliesView.requestTab.trigger.upstream_error'),
}))

const triggerOptions = computed<AnomalyTypeOption[]>(() => [
  { value: '', label: t('formatAnomaliesView.requestTab.trigger.all') },
  { value: 'param_rejected', label: t('formatAnomaliesView.requestTab.trigger.param_rejected'), description: t('formatAnomaliesView.requestTab.triggerDesc.param_rejected') },
  { value: 'mode_mismatch', label: t('formatAnomaliesView.requestTab.trigger.mode_mismatch'), description: t('formatAnomaliesView.requestTab.triggerDesc.mode_mismatch') },
  { value: 'upstream_error', label: t('formatAnomaliesView.requestTab.trigger.upstream_error'), description: t('formatAnomaliesView.requestTab.triggerDesc.upstream_error') },
])

const reqTotalPages = computed(() => Math.max(1, Math.ceil(reqTotal.value / reqPageSize.value)))
const reqOffset = computed(() => (reqPage.value - 1) * reqPageSize.value)
const reqUnresolvedTotal = computed(() => reqAnomalies.value.filter((a) => !a.resolved).length)
const reqRecoveredTotal = computed(() => reqAnomalies.value.filter((a) => a.recovered_count > 0).length)

const anomalyTypeLabels: Record<string, string> = {
  missing_usage_block: t('formatAnomaliesView.anomalyType.missing_usage_block'),
  zero_completion_tokens: t('formatAnomaliesView.anomalyType.zero_completion_tokens'),
  extraction_failed: t('formatAnomaliesView.anomalyType.extraction_failed'),
  unexpected_structure: t('formatAnomaliesView.anomalyType.unexpected_structure'),
  null_usage_values: t('formatAnomaliesView.anomalyType.null_usage_values'),
}

const anomalyTypeOptions: AnomalyTypeOption[] = [
  { value: '', label: t('formatAnomaliesView.anomalyType.all') },
  { value: 'missing_usage_block', label: t('formatAnomaliesView.anomalyType.missing_usage_block'), description: t('formatAnomaliesView.anomalyTypeDescription.missing_usage_block') },
  { value: 'zero_completion_tokens', label: 'Completion Tokens = 0', description: t('formatAnomaliesView.anomalyTypeDescription.zero_completion_tokens') },
  { value: 'extraction_failed', label: t('formatAnomaliesView.anomalyType.extraction_failed'), description: t('formatAnomaliesView.anomalyTypeDescription.extraction_failed') },
  { value: 'unexpected_structure', label: t('formatAnomaliesView.anomalyType.unexpected_structure'), description: t('formatAnomaliesView.anomalyTypeDescription.unexpected_structure') },
  { value: 'null_usage_values', label: t('formatAnomaliesView.anomalyType.null_usage_values'), description: t('formatAnomaliesView.anomalyTypeDescription.null_usage_values') },
]

const severityLabels: Record<string, string> = {
  low: t('formatAnomaliesView.severity.low'),
  medium: t('formatAnomaliesView.severity.medium'),
  high: t('formatAnomaliesView.severity.high'),
  critical: t('formatAnomaliesView.severity.critical'),
}

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const offset = computed(() => (page.value - 1) * pageSize.value)
const totalAnomalies = computed(() => summaries.value.reduce((sum, s) => sum + s.anomaly_count, 0))
const unresolvedAnomalies = computed(() => summaries.value.reduce((sum, s) => sum + (s.anomaly_count - s.resolved_count), 0))
const criticalAnomalies = computed(() => summaries.value.filter((s) => s.severity === 'critical').reduce((sum, s) => sum + s.anomaly_count, 0))

function severityClass(severity: string) {
  switch (severity) {
    case 'critical':
      return 'badge-critical'
    case 'high':
      return 'badge-high'
    case 'medium':
      return 'badge-medium'
    default:
      return 'badge-low'
  }
}

function fmtTime(value?: string) {
  if (!value) return '—'
  return formatDateTime(value, {
    locale: localeRef.value,
    options: {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    },
  })
}

function truncate(value: string | undefined, n: number) {
  if (!value) return '—'
  return value.length > n ? value.slice(0, n) + '...' : value
}

async function load() {
  loading.value = true
  error.value = null
  try {
    const resp = await getFormatAnomalies({
      limit: pageSize.value,
      offset: offset.value,
      provider: providerFilter.value || undefined,
      model: modelFilter.value || undefined,
      anomaly_type: anomalyTypeFilter.value || undefined,
      unresolved_only: unresolvedOnly.value,
    })
    anomalies.value = resp.anomalies
    total.value = resp.count
  } catch (err: any) {
    error.value = err?.message || t('formatAnomaliesView.error.loadFailed')
  } finally {
    loading.value = false
  }
}

async function loadSummary() {
  summaryLoading.value = true
  try {
    const resp = await getFormatAnomalySummary(summaryHours.value)
    summaries.value = resp.summaries
  } catch (err: any) {
    error.value = err?.message || t('formatAnomaliesView.error.summaryLoadFailed')
  } finally {
    summaryLoading.value = false
  }
}

async function refreshAll() {
  await Promise.all([load(), loadSummary()])
}

function openDetail(item: FormatAnomalyRecord) {
  selected.value = item
  resolutionNotes.value = item.resolution_notes || ''
}

function closeDetail() {
  selected.value = null
  resolutionNotes.value = ''
}

async function markResolved() {
  if (!selected.value) return
  resolving.value = true
  try {
    await resolveFormatAnomaly(selected.value.id, resolutionNotes.value)
    await Promise.all([load(), loadSummary()])
    closeDetail()
  } catch (err: any) {
    error.value = err?.message || t('formatAnomaliesView.error.markFailed')
  } finally {
    resolving.value = false
  }
}

function applyFilters() {
  page.value = 1
  load()
}

function prevPage() {
  if (page.value <= 1) return
  page.value -= 1
  load()
}

function nextPage() {
  if (page.value >= totalPages.value) return
  page.value += 1
  load()
}

// ── 请求错误 tab 逻辑 ────────────────────────────────────────────────
async function loadReqAnomalies() {
  reqLoading.value = true
  reqError.value = null
  try {
    const resp = await getRequestAnomalies({
      limit: reqPageSize.value,
      offset: reqOffset.value,
      day: reqDayFilter.value || undefined,
      provider: reqProviderFilter.value || undefined,
      model: reqModelFilter.value || undefined,
      trigger: reqTriggerFilter.value || undefined,
      unresolved_only: reqUnresolvedOnly.value,
    })
    reqAnomalies.value = resp.anomalies
    reqTotal.value = resp.count
    reqSelectedIds.value = []
  } catch (err: any) {
    reqError.value = err?.message || t('formatAnomaliesView.error.loadFailed')
  } finally {
    reqLoading.value = false
  }
}

function applyReqFilters() {
  reqPage.value = 1
  loadReqAnomalies()
}

function reqPrevPage() {
  if (reqPage.value <= 1) return
  reqPage.value -= 1
  loadReqAnomalies()
}

function reqNextPage() {
  if (reqPage.value >= reqTotalPages.value) return
  reqPage.value += 1
  loadReqAnomalies()
}

function toggleReqSelected(id: number) {
  const idx = reqSelectedIds.value.indexOf(id)
  if (idx >= 0) reqSelectedIds.value.splice(idx, 1)
  else reqSelectedIds.value.push(id)
}

const reqAllSelected = computed(() =>
  reqAnomalies.value.length > 0 && reqAnomalies.value.every((a) => reqSelectedIds.value.includes(a.id)),
)

function toggleReqSelectAll() {
  if (reqAllSelected.value) reqSelectedIds.value = []
  else reqSelectedIds.value = reqAnomalies.value.filter((a) => !a.resolved).map((a) => a.id)
}

function openReqDetail(item: RequestAnomalyRecord) {
  reqDetail.value = item
  reqNotes.value = item.resolution_notes || ''
}

function closeReqDetail() {
  reqDetail.value = null
  reqNotes.value = ''
}

async function resolveReqOne() {
  if (!reqDetail.value) return
  reqResolving.value = true
  try {
    await resolveRequestAnomaly(reqDetail.value.id, reqNotes.value)
    closeReqDetail()
    await loadReqAnomalies()
    refreshRequestAnomalyBadge()
  } catch (err: any) {
    reqError.value = err?.message || t('formatAnomaliesView.error.markFailed')
  } finally {
    reqResolving.value = false
  }
}

async function batchResolveSelected() {
  if (reqSelectedIds.value.length === 0) return
  reqBatchResolving.value = true
  try {
    await batchResolveRequestAnomalies({ ids: reqSelectedIds.value })
    await loadReqAnomalies()
    refreshRequestAnomalyBadge()
  } catch (err: any) {
    reqError.value = err?.message || t('formatAnomaliesView.error.markFailed')
  } finally {
    reqBatchResolving.value = false
  }
}

async function batchResolveFiltered() {
  reqBatchResolving.value = true
  try {
    await batchResolveRequestAnomalies({
      all_unresolved: true,
      day: reqDayFilter.value || undefined,
      provider: reqProviderFilter.value || undefined,
      model: reqModelFilter.value || undefined,
      trigger: reqTriggerFilter.value || undefined,
    })
    await loadReqAnomalies()
    refreshRequestAnomalyBadge()
  } catch (err: any) {
    reqError.value = err?.message || t('formatAnomaliesView.error.markFailed')
  } finally {
    reqBatchResolving.value = false
  }
}

function switchTab(tab: TabKey) {
  activeTab.value = tab
  if (tab === 'request' && reqAnomalies.value.length === 0 && !reqLoading.value) {
    loadReqAnomalies()
  }
}

onMounted(async () => {
  if (!isSuperAdmin()) {
    error.value = t('formatAnomaliesView.error.needSuperAdmin')
    return
  }
  await Promise.all([load(), loadSummary()])
})
onUnmounted(() => {
  closeReqDetail()
  closeDetail()
})
</script>


<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>{{ t('formatAnomaliesView.pageTitle') }}</h1>
        <p>{{ t('formatAnomaliesView.pageSubtitle') }}</p>
      </div>
      <button class="btn" @click="refreshAll" :disabled="loading || summaryLoading">{{ t('formatAnomaliesView.filter.refresh') }}</button>
    </div>

    <div class="tabs" role="tablist">
      <button
        type="button"
        role="tab"
        :aria-selected="activeTab === 'format'"
        class="tab"
        :class="{ 'tab--active': activeTab === 'format' }"
        @click="switchTab('format')"
      >{{ t('formatAnomaliesView.tabs.format') }}</button>
      <button
        type="button"
        role="tab"
        :aria-selected="activeTab === 'request'"
        class="tab"
        :class="{ 'tab--active': activeTab === 'request' }"
        @click="switchTab('request')"
      >{{ t('formatAnomaliesView.tabs.request') }}</button>
    </div>

    <!-- ── Tab 1: 响应格式异常（原有） ─────────────────────────── -->
    <template v-if="activeTab === 'format'">
      <div v-if="!summaryLoading" class="stats">
        <div class="stat-card">
          <div class="stat-label">{{ t('formatAnomaliesView.stats.total') }}</div>
          <div class="stat-value">{{ totalAnomalies }}</div>
        </div>
        <div class="stat-card warning">
          <div class="stat-label">{{ t('formatAnomaliesView.stats.unresolved') }}</div>
          <div class="stat-value">{{ unresolvedAnomalies }}</div>
        </div>
        <div class="stat-card danger">
          <div class="stat-label">{{ t('formatAnomaliesView.stats.critical') }}</div>
          <div class="stat-value">{{ criticalAnomalies }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('formatAnomaliesView.stats.window') }}</div>
          <div class="stat-value">{{ summaryHours }}h</div>
        </div>
      </div>

      <div class="filters">
        <div class="filter-field filter-field-provider">
          <label for="provider-filter">{{ t('formatAnomaliesView.filter.provider') }}</label>
          <ProviderPicker v-model="providerFilter" :title="t('formatAnomaliesView.filter.provider')" :placeholder="t('formatAnomaliesView.filter.providerPlaceholder')" />
        </div>
        <div class="filter-field filter-field-model">
          <label for="model-filter">{{ t('formatAnomaliesView.filter.model') }}</label>
          <ModelPicker v-model="modelFilter" :title="t('formatAnomaliesView.filter.model')" :placeholder="t('formatAnomaliesView.filter.modelPlaceholder')" />
        </div>
        <div class="filter-field filter-field-type">
          <label for="type-filter">{{ t('formatAnomaliesView.filter.anomalyType') }}</label>
          <AnomalyTypePicker v-model="anomalyTypeFilter" :options="anomalyTypeOptions" :title="t('formatAnomaliesView.filter.anomalyType')" :placeholder="t('formatAnomaliesView.filter.anomalyTypePlaceholder')" />
        </div>
        <label class="checkbox checkbox-inline">
          <input v-model="unresolvedOnly" type="checkbox" />
          <span>{{ t('formatAnomaliesView.filter.unresolvedOnly') }}</span>
        </label>
        <div class="filter-actions">
          <button class="btn btn-primary" @click="applyFilters" :disabled="loading">{{ t('formatAnomaliesView.filter.query') }}</button>
          <button class="btn" @click="refreshAll" :disabled="loading || summaryLoading">{{ t('formatAnomaliesView.filter.refresh') }}</button>
        </div>
      </div>

      <div v-if="error" class="error-banner">{{ error }}</div>

      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t('formatAnomaliesView.table.detectedAt') }}</th>
              <th>{{ t('formatAnomaliesView.table.severity') }}</th>
              <th>{{ t('formatAnomaliesView.table.anomalyType') }}</th>
              <th>{{ t('formatAnomaliesView.table.providerModel') }}</th>
              <th>{{ t('formatAnomaliesView.table.requestId') }}</th>
              <th>{{ t('formatAnomaliesView.table.tokenInfo') }}</th>
              <th>{{ t('formatAnomaliesView.table.status') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading">
              <td colspan="8" class="empty">{{ t('formatAnomaliesView.table.loading') }}</td>
            </tr>
            <tr v-else-if="anomalies.length === 0">
              <td colspan="8" class="empty">{{ t('formatAnomaliesView.table.noData') }}</td>
            </tr>
            <tr v-for="item in anomalies" :key="item.id">
              <td>{{ fmtTime(item.detected_at) }}</td>
              <td><span class="badge" :class="severityClass(item.severity)">{{ severityLabels[item.severity] || item.severity }}</span></td>
              <td>{{ anomalyTypeLabels[item.anomaly_type] || item.anomaly_type }}</td>
              <td>
                <div class="stacked">
                  <span>{{ item.provider_code || '—' }}</span>
                  <span class="muted">{{ item.client_model || '—' }}</span>
                </div>
              </td>
              <td><code>{{ truncate(item.request_id, 18) }}</code></td>
              <td>
                <div class="stacked">
                  <span>{{ t('formatAnomaliesView.table.expectedTokens', { count: item.expected_tokens ?? '—' }) }}</span>
                  <span class="muted">{{ t('formatAnomaliesView.table.actualTokens', { count: item.actual_tokens ?? '—' }) }}</span>
                </div>
              </td>
              <td>
                <span :class="item.resolved ? 'status-ok' : 'status-warn'">
                  {{ item.resolved ? t('formatAnomaliesView.status.resolved') : t('formatAnomaliesView.status.unresolved') }}
                </span>
              </td>
              <td><button class="btn btn-link" @click="openDetail(item)">{{ t('formatAnomaliesView.table.viewDetail') }}</button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="pager">
        <button class="btn" @click="prevPage" :disabled="page <= 1 || loading">{{ t('formatAnomaliesView.pager.prev') }}</button>
        <span>{{ t('formatAnomaliesView.pager.summary', { page, totalPages, total }) }}</span>
        <button class="btn" @click="nextPage" :disabled="page >= totalPages || loading">{{ t('formatAnomaliesView.pager.next') }}</button>
      </div>

      <div v-if="selected" class="modal-mask" @click="closeDetail">
        <div class="modal" @click.stop>
          <div class="modal-header">
            <h2>{{ t('formatAnomaliesView.detail.title') }}</h2>
            <button class="btn" @click="closeDetail">{{ t('formatAnomaliesView.detail.close') }}</button>
          </div>
          <div class="detail-grid">
            <div><strong>{{ t('formatAnomaliesView.detail.requestId') }}</strong><div><code>{{ selected.request_id }}</code></div></div>
            <div><strong>{{ t('formatAnomaliesView.detail.detectedAt') }}</strong><div>{{ fmtTime(selected.detected_at) }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.detail.provider') }}</strong><div>{{ selected.provider_code || '—' }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.detail.model') }}</strong><div>{{ selected.client_model || '—' }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.detail.outboundModel') }}</strong><div>{{ selected.outbound_model || '—' }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.detail.usageSource') }}</strong><div>{{ selected.usage_source || '—' }}</div></div>
          </div>
          <div class="detail-block">
            <strong>{{ t('formatAnomaliesView.detail.responseStructure') }}</strong>
            <pre>{{ JSON.stringify(selected.response_structure || {}, null, 2) }}</pre>
          </div>
          <div class="detail-block">
            <strong>{{ t('formatAnomaliesView.detail.responseSample') }}</strong>
            <pre>{{ selected.response_sample || '—' }}</pre>
          </div>
          <div v-if="!selected.resolved" class="detail-block">
            <strong>{{ t('formatAnomaliesView.detail.resolutionNotes') }}</strong>
            <textarea v-model="resolutionNotes" rows="4" :placeholder="t('formatAnomaliesView.detail.resolutionNotesPlaceholder')" />
            <div class="detail-actions">
              <button class="btn btn-primary" @click="markResolved" :disabled="resolving">
                {{ resolving ? t('formatAnomaliesView.detail.processing') : t('formatAnomaliesView.detail.markResolved') }}
              </button>
            </div>
          </div>
          <div v-else class="detail-block">
            <strong>{{ t('formatAnomaliesView.detail.resolutionInfo') }}</strong>
            <div>{{ fmtTime(selected.resolved_at) }}</div>
            <div class="muted">{{ selected.resolution_notes || t('formatAnomaliesView.detail.noNotes') }}</div>
          </div>
        </div>
      </div>
    </template>

    <!-- ── Tab 2: 请求错误（reqprobe） ────────────────────────── -->
    <template v-else>
      <div class="stats stats-3">
        <div class="stat-card warning">
          <div class="stat-label">{{ t('formatAnomaliesView.requestTab.stats.unresolved') }}</div>
          <div class="stat-value">{{ reqUnresolvedTotal }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('formatAnomaliesView.requestTab.stats.autoRecovered') }}</div>
          <div class="stat-value">{{ reqRecoveredTotal }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('formatAnomaliesView.requestTab.stats.total') }}</div>
          <div class="stat-value">{{ reqTotal }}</div>
        </div>
      </div>

      <div class="filters">
        <div class="filter-field">
          <label for="req-day-filter">{{ t('formatAnomaliesView.requestTab.filter.day') }}</label>
          <input id="req-day-filter" v-model="reqDayFilter" type="date" class="input" />
        </div>
        <div class="filter-field">
          <label for="req-provider-filter">{{ t('formatAnomaliesView.filter.provider') }}</label>
          <ProviderPicker v-model="reqProviderFilter" :title="t('formatAnomaliesView.filter.provider')" :placeholder="t('formatAnomaliesView.filter.providerPlaceholder')" />
        </div>
        <div class="filter-field">
          <label for="req-model-filter">{{ t('formatAnomaliesView.filter.model') }}</label>
          <ModelPicker v-model="reqModelFilter" :title="t('formatAnomaliesView.filter.model')" :placeholder="t('formatAnomaliesView.filter.modelPlaceholder')" />
        </div>
        <div class="filter-field filter-field-type">
          <label for="req-trigger-filter">{{ t('formatAnomaliesView.requestTab.filter.trigger') }}</label>
          <AnomalyTypePicker v-model="reqTriggerFilter" :options="triggerOptions" :title="t('formatAnomaliesView.requestTab.filter.trigger')" :placeholder="t('formatAnomaliesView.requestTab.filter.triggerPlaceholder')" />
        </div>
        <label class="checkbox checkbox-inline">
          <input v-model="reqUnresolvedOnly" type="checkbox" />
          <span>{{ t('formatAnomaliesView.filter.unresolvedOnly') }}</span>
        </label>
        <div class="filter-actions">
          <button class="btn btn-primary" @click="applyReqFilters" :disabled="reqLoading">{{ t('formatAnomaliesView.filter.query') }}</button>
          <button class="btn" @click="loadReqAnomalies" :disabled="reqLoading">{{ t('formatAnomaliesView.filter.refresh') }}</button>
        </div>
      </div>

      <div v-if="reqError" class="error-banner">{{ reqError }}</div>

      <div class="batch-bar">
        <span class="muted">{{ t('formatAnomaliesView.requestTab.batch.selected', { n: reqSelectedIds.length }) }}</span>
        <button class="btn" @click="batchResolveSelected" :disabled="reqBatchResolving || reqSelectedIds.length === 0">
          {{ t('formatAnomaliesView.requestTab.batch.resolveSelected') }}
        </button>
        <button class="btn" @click="batchResolveFiltered" :disabled="reqBatchResolving">
          {{ t('formatAnomaliesView.requestTab.batch.resolveFiltered') }}
        </button>
      </div>

      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th class="col-check">
                <input type="checkbox" :checked="reqAllSelected" :disabled="reqAnomalies.length === 0" @change="toggleReqSelectAll" />
              </th>
              <th>{{ t('formatAnomaliesView.requestTab.table.day') }}</th>
              <th>{{ t('formatAnomaliesView.table.providerModel') }}</th>
              <th>{{ t('formatAnomaliesView.requestTab.table.trigger') }}</th>
              <th>{{ t('formatAnomaliesView.requestTab.table.param') }}</th>
              <th>{{ t('formatAnomaliesView.requestTab.table.status') }}</th>
              <th>{{ t('formatAnomaliesView.requestTab.table.occurrences') }}</th>
              <th>{{ t('formatAnomaliesView.table.status') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="reqLoading">
              <td colspan="9" class="empty">{{ t('formatAnomaliesView.table.loading') }}</td>
            </tr>
            <tr v-else-if="reqAnomalies.length === 0">
              <td colspan="9" class="empty">{{ t('formatAnomaliesView.table.noData') }}</td>
            </tr>
            <tr v-for="item in reqAnomalies" :key="item.id" :class="{ 'row-resolved': item.resolved }">
              <td class="col-check">
                <input v-if="!item.resolved" type="checkbox" :checked="reqSelectedIds.includes(item.id)" @change="toggleReqSelected(item.id)" />
              </td>
              <td><code>{{ item.day }}</code></td>
              <td>
                <div class="stacked">
                  <span>{{ item.provider_code || '—' }}</span>
                  <span class="muted">{{ item.outbound_model || item.client_model || '—' }}</span>
                </div>
              </td>
              <td>
                <div class="stacked">
                  <span class="badge" :class="item.trigger === 'param_rejected' ? 'badge-medium' : 'badge-high'">
                    {{ triggerLabels[item.trigger as RequestAnomalyTrigger] || item.trigger }}
                  </span>
                  <span class="muted">{{ item.error_kind || '—' }}</span>
                </div>
              </td>
              <td>
                <div class="stacked">
                  <span v-if="item.param"><code>{{ item.param }}</code></span>
                  <span v-else class="muted">—</span>
                  <span v-if="item.suggest_mode" class="muted">→ {{ item.suggest_mode }}</span>
                </div>
              </td>
              <td>{{ item.http_status }}</td>
              <td>
                <div class="stacked">
                  <span>×{{ item.occurrences }}</span>
                  <span v-if="item.recovered_count > 0" class="status-ok">
                    {{ t('formatAnomaliesView.requestTab.table.recoveredCount', { n: item.recovered_count }) }}
                  </span>
                </div>
              </td>
              <td>
                <span :class="item.resolved ? 'status-ok' : 'status-warn'">
                  {{ item.resolved ? t('formatAnomaliesView.status.resolved') : t('formatAnomaliesView.status.unresolved') }}
                </span>
              </td>
              <td><button class="btn btn-link" @click="openReqDetail(item)">{{ t('formatAnomaliesView.table.viewDetail') }}</button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="pager">
        <button class="btn" @click="reqPrevPage" :disabled="reqPage <= 1 || reqLoading">{{ t('formatAnomaliesView.pager.prev') }}</button>
        <span>{{ t('formatAnomaliesView.pager.summary', { page: reqPage, totalPages: reqTotalPages, total: reqTotal }) }}</span>
        <button class="btn" @click="reqNextPage" :disabled="reqPage >= reqTotalPages || reqLoading">{{ t('formatAnomaliesView.pager.next') }}</button>
      </div>

      <div v-if="reqDetail" class="modal-mask" @click="closeReqDetail">
        <div class="modal" @click.stop>
          <div class="modal-header">
            <h2>{{ t('formatAnomaliesView.requestTab.detail.title') }}</h2>
            <button class="btn" @click="closeReqDetail">{{ t('formatAnomaliesView.detail.close') }}</button>
          </div>
          <div class="detail-grid">
            <div><strong>{{ t('formatAnomaliesView.requestTab.table.day') }}</strong><div><code>{{ reqDetail.day }}</code></div></div>
            <div><strong>{{ t('formatAnomaliesView.detail.provider') }}</strong><div>{{ reqDetail.provider_code || '—' }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.requestTab.detail.clientModel') }}</strong><div>{{ reqDetail.client_model || '—' }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.detail.outboundModel') }}</strong><div>{{ reqDetail.outbound_model || '—' }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.requestTab.detail.protocol') }}</strong><div>{{ reqDetail.protocol || '—' }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.requestTab.table.trigger') }}</strong><div>{{ triggerLabels[reqDetail.trigger] || reqDetail.trigger }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.requestTab.table.param') }}</strong><div><code v-if="reqDetail.param">{{ reqDetail.param }}</code><span v-else>—</span></div></div>
            <div><strong>{{ t('formatAnomaliesView.requestTab.detail.suggestMode') }}</strong><div>{{ reqDetail.suggest_mode || '—' }}</div></div>
            <div><strong>HTTP</strong><div>{{ reqDetail.http_status }} / {{ reqDetail.error_kind || '—' }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.requestTab.detail.lastSeen') }}</strong><div>{{ fmtTime(reqDetail.last_seen) }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.requestTab.detail.firstSeen') }}</strong><div>{{ fmtTime(reqDetail.first_seen) }}</div></div>
            <div><strong>{{ t('formatAnomaliesView.table.requestId') }}</strong><div><code>{{ truncate(reqDetail.last_request_id, 24) }}</code></div></div>
          </div>
          <div class="detail-block">
            <strong>{{ t('formatAnomaliesView.requestTab.detail.errorSample') }}</strong>
            <pre>{{ reqDetail.error_sample || '—' }}</pre>
          </div>
          <div class="detail-block">
            <strong>{{ t('formatAnomaliesView.requestTab.detail.occurrences') }}</strong>
            <div>×{{ reqDetail.occurrences }}</div>
            <div v-if="reqDetail.recovered_count > 0" class="status-ok">
              {{ t('formatAnomaliesView.requestTab.detail.recoveredHint', { n: reqDetail.recovered_count }) }}
            </div>
          </div>
          <div v-if="!reqDetail.resolved" class="detail-block">
            <strong>{{ t('formatAnomaliesView.detail.resolutionNotes') }}</strong>
            <textarea v-model="reqNotes" rows="4" :placeholder="t('formatAnomaliesView.detail.resolutionNotesPlaceholder')" />
            <div class="detail-actions">
              <button class="btn btn-primary" @click="resolveReqOne" :disabled="reqResolving">
                {{ reqResolving ? t('formatAnomaliesView.detail.processing') : t('formatAnomaliesView.detail.markResolved') }}
              </button>
            </div>
          </div>
          <div v-else class="detail-block">
            <strong>{{ t('formatAnomaliesView.detail.resolutionInfo') }}</strong>
            <div>{{ fmtTime(reqDetail.resolved_at) }}</div>
            <div class="muted">{{ reqDetail.resolution_notes || t('formatAnomaliesView.detail.noNotes') }}</div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.page {
  padding: 16px 18px 18px;
  color: var(--text);
}
.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 12px;
}
.page-header h1 {
  margin: 0 0 4px;
  font-size: 24px;
  line-height: 1.15;
  color: var(--text);
}
.page-header p {
  margin: 0;
  color: var(--muted);
  font-size: 13px;
}
.tabs {
  display: flex;
  gap: 6px;
  margin-bottom: 14px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 0;
}
.tab {
  border: 1px solid transparent;
  border-bottom: none;
  background: transparent;
  color: var(--muted);
  font: inherit;
  font-weight: 600;
  font-size: 13px;
  padding: 8px 14px;
  border-radius: 8px 8px 0 0;
  cursor: pointer;
}
.tab:hover {
  color: var(--text);
  background: var(--bg-subtle);
}
.tab--active {
  color: var(--accent-h);
  background: var(--card);
  border-color: var(--border);
  position: relative;
  top: 1px;
}
.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}
.stats-3 {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}
.stat-card {
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
  padding: 12px 14px;
  box-shadow: inset 0 1px 0 color-mix(in srgb, var(--kx-text) 4%, transparent);
}
.stat-card.warning {
  border-left: 4px solid var(--warning);
}
.stat-card.danger {
  border-left: 4px solid var(--danger);
}
.stat-label {
  color: var(--muted);
  font-size: 12px;
  margin-bottom: 6px;
}
.stat-value {
  font-size: 24px;
  font-weight: 600;
  line-height: 1.1;
  color: var(--text);
}
.filters {
  display: grid;
  grid-template-columns: minmax(150px, 0.9fr) minmax(160px, 1.1fr) minmax(180px, 1.1fr) minmax(210px, 1.3fr) auto auto;
  gap: 10px;
  align-items: end;
  margin-bottom: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
}
.filter-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
}
.filter-field label {
  color: var(--muted);
  font-size: 11px;
  line-height: 1;
}
.input,
.select,
textarea {
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 7px 10px;
  font: inherit;
  color: var(--text);
  background: var(--bg-subtle);
}
.input,
.select {
  min-width: 0;
}
.checkbox {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--text);
}
.checkbox-inline {
  align-self: center;
  margin-top: 18px;
  white-space: nowrap;
}
.filter-actions {
  display: inline-flex;
  gap: 8px;
  justify-content: flex-end;
  align-self: center;
  margin-top: 18px;
}
.batch-bar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}
.btn {
  border: 1px solid var(--border);
  background: var(--bg-subtle);
  color: var(--text);
  border-radius: 6px;
  padding: 7px 12px;
  cursor: pointer;
  transition: background 0.15s ease, border-color 0.15s ease, color 0.15s ease;
}
.btn:hover:not(:disabled) {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-color: color-mix(in srgb, var(--accent) 40%, transparent);
}
.btn:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.btn-primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--on-primary);
}
.btn-primary:hover:not(:disabled) {
  background: var(--accent-h);
  border-color: var(--accent-h);
}
.btn-link {
  border: none;
  color: var(--accent-h);
  background: transparent;
  padding: 0;
}
.error-banner {
  margin-bottom: 12px;
  padding: 10px 12px;
  border-radius: 8px;
  background: var(--danger-bg);
  color: var(--danger-bd);
  border: 1px solid var(--danger-bd);
}
.table-wrap {
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
}
.table {
  width: 100%;
  border-collapse: collapse;
  color: var(--text);
}
.table th,
.table td {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
  text-align: left;
  vertical-align: top;
}
.table th {
  background: var(--bg-subtle);
  color: var(--muted);
  font-weight: 600;
  font-size: 12px;
}
.table td {
  font-size: 13px;
}
.table tbody tr:hover {
  background: var(--bg-hover);
}
.row-resolved {
  opacity: 0.55;
}
.col-check {
  width: 36px;
}
.empty {
  text-align: center;
  color: var(--muted);
}
.stacked {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.muted {
  color: var(--muted);
  font-size: 12px;
}
.badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 600;
  border: 1px solid transparent;
}
.badge-low {
  background: color-mix(in srgb, var(--accent) 12%, transparent);
  border-color: color-mix(in srgb, var(--accent) 22%, transparent);
  color: var(--accent-h);
}
.badge-medium {
  background: var(--warning-bg);
  border-color: var(--warning-bd);
  color: var(--warning-bd);
}
.badge-high {
  background: var(--warning-bd);
  border-color: var(--warning-bd);
  color: var(--warning-bd);
}
.badge-critical {
  background: var(--danger-bg);
  border-color: var(--danger-bd);
  color: var(--danger-bd);
}
.status-ok {
  color: var(--success);
}
.status-warn {
  color: var(--warning);
}
code {
  display: inline-block;
  padding: 2px 6px;
  border-radius: 6px;
  background: var(--bg-subtle);
  color: var(--text);
}
.pager {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 12px;
  color: var(--muted);
  font-size: 13px;
}
.modal-mask {
  position: fixed;
  inset: 0;
  background: var(--overlay-strong);
  backdrop-filter: blur(3px);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}
.modal {
  width: min(920px, 100%);
  max-height: 90vh;
  overflow: auto;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 18px;
  color: var(--text);
  box-shadow: 0 24px 60px var(--overlay-strong);
}
.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 14px;
}
.modal-header h2 {
  margin: 0;
  color: var(--text);
  font-size: 20px;
}
.detail-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 14px;
}
.detail-block {
  margin-bottom: 14px;
}
.detail-block pre {
  margin: 8px 0 0;
  padding: 12px;
  background: var(--bg);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: auto;
  font-size: 12px;
}
.detail-actions {
  margin-top: 12px;
}
textarea {
  width: 100%;
  margin-top: 8px;
}
@media (max-width: 1024px) {
  .filters {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .checkbox-inline,
  .filter-actions {
    margin-top: 0;
    align-self: end;
  }
}
@media (max-width: 1024px) {
  .page {
    padding: 14px;
  }
  .stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .stats-3 {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .filters {
    grid-template-columns: 1fr;
  }
  .checkbox-inline,
  .filter-actions {
    margin-top: 0;
    justify-content: flex-start;
  }
  .detail-grid {
    grid-template-columns: 1fr;
  }
  .pager {
    flex-direction: column;
    gap: 10px;
  }
}
</style>
