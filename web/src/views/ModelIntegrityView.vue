<script setup lang="ts">
// ModelIntegrityView.vue — 模型完整性监控
//
// Mirrors FormatAnomaliesView's layout / interaction (KPI cards,
// filters, paginated table, modal detail drawer, resolve action) but
// is backed by web/src/api/integrity.ts (the /api/admin/model-integrity
// endpoints). It adds a fingerprint-drift tab, which the format
// anomalies view has no equivalent for.
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import { computed, onMounted, ref } from 'vue'
import {
  getModelIntegrityEvents,
  getModelIntegritySummary,
  getModelIntegrityFingerprintDrift,
  resolveModelIntegrity,
  type ModelIntegrityRecord,
  type ModelIntegritySummary,
} from '../api/integrity'
import { isSuperAdmin } from '../store'
import ModelPicker from '../components/ModelPicker.vue'
import ProviderPicker from '../components/ProviderPicker.vue'
import AnomalyTypePicker, { type AnomalyTypeOption } from '../components/AnomalyTypePicker.vue'

const { t } = useI18n()

type Tab = 'events' | 'drift'
const tab = ref<Tab>('events')

const events = ref<ModelIntegrityRecord[]>([])
const summaries = ref<ModelIntegritySummary[]>([])
const driftEvents = ref<ModelIntegrityRecord[]>([])
const loading = ref(false)
const summaryLoading = ref(false)
const driftLoading = ref(false)
const error = ref<string | null>(null)

const providerFilter = ref('')
const modelFilter = ref('')
const anomalyTypeFilter = ref('')
const severityFilter = ref('')
const unresolvedOnly = ref(true)
const page = ref(1)
const pageSize = ref(50)
const total = ref(0)
const summaryHours = ref(24)
const driftDays = ref(7)

const selected = ref<ModelIntegrityRecord | null>(null)
const resolutionNotes = ref('')
const resolving = ref(false)

// Integrity anomaly types come from domains/streaming/integrity/signals.go.
const anomalyTypeLabels: Record<string, string> = {
  model_mismatch: t('modelIntegrityView.anomalyType.model_mismatch'),
  finish_refusal: t('modelIntegrityView.anomalyType.finish_refusal'),
  finish_truncation: t('modelIntegrityView.anomalyType.finish_truncation'),
  token_arith_fail: t('modelIntegrityView.anomalyType.token_arith_fail'),
  empty_response: t('modelIntegrityView.anomalyType.empty_response'),
  repeated_content: t('modelIntegrityView.anomalyType.repeated_content'),
  fingerprint_drift: t('modelIntegrityView.anomalyType.fingerprint_drift'),
}

const anomalyTypeOptions: AnomalyTypeOption[] = [
  { value: '', label: t('modelIntegrityView.anomalyType.all') },
  { value: 'model_mismatch', label: t('modelIntegrityView.anomalyType.model_mismatch'), description: t('modelIntegrityView.anomalyTypeDescription.model_mismatch') },
  { value: 'finish_refusal', label: t('modelIntegrityView.anomalyType.finish_refusal'), description: t('modelIntegrityView.anomalyTypeDescription.finish_refusal') },
  { value: 'finish_truncation', label: t('modelIntegrityView.anomalyType.finish_truncation'), description: t('modelIntegrityView.anomalyTypeDescription.finish_truncation') },
  { value: 'token_arith_fail', label: t('modelIntegrityView.anomalyType.token_arith_fail'), description: t('modelIntegrityView.anomalyTypeDescription.token_arith_fail') },
  { value: 'empty_response', label: t('modelIntegrityView.anomalyType.empty_response'), description: t('modelIntegrityView.anomalyTypeDescription.empty_response') },
  { value: 'repeated_content', label: t('modelIntegrityView.anomalyType.repeated_content'), description: t('modelIntegrityView.anomalyTypeDescription.repeated_content') },
  { value: 'fingerprint_drift', label: t('modelIntegrityView.anomalyType.fingerprint_drift'), description: t('modelIntegrityView.anomalyTypeDescription.fingerprint_drift') },
]

const severityOptions = [
  { value: '', label: t('modelIntegrityView.severity.all') },
  { value: 'low', label: t('modelIntegrityView.severity.low') },
  { value: 'medium', label: t('modelIntegrityView.severity.medium') },
  { value: 'high', label: t('modelIntegrityView.severity.high') },
  { value: 'critical', label: t('modelIntegrityView.severity.critical') },
]

