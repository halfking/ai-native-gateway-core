<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { formatDateTime } from '../utils/datetime'
import { useI18n } from 'vue-i18n'
import { getAnnotationStats, type StatsResponse } from '../api/annotations'
import {
  getTaskTypeCorrectionStats,
  exportCorrectionsBlob,
  importCorrectionsFile,
  applyTierConfig,
  type CorrectionStatsResponse,
  type TaskProfileSuggestion,
} from '../api/taskProfile'

const { t } = useI18n()

const stats = ref<StatsResponse | null>(null)
const loading = ref(false)
const error = ref('')

const mlAccuracy = computed(() => {
  if (!stats.value || !stats.value.overall) return 0
  return stats.value.overall.accuracy_percent || 0
})

const correctPredictions = computed(() => {
  if (!stats.value || !stats.value.overall) return 0
  return stats.value.overall.correct_count || 0
})

const incorrectPredictions = computed(() => {
  if (!stats.value || !stats.value.overall) return 0
  return stats.value.overall.incorrect_count || 0
})

const totalAnnotations = computed(() => {
  if (!stats.value || !stats.value.overall) return 0
  return stats.value.overall.total_annotations || 0
})

const numAnnotators = computed(() => {
  if (!stats.value || !stats.value.overall) return 0
  return stats.value.overall.num_annotators || 0
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    stats.value = await getAnnotationStats()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('annotation.stats.loadFailed')
    stats.value = null
  } finally {
    loading.value = false
  }
}

function accuracyColor(accuracy: number): string {
  if (accuracy >= 80) return 'var(--success)'
  if (accuracy >= 60) return 'var(--warning)'
  return 'var(--danger)'
}

// ── taskprofile 任务类型修正统计（2026-09-19 审计轮接入）────────────────
const tpStats = ref<CorrectionStatsResponse | null>(null)
const tpLoading = ref(false)
const tpError = ref('')
const tpNotice = ref('')
const exporting = ref(false)
const importing = ref(false)
const applying = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

const tpRows = computed(() => {
  if (!tpStats.value) return []
  return Object.values(tpStats.value.stats).map((s) => ({
    ...s,
    suggestion: tpStats.value?.suggestions?.[s.task_type] as TaskProfileSuggestion | undefined,
  })).sort((a, b) => b.correction_rate - a.correction_rate)
})

async function loadTpStats() {
  tpLoading.value = true
  tpError.value = ''
  try {
    tpStats.value = await getTaskTypeCorrectionStats(30)
  } catch (e: unknown) {
    tpError.value = e instanceof Error ? e.message : t('annotation.stats.loadFailed')
    tpStats.value = null
  } finally {
    tpLoading.value = false
  }
}

async function exportCsv() {
  exporting.value = true
  tpError.value = ''
  try {
    const blob = await exportCorrectionsBlob({ sinceDays: 30 })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `task-type-corrections-${new Date().toISOString().slice(0, 10)}.csv`
    a.click()
    URL.revokeObjectURL(url)
  } catch (e: unknown) {
    tpError.value = e instanceof Error ? e.message : String(e)
  } finally {
    exporting.value = false
  }
}

function pickImportFile() {
  fileInput.value?.click()
}

async function onImportFile(ev: Event) {
  const input = ev.target as HTMLInputElement
  const f = input.files?.[0]
  if (!f) return
  importing.value = true
  tpError.value = ''
  tpNotice.value = ''
  try {
    const r = await importCorrectionsFile(f)
    tpNotice.value = t('annotation.stats.importDone', {
      imported: r.summary.imported,
      skipped: r.summary.skipped,
      errors: r.summary.row_errors.length,
    })
    await loadTpStats()
  } catch (e: unknown) {
    tpError.value = e instanceof Error ? e.message : String(e)
  } finally {
    importing.value = false
    input.value = ''
  }
}

