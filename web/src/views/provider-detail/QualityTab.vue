<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  getProviderQualityProfile,
  getProviderQualityTrend,
  getProviderErrors,
  getProviderHealthEvents,
  acknowledgeHealthEvent,
  type QualityProfile,
  type QualityTrend,
  type ErrorDetail,
  type HealthEvent,
} from '../../api/provider-quality'

const props = defineProps<{
  providerId: number
}>()

const { t } = useI18n()
const loading = ref(false)
const profile = ref<QualityProfile | null>(null)
const trend = ref<QualityTrend[]>([])
const errors = ref<ErrorDetail[]>([])
const events = ref<HealthEvent[]>([])
const timeRange = ref<24 | 168 | 720>(24)

async function loadData() {
  loading.value = true
  try {
    const [profileData, trendData, errorsData, eventsData] = await Promise.all([
      getProviderQualityProfile(props.providerId),
      getProviderQualityTrend(props.providerId, timeRange.value),
      getProviderErrors(props.providerId),
      getProviderHealthEvents(props.providerId),
    ])
    
    profile.value = profileData
    trend.value = trendData
    errors.value = errorsData
    events.value = eventsData
  } catch (e: unknown) {
    const err = e as Error
    ElMessage.error(err.message || '加载质量画像失败')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadData()
})

const gradeColor = computed(() => {
  if (!profile.value) return ''
  const grade = profile.value.quality_grade
  const colors: Record<string, string> = {
    'S': 'color: #67C23A; font-weight: bold;',
    'A': 'color: #409EFF; font-weight: bold;',
    'B': 'color: #E6A23C;',
    'C': 'color: #F56C6C;',
    'D': 'color: #909399;',
  }
  return colors[grade] || ''
})

const trustGradeColor = computed(() => {
  if (!profile.value) return ''
  const score = profile.value.trustworthiness_score
  if (score >= 90) return 'color: #67C23A;'
  if (score >= 80) return 'color: #409EFF;'
  if (score >= 70) return 'color: #E6A23C;'
  if (score >= 60) return 'color: #F56C6C;'
  return 'color: #F56C6C; font-weight: bold;'
})

const healthStatus = computed(() => {
  if (!profile.value) return { text: '未知', type: 'info' }
  
  if (profile.value.consecutive_failures >= 5) {
    return { text: '熔断', type: 'danger' }
  }
  if (profile.value.quality_score_overall >= 90) {
    return { text: '健康', type: 'success' }
  }
  if (profile.value.quality_score_overall >= 70) {
    return { text: '轻度降级', type: 'warning' }
  }
  return { text: '严重降级', type: 'danger' }
})

const unresolvedEventsCount = computed(() => {
  return events.value.filter(e => !e.resolved_at).length
})

const radarData = computed(() => {
  if (!profile.value) return []
  return [
    { name: '可用性', value: profile.value.availability_score },
    { name: '性能', value: profile.value.performance_score },
    { name: '可信度', value: profile.value.trustworthiness_score },
    { name: '稳定性', value: profile.value.stability_score },
    { name: '成本', value: profile.value.cost_efficiency_score },
  ]
})

async function handleAcknowledge(eventId: number) {
  try {
    await acknowledgeHealthEvent(props.providerId, eventId)
    ElMessage.success('已确认告警')
    await loadData()
  } catch (e: unknown) {
    const err = e as Error
    ElMessage.error(err.message || '确认失败')
  }
}

function formatTime(time: string | null) {
  if (!time) return '-'
  return new Date(time).toLocaleString('zh-CN')
}