const severityLabels: Record<string, string> = {
  low: t('modelIntegrityView.severity.low'),
  medium: t('modelIntegrityView.severity.medium'),
  high: t('modelIntegrityView.severity.high'),
  critical: t('modelIntegrityView.severity.critical'),
}

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))
const offset = computed(() => (page.value - 1) * pageSize.value)
const totalEvents = computed(() => summaries.value.reduce((sum, s) => sum + s.anomaly_count, 0))
const unresolvedEvents = computed(() => summaries.value.reduce((sum, s) => sum + (s.anomaly_count - s.resolved_count), 0))
const criticalEvents = computed(() => summaries.value.filter((s) => s.severity === 'critical').reduce((sum, s) => sum + s.anomaly_count, 0))

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
    const resp = await getModelIntegrityEvents({
      limit: pageSize.value,
      offset: offset.value,
      provider: providerFilter.value || undefined,
      model: modelFilter.value || undefined,
      anomaly_type: anomalyTypeFilter.value || undefined,
      severity: severityFilter.value || undefined,
      unresolved_only: unresolvedOnly.value,
    })
    events.value = resp.events
    total.value = resp.count
  } catch (err: any) {
    error.value = err?.message || t('modelIntegrityView.error.loadFailed')
  } finally {
    loading.value = false
  }
}

async function loadSummary() {
  summaryLoading.value = true
  try {
    const resp = await getModelIntegritySummary(summaryHours.value)
    summaries.value = resp.summaries
  } catch (err: any) {
    error.value = err?.message || t('modelIntegrityView.error.summaryLoadFailed')
  } finally {
    summaryLoading.value = false
  }
}

async function loadDrift() {
  driftLoading.value = true
  error.value = null
  try {
    const resp = await getModelIntegrityFingerprintDrift(driftDays.value)
    driftEvents.value = resp.events
  } catch (err: any) {
    error.value = err?.message || t('modelIntegrityView.error.driftLoadFailed')
  } finally {
    driftLoading.value = false
  }
}

async function refreshAll() {
  await Promise.all([load(), loadSummary(), tab.value === 'drift' ? loadDrift() : Promise.resolve()])
}

