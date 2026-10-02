<script setup lang="ts">
// AnnotationStatsView.vue — 标注统计面板（2026-10-02 整合轮收编）。
//
// AutoRoutingOpsView 的「标注统计」面板：供应商准确率 / 标注人统计 /
// 原因分布三张明细表。总标注数·准确率·标注人数等头部指标已上收宿主
// KPI 行（原五张大卡去重）；原内嵌的 taskprofile 修正区块（导出/导入/
// 应用建议 + 修正率表）与任务档案面板重复，已从本页移除——闭环操作统一
// 收口到「任务档案」页签。
import { ref, onMounted } from 'vue'
import { formatDateTime } from '../utils/datetime'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import { getAnnotationStats, type StatsResponse } from '../api/annotations'
import DataTable from '../components/ui/DataTable.vue'

const { t } = useI18n()

const stats = ref<StatsResponse | null>(null)
const loading = ref(false)
const error = ref('')

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

function fmtTime(s: string | undefined | null) {
  if (!s) return '-'
  return formatDateTime(s, { locale: localeRef.value, options: { hour12: false } })
}

onMounted(load)
</script>

<template>
  <div class="panel">
    <div class="panel-toolbar">
      <span class="panel-title">{{ t('annotation.stats.detailTitle') }}</span>
      <button class="btn btn-sm" :disabled="loading" @click="load">
        {{ loading ? t('annotation.stats.refreshing') : t('annotation.stats.refresh') }}
      </button>
    </div>

    <div v-if="error" class="alert alert-danger" role="alert">{{ error }}</div>

    <div class="stats-container">
      <!-- Provider Accuracy Table -->
      <section class="stats-section">
        <h3>{{ t('annotation.stats.providerAccuracy') }}</h3>
        <DataTable :loading="loading" :empty="!stats || !stats.by_provider || stats.by_provider.length === 0" :empty-text="t('annotation.stats.noData')" min-width="720px">
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
              <tr v-for="p in stats?.by_provider ?? []" :key="p.provider">
                <td><span class="badge badge-blue">{{ p.provider }}</span></td>
                <td class="col-number">{{ p.total_predictions }}</td>
                <td class="col-number text-success">{{ p.correct_predictions }}</td>
                <td class="col-number text-danger">{{ p.incorrect_predictions }}</td>
                <td class="col-number">
                  <span class="accuracy-badge" :style="{ color: accuracyColor(p.accuracy_percent) }">
                    {{ p.accuracy_percent.toFixed(1) }}%
                  </span>
                </td>
                <td class="col-number">{{ (p.avg_confidence * 100).toFixed(1) }}%</td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>

      <!-- Annotator Stats Table -->
      <section class="stats-section">
        <h3>{{ t('annotation.stats.annotatorStats') }}</h3>
        <DataTable :loading="loading" :empty="!stats || !stats.by_annotator || stats.by_annotator.length === 0" :empty-text="t('annotation.stats.noData')" min-width="720px">
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
              <tr v-for="a in stats?.by_annotator ?? []" :key="a.annotator">
                <td><strong>{{ a.annotator }}</strong></td>
                <td class="col-number">{{ a.total_annotations }}</td>
                <td class="col-number text-success">{{ a.correct_count }}</td>
                <td class="col-number text-danger">{{ a.incorrect_count }}</td>
                <td class="col-number">
                  <span class="accuracy-badge" :style="{ color: accuracyColor(a.accuracy_percent) }">
                    {{ a.accuracy_percent.toFixed(1) }}%
                  </span>
                </td>
                <td>{{ fmtTime(a.last_annotation_at) }}</td>
              </tr>
            </tbody>
          </table>
        </DataTable>
      </section>

      <!-- Reason Distribution Table -->
      <section class="stats-section">
        <h3>{{ t('annotation.stats.reasonDistribution') }}</h3>
        <DataTable :loading="loading" :empty="!stats || !stats.by_reason || stats.by_reason.length === 0" :empty-text="t('annotation.stats.noData')" min-width="720px">
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
              <tr v-for="r in stats?.by_reason ?? []" :key="r.reason">
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
        </DataTable>
      </section>
    </div>
  </div>
</template>

<style scoped>
/* 嵌入面板：宿主 AutoRoutingOpsView 提供外层 padding 与纵向间距 */
.panel-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 0.5rem;
  flex-wrap: wrap;
}

.panel-title {
  font-size: 0.9375rem;
  font-weight: 600;
}

.stats-container {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;
  margin-top: 0.75rem;
}

.stats-section h3 {
  margin: 0 0 0.5rem 0;
  font-size: 1rem;
  font-weight: 600;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
}

.data-table th {
  text-align: left;
  padding: 0.6rem 0.8rem;
  background: var(--bg-secondary);
  border-bottom: 1px solid var(--border);
  font-weight: 600;
  font-size: 0.8125rem;
}

.data-table td {
  padding: 0.6rem 0.8rem;
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
