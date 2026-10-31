<script setup lang="ts">
// ModelCredibilityView.vue — 模型可信度（防掺水）
//
// 回答一个问题：**这个供应商给我的，是不是它声称的那个模型？**
//
// 四个子分数（身份自洽 / 能力水位 / 指纹一致 / 参考基线）分开显示，
// 是为了让运维能看出是**哪一项测量**导致了结论 —— 一个不透明的总分
// 与「猜的」没有区别。
//
// 数据来自 /api/admin/model-credibility/*（见 web/src/api/model-credibility.ts），
// 后端判定逻辑在 domains/modelcredibility。
import { useI18n } from 'vue-i18n'
import { computed, onMounted, ref } from 'vue'
import { Warning } from '@element-plus/icons-vue'
import { surfaceError } from '../i18n/errorSurface'
import { formatDateTime } from '../utils/datetime'
import {
  getModelCredibilityCatalog,
  getModelCredibilityDetail,
  getModelCredibilityHistory,
  getModelCredibilityList,
  triggerModelCredibilityScan,
  triggerModelCredibilityScanAll,
  type CredibilityVerdict,
  type ModelCredibilityCatalogRow,
  type ModelCredibilityDetail,
  type ModelCredibilityRow,
  type ModelCredibilityRun,
} from '../api/model-credibility'

const { t } = useI18n()

const rows = ref<ModelCredibilityRow[]>([])
const catalog = ref<ModelCredibilityCatalogRow[]>([])
const loading = ref(false)
const scanning = ref(false)
const error = ref<string | null>(null)

const verdictFilter = ref('')
const modelFilter = ref('')

// 排序：默认按检测时间倒序（最新在前）；可切到模型名或可信度。
// ★ 必须给「按名称」排序：这张表是「模型/供应商」实体的列表，
//   运维的第一反应是「按名字找那条」，而不是先扫一遍时间列。
type SortMode = 'time' | 'name' | 'score'
const sortMode = ref<SortMode>('time')

const detail = ref<ModelCredibilityDetail | null>(null)
const history = ref<ModelCredibilityRun[]>([])
const detailOpen = ref(false)
const detailLoading = ref(false)

const verdictOptions = computed(() => [
  { value: '', label: t('modelCredibilityView.filter.verdictPlaceholder') },
  { value: 'verified', label: t('modelCredibilityView.verdict.verified') },
  { value: 'suspected', label: t('modelCredibilityView.verdict.suspected') },
  { value: 'diluted', label: t('modelCredibilityView.verdict.diluted') },
  { value: 'unknown', label: t('modelCredibilityView.verdict.unknown') },
])

function verdictLabel(v: string): string {
  switch (v) {
    case 'verified':
      return t('modelCredibilityView.verdict.verified')
    case 'suspected':
      return t('modelCredibilityView.verdict.suspected')
    case 'diluted':
      return t('modelCredibilityView.verdict.diluted')
    case 'unknown':
      return t('modelCredibilityView.verdict.unknown')
    default:
      return v
  }
}

function verdictClass(v: string): string {
  switch (v) {
    case 'verified':
      return 'verdict-verified'
    case 'suspected':
      return 'verdict-suspected'
    case 'diluted':
      return 'verdict-diluted'
    default:
      return 'verdict-unknown'
  }
}

function statusLabel(s: string): string {
  switch (s) {
    case 'success':
      return t('modelCredibilityView.status.success')
    case 'partial':
      return t('modelCredibilityView.status.partial')
    case 'failed':
      return t('modelCredibilityView.status.failed')
    case 'inconclusive':
      return t('modelCredibilityView.status.inconclusive')
    default:
      return t('modelCredibilityView.status.unknown')
  }
}

function fmtScore(n: number | null | undefined, digits: number): string {
  if (n == null) return '—'
  return n.toFixed(digits)
}

