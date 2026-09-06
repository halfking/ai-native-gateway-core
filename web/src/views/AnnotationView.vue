<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { store } from '../store'
import { fmtDateCompact } from '../i18n/useFormat'
import { localeRef } from '../i18n'
import {
  getSamples,
  createAnnotation,
  batchAnnotate,
  deleteAnnotation,
  type AnnotationSample,
  type SamplesParams,
} from '../api/annotations'
import AnnotationForm from '../components/AnnotationForm.vue'

const { t } = useI18n()

const samples = ref<AnnotationSample[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(50)
const loading = ref(false)
const error = ref('')

// Filters
const filterStartDate = ref('')
const filterEndDate = ref('')
const filterMinConfidence = ref<number | undefined>(undefined)
const filterMaxConfidence = ref<number | undefined>(1.0)
const filterAnnotated = ref<boolean | undefined>(undefined)
const filterAnnotator = ref('')

// Selection
const selectedIds = ref<Set<string>>(new Set())

// Drawer state
const drawerVisible = ref(false)
const drawerMode = ref<'annotate' | 'view'>('annotate')
const currentSample = ref<AnnotationSample | null>(null)

// Batch annotation
const showBatchDialog = ref(false)
const batchHumanProvider = ref('')
const batchIsCorrect = ref(true)
const batchReason = ref('correct')
const batchAnnotator = ref('')

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / size.value)))

const defaultAnnotator = computed(() => store.userInfo?.username || '')

const selectedCount = computed(() => selectedIds.value.size)

