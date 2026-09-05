<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  getProviderQualityDetail,
} from '../../api/quality'
import type { ModelQualityProfile, ProviderQualityData, QualityGrade } from '../../types/quality-api'
import {
  formatQualityScore,
  formatCalculatedAt,
  QUALITY_GRADE_COLORS,
  QUALITY_GRADE_LABELS,
} from '../../types/quality-api'

const props = defineProps<{
  providerId: number
}>()
const { t } = useI18n()
const qp = (key: string): string => t(`providerDetailPage.quality.${key}`)

const loading = ref(false)
const error = ref('')
const data = ref<ProviderQualityData | null>(null)

async function loadData() {
  loading.value = true
  error.value = ''
  try {
    data.value = await getProviderQualityDetail(props.providerId)
  } catch (e: unknown) {
    data.value = null
    error.value = e instanceof Error ? e.message : '加载质量画像失败'
  } finally {
    loading.value = false
  }
}

onMounted(loadData)
watch(() => props.providerId, () => {
  if (!Number.isNaN(props.providerId)) loadData()
})

const bestModel = computed<ModelQualityProfile | null>(() => {
  const models = data.value?.models
  if (!models?.length) return null
  return [...models].sort((a, b) => b.quality_score - a.quality_score)[0] ?? null
})

const requestStats = computed(() => data.value?.request_stats ?? null)

const fmtNum = (n: number | undefined | null): string =>
  new Intl.NumberFormat('zh-CN').format(n ?? 0)

const radarData = computed(() => {
  const scores = bestModel.value?.scores
  if (!scores) return []
  return [
    { name: qp('availability'), value: scores.availability },
    { name: qp('performance'), value: scores.performance },
    { name: qp('stability'), value: scores.stability },
    { name: qp('costEfficiency'), value: scores.cost_efficiency },
  ]
})

function gradeStyle(grade: string | undefined): Record<string, string> {
  if (!grade) return {}
  const color = QUALITY_GRADE_COLORS[grade as QualityGrade]
  return color ? { backgroundColor: color, color: '#fff' } : {}
}

function gradeLabel(grade: string | undefined): string {
  if (!grade) return '—'
  return QUALITY_GRADE_LABELS[grade as QualityGrade] || grade
}

function scoreColor(n: number): string {
  if (n >= 90) return 'var(--success)'
  if (n >= 70) return 'var(--accent)'
  if (n >= 60) return 'var(--warning)'
  return 'var(--danger)'
}

function barWidth(n: number): string {
  return `${Math.max(0, Math.min(100, n))}%`
}

function modelLabel(name: string | null | undefined): string {
  if (!name) return qp('providerAggregate')
  return name
}
</script>