async function applySuggestions() {
  if (!window.confirm(t('annotation.stats.applyConfirm'))) return
  applying.value = true
  tpError.value = ''
  tpNotice.value = ''
  try {
    const r = await applyTierConfig([])
    tpNotice.value = r.applied.length === 0
      ? t('annotation.stats.applyNone')
      : t('annotation.stats.applyDone', {
          types: r.applied.map((a) => `${a.task_type}→${a.preferred_tier}`).join(', '),
        })
    await loadTpStats()
  } catch (e: unknown) {
    tpError.value = e instanceof Error ? e.message : String(e)
  } finally {
    applying.value = false
  }
}

onMounted(() => {
  load()
  loadTpStats()
})
</script>

<template>
  <div class="stats-page">
    <div class="page-header">
      <h2>{{ t('annotation.stats.title') }}</h2>
      <button class="btn btn-primary btn-sm" :disabled="loading" @click="load">
        {{ loading ? t('annotation.stats.refreshing') : t('annotation.stats.refresh') }}
      </button>
    </div>

    <p class="page-desc">{{ t('annotation.stats.desc') }}</p>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>

    <div v-if="loading" class="loading-container">
      <p>{{ t('annotation.stats.loading') }}</p>
    </div>

    <div v-else-if="stats" class="stats-container">
      <!-- Overall Stats Cards -->
      <div class="stats-cards">
        <div class="stat-card">
          <div class="stat-icon stat-icon-primary">📊</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('annotation.stats.totalAnnotations') }}</div>
            <div class="stat-value">{{ totalAnnotations }}</div>
          </div>
        </div>

        <div class="stat-card">
          <div class="stat-icon stat-icon-success">✅</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('annotation.stats.mlAccuracy') }}</div>
            <div class="stat-value" :style="{ color: accuracyColor(mlAccuracy) }">
              {{ mlAccuracy.toFixed(1) }}%
            </div>
          </div>
        </div>

        <div class="stat-card">
          <div class="stat-icon stat-icon-green">✓</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('annotation.stats.correctPredictions') }}</div>
            <div class="stat-value">{{ correctPredictions }}</div>
          </div>
        </div>

        <div class="stat-card">
          <div class="stat-icon stat-icon-red">✗</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('annotation.stats.incorrectPredictions') }}</div>
            <div class="stat-value">{{ incorrectPredictions }}</div>
          </div>
        </div>

        <div class="stat-card">
          <div class="stat-icon stat-icon-info">👥</div>
          <div class="stat-content">
            <div class="stat-label">{{ t('annotation.stats.numAnnotators') }}</div>
            <div class="stat-value">{{ numAnnotators }}</div>
          </div>
        </div>
      </div>

      <!-- Provider Accuracy Table -->
      <div class="stats-section">
        <h3>{{ t('annotation.stats.providerAccuracy') }}</h3>
        <div class="table-container">
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ t('annotation.stats.provider') }}</th>
                <th class="col-number">{{ t('annotation.stats.totalPredictions') }}</th>
                <th class="col-number">{{ t('annotation.stats.correct') }}</th>
                <th class="col-number">{{ t('annotation.stats.incorrect') }}</th>
                <th class="col-number">{{ t('annotation.stats.accuracy') }}</th>
                <th class="col-number">{{ t('annotation.stats.avgConfidence') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!stats.by_provider || stats.by_provider.length === 0">
                <td colspan="6" class="empty-row">{{ t('annotation.stats.noData') }}</td>
              </tr>
              <tr v-for="p in stats.by_provider" :key="p.provider">
                <td><span class="badge badge-blue">{{ p.provider }}</span></td>
                <td class="col-number">{{ p.total_predictions }}</td>
                <td class="col-number text-success">{{ p.correct_predictions }}</td>
                <td class="col-number text-danger">{{ p.incorrect_predictions }}</td>
                <td class="col-number">
                  <span
                    class="accuracy-badge"
                    :style="{ color: accuracyColor(p.accuracy_percent) }"
                  >
                    {{ p.accuracy_percent.toFixed(1) }}%
                  </span>
                </td>
                <td class="col-number">{{ (p.avg_confidence * 100).toFixed(1) }}%</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Annotator Stats Table -->
      <div class="stats-section">
        <h3>{{ t('annotation.stats.annotatorStats') }}</h3>
        <div class="table-container">
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ t('annotation.stats.annotator') }}</th>
                <th class="col-number">{{ t('annotation.stats.totalAnnotations') }}</th>
                <th class="col-number">{{ t('annotation.stats.correct') }}</th>
                <th class="col-number">{{ t('annotation.stats.incorrect') }}</th>
                <th class="col-number">{{ t('annotation.stats.accuracy') }}</th>
                <th>{{ t('annotation.stats.lastAnnotation') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!stats.by_annotator || stats.by_annotator.length === 0">
                <td colspan="6" class="empty-row">{{ t('annotation.stats.noData') }}</td>
              </tr>
              <tr v-for="a in stats.by_annotator" :key="a.annotator">
                <td><strong>{{ a.annotator }}</strong></td>
                <td class="col-number">{{ a.total_annotations }}</td>
                <td class="col-number text-success">{{ a.correct_count }}</td>
                <td class="col-number text-danger">{{ a.incorrect_count }}</td>
                <td class="col-number">
                  <span
                    class="accuracy-badge"
                    :style="{ color: accuracyColor(a.accuracy_percent) }"
                  >
                    {{ a.accuracy_percent.toFixed(1) }}%
                  </span>
                </td>
                <td>{{ formatDateTime(a.last_annotation_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Reason Distribution Table -->
      <div class="stats-section">
        <h3>{{ t('annotation.stats.reasonDistribution') }}</h3>
        <div class="table-container">
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ t('annotation.stats.reason') }}</th>
                <th class="col-number">{{ t('annotation.stats.count') }}</th>
                <th class="col-number">{{ t('annotation.stats.percentage') }}</th>
                <th class="col-bar">{{ t('annotation.stats.distribution') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="!stats.by_reason || stats.by_reason.length === 0">
                <td colspan="4" class="empty-row">{{ t('annotation.stats.noData') }}</td>
              </tr>
              <tr v-for="r in stats.by_reason" :key="r.reason">
                <td>
                  <span class="badge badge-gray">{{ t(`annotation.reasons.${r.reason}`) }}</span>
                </td>
                <td class="col-number">{{ r.count }}</td>
                <td class="col-number">{{ r.percentage.toFixed(1) }}%</td>
                <td class="col-bar">
                  <div class="bar-container">
                    <div class="bar-fill" :style="{ width: r.percentage + '%' }"></div>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- taskprofile 任务类型修正统计（独立加载态，不依赖 P2.1 统计） -->
    <div class="stats-section tp-section">
      <div class="tp-header">
        <h3>{{ t('annotation.stats.taskCorrections') }}</h3>
        <div class="tp-actions">
          <button class="btn btn-sm" :disabled="exporting" @click="exportCsv">
            {{ exporting ? t('annotation.stats.exporting') : t('annotation.stats.exportCsv') }}
          </button>
          <button class="btn btn-sm" :disabled="importing" @click="pickImportFile">
            {{ importing ? t('annotation.stats.importing') : t('annotation.stats.importCsv') }}
          </button>
          <button class="btn btn-sm btn-primary" :disabled="applying" @click="applySuggestions">
            {{ applying ? t('annotation.stats.applying') : t('annotation.stats.applySuggestions') }}
          </button>
          <input
            ref="fileInput"
            type="file"
            accept=".csv,text/csv"
            style="display: none"
            @change="onImportFile"
          />
        </div>
      </div>

      <div v-if="tpNotice" class="alert alert-success" role="status">{{ tpNotice }}</div>
      <div v-if="tpError" class="alert alert-danger" role="alert">{{ tpError }}</div>

      <div v-if="tpLoading" class="loading-container">
        <p>{{ t('annotation.stats.loading') }}</p>
      </div>

      <div v-else-if="tpRows.length" class="table-container">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('annotation.stats.tpTaskType') }}</th>
              <th class="col-number">{{ t('annotation.stats.tpTotal') }}</th>
              <th class="col-number">{{ t('annotation.stats.tpAgrees') }}</th>
              <th class="col-number">{{ t('annotation.stats.tpCorrected') }}</th>
              <th class="col-number">{{ t('annotation.stats.tpRate') }}</th>
              <th>{{ t('annotation.stats.tpSuggested') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in tpRows" :key="row.task_type">
              <td><span class="badge badge-blue">{{ row.task_type }}</span></td>
              <td class="col-number">{{ row.total }}</td>
              <td class="col-number text-success">{{ row.agrees }}</td>
              <td class="col-number text-danger">{{ row.corrected }}</td>
              <td class="col-number">{{ (row.correction_rate * 100).toFixed(1) }}%</td>
              <td>
                <span v-if="row.suggestion" class="badge badge-gray">
                  {{ row.suggestion.tier }} · {{ row.suggestion.tier_source }}
                </span>
                <span v-else class="tp-muted">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="tp-muted">{{ t('annotation.stats.noData') }}</p>
    </div>
  </div>
</template>

<style scoped>
.tp-section {
  margin-top: 1.5rem;
}

.tp-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-bottom: 1rem;
}

.tp-header h3 {
  margin: 0;
  font-size: 1.125rem;
  font-weight: 600;
}

.tp-actions {
  display: flex;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.tp-muted {
  color: var(--text-muted);
}

.stats-page {
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

.page-desc {
  color: var(--text-muted);
  margin-bottom: 1.5rem;
}

.loading-container {
  text-align: center;
  padding: 3rem;
  color: var(--text-muted);
}

.stats-container {
  display: flex;
  flex-direction: column;
  gap: 2rem;
}

/* Stats Cards */
.stats-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 1rem;
}

.stat-card {
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 1.25rem;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  transition: box-shadow 0.2s;
}

.stat-card:hover {
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
}

.stat-icon {
  width: 48px;
  height: 48px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.5rem;
  border-radius: 8px;
  flex-shrink: 0;
}

.stat-icon-primary {
  background: var(--info-bg);
}

.stat-icon-success {
  background: var(--success-bg);
}

.stat-icon-green {
  background: var(--success-bg);
}

.stat-icon-red {
  background: var(--danger-bg);
}

.stat-icon-info {
  background: color-mix(in srgb, var(--purple) 14%, transparent);
}

.stat-content {
  flex: 1;
  min-width: 0;
}

.stat-label {
  font-size: 0.875rem;
  color: var(--text-muted);
  margin-bottom: 0.25rem;
}

.stat-value {
  font-size: 1.5rem;
  font-weight: 600;
  color: var(--text);
}

/* Stats Section */
.stats-section {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 1.5rem;
}

.stats-section h3 {
  margin: 0 0 1rem 0;
  font-size: 1.125rem;
  font-weight: 600;
}

/* Table */
.table-container {
  overflow-x: auto;
  border: 1px solid var(--border);
  border-radius: 6px;
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

.col-number {
  text-align: right;
  width: 100px;
}

.col-bar {
  width: 200px;
}

.empty-row {
  text-align: center;
  padding: 2rem !important;
  color: var(--text-muted);
}

.text-success {
  color: var(--success);
}

.text-danger {
  color: var(--danger);
}

.accuracy-badge {
  font-weight: 600;
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

.badge-gray {
  background: var(--bg-secondary);
  color: var(--text);
}

/* Bar Chart */
.bar-container {
  width: 100%;
  height: 20px;
  background: var(--bg-secondary);
  border-radius: 4px;
  overflow: hidden;
}

.bar-fill {
  height: 100%;
  background: linear-gradient(90deg, var(--accent), var(--probe-cyan));
  border-radius: 4px;
  transition: width 0.3s ease;
}
</style>
