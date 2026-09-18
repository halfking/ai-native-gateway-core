<script setup lang="ts">
// AnnotationView.vue — 首轮会话标注工作台(2026-09-14 重构)。
//
// 语义变更:旧版列出每条 auto 路由请求;新版以「会话第一轮」为行
// (session_turns.turn_no = 1 且带有 auto_route_selections 决策),
// 展示会话标题/客户端/原任务类型/自动模型/置信度/状态/标注信息,
// 人工标注沉淀「任务类型 + 所选模型」用于 auto 任务类型定位训练。
// 旧请求级样本列表仍在 GET /api/admin/annotations/samples 保留。
import { ref, computed, onMounted } from 'vue'
import { formatDateTime } from '../utils/datetime'
import { useI18n } from 'vue-i18n'
import { store } from '../store'
import { localeRef } from '../i18n'
import { getFirstTurnSamples, createAnnotation, type FirstTurnSample, type FirstTurnParams } from '../api/annotations'
import { createTaskTypeCorrection } from '../api/taskProfile'
import { L1_TASK_TYPES, listL1TaskTypes, type L1TaskTypeMeta } from '../api-work-types'
import { getAvailableModelsRaw } from '../api/models'
import { getUnifiedRequestDetail, type UnifiedRequestDetail } from '../api/requestDetail'
import DataTable from '../components/ui/DataTable.vue'
import PaginationBar from '../components/ui/PaginationBar.vue'
import AppModal from '../components/ui/AppModal.vue'
import AnnotationForm from '../components/AnnotationForm.vue'

const { t } = useI18n()

const samples = ref<FirstTurnSample[]>([])
const total = ref(0)
const page = ref(1)
const size = ref(50)
const loading = ref(false)
const error = ref('')
// R43: taskprofile 修正写入失败的非阻塞提示（主标注成功但修正未落库时可见）
const correctionWarning = ref('')

// 默认当天(UTC 日期串,与后端默认口径一致)
function utcToday(): string {
  return new Date().toISOString().slice(0, 10)
}

// Filters
const filterStartDate = ref(utcToday())
const filterEndDate = ref(utcToday())
const filterTaskType = ref('')
const filterModel = ref('')
const filterHumanTaskType = ref('')
const filterAnnotated = ref<boolean | undefined>(undefined)
const filterMinConfidence = ref<number | undefined>(undefined)
const filterMaxConfidence = ref<number | undefined>(undefined)

// Option sources
const taskTypeOptions = ref<{ key: string; label: string }[]>(L1_TASK_TYPES.map(x => ({ key: x.key, label: x.label })))
const modelOptions = ref<string[]>([])

async function loadOptions() {
  // Best-effort: keep the seed/static lists on failure.
  try {
    const r = await listL1TaskTypes()
    if (r.items?.length) {
      taskTypeOptions.value = r.items.map((x: L1TaskTypeMeta) => ({ key: x.key, label: `${x.icon ? x.icon + ' ' : ''}${x.label || x.key}` }))
    }
  } catch { /* seed list fallback */ }
  try {
    modelOptions.value = await getAvailableModelsRaw()
  } catch { /* empty fallback */ }
}

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / size.value)))

const defaultAnnotator = computed(() => store.userInfo?.username || '')