<template>
  <div class="quality-tab">
    <div v-if="loading" class="empty">{{ qp('loading') }}</div>
    <div v-else-if="error" class="alert alert-danger">{{ error }}</div>
    <div v-else-if="!data || !data.models?.length" class="empty">
      {{ qp('noData') }}
    </div>
    <template v-else>
      <div v-if="requestStats" class="card stats-card">
        <h3 class="section-title">{{ qp('requestStats') }}</h3>
        <div class="request-stats-row">
          <div class="stat-cell">
            <div class="stat-label">{{ qp('totalRequests') }}</div>
            <div class="stat-value">{{ fmtNum(requestStats.total_requests) }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-label">{{ qp('monthRequests') }}</div>
            <div class="stat-value">{{ fmtNum(requestStats.month_requests) }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-label">{{ qp('weekRequests') }}</div>
            <div class="stat-value">{{ fmtNum(requestStats.week_requests) }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-label">{{ qp('dayRequests') }}</div>
            <div class="stat-value">{{ fmtNum(requestStats.day_requests) }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-label">{{ qp('successCount') }}</div>
            <div class="stat-value success">{{ fmtNum(requestStats.success_count) }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-label">{{ qp('failureCount') }}</div>
            <div class="stat-value failure">{{ fmtNum(requestStats.failure_count) }}</div>
          </div>
          <div class="stat-cell">
            <div class="stat-label">{{ qp('totalTokens') }}</div>
            <div class="stat-value">{{ fmtNum(requestStats.total_tokens) }}</div>
          </div>
        </div>
      </div>

      <div class="overview-row">
        <div class="stat-card">
          <div class="stat-label">{{ qp('overallScore') }}</div>
          <div class="stat-value" :style="{ color: scoreColor(bestModel?.quality_score ?? 0) }">
            {{ bestModel ? formatQualityScore(bestModel.quality_score) : '—' }}
            <span
              v-if="bestModel"
              class="grade-badge"
              :style="gradeStyle(bestModel.quality_grade)"
            >{{ bestModel.quality_grade }}</span>
          </div>
          <div class="stat-desc">{{ gradeLabel(bestModel?.quality_grade) }} · {{ modelLabel(bestModel?.model_name) }}</div>
        </div>
        <div class="stat-card" v-for="item in radarData" :key="item.name">
          <div class="stat-label">{{ item.name }}</div>
          <div class="stat-value" :style="{ color: scoreColor(item.value) }">
            {{ formatQualityScore(item.value) }}
          </div>
          <div class="bar-track">
            <div class="bar-fill" :style="{ width: barWidth(item.value), background: scoreColor(item.value) }" />
          </div>
        </div>
      </div>

      <div class="card models-card">
        <h3 class="section-title">{{ qp('modelDetails') }}</h3>
        <table class="quality-table">
          <thead>
            <tr>
              <th>{{ qp('model') }}</th>
              <th>{{ qp('overallQuality') }}</th>
              <th>{{ qp('grade') }}</th>
              <th>{{ qp('availability') }}</th>
              <th>{{ qp('performance') }}</th>
              <th>{{ qp('stability') }}</th>
              <th>{{ qp('costEfficiency') }}</th>
              <th>{{ qp('updatedAt') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in data.models" :key="m.model_name || '__provider__'">
              <td>{{ modelLabel(m.model_name) }}</td>
              <td class="num">{{ formatQualityScore(m.quality_score) }}</td>
              <td>
                <span class="grade-badge" :style="gradeStyle(m.quality_grade)">{{ m.quality_grade }}</span>
                <span class="muted"> {{ gradeLabel(m.quality_grade) }}</span>
              </td>
              <td class="num">{{ formatQualityScore(m.scores.availability) }}</td>
              <td class="num">{{ formatQualityScore(m.scores.performance) }}</td>
              <td class="num">{{ formatQualityScore(m.scores.stability) }}</td>
              <td class="num">{{ formatQualityScore(m.scores.cost_efficiency) }}</td>
              <td class="muted">{{ formatCalculatedAt(m.calculated_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<style scoped>
.quality-tab { margin-top: 8px; }
.overview-row {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.stat-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 14px 16px;
}
.stat-label { font-size: 12px; color: var(--muted); margin-bottom: 6px; }
.stat-value { font-size: 22px; font-weight: 700; display: flex; align-items: center; gap: 8px; }
.stat-desc { font-size: 12px; color: var(--muted); margin-top: 6px; }
.grade-badge {
  display: inline-block;
  font-size: 11px;
  font-weight: 700;
  padding: 2px 7px;
  border-radius: 4px;
  line-height: 1.4;
}
.bar-track {
  margin-top: 8px;
  height: 6px;
  background: rgba(128,128,128,0.2);
  border-radius: 3px;
  overflow: hidden;
}
.bar-fill { height: 100%; border-radius: 3px; }
.stats-card { padding: 16px; margin-bottom: 16px; }
.request-stats-row {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 12px;
}
.stat-cell {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px 14px;
}
.stat-cell .stat-value { font-size: 20px; }
.stat-value.success { color: var(--success); }
.stat-value.failure { color: var(--danger); }
.models-card { padding: 16px; }
.section-title { margin: 0 0 12px; font-size: 14px; }
.quality-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.quality-table th,
.quality-table td {
  text-align: left;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
}
.quality-table th { color: var(--muted); font-weight: 600; font-size: 12px; }
.quality-table .num { font-variant-numeric: tabular-nums; font-weight: 600; }
.muted { color: var(--muted); font-size: 12px; }
.empty { padding: 32px; text-align: center; color: var(--muted); }
</style>
