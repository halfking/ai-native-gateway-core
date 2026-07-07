<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import { computed, onMounted, ref } from 'vue'
import {
  getFormatAnomalies,
  getFormatAnomalySummary,
  resolveFormatAnomaly,
  type FormatAnomalyRecord,
  type FormatAnomalySummary,
} from '../api'
import { isSuperAdmin } from '../store'
import ModelPicker from '../components/ModelPicker.vue'
import ProviderPicker from '../components/ProviderPicker.vue'
import AnomalyTypePicker, { type AnomalyTypeOption } from '../components/AnomalyTypePicker.vue'

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
  return new Date(value).toLocaleString(localeRef.value, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
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

onMounted(async () => {
  if (!isSuperAdmin()) {
    error.value = t('formatAnomaliesView.error.needSuperAdmin')
    return
  }
  await Promise.all([load(), loadSummary()])
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
  margin-bottom: 16px;
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
.stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}
.stat-card {
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
  padding: 12px 14px;
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.03);
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
  grid-template-columns: minmax(160px, 1.1fr) minmax(180px, 1.1fr) minmax(210px, 1.3fr) auto auto;
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
  background: rgba(99, 102, 241, 0.12);
  border-color: rgba(99, 102, 241, 0.4);
}
.btn:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}
.btn-primary {
  background: var(--accent);
  border-color: var(--accent);
  color: #fff;
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
  background: rgba(248, 81, 73, 0.12);
  color: #ffb4ad;
  border: 1px solid rgba(248, 81, 73, 0.32);
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
  background: rgba(255, 255, 255, 0.02);
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
  background: rgba(99, 102, 241, 0.12);
  border-color: rgba(99, 102, 241, 0.22);
  color: #c7d2fe;
}
.badge-medium {
  background: rgba(210, 153, 34, 0.14);
  border-color: rgba(210, 153, 34, 0.24);
  color: #f7d58a;
}
.badge-high {
  background: rgba(249, 115, 22, 0.14);
  border-color: rgba(249, 115, 22, 0.24);
  color: #fdba74;
}
.badge-critical {
  background: rgba(248, 81, 73, 0.14);
  border-color: rgba(248, 81, 73, 0.26);
  color: #ffb4ad;
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
  background: rgba(15, 17, 23, 0.78);
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
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.45);
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
@media (max-width: 1200px) {
  .filters {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .checkbox-inline,
  .filter-actions {
    margin-top: 0;
    align-self: end;
  }
}
@media (max-width: 900px) {
  .page {
    padding: 14px;
  }
  .stats {
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