const allSelected = computed(() => {
  if (samples.value.length === 0) return false
  return samples.value.every(s => selectedIds.value.has(s.request_id))
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const params: SamplesParams = {
      page: page.value,
      size: size.value,
    }

    if (filterStartDate.value) params.start_date = filterStartDate.value
    if (filterEndDate.value) params.end_date = filterEndDate.value
    if (filterMinConfidence.value != null) params.min_confidence = filterMinConfidence.value
    if (filterMaxConfidence.value != null) params.max_confidence = filterMaxConfidence.value
    if (filterAnnotated.value != null) params.annotated = filterAnnotated.value
    if (filterAnnotator.value) params.annotator = filterAnnotator.value

    const r = await getSamples(params)
    samples.value = r.samples || []
    total.value = r.total || 0
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('annotation.loadFailed')
    samples.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function resetPageAndLoad() {
  page.value = 1
  selectedIds.value.clear()
  load()
}

function changePage(delta: number) {
  const next = page.value + delta
  if (next < 1 || next > totalPages.value) return
  page.value = next
  selectedIds.value.clear()
  load()
}

function clearFilters() {
  filterStartDate.value = ''
  filterEndDate.value = ''
  filterMinConfidence.value = undefined
  filterMaxConfidence.value = 1.0
  filterAnnotated.value = undefined
  filterAnnotator.value = ''
  resetPageAndLoad()
}

function toggleSelectAll() {
  if (allSelected.value) {
    selectedIds.value.clear()
  } else {
    samples.value.forEach(s => selectedIds.value.add(s.request_id))
  }
}

function toggleSelect(requestId: string) {
  if (selectedIds.value.has(requestId)) {
    selectedIds.value.delete(requestId)
  } else {
    selectedIds.value.add(requestId)
  }
}

function openAnnotateDrawer(sample: AnnotationSample) {
  currentSample.value = sample
  drawerMode.value = 'annotate'
  drawerVisible.value = true
}

function openViewDrawer(sample: AnnotationSample) {
  currentSample.value = sample
  drawerMode.value = 'view'
  drawerVisible.value = true
}

function closeDrawer() {
  drawerVisible.value = false
  currentSample.value = null
}

async function handleAnnotationSubmit(data: {
  human_provider: string
  is_correct: boolean
  reason: string
  annotator: string
}) {
  if (!currentSample.value) return

  try {
    await createAnnotation({
      request_id: currentSample.value.request_id,
      ...data,
    })
    closeDrawer()
    load() // Reload to update the list
    showSuccess(t('annotation.annotationCreated'))
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('annotation.annotationFailed')
  }
}

function openBatchDialog() {
  if (selectedCount.value === 0) {
    showWarning(t('annotation.noSamplesSelected'))
    return
  }
  batchHumanProvider.value = ''
  batchIsCorrect.value = true
  batchReason.value = 'correct'
  batchAnnotator.value = defaultAnnotator.value
  showBatchDialog.value = true
}

function closeBatchDialog() {
  showBatchDialog.value = false
}

async function handleBatchSubmit() {
  if (selectedCount.value === 0) return

  try {
    const requestIds = Array.from(selectedIds.value)
    const r = await batchAnnotate({
      request_ids: requestIds,
      human_provider: batchHumanProvider.value,
      is_correct: batchIsCorrect.value,
      reason: batchReason.value,
      annotator: batchAnnotator.value,
    })

    closeBatchDialog()
    selectedIds.value.clear()
    load()

    if (r.failed > 0) {
      showWarning(t('annotation.batchPartialSuccess', { success: r.success, failed: r.failed }))
    } else {
      showSuccess(t('annotation.batchSuccess', { count: r.success }))
    }
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('annotation.batchFailed')
  }
}

async function handleDelete(requestId: string) {
  if (!confirm(t('annotation.confirmDelete'))) return

  try {
    await deleteAnnotation(requestId)
    load()
    showSuccess(t('annotation.deleteSuccess'))
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('annotation.deleteFailed')
  }
}

function showSuccess(message: string) {
  // Simple success notification - can be enhanced with Element Plus ElMessage
  console.log('[SUCCESS]', message)
}

function showWarning(message: string) {
  // Simple warning notification
  console.warn('[WARNING]', message)
}

function confidenceBadgeClass(confidence: number): string {
  if (confidence >= 0.8) return 'badge-green'
  if (confidence >= 0.5) return 'badge-yellow'
  return 'badge-red'
}

function fmtTime(s: string | undefined) {
  if (!s) return '-'
  return new Date(s).toLocaleString(localeRef.value, { hour12: false })
}

onMounted(load)
</script>

<template>
  <div class="annotation-page">
    <div class="page-header">
      <h2>{{ t('annotation.page.title') }}</h2>
      <div class="header-actions">
        <span class="count-chip" aria-live="polite">{{ t('annotation.page.totalChip', { n: total }) }}</span>
        <button class="btn btn-primary btn-sm" :disabled="loading" @click="load">
          {{ loading ? t('annotation.page.refreshing') : t('annotation.page.refresh') }}
        </button>
      </div>
    </div>

    <p class="page-desc">{{ t('annotation.page.desc') }}</p>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>

    <!-- Filter Bar -->
    <div class="compact-filter-bar compact-filter-bar--stacked">
      <div class="cf-row">
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.startDate') }}</span>
          <input
            v-model="filterStartDate"
            type="date"
            class="cf-input"
            :aria-label="t('annotation.filter.startDate')"
          />
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.endDate') }}</span>
          <input
            v-model="filterEndDate"
            type="date"
            class="cf-input"
            :aria-label="t('annotation.filter.endDate')"
          />
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.minConfidence') }}</span>
          <input
            v-model.number="filterMinConfidence"
            type="number"
            min="0"
            max="1"
            step="0.1"
            class="cf-input"
            placeholder="0.0"
          />
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.maxConfidence') }}</span>
          <input
            v-model.number="filterMaxConfidence"
            type="number"
            min="0"
            max="1"
            step="0.1"
            class="cf-input"
            placeholder="1.0"
          />
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.annotated') }}</span>
          <select v-model="filterAnnotated" class="cf-input">
            <option :value="undefined">{{ t('annotation.filter.all') }}</option>
            <option :value="true">{{ t('annotation.filter.annotatedOnly') }}</option>
            <option :value="false">{{ t('annotation.filter.unannotatedOnly') }}</option>
          </select>
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.annotator') }}</span>
          <input
            v-model="filterAnnotator"
            type="text"
            class="cf-input"
            :placeholder="t('annotation.filter.annotatorPlaceholder')"
          />
        </div>
      </div>
      <div class="cf-row">
        <button class="btn btn-primary btn-sm" :disabled="loading" @click="resetPageAndLoad">
          {{ t('annotation.filter.query') }}
        </button>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="clearFilters">
          {{ t('annotation.filter.reset') }}
        </button>
      </div>
    </div>

    <!-- Batch Actions -->
    <div v-if="selectedCount > 0" class="batch-actions">
      <span class="batch-info">{{ t('annotation.batchSelected', { count: selectedCount }) }}</span>
      <button class="btn btn-primary btn-sm" @click="openBatchDialog">
        {{ t('annotation.batchAnnotate') }}
      </button>
      <button class="btn btn-ghost btn-sm" @click="selectedIds.clear()">
        {{ t('annotation.clearSelection') }}
      </button>
    </div>

    <!-- Pagination -->
    <div v-if="!loading && total > 0" class="pagination-bar">
      <div class="pagination-meta">
        <span>{{ t('annotation.pagination.total', { n: total }) }}</span>
        <span>{{ t('annotation.pagination.pageOf', { page, total: totalPages }) }}</span>
        <label class="page-size-label">
          <span class="text-muted">{{ t('annotation.pagination.perPage') }}</span>
          <select v-model.number="size" class="page-size-select" @change="resetPageAndLoad">
            <option :value="25">25</option>
            <option :value="50">50</option>
            <option :value="100">100</option>
            <option :value="200">200</option>
          </select>
        </label>
      </div>
      <div class="pagination-controls">
        <button
          class="btn btn-ghost btn-sm"
          :disabled="page <= 1"
          @click="changePage(-1)"
        >
          {{ t('annotation.pagination.prev') }}
        </button>
        <button
          class="btn btn-ghost btn-sm"
          :disabled="page >= totalPages"
          @click="changePage(1)"
        >
          {{ t('annotation.pagination.next') }}
        </button>
      </div>
    </div>

    <!-- Samples Table -->
    <div class="table-container">
      <table class="data-table">
        <thead>
          <tr>
            <th class="col-checkbox">
              <input type="checkbox" :checked="allSelected" @change="toggleSelectAll" />
            </th>
            <th class="col-request-id">{{ t('annotation.table.requestId') }}</th>
            <th>{{ t('annotation.table.model') }}</th>
            <th>{{ t('annotation.table.taskType') }}</th>
            <th class="col-confidence">{{ t('annotation.table.confidence') }}</th>
            <th>{{ t('annotation.table.autoProvider') }}</th>
            <th class="col-status">{{ t('annotation.table.status') }}</th>
            <th class="col-actions">{{ t('annotation.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-if="loading">
            <td colspan="8" class="loading-row">{{ t('annotation.loading') }}</td>
          </tr>
          <tr v-else-if="samples.length === 0">
            <td colspan="8" class="empty-row">{{ t('annotation.noSamples') }}</td>
          </tr>
          <tr v-for="sample in samples" :key="sample.request_id" :class="{ 'row-selected': selectedIds.has(sample.request_id) }">
            <td class="col-checkbox">
              <input
                type="checkbox"
                :checked="selectedIds.has(sample.request_id)"
                @change="toggleSelect(sample.request_id)"
              />
            </td>
            <td class="col-request-id">
              <code class="text-mono">{{ sample.request_id }}</code>
            </td>
            <td>{{ sample.model_name }}</td>
            <td>{{ sample.task_type }}</td>
            <td class="col-confidence">
              <span :class="confidenceBadgeClass(sample.confidence)" class="badge">
                {{ (sample.confidence * 100).toFixed(1) }}%
              </span>
            </td>
            <td>
              <span class="badge badge-blue">{{ sample.auto_provider }}</span>
            </td>
            <td class="col-status">
              <span v-if="sample.human_provider" class="badge badge-green">
                {{ t('annotation.table.annotated') }}
              </span>
              <span v-else class="badge badge-gray">
                {{ t('annotation.table.unannotated') }}
              </span>
            </td>
            <td class="col-actions">
              <button
                v-if="!sample.human_provider"
                class="btn-link"
                @click="openAnnotateDrawer(sample)"
              >
                {{ t('annotation.table.annotate') }}
              </button>
              <template v-else>
                <button class="btn-link" @click="openViewDrawer(sample)">
                  {{ t('annotation.table.view') }}
                </button>
                <button class="btn-link text-danger" @click="handleDelete(sample.request_id)">
                  {{ t('annotation.table.delete') }}
                </button>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Annotation Drawer -->
    <div v-if="drawerVisible" class="drawer-overlay" @click.self="closeDrawer">
      <div class="drawer">
        <div class="drawer-header">
          <h3>
            {{ drawerMode === 'annotate' ? t('annotation.drawer.annotateTitle') : t('annotation.drawer.viewTitle') }}
          </h3>
          <button class="btn-close" @click="closeDrawer">&times;</button>
        </div>
        <div class="drawer-body">
          <AnnotationForm
            v-if="drawerMode === 'annotate'"
            :sample="currentSample"
            :default-annotator="defaultAnnotator"
            @submit="handleAnnotationSubmit"
            @cancel="closeDrawer"
          />
          <div v-else-if="currentSample" class="annotation-detail">
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.humanProvider') }}:</span>
              <span class="badge badge-blue">{{ currentSample.human_provider }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.isCorrect') }}:</span>
              <span :class="currentSample.is_correct ? 'badge badge-green' : 'badge badge-red'">
                {{ currentSample.is_correct ? t('annotation.form.correct') : t('annotation.form.incorrect') }}
              </span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.reason') }}:</span>
              <span>{{ t(`annotation.reasons.${currentSample.reason}`) }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.annotator') }}:</span>
              <span>{{ currentSample.annotator }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.annotatedAt') }}:</span>
              <span>{{ fmtTime(currentSample.annotated_at) }}</span>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Batch Annotation Dialog -->
    <div v-if="showBatchDialog" class="modal-overlay" @click.self="closeBatchDialog">
      <div class="modal">
        <h3>{{ t('annotation.batch.title') }}</h3>
        <p class="modal-desc">{{ t('annotation.batch.desc', { count: selectedCount }) }}</p>
        
        <div class="form-body">
          <div class="form-field">
            <label class="form-label">{{ t('annotation.form.humanProvider') }}</label>
            <select v-model="batchHumanProvider" class="form-select">
              <option value="">{{ t('annotation.form.selectProvider') }}</option>
              <option v-for="p in ['openai', 'anthropic', 'aws_bedrock', 'google', 'azure']" :key="p" :value="p">
                {{ p }}
              </option>
            </select>
          </div>
          
          <div class="form-field">
            <label class="form-label">{{ t('annotation.form.isCorrect') }}</label>
            <div class="radio-group">
              <label class="radio-label">
                <input v-model="batchIsCorrect" type="radio" :value="true" />
                <span>{{ t('annotation.form.correct') }}</span>
              </label>
              <label class="radio-label">
                <input v-model="batchIsCorrect" type="radio" :value="false" />
                <span>{{ t('annotation.form.incorrect') }}</span>
              </label>
            </div>
          </div>
          
          <div class="form-field">
            <label class="form-label">{{ t('annotation.form.reason') }}</label>
            <select v-model="batchReason" class="form-select">
              <option value="correct">{{ t('annotation.reasons.correct') }}</option>
              <option value="performance">{{ t('annotation.reasons.performance') }}</option>
              <option value="cost">{{ t('annotation.reasons.cost') }}</option>
              <option value="availability">{{ t('annotation.reasons.availability') }}</option>
              <option value="quality">{{ t('annotation.reasons.quality') }}</option>
              <option value="other">{{ t('annotation.reasons.other') }}</option>
            </select>
          </div>
          
          <div class="form-field">
            <label class="form-label">{{ t('annotation.form.annotator') }}</label>
            <input v-model="batchAnnotator" type="text" class="form-input" />
          </div>
        </div>
        
        <div class="modal-actions">
          <button class="btn btn-ghost" @click="closeBatchDialog">{{ t('annotation.form.cancel') }}</button>
          <button
            class="btn btn-primary"
            :disabled="!batchHumanProvider || !batchAnnotator"
            @click="handleBatchSubmit"
          >
            {{ t('annotation.batch.submit') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.annotation-page {
  padding: 1.5rem;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 1rem;
}

.page-header h2 {
  margin: 0;
  font-size: 1.5rem;
  font-weight: 600;
}

.header-actions {
  display: flex;
  gap: 1rem;
  align-items: center;
}

.count-chip {
  padding: 0.25rem 0.75rem;
  background: var(--bg-secondary);
  border-radius: 12px;
  font-size: 0.875rem;
  font-weight: 500;
}

.page-desc {
  color: var(--text-muted);
  margin-bottom: 1.5rem;
}

.batch-actions {
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 0.75rem 1rem;
  background: var(--primary-light);
  border-radius: 6px;
  margin-bottom: 1rem;
}

.batch-info {
  font-weight: 500;
  color: var(--primary);
}

.table-container {
  overflow-x: auto;
  border: 1px solid var(--border);
  border-radius: 6px;
  margin-top: 1rem;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
}

.data-table th {
  text-align: left;
  padding: 0.75rem 1rem;
  background: var(--bg-secondary);
  border-bottom: 1px solid var(--border);
  font-weight: 600;
  font-size: 0.875rem;
}

.data-table td {
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--border);
  font-size: 0.875rem;
}

.data-table tbody tr:hover {
  background: var(--bg-hover);
}

.row-selected {
  background: var(--primary-light) !important;
}

.col-checkbox {
  width: 40px;
  text-align: center;
}

.col-request-id {
  min-width: 150px;
}

.col-confidence,
.col-status {
  width: 100px;
  text-align: center;
}

.col-actions {
  width: 150px;
  text-align: right;
}

.loading-row,
.empty-row {
  text-align: center;
  padding: 2rem !important;
  color: var(--text-muted);
}

.text-mono {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
}

.btn-link {
  background: none;
  border: none;
  color: var(--primary);
  cursor: pointer;
  padding: 0;
  margin: 0 0.5rem;
  text-decoration: underline;
  font-size: 0.875rem;
}

.btn-link:hover {
  color: var(--primary-hover);
}

.btn-link.text-danger {
  color: var(--danger);
}

/* Drawer styles */
.drawer-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  justify-content: flex-end;
  z-index: 1000;
}

.drawer {
  width: 500px;
  max-width: 90vw;
  background: var(--bg);
  box-shadow: -2px 0 8px rgba(0, 0, 0, 0.15);
  display: flex;
  flex-direction: column;
}

.drawer-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 1.5rem;
  border-bottom: 1px solid var(--border);
}

.drawer-header h3 {
  margin: 0;
  font-size: 1.25rem;
  font-weight: 600;
}

.btn-close {
  background: none;
  border: none;
  font-size: 1.5rem;
  cursor: pointer;
  color: var(--text-muted);
  padding: 0;
  width: 2rem;
  height: 2rem;
  display: flex;
  align-items: center;
  justify-content: center;
}

.btn-close:hover {
  color: var(--text);
}

.drawer-body {
  flex: 1;
  overflow-y: auto;
  padding: 1.5rem;
}

.annotation-detail {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.detail-row {
  display: flex;
  align-items: center;
  gap: 1rem;
}

.detail-label {
  font-weight: 500;
  color: var(--text-muted);
  min-width: 120px;
}

/* Modal styles */
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal {
  background: var(--bg);
  border-radius: 8px;
  padding: 1.5rem;
  width: 500px;
  max-width: 90vw;
  box-shadow: 0 4px 16px rgba(0, 0, 0, 0.2);
}

.modal h3 {
  margin: 0 0 0.5rem 0;
  font-size: 1.25rem;
  font-weight: 600;
}

.modal-desc {
  color: var(--text-muted);
  margin-bottom: 1.5rem;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.75rem;
  margin-top: 1.5rem;
}

.form-body {
  display: flex;
  flex-direction: column;
  gap: 1rem;
}

.form-field {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.form-label {
  font-size: 0.875rem;
  font-weight: 500;
}

.form-select,
.form-input {
  padding: 0.5rem 0.75rem;
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 0.875rem;
  background: var(--bg);
  color: var(--text);
}

.radio-group {
  display: flex;
  gap: 1.5rem;
}

.radio-label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  cursor: pointer;
}

.badge {
  padding: 0.25rem 0.5rem;
  border-radius: 3px;
  font-size: 0.75rem;
  font-weight: 500;
  white-space: nowrap;
}

.badge-blue {
  background: #e3f2fd;
  color: #1976d2;
}

.badge-green {
  background: #e8f5e9;
  color: #388e3c;
}

.badge-yellow {
  background: #fff3e0;
  color: #f57c00;
}

.badge-red {
  background: #ffebee;
  color: #d32f2f;
}

.badge-gray {
  background: var(--bg-secondary);
  color: var(--text-muted);
}
</style>