function formatDuration(seconds: number | null) {
  if (!seconds) return '-'
  if (seconds < 60) return `${seconds}秒`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}分钟`
  return `${Math.floor(seconds / 3600)}小时`
}
</script>

<template>
  <div v-loading="loading" class="quality-tab">
    <el-row :gutter="16" class="overview-row">
      <el-col :span="6">
        <el-card shadow="hover">
          <div class="stat-card">
            <div class="stat-label">综合评分</div>
            <div class="stat-value" :style="gradeColor">
              {{ profile?.quality_score_overall?.toFixed(1) || '-' }}
              <span class="stat-grade">{{ profile?.quality_grade || '-' }}</span>
            </div>
            <div class="stat-desc">
              <el-tag :type="healthStatus.type" size="small">
                {{ healthStatus.text }}
              </el-tag>
            </div>
          </div>
        </el-card>
      </el-col>
      
      <el-col :span="6">
        <el-card shadow="hover">
          <div class="stat-card">
            <div class="stat-label">可信度</div>
            <div class="stat-value" :style="trustGradeColor">
              {{ profile?.trustworthiness_score?.toFixed(1) || '-' }}
            </div>
            <div class="stat-desc">
              <span v-if="profile?.anomaly_count_24h" style="color: #F56C6C;">
                24h 异常 {{ profile.anomaly_count_24h }} 次
              </span>
              <span v-else style="color: #67C23A;">正常</span>
            </div>
          </div>
        </el-card>
      </el-col>
      
      <el-col :span="6">
        <el-card shadow="hover">
          <div class="stat-card">
            <div class="stat-label">成功率 (24h)</div>
            <div class="stat-value" :style="{ color: (profile?.success_rate_24h || 0) >= 99 ? '#67C23A' : '#F56C6C' }">
              {{ profile?.success_rate_24h?.toFixed(2) || '-' }}%
            </div>
            <div class="stat-desc">
              5xx: {{ profile?.error_rate_5xx_24h?.toFixed(2) || 0 }}%
            </div>
          </div>
        </el-card>
      </el-col>
      
      <el-col :span="6">
        <el-card shadow="hover">
          <div class="stat-card">
            <div class="stat-label">P95 延迟 (1h)</div>
            <div class="stat-value">
              {{ profile?.latency_p95_1h || '-' }}<span class="stat-unit">ms</span>
            </div>
            <div class="stat-desc">
              P99: {{ profile?.latency_p99_24h || '-' }}ms
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="16" style="margin-top: 16px;">
      <el-col :span="10">
        <el-card shadow="hover">
          <template #header>
            <div class="card-header">
              <span>五维质量雷达</span>
            </div>
          </template>
          <div class="radar-chart" style="height: 300px;">
            <div class="radar-item" v-for="item in radarData" :key="item.name">
              <div class="radar-label">{{ item.name }}</div>
              <el-progress 
                :percentage="item.value" 
                :stroke-width="12"
                :color="item.value >= 90 ? '#67C23A' : item.value >= 70 ? '#409EFF' : '#F56C6C'"
              />
            </div>
          </div>
        </el-card>
      </el-col>
      
      <el-col :span="14">
        <el-card shadow="hover">
          <template #header>
            <div class="card-header">
              <span>质量趋势</span>
              <el-radio-group v-model="timeRange" size="small" @change="loadData">
                <el-radio-button :value="24">24小时</el-radio-button>
                <el-radio-button :value="168">7天</el-radio-button>
                <el-radio-button :value="720">30天</el-radio-button>
              </el-radio-group>
            </div>
          </template>
          <div style="height: 300px; display: flex; align-items: center; justify-content: center; color: #909399;">
            <div v-if="trend.length === 0">暂无趋势数据</div>
            <div v-else style="width: 100%;">
              <div v-for="(item, idx) in trend.slice(0, 10)" :key="idx" style="margin-bottom: 8px;">
                <div style="font-size: 12px; color: #909399;">
                  {{ new Date(item.timestamp).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: 'numeric' }) }}
                </div>
                <el-progress 
                  :percentage="item.quality_score" 
                  :stroke-width="8"
                  :show-text="false"
                  :color="item.quality_score >= 90 ? '#67C23A' : item.quality_score >= 70 ? '#409EFF' : '#F56C6C'"
                />
              </div>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-card shadow="hover" style="margin-top: 16px;">
      <template #header>
        <div class="card-header">
          <span>详细指标</span>
        </div>
      </template>
      
      <el-tabs type="border-card">
        <el-tab-pane label="可用性">
          <el-descriptions :column="3" border>
            <el-descriptions-item label="成功率 (5m)">
              {{ profile?.success_rate_5m?.toFixed(2) || '-' }}%
            </el-descriptions-item>
            <el-descriptions-item label="成功率 (1h)">
              {{ profile?.success_rate_1h?.toFixed(2) || '-' }}%
            </el-descriptions-item>
            <el-descriptions-item label="成功率 (24h)">
              {{ profile?.success_rate_24h?.toFixed(2) || '-' }}%
            </el-descriptions-item>
          </el-descriptions>
        </el-tab-pane>

        <el-tab-pane label="性能">
          <el-descriptions :column="3" border>
            <el-descriptions-item label="P50 延迟 (5m)">{{ profile?.latency_p50_5m || '-' }}ms</el-descriptions-item>
            <el-descriptions-item label="P95 延迟 (5m)">{{ profile?.latency_p95_5m || '-' }}ms</el-descriptions-item>
            <el-descriptions-item label="P99 延迟 (5m)">{{ profile?.latency_p99_5m || '-' }}ms</el-descriptions-item>
          </el-descriptions>
        </el-tab-pane>

        <el-tab-pane label="可信度（反欺诈）">
          <el-descriptions :column="2" border>
            <el-descriptions-item label="可信度评分">
              <span :style="trustGradeColor">
                {{ profile?.trustworthiness_score?.toFixed(1) || '-' }} 
                ({{ profile?.trustworthiness_grade || '-' }})
              </span>
            </el-descriptions-item>
          </el-descriptions>
        </el-tab-pane>
      </el-tabs>
    </el-card>
  </div>
</template>

<style scoped>
.quality-tab {
  padding: 16px;
}

.stat-card {
  text-align: center;
  padding: 8px 0;
}

.stat-label {
  font-size: 14px;
  color: #909399;
  margin-bottom: 8px;
}

.stat-value {
  font-size: 32px;
  font-weight: bold;
  line-height: 1.2;
  margin-bottom: 8px;
}

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
</style>