function anomalyLabel(key: string): string {
  const map: Record<string, string> = {
    identity_model_name_mismatch: t('modelCredibilityView.anomaly.identity_model_name_mismatch'),
    identity_family_mismatch: t('modelCredibilityView.anomaly.identity_family_mismatch'),
    identity_reported_model_absent: t('modelCredibilityView.anomaly.identity_reported_model_absent'),
    capability_below_peer_best: t('modelCredibilityView.anomaly.capability_below_peer_best'),
    capability_severely_below_peer_best: t('modelCredibilityView.anomaly.capability_severely_below_peer_best'),
    capability_above_peer_best: t('modelCredibilityView.anomaly.capability_above_peer_best'),
    consistency_prompt_token_drift: t('modelCredibilityView.anomaly.consistency_prompt_token_drift'),
    consistency_system_fingerprint_drift: t('modelCredibilityView.anomaly.consistency_system_fingerprint_drift'),
    reference_below_expected_accuracy: t('modelCredibilityView.anomaly.reference_below_expected_accuracy'),
    capability_no_measurement: t('modelCredibilityView.anomaly.capability_no_measurement'),
    capability_peer_sample_too_small: t('modelCredibilityView.anomaly.capability_peer_sample_too_small'),
    identity_probe_failed: t('modelCredibilityView.anomaly.identity_probe_failed'),
    identity_probe_empty_response: t('modelCredibilityView.anomaly.identity_probe_empty_response'),
  }
  return map[key] || key
}

// ── 统计 ────────────────────────────────────────────────────────────────
// ★ 计数来自**当前列表**而不是 catalog（catalog 是按 canonical 聚合的，
// 与「节点」不是同一个口径）⇒ 统计口径与表格口径一致。
const stats = computed(() => {
  const out = { total: rows.value.length, verified: 0, suspected: 0, diluted: 0, unknown: 0, providers: new Set<number>() }
  for (const r of rows.value) {
    out.providers.add(r.provider_id)
    if (r.verdict === 'verified') out.verified++
    else if (r.verdict === 'suspected') out.suspected++
    else if (r.verdict === 'diluted') out.diluted++
    else out.unknown++
  }
  return out
})

const filteredRows = computed(() => {
  const needle = modelFilter.value.trim().toLowerCase()
  if (!needle) return rows.value
  return rows.value.filter(
    (r) =>
      r.raw_model_name.toLowerCase().includes(needle) ||
      r.canonical_name.toLowerCase().includes(needle) ||
      (r.reported_model || '').toLowerCase().includes(needle),
  )
})

const sortedRows = computed(() => {
  const out = [...filteredRows.value]
  switch (sortMode.value) {
    case 'name':
      out.sort((a, b) => a.raw_model_name.localeCompare(b.raw_model_name))
      break
    case 'score':
      // 最可疑的排最前：掺水优先处理。
      out.sort((a, b) => (a.credibility_score ?? 101) - (b.credibility_score ?? 101))
      break
    default:
      // 最新检测的排最前。measured_at 是 ISO 串（UTC），字典序即时间序。
      out.sort((a, b) => (b.measured_at ?? '').localeCompare(a.measured_at ?? ''))
      break
  }
  return out
})

async function load() {
  loading.value = true
  error.value = null
  try {
    const params: Record<string, any> = { limit: 500 }
    if (verdictFilter.value) params.verdict = verdictFilter.value
    const [list, cat] = await Promise.all([
      getModelCredibilityList(params),
      getModelCredibilityCatalog(),
    ])
    rows.value = list
    catalog.value = cat
  } catch (err) {
    error.value = surfaceError(err, { key: 'modelCredibilityView.error.loadFailed' })
  } finally {
    loading.value = false
  }
}

async function openDetail(row: ModelCredibilityRow) {
  detailOpen.value = true
  detailLoading.value = true
  detail.value = null
  history.value = []
  try {
    const [d, h] = await Promise.all([
      getModelCredibilityDetail(row.credential_id, row.raw_model_name),
      getModelCredibilityHistory(row.credential_id, row.raw_model_name, 20),
    ])
    detail.value = d
    history.value = h
  } catch (err) {
    error.value = surfaceError(err, { key: 'modelCredibilityView.error.detailFailed' })
  } finally {
    detailLoading.value = false
  }
}

async function scanOne(row: ModelCredibilityRow) {
  scanning.value = true
  error.value = null
  try {
    await triggerModelCredibilityScan(row.credential_id, row.raw_model_name)
    await load()
  } catch (err) {
    error.value = surfaceError(err, { key: 'modelCredibilityView.error.scanFailed' })
  } finally {
    scanning.value = false
  }
}