function openDetail(item: ModelIntegrityRecord) {
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
    await resolveModelIntegrity(selected.value.id, resolutionNotes.value)
    await Promise.all([load(), loadSummary()])
    closeDetail()
  } catch (err: any) {
    error.value = err?.message || t('modelIntegrityView.error.markFailed')
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

function switchTab(next: Tab) {
  tab.value = next
  if (next === 'drift' && driftEvents.value.length === 0) {
    loadDrift()
  }
}

onMounted(async () => {
  if (!isSuperAdmin()) {
    error.value = t('modelIntegrityView.error.needSuperAdmin')
    return
  }
  await Promise.all([load(), loadSummary()])
})
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>{{ t('modelIntegrityView.pageTitle') }}</h1>
        <p>{{ t('modelIntegrityView.pageSubtitle') }}</p>
      </div>
      <button class="btn" @click="refreshAll" :disabled="loading || summaryLoading || driftLoading">{{ t('modelIntegrityView.filter.refresh') }}</button>
    </div>

    <div class="tabs">
      <button class="tab" :class="{ active: tab === 'events' }" @click="switchTab('events')">{{ t('modelIntegrityView.tabs.events') }}</button>
      <button class="tab" :class="{ active: tab === 'drift' }" @click="switchTab('drift')">{{ t('modelIntegrityView.tabs.drift') }}</button>
    </div>

    <template v-if="tab === 'events'">
      <div v-if="!summaryLoading" class="stats">
        <div class="stat-card">
          <div class="stat-label">{{ t('modelIntegrityView.stats.total') }}</div>
          <div class="stat-value">{{ totalEvents }}</div>
        </div>
        <div class="stat-card warning">
          <div class="stat-label">{{ t('modelIntegrityView.stats.unresolved') }}</div>
          <div class="stat-value">{{ unresolvedEvents }}</div>
        </div>
        <div class="stat-card danger">
          <div class="stat-label">{{ t('modelIntegrityView.stats.critical') }}</div>
          <div class="stat-value">{{ criticalEvents }}</div>
        </div>
        <div class="stat-card">
          <div class="stat-label">{{ t('modelIntegrityView.stats.window') }}</div>
          <div class="stat-value">{{ summaryHours }}h</div>
        </div>
      </div>

      <div class="filters">
        <div class="filter-field filter-field-provider">
          <label for="provider-filter">{{ t('modelIntegrityView.filter.provider') }}</label>
          <ProviderPicker v-model="providerFilter" :title="t('modelIntegrityView.filter.provider')" :placeholder="t('modelIntegrityView.filter.providerPlaceholder')" />
        </div>
        <div class="filter-field filter-field-model">
          <label for="model-filter">{{ t('modelIntegrityView.filter.model') }}</label>
          <ModelPicker v-model="modelFilter" :title="t('modelIntegrityView.filter.model')" :placeholder="t('modelIntegrityView.filter.modelPlaceholder')" />
        </div>
        <div class="filter-field filter-field-type">
          <label for="type-filter">{{ t('modelIntegrityView.filter.anomalyType') }}</label>
          <AnomalyTypePicker v-model="anomalyTypeFilter" :options="anomalyTypeOptions" :title="t('modelIntegrityView.filter.anomalyType')" :placeholder="t('modelIntegrityView.filter.anomalyTypePlaceholder')" />
        </div>
        <div class="filter-field filter-field-severity">
          <label for="severity-filter">{{ t('modelIntegrityView.filter.severity') }}</label>
          <select id="severity-filter" v-model="severityFilter" class="select">
            <option v-for="opt in severityOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
        </div>
        <label class="checkbox checkbox-inline">
          <input v-model="unresolvedOnly" type="checkbox" />
          <span>{{ t('modelIntegrityView.filter.unresolvedOnly') }}</span>
        </label>
        <div class="filter-actions">
          <button class="btn btn-primary" @click="applyFilters" :disabled="loading">{{ t('modelIntegrityView.filter.query') }}</button>
          <button class="btn" @click="refreshAll" :disabled="loading || summaryLoading">{{ t('modelIntegrityView.filter.refresh') }}</button>
        </div>
      </div>

      <div v-if="error" class="error-banner">{{ error }}</div>

      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t('modelIntegrityView.table.detectedAt') }}</th>
              <th>{{ t('modelIntegrityView.table.severity') }}</th>
              <th>{{ t('modelIntegrityView.table.anomalyType') }}</th>
              <th>{{ t('modelIntegrityView.table.providerModel') }}</th>
              <th>{{ t('modelIntegrityView.table.requestId') }}</th>
              <th>{{ t('modelIntegrityView.table.actual') }}</th>
              <th>{{ t('modelIntegrityView.table.status') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="loading">
              <td colspan="8" class="empty">{{ t('modelIntegrityView.table.loading') }}</td>
            </tr>
            <tr v-else-if="events.length === 0">
              <td colspan="8" class="empty">{{ t('modelIntegrityView.table.noData') }}</td>
            </tr>
            <tr v-for="item in events" :key="item.id">
              <td>{{ fmtTime(item.detected_at) }}</td>
              <td><span class="badge" :class="severityClass(item.severity)">{{ severityLabels[item.severity] || item.severity }}</span></td>
              <td>{{ anomalyTypeLabels[item.anomaly_type] || item.anomaly_type }}</td>
              <td>
                <div class="stacked">
                  <span>{{ item.provider_code || '—' }}</span>
                  <span class="muted">{{ item.raw_model_name || item.client_model || '—' }}</span>
                </div>
              </td>
              <td><code>{{ truncate(item.request_id, 18) }}</code></td>
              <td><code>{{ truncate(item.actual_value, 20) }}</code></td>
              <td>
                <span :class="item.resolved ? 'status-ok' : 'status-warn'">
                  {{ item.resolved ? t('modelIntegrityView.status.resolved') : t('modelIntegrityView.status.unresolved') }}
                </span>
              </td>
              <td><button class="btn btn-link" @click="openDetail(item)">{{ t('modelIntegrityView.table.viewDetail') }}</button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="pager">
        <button class="btn" @click="prevPage" :disabled="page <= 1 || loading">{{ t('modelIntegrityView.pager.prev') }}</button>
        <span>{{ t('modelIntegrityView.pager.summary', { page, totalPages, total }) }}</span>
        <button class="btn" @click="nextPage" :disabled="page >= totalPages || loading">{{ t('modelIntegrityView.pager.next') }}</button>
      </div>
    </template>

    <template v-else>
      <div class="drift-controls">
        <label>{{ t('modelIntegrityView.drift.days') }}</label>
        <input v-model.number="driftDays" type="number" min="1" max="90" class="input" />
        <button class="btn btn-primary" @click="loadDrift" :disabled="driftLoading">{{ t('modelIntegrityView.drift.query') }}</button>
      </div>

      <div v-if="error" class="error-banner">{{ error }}</div>

      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>{{ t('modelIntegrityView.table.detectedAt') }}</th>
              <th>{{ t('modelIntegrityView.table.severity') }}</th>
              <th>{{ t('modelIntegrityView.table.providerModel') }}</th>
              <th>{{ t('modelIntegrityView.table.actual') }}</th>
              <th>{{ t('modelIntegrityView.table.requestId') }}</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="driftLoading">
              <td colspan="6" class="empty">{{ t('modelIntegrityView.table.loading') }}</td>
            </tr>
            <tr v-else-if="driftEvents.length === 0">
              <td colspan="6" class="empty">{{ t('modelIntegrityView.drift.noData') }}</td>
            </tr>
            <tr v-for="item in driftEvents" :key="item.id">
              <td>{{ fmtTime(item.detected_at) }}</td>
              <td><span class="badge" :class="severityClass(item.severity)">{{ severityLabels[item.severity] || item.severity }}</span></td>
              <td>
                <div class="stacked">
                  <span>{{ item.provider_code || '—' }}</span>
                  <span class="muted">{{ item.raw_model_name || item.client_model || '—' }}</span>
                </div>
              </td>
              <td><code>{{ truncate(item.actual_value, 24) }}</code></td>
              <td><code>{{ truncate(item.request_id, 18) }}</code></td>
              <td><button class="btn btn-link" @click="openDetail(item)">{{ t('modelIntegrityView.table.viewDetail') }}</button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <div v-if="selected" class="modal-mask" @click="closeDetail">
      <div class="modal" @click.stop>
        <div class="modal-header">
          <h2>{{ t('modelIntegrityView.detail.title') }}</h2>
          <button class="btn" @click="closeDetail">{{ t('modelIntegrityView.detail.close') }}</button>
        </div>
        <div class="detail-grid">
          <div><strong>{{ t('modelIntegrityView.detail.requestId') }}</strong><div><code>{{ selected.request_id }}</code></div></div>
          <div><strong>{{ t('modelIntegrityView.detail.detectedAt') }}</strong><div>{{ fmtTime(selected.detected_at) }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.provider') }}</strong><div>{{ selected.provider_code || '—' }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.model') }}</strong><div>{{ selected.raw_model_name || selected.client_model || '—' }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.outboundModel') }}</strong><div>{{ selected.outbound_model || '—' }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.credential') }}</strong><div>{{ selected.credential_id ?? '—' }}</div></div>
          <div><strong>{{ t('modelIntegrityView.detail.expected') }}</strong><div><code>{{ selected.expected_value || '—' }}</code></div></div>
          <div><strong>{{ t('modelIntegrityView.detail.actual') }}</strong><div><code>{{ selected.actual_value || '—' }}</code></div></div>
        </div>
        <div class="detail-block">
          <strong>{{ t('modelIntegrityView.detail.context') }}</strong>
          <pre>{{ JSON.stringify(selected.context || {}, null, 2) }}</pre>
        </div>
        <div v-if="selected.sample" class="detail-block">
          <strong>{{ t('modelIntegrityView.detail.sample') }}</strong>
          <pre>{{ selected.sample }}</pre>
        </div>
        <div v-if="!selected.resolved" class="detail-block">
          <strong>{{ t('modelIntegrityView.detail.resolutionNotes') }}</strong>
          <textarea v-model="resolutionNotes" rows="4" :placeholder="t('modelIntegrityView.detail.resolutionNotesPlaceholder')" />
          <div class="detail-actions">
            <button class="btn btn-primary" @click="markResolved" :disabled="resolving">
              {{ resolving ? t('modelIntegrityView.detail.processing') : t('modelIntegrityView.detail.markResolved') }}
            </button>
          </div>
        </div>
        <div v-else class="detail-block">
          <strong>{{ t('modelIntegrityView.detail.resolutionInfo') }}</strong>
          <div>{{ fmtTime(selected.resolved_at) }}</div>
          <div class="muted">{{ selected.resolution_notes || t('modelIntegrityView.detail.noNotes') }}</div>
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
.tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 14px;
  border-bottom: 1px solid var(--border);
}
.tab {
  border: none;
  background: transparent;
  color: var(--muted);
  padding: 8px 14px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  font: inherit;
}
.tab.active {
  color: var(--text);
  border-bottom-color: var(--accent);
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
  grid-template-columns: minmax(150px, 1fr) minmax(170px, 1fr) minmax(200px, 1.2fr) minmax(130px, 1fr) auto auto;
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
.drift-controls {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--card);
}
.drift-controls .input {
  width: 90px;
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