async function load() {
  loading.value = true
  error.value = ''
  correctionWarning.value = ''
  try {
    const params: FirstTurnParams = {
      page: page.value,
      size: size.value,
    }
    if (filterStartDate.value) params.start_date = filterStartDate.value
    if (filterEndDate.value) params.end_date = filterEndDate.value
    if (filterTaskType.value) params.task_type = filterTaskType.value
    if (filterModel.value) params.model = filterModel.value
    if (filterHumanTaskType.value) params.human_task_type = filterHumanTaskType.value
    if (filterAnnotated.value != null) params.annotated = filterAnnotated.value
    if (filterMinConfidence.value != null) params.min_confidence = filterMinConfidence.value
    if (filterMaxConfidence.value != null) params.max_confidence = filterMaxConfidence.value

    const r = await getFirstTurnSamples(params)
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
  load()
}

function changePage(delta: number) {
  const next = page.value + delta
  if (next < 1 || next > totalPages.value) return
  page.value = next
  load()
}

function onPageSizeChange(next: number) {
  size.value = next
  resetPageAndLoad()
}

function clearFilters() {
  filterStartDate.value = utcToday()
  filterEndDate.value = utcToday()
  filterTaskType.value = ''
  filterModel.value = ''
  filterHumanTaskType.value = ''
  filterAnnotated.value = undefined
  filterMinConfidence.value = undefined
  filterMaxConfidence.value = undefined
  resetPageAndLoad()
}

// ── 标注弹窗状态 ──
const modalVisible = ref(false)
const modalMode = ref<'annotate' | 'view'>('annotate')
const currentSample = ref<FirstTurnSample | null>(null)
const detail = ref<UnifiedRequestDetail | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
const requestPreview = ref('')

async function openModal(sample: FirstTurnSample, mode: 'annotate' | 'view') {
  currentSample.value = sample
  modalMode.value = mode
  modalVisible.value = true
  detail.value = null
  detailError.value = ''
  requestPreview.value = ''
  detailLoading.value = true
  try {
    detail.value = await getUnifiedRequestDetail(sample.request_id)
    const body = detail.value?.bodies?.request_body
    if (body != null) {
      let text = ''
      if (typeof body === 'string') text = body
      else {
        try { text = JSON.stringify(body, null, 2) } catch { text = String(body) }
      }
      requestPreview.value = text.length > 8000 ? text.slice(0, 8000) + '\n…' : text
    }
  } catch (e: unknown) {
    detailError.value = e instanceof Error ? e.message : t('annotation.detail.loadFailed')
  } finally {
    detailLoading.value = false
  }
}

function openAnnotate(sample: FirstTurnSample) { void openModal(sample, 'annotate') }
function openView(sample: FirstTurnSample) { void openModal(sample, 'view') }

function closeModal() {
  modalVisible.value = false
  currentSample.value = null
  detail.value = null
}

async function handleAnnotationSubmit(data: {
  task_type: string
  model: string
  human_provider: string
  is_correct: boolean
  reason: string
  annotator: string
}) {
  if (!currentSample.value) return
  try {
    await createAnnotation({
      request_id: currentSample.value.request_id,
      task_type: data.task_type,
      model: data.model,
      human_provider: data.human_provider,
      is_correct: data.is_correct,
      reason: data.reason,
      annotator: data.annotator,
    })
    // taskprofile 闭环（2026-09-18）：工作台收集的人工任务类型同时作为
    // 逐请求修正写入 task_type_corrections（best-effort——失败/重复不阻塞
    // 主标注流，409 表示该请求已有修正）。
    // R43 (2026-09-18): 失败不再静默吞掉——写 warning 条（409 幂等除外）。
    // 此前 catch(() => undefined) 让词汇表不匹配等 400 完全不可见。
    if (data.task_type) {
      createTaskTypeCorrection({
        request_id: currentSample.value.request_id,
        human_task_type: data.task_type,
        annotator: data.annotator,
        reason: data.is_correct ? 'correct' : data.reason,
      }).catch((corrErr: unknown) => {
        const msg = corrErr instanceof Error ? corrErr.message : String(corrErr ?? '')
        if (/409|already/i.test(msg)) return
        correctionWarning.value = t('annotation.correctionWriteFailed', { msg })
      })
    }
    closeModal()
    load()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('annotation.annotationFailed')
  }
}

function isAnnotated(s: FirstTurnSample): boolean {
  return !!(s.human_provider || s.human_task_type)
}

function confidenceBadgeClass(confidence: number): string {
  if (confidence >= 0.8) return 'badge-green'
  if (confidence >= 0.5) return 'badge-yellow'
  return 'badge-red'
}

function fmtTime(s: string | undefined | null) {
  if (!s) return '-'
  return formatDateTime(s, { locale: localeRef.value, options: { hour12: false } })
}

function fmtMetaStatus(d: UnifiedRequestDetail | null): string {
  if (!d) return '-'
  if (d.meta.request_status) return d.meta.request_status
  if (d.meta.success != null) return d.meta.success ? 'success' : 'failure'
  return '-'
}

onMounted(() => {
  load()
  void loadOptions()
})
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

    <p class="page-desc">{{ t('annotation.page.firstTurnDesc') }}</p>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>
    <div v-if="correctionWarning" class="alert alert-warning" role="alert">{{ correctionWarning }}</div>

    <!-- Filter Bar -->
    <div class="compact-filter-bar compact-filter-bar--stacked">
      <div class="cf-row">
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.startDate') }}</span>
          <input v-model="filterStartDate" type="date" class="cf-input" :aria-label="t('annotation.filter.startDate')" />
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.endDate') }}</span>
          <input v-model="filterEndDate" type="date" class="cf-input" :aria-label="t('annotation.filter.endDate')" />
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.taskType') }}</span>
          <select v-model="filterTaskType" class="cf-input">
            <option value="">{{ t('annotation.filter.all') }}</option>
            <option v-for="tt in taskTypeOptions" :key="tt.key" :value="tt.key">{{ tt.label }}</option>
          </select>
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.model') }}</span>
          <select v-model="filterModel" class="cf-input">
            <option value="">{{ t('annotation.filter.all') }}</option>
            <option v-for="m in modelOptions" :key="m" :value="m">{{ m }}</option>
            <option v-if="filterModel && !modelOptions.includes(filterModel)" :value="filterModel">{{ filterModel }}</option>
          </select>
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.humanTaskType') }}</span>
          <select v-model="filterHumanTaskType" class="cf-input">
            <option value="">{{ t('annotation.filter.all') }}</option>
            <option v-for="tt in taskTypeOptions" :key="tt.key" :value="tt.key">{{ tt.label }}</option>
          </select>
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
          <span class="cf-label">{{ t('annotation.filter.minConfidence') }}</span>
          <input v-model.number="filterMinConfidence" type="number" min="0" max="1" step="0.1" class="cf-input" placeholder="0.0" />
        </div>
        <div class="cf-field">
          <span class="cf-label">{{ t('annotation.filter.maxConfidence') }}</span>
          <input v-model.number="filterMaxConfidence" type="number" min="0" max="1" step="0.1" class="cf-input" placeholder="1.0" />
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

    <!-- Pagination -->
    <PaginationBar
      :page="page"
      :page-size="size"
      :total="total"
      :page-sizes="[25, 50, 100, 200]"
      @prev="changePage(-1)"
      @next="changePage(1)"
      @change-size="onPageSizeChange"
    />

    <!-- First-Turn Sessions Table -->
    <DataTable :loading="loading" :empty="!loading && samples.length === 0" :empty-text="t('annotation.noSamples')" min-width="1080px">
      <table class="data-table">
        <thead>
          <tr>
            <th class="col-time">{{ t('annotation.table.time') }}</th>
            <th class="col-title">{{ t('annotation.table.title') }}</th>
            <th>{{ t('annotation.table.client') }}</th>
            <th>{{ t('annotation.table.taskType') }}</th>
            <th>{{ t('annotation.table.model') }}</th>
            <th class="col-confidence">{{ t('annotation.table.confidence') }}</th>
            <th class="col-status">{{ t('annotation.table.status') }}</th>
            <th class="col-anno">{{ t('annotation.table.annotationInfo') }}</th>
            <th class="col-actions">{{ t('annotation.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="sample in samples" :key="sample.request_id">
            <td class="col-time text-mono">{{ fmtTime(sample.ts) }}</td>
            <td class="col-title" :title="sample.title || sample.request_id">
              <span v-if="sample.title" class="title-text">{{ sample.title }}</span>
              <code v-else class="text-mono">{{ sample.request_id }}</code>
            </td>
            <td>{{ sample.client || '-' }}</td>
            <td><span class="badge badge-blue">{{ sample.task_type }}</span></td>
            <td><code class="text-mono">{{ sample.chosen_model }}</code></td>
            <td class="col-confidence">
              <span v-if="sample.confidence != null" :class="confidenceBadgeClass(sample.confidence)" class="badge">
                {{ (sample.confidence * 100).toFixed(1) }}%
              </span>
              <span v-else>-</span>
            </td>
            <td class="col-status">
              <span v-if="sample.success === true" class="badge badge-green">{{ t('annotation.table.statusOk', { code: sample.status_code ?? '' }) }}</span>
              <span v-else-if="sample.success === false" class="badge badge-red">{{ t('annotation.table.statusFail', { code: sample.status_code ?? '' }) }}</span>
              <span v-else class="badge badge-gray">-</span>
            </td>
            <td class="col-anno">
              <template v-if="isAnnotated(sample)">
                <span class="badge badge-green">{{ t('annotation.table.annotated') }}</span>
                <div class="anno-meta">
                  <span v-if="sample.human_task_type" class="badge badge-blue">{{ sample.human_task_type }}</span>
                  <span v-if="sample.is_correct === false" class="badge badge-red">{{ t('annotation.form.incorrect') }}</span>
                  <span class="text-muted anno-annotator" :title="`${sample.annotator || ''} · ${fmtTime(sample.annotated_at)}`">
                    {{ sample.annotator }}
                  </span>
                </div>
              </template>
              <span v-else class="badge badge-gray">{{ t('annotation.table.unannotated') }}</span>
            </td>
            <td class="col-actions">
              <button v-if="!isAnnotated(sample)" class="btn-link" @click="openAnnotate(sample)">
                {{ t('annotation.table.annotate') }}
              </button>
              <button v-else class="btn-link" @click="openView(sample)">
                {{ t('annotation.table.view') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </DataTable>

    <!-- Annotate / View Modal -->
    <AppModal
      :model-value="modalVisible"
      :title="modalMode === 'annotate' ? t('annotation.drawer.annotateTitle') : t('annotation.drawer.viewTitle')"
      size="lg"
      @update:model-value="modalVisible = $event"
      @close="closeModal"
    >
      <div class="modal-sample-summary">
        <div class="detail-row">
          <span class="detail-label">{{ t('annotation.form.requestId') }}:</span>
          <code class="text-mono">{{ currentSample?.request_id }}</code>
        </div>
        <div class="detail-row">
          <span class="detail-label">{{ t('annotation.table.session') }}:</span>
          <code class="text-mono">{{ currentSample?.session_id }}</code>
        </div>
        <div class="detail-row">
          <span class="detail-label">{{ t('annotation.form.autoRoute') }}:</span>
          <span>
            <span class="badge badge-blue">{{ currentSample?.task_type }}</span>
            <code class="text-mono">{{ currentSample?.chosen_model }}</code>
            <span v-if="currentSample?.confidence != null" class="badge" :class="confidenceBadgeClass(currentSample.confidence)">
              {{ (currentSample.confidence * 100).toFixed(1) }}%
            </span>
          </span>
        </div>
      </div>

      <!-- First-turn detail: metadata + request body -->
      <section class="detail-section">
        <h4>{{ t('annotation.detail.metaSection') }}</h4>
        <div v-if="detailLoading" class="text-muted">{{ t('annotation.loading') }}</div>
        <div v-else-if="detailError" class="alert alert-danger">{{ detailError }}</div>
        <template v-else-if="detail">
          <div class="meta-grid">
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.clientModel') }}:</span>
              <span>{{ detail.meta.client_model || '-' }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.status') }}:</span>
              <span>{{ fmtMetaStatus(detail) }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.latency') }}:</span>
              <span>{{ detail.meta.latency_ms != null ? detail.meta.latency_ms + ' ms' : '-' }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.turnNumber') }}:</span>
              <span>{{ detail.meta.turn_number ?? '-' }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.totalTurns') }}:</span>
              <span>{{ currentSample?.total_turns ?? '-' }}</span>
            </div>
            <div class="detail-row">
              <span class="detail-label">{{ t('annotation.detail.source') }}:</span>
              <span><span class="badge badge-gray">{{ detail.source }}</span></span>
            </div>
          </div>
          <template v-if="requestPreview">
            <h4>{{ t('annotation.detail.requestSection') }}</h4>
            <pre class="request-preview">{{ requestPreview }}</pre>
          </template>
        </template>
      </section>

      <!-- Existing annotation (view mode) -->
      <section v-if="modalMode === 'view' && currentSample && isAnnotated(currentSample)" class="detail-section">
        <h4>{{ t('annotation.detail.annotationSection') }}</h4>
        <div class="meta-grid">
          <div class="detail-row">
            <span class="detail-label">{{ t('annotation.form.taskType') }}:</span>
            <span><span class="badge badge-blue">{{ currentSample.human_task_type || '-' }}</span></span>
          </div>
          <div class="detail-row">
            <span class="detail-label">{{ t('annotation.form.model') }}:</span>
            <span><code class="text-mono">{{ currentSample.human_model || currentSample.human_provider || '-' }}</code></span>
          </div>
          <div class="detail-row">
            <span class="detail-label">{{ t('annotation.detail.isCorrect') }}:</span>
            <span :class="currentSample.is_correct ? 'badge badge-green' : 'badge badge-red'">
              {{ currentSample.is_correct ? t('annotation.form.correct') : t('annotation.form.incorrect') }}
            </span>
          </div>
          <div class="detail-row">
            <span class="detail-label">{{ t('annotation.detail.reason') }}:</span>
            <span>{{ currentSample.reason ? t(`annotation.reasons.${currentSample.reason}`) : '-' }}</span>
          </div>
          <div class="detail-row">
            <span class="detail-label">{{ t('annotation.detail.annotator') }}:</span>
            <span>{{ currentSample.annotator || '-' }}</span>
          </div>
          <div class="detail-row">
            <span class="detail-label">{{ t('annotation.detail.annotatedAt') }}:</span>
            <span>{{ fmtTime(currentSample.annotated_at) }}</span>
          </div>
        </div>
      </section>

      <!-- Annotate form -->
      <AnnotationForm
        v-if="modalMode === 'annotate'"
        :sample="currentSample"
        :default-annotator="defaultAnnotator"
        :task-types="taskTypeOptions"
        :models="modelOptions"
        @submit="handleAnnotationSubmit"
        @cancel="closeModal"
      />
    </AppModal>
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

.col-time {
  width: 150px;
  white-space: nowrap;
}

.col-title {
  min-width: 220px;
  max-width: 320px;
}

.title-text {
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-all;
}

.col-confidence,
.col-status {
  width: 100px;
  text-align: center;
}

.col-anno {
  min-width: 180px;
}

.anno-meta {
  display: flex;
  gap: 0.35rem;
  align-items: center;
  margin-top: 0.25rem;
  flex-wrap: wrap;
}

.anno-annotator {
  font-size: 0.75rem;
}

.col-actions {
  width: 110px;
  text-align: right;
}

.text-mono {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  word-break: break-all;
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

/* Modal internals */
.modal-sample-summary {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  padding-bottom: 0.75rem;
  margin-bottom: 0.75rem;
  border-bottom: 1px solid var(--border);
}

.detail-section {
  margin-top: 1rem;
}

.detail-section h4 {
  margin: 0 0 0.5rem 0;
  font-size: 0.9375rem;
  font-weight: 600;
}

.meta-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 0.5rem 1.5rem;
}

.detail-row {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
  font-size: 0.875rem;
}

.detail-label {
  font-weight: 500;
  color: var(--text-muted);
  white-space: nowrap;
}

.request-preview {
  max-height: 260px;
  overflow: auto;
  padding: 0.75rem;
  background: var(--bg-secondary);
  border: 1px solid var(--border);
  border-radius: 6px;
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  white-space: pre-wrap;
  word-break: break-word;
}

.badge {
  padding: 0.25rem 0.5rem;
  border-radius: 3px;
  font-size: 0.75rem;
  font-weight: 500;
  white-space: nowrap;
}

.badge-blue {
  background: var(--info-bg);
  color: var(--accent);
}

.badge-green {
  background: var(--success-bg);
  color: var(--success-strong);
}

.badge-yellow {
  background: var(--warning-bg);
  color: var(--warning-strong);
}

.badge-red {
  background: var(--danger-bg);
  color: var(--danger-strong);
}

.badge-gray {
  background: var(--bg-secondary);
  color: var(--text-muted);
}
</style>