async function scanAll() {
  scanning.value = true
  error.value = null
  try {
    await triggerModelCredibilityScanAll()
    await load()
  } catch (err) {
    error.value = surfaceError(err, { key: 'modelCredibilityView.error.scanFailed' })
  } finally {
    scanning.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h1>{{ t('modelCredibilityView.pageTitle') }}</h1>
        <p>{{ t('modelCredibilityView.pageSubtitle') }}</p>
      </div>
      <div class="header-actions">
        <button class="btn" @click="load" :disabled="loading">
          {{ t('modelCredibilityView.filter.refresh') }}
        </button>
        <button class="btn btn-primary" @click="scanAll" :disabled="scanning || loading">
          {{ scanning ? t('modelCredibilityView.filter.scanning') : t('modelCredibilityView.filter.scanAll') }}
        </button>
      </div>
    </div>

    <div class="stats">
      <div class="stat-card">
        <div class="stat-label">{{ t('modelCredibilityView.stats.total') }}</div>
        <div class="stat-value">{{ stats.total }}</div>
      </div>
      <div class="stat-card ok">
        <div class="stat-label">{{ t('modelCredibilityView.stats.verified') }}</div>
        <div class="stat-value">{{ stats.verified }}</div>
      </div>
      <div class="stat-card warning">
        <div class="stat-label">{{ t('modelCredibilityView.stats.suspected') }}</div>
        <div class="stat-value">{{ stats.suspected }}</div>
      </div>
      <div class="stat-card danger">
        <div class="stat-label">{{ t('modelCredibilityView.stats.diluted') }}</div>
        <div class="stat-value">{{ stats.diluted }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('modelCredibilityView.stats.unknown') }}</div>
        <div class="stat-value">{{ stats.unknown }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('modelCredibilityView.stats.providers') }}</div>
        <div class="stat-value">{{ stats.providers.size }}</div>
      </div>
    </div>

    <div class="filters">
      <div class="filter-field">
        <label for="verdict-filter">{{ t('modelCredibilityView.filter.verdict') }}</label>
        <select id="verdict-filter" v-model="verdictFilter" class="select">
          <option v-for="opt in verdictOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
        </select>
      </div>
      <div class="filter-field">
        <label for="model-filter">{{ t('modelCredibilityView.filter.model') }}</label>
        <input
          id="model-filter"
          v-model="modelFilter"
          class="input"
          type="text"
          :placeholder="t('modelCredibilityView.filter.modelPlaceholder')"
        />
      </div>
      <div class="filter-field">
        <label for="sort-mode">{{ t('modelCredibilityView.filter.sort') }}</label>
        <select id="sort-mode" v-model="sortMode" class="select">
          <option value="time">{{ t('modelCredibilityView.filter.sortTime') }}</option>
          <option value="name">{{ t('modelCredibilityView.filter.sortName') }}</option>
          <option value="score">{{ t('modelCredibilityView.filter.sortScore') }}</option>
        </select>
      </div>
      <div class="filter-actions">
        <button class="btn btn-primary" @click="load" :disabled="loading">
          {{ t('modelCredibilityView.filter.query') }}
        </button>
      </div>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <div v-if="!loading && sortedRows.length === 0" class="empty-state">
      <p>{{ t('modelCredibilityView.empty.noData') }}</p>
      <p class="empty-hint">{{ t('modelCredibilityView.empty.hint') }}</p>
    </div>

    <div v-else class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>{{ t('modelCredibilityView.table.provider') }}</th>
            <th>{{ t('modelCredibilityView.table.credential') }}</th>
            <th>{{ t('modelCredibilityView.table.model') }}</th>
            <th>{{ t('modelCredibilityView.detail.reportedModel') }}</th>
            <th>{{ t('modelCredibilityView.table.credibility') }}</th>
            <th>{{ t('modelCredibilityView.dimension.identity') }}</th>
            <th>{{ t('modelCredibilityView.dimension.capability') }}</th>
            <th>{{ t('modelCredibilityView.dimension.consistency') }}</th>
            <th>{{ t('modelCredibilityView.dimension.reference') }}</th>
            <th>{{ t('modelCredibilityView.table.verdict') }}</th>
            <th>{{ t('modelCredibilityView.table.measuredAt') }}</th>
            <th>{{ t('modelCredibilityView.table.action') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in sortedRows" :key="`${row.credential_id}:${row.raw_model_name}`">
            <td>{{ row.provider_name || '—' }}</td>
            <td>{{ row.credential_label || row.credential_id }}</td>
            <td>{{ row.raw_model_name }}</td>
            <td>
              {{ row.reported_model || '—' }}
              <!-- craft-floor ban-glyph-icons：≠ 字形换 SVG（R183 批判式审计档
                   在案的同族存量；icon 默认 1em，尺寸走 .mismatch-mark 的 font-size） -->
              <span v-if="row.reported_model && row.reported_model !== row.raw_model_name" class="mismatch-mark">
                <Warning />
              </span>
            </td>
            <td>
              <span class="score">{{ fmtScore(row.credibility_score, 1) }}</span>
            </td>
            <td>{{ fmtScore(row.identity_score, 0) }}</td>
            <td>{{ fmtScore(row.capability_score, 0) }}</td>
            <td>{{ fmtScore(row.consistency_score, 0) }}</td>
            <td>{{ fmtScore(row.reference_score, 0) }}</td>
            <td>
              <span class="verdict-badge" :class="verdictClass(row.verdict)">{{ verdictLabel(row.verdict) }}</span>
              <span class="dims">{{ row.measured_dimensions }}/4</span>
            </td>
            <td>{{ row.measured_at ? formatDateTime(row.measured_at) : '—' }}</td>
            <td class="row-actions">
              <button class="btn btn-sm" @click="openDetail(row)">{{ t('modelCredibilityView.table.action') }}</button>
              <button class="btn btn-sm" @click="scanOne(row)" :disabled="scanning">
                {{ t('modelCredibilityView.filter.scanAll') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="detailOpen" class="modal-overlay" @click.self="detailOpen = false">
      <div class="modal">
        <div class="modal-header">
          <h2>{{ t('modelCredibilityView.detail.title') }}</h2>
          <button class="btn btn-sm" @click="detailOpen = false">{{ t('modelCredibilityView.detail.close') }}</button>
        </div>
        <div class="modal-body">
          <p v-if="detailLoading" class="modal-hint">…</p>
          <template v-else-if="detail">
            <div class="detail-grid">
              <div class="detail-item">
                <span class="detail-label">{{ t('modelCredibilityView.detail.requestedModel') }}</span>
                <span class="detail-value">{{ detail.raw_model_name }}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">{{ t('modelCredibilityView.detail.reportedModel') }}</span>
                <span class="detail-value">{{ detail.reported_model || '—' }}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">{{ t('modelCredibilityView.detail.promptSignature') }}</span>
                <span class="detail-value">{{ detail.evidence?.prompt_tokens ?? '—' }}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">{{ t('modelCredibilityView.detail.fingerprint') }}</span>
                <span class="detail-value">{{ detail.system_fingerprint || '—' }}</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">{{ t('modelCredibilityView.detail.measuredDimensions') }}</span>
                <span class="detail-value">{{ detail.measured_dimensions }}/4</span>
              </div>
              <div class="detail-item">
                <span class="detail-label">{{ t('modelCredibilityView.table.verdict') }}</span>
                <span class="detail-value">
                  <span class="verdict-badge" :class="verdictClass(detail.verdict)">{{ verdictLabel(detail.verdict) }}</span>
                </span>
              </div>
            </div>

            <h3 class="section-title">{{ t('modelCredibilityView.detail.anomalies') }}</h3>
            <ul v-if="detail.anomalies && detail.anomalies.length" class="anomaly-list">
              <li v-for="a in detail.anomalies" :key="a">{{ anomalyLabel(a) }}</li>
            </ul>
            <p v-else class="modal-hint">—</p>

            <h3 class="section-title">{{ t('modelCredibilityView.detail.evidence') }}</h3>
            <pre class="evidence-block">{{ JSON.stringify(detail.evidence, null, 2) }}</pre>

            <h3 class="section-title">{{ t('modelCredibilityView.detail.history') }}</h3>
            <div v-if="history.length" class="table-wrap">
              <table class="table">
                <thead>
                  <tr>
                    <th>{{ t('modelCredibilityView.table.measuredAt') }}</th>
                    <th>{{ t('modelCredibilityView.table.credibility') }}</th>
                    <th>{{ t('modelCredibilityView.table.verdict') }}</th>
                    <th>{{ t('modelCredibilityView.table.dimensions') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="(h, i) in history" :key="i">
                    <td>{{ formatDateTime(h.measured_at) }}</td>
                    <td>{{ fmtScore(h.credibility_score, 1) }}</td>
                    <td>
                      <span class="verdict-badge" :class="verdictClass(h.verdict)">{{ verdictLabel(h.verdict) }}</span>
                    </td>
                    <td>{{ h.measured_dimensions }}/4</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-else class="modal-hint">{{ t('modelCredibilityView.detail.noHistory') }}</p>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page {
  padding: 24px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 24px;
}

.page-header h1 {
  margin: 0 0 8px;
  font-size: 20px;
}

.page-header p {
  margin: 0;
  max-width: 880px;
  line-height: 1.6;
  opacity: 0.75;
}

.header-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}

.stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 12px;
  margin-bottom: 24px;
}

.stat-card {
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 16px;
}

.stat-card.ok {
  border-color: var(--kx-success);
}

.stat-card.warning {
  border-color: var(--kx-warning);
}

.stat-card.danger {
  border-color: var(--kx-danger);
}

.stat-label {
  font-size: 12px;
  opacity: 0.7;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 24px;
  font-weight: 600;
}

.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: flex-end;
  margin-bottom: 16px;
}

.filter-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.filter-field label {
  font-size: 12px;
  opacity: 0.7;
}

.filter-actions {
  display: flex;
  gap: 8px;
}

.error-banner {
  border: 1px solid var(--kx-danger);
  border-radius: 6px;
  padding: 12px 16px;
  margin-bottom: 16px;
}

.empty-state {
  padding: 40px 24px;
  text-align: center;
  opacity: 0.8;
}

.empty-hint {
  font-size: 13px;
  opacity: 0.7;
}

.table-wrap {
  overflow-x: auto;
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.table th,
.table td {
  border-bottom: 1px solid var(--kx-border);
  padding: 8px 12px;
  text-align: left;
  white-space: nowrap;
}

.score {
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.mismatch-mark {
  margin-left: 8px;
  color: var(--kx-danger);
  font-weight: 700;
  /* SVG 图标显式定尺寸（裸 svg 不带 width/height 会按包含块宽度渲染） */
  font-size: 12px;
  display: inline-flex;
  vertical-align: -1px;
}

.verdict-badge {
  display: inline-block;
  border-radius: 4px;
  padding: 1px 8px;
  font-size: 12px;
  border: 1px solid transparent;
}

.verdict-verified {
  color: var(--kx-success);
  border-color: var(--kx-success);
}

.verdict-suspected {
  color: var(--kx-warning);
  border-color: var(--kx-warning);
}

.verdict-diluted {
  color: var(--kx-danger);
  border-color: var(--kx-danger);
}

.verdict-unknown {
  opacity: 0.7;
  border-color: var(--kx-border);
}

.dims {
  margin-left: 8px;
  font-size: 11px;
  opacity: 0.6;
}

.row-actions {
  display: flex;
  gap: 8px;
}

.modal-overlay {
  position: fixed;
  inset: 0;
  background: var(--overlay-strong);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 2000;
}

.modal {
  background: var(--kx-bg);
  border-radius: 8px;
  width: min(880px, 92vw);
  max-height: 86vh;
  display: flex;
  flex-direction: column;
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px;
  border-bottom: 1px solid var(--kx-border);
}

.modal-header h2 {
  margin: 0;
  font-size: 16px;
}

.modal-body {
  padding: 16px;
  overflow-y: auto;
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}

.detail-item {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.detail-label {
  font-size: 12px;
  opacity: 0.7;
}

.detail-value {
  word-break: break-all;
}

.section-title {
  margin: 16px 0 8px;
  font-size: 14px;
}

.anomaly-list {
  margin: 0;
  padding-left: 24px;
  font-size: 13px;
}

.evidence-block {
  background: var(--bg-subtle);
  border-radius: 6px;
  padding: 12px;
  font-size: 12px;
  overflow-x: auto;
  white-space: pre-wrap;
  word-break: break-all;
}

.modal-hint {
  opacity: 0.7;
  font-size: 13px;
}
</style>
