<template>
  <el-card shadow="never" class="health-panel">
    <template #header>
      <div class="health-header">
        <span>健康评分</span>
        <el-tooltip v-if="health" content="健康评分基于多项指标计算，100分起扣" placement="top">
          <el-icon><QuestionFilled /></el-icon>
        </el-tooltip>
      </div>
    </template>

    <!-- 加载态 -->
    <div v-if="loading" class="loading-box">
      <el-icon class="is-loading"><Loading /></el-icon>
      <span>加载健康数据...</span>
    </div>

    <!-- 空数据态 -->
    <div v-else-if="!health" class="empty-state">
      <el-icon :size="48" color="var(--el-text-color-secondary)"><InfoFilled /></el-icon>
      <p>健康评分建设中</p>
      <span class="muted">该会话暂未计算健康评分</span>
    </div>

    <!-- 健康数据展示 -->
    <template v-else>
      <div class="health-summary">
        <!-- 评分与等级 -->
        <div class="score-section">
          <div class="score-value">
            <span class="score-number">{{ health.health_score }}</span>
            <span class="score-total">/100</span>
          </div>
          <div class="grade-badge" :class="`grade-${health.health_grade}`">
            <span class="grade-icon">{{ gradeIcon(health.health_grade) }}</span>
            <span class="grade-text">等级 {{ health.health_grade }}</span>
            <span class="grade-label">{{ gradeLabel(health.health_grade) }}</span>
          </div>
        </div>

        <!-- 结果摘要 -->
        <el-divider />
        <div class="outcome-section">
          <div class="outcome-line">
            <span class="outcome-label">结果：</span>
            <el-tag :type="outcomeType(health.outcome)" size="default">
              {{ outcomeIcon(health.outcome) }} {{ outcomeLabel(health.outcome) }}
            </el-tag>
            <span class="outcome-detail">（{{ health.outcome_reason }}）</span>
          </div>
          <div class="metrics-line">
            <span class="metric-item">错误率: <strong>{{ (health.error_rate * 100).toFixed(1) }}%</strong></span>
            <span class="metric-item">平均延迟: <strong>{{ health.avg_latency_ms }}ms</strong></span>
          </div>
        </div>

        <!-- 扣分明细 -->
        <el-divider />
        <div class="penalties-section">
          <div class="penalties-header">
            <span class="penalties-title">扣分明细</span>
            <span v-if="health.penalties.length > 0" class="penalties-count">（{{ health.penalties.length }} 项）</span>
            <span v-else class="muted">无扣分项</span>
          </div>
          
          <div v-if="health.penalties.length > 0" class="penalties-list">
            <div
              v-for="(penalty, index) in health.penalties"
              :key="index"
              class="penalty-item"
              :class="{ clickable: hasPenaltyTarget(penalty.reason) }"
              @click="jumpToPenaltySource(penalty)"
            >
              <div class="penalty-icon">
                <el-icon :size="18" :color="penaltyColor(penalty.deduction)">
                  <WarningFilled />
                </el-icon>
              </div>
              <div class="penalty-content">
                <div class="penalty-title">
                  <span class="penalty-reason">{{ penaltyReasonLabel(penalty.reason) }}</span>
                  <span class="penalty-deduction">{{ penalty.deduction }}</span>
                  <el-icon v-if="hasPenaltyTarget(penalty.reason)" class="jump-icon" :size="14">
                    <Right />
                  </el-icon>
                </div>
                <div class="penalty-detail">{{ penalty.detail }}</div>
              </div>
            </div>
          </div>
          <div v-else class="no-penalties">
            <el-icon :size="24" color="var(--el-color-success)"><SuccessFilled /></el-icon>
            <span>该会话健康状况良好，无扣分项</span>
          </div>
        </div>

        <!-- 计算时间 -->
        <div class="computed-time">
          <el-text type="info" size="small">
            <el-icon><Clock /></el-icon>
            计算时间: {{ formatTime(health.computed_at) }}
          </el-text>
        </div>
      </div>
    </template>
  </el-card>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { 
  Loading, QuestionFilled, InfoFilled, WarningFilled, 
  SuccessFilled, Right, Clock 
} from '@element-plus/icons-vue'

// Props
interface Props {
  gwSessionId: string
}
const props = defineProps<Props>()

// Emits
const emit = defineEmits<{
  jumpTo: [target: string]
}>()

// Types
interface PenaltyItem {
  reason: string
  deduction: number
  detail: string
}

interface SessionHealth {
  gw_session_id: string
  health_score: number
  health_grade: string
  outcome: string
  outcome_reason: string
  error_rate: number
  avg_latency_ms: number
  computed_at: string
  penalties: PenaltyItem[]
}

// State
const health = ref<SessionHealth | null>(null)
const loading = ref(false)

// 扣分项跳转映射
const penaltyJumpMap: Record<string, string> = {
  'high_latency': '#timeline',
  'frequent_model_switch': '#model-switches',
  'compliance_issue': '#compliance',
  'per_error': '#timeline',
  'error_ended': '#timeline',
  'abandoned': '#timeline',
  'prompt_injection': '#compliance',
  'pii_detected': '#compliance',
  'toxic_output': '#compliance',
  'sensitive': '#compliance'
}

// Methods
const loadHealth = async () => {
  loading.value = true
  try {
    const response = await fetch(`/api/admin/sessions/${props.gwSessionId}/health`, {
      credentials: 'include'
    })
    
    if (response.status === 404) {
      // 会话未找到或健康分未计算
      health.value = null
      return
    }
    
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }
    
    health.value = await response.json()
  } catch (error: any) {
    console.error('加载健康数据失败:', error)
    ElMessage.warning('健康评分数据加载失败')
    health.value = null
  } finally {
    loading.value = false
  }
}

// 等级相关
const gradeIcon = (grade: string): string => {
  const icons: Record<string, string> = {
    'A': '🟢',
    'B': '🔵',
    'C': '🟡',
    'D': '🟠',
    'F': '🔴'
  }
  return icons[grade] || '⚪'
}

const gradeLabel = (grade: string): string => {
  const labels: Record<string, string> = {
    'A': '优秀',
    'B': '良好',
    'C': '一般',
    'D': '较差',
    'F': '异常'
  }
  return labels[grade] || '未知'
}

// 结果相关
const outcomeLabel = (outcome: string): string => {
  const labels: Record<string, string> = {
    'completed': '正常完成',
    'error': '错误主导',
    'abandoned': '被放弃',
    'unknown': '未知'
  }
  return labels[outcome] || outcome
}

const outcomeIcon = (outcome: string): string => {
  const icons: Record<string, string> = {
    'completed': '✅',
    'error': '❌',
    'abandoned': '⚠️',
    'unknown': '❓'
  }
  return icons[outcome] || ''
}

const outcomeType = (outcome: string): string => {
  const types: Record<string, string> = {
    'completed': 'success',
    'error': 'danger',
    'abandoned': 'warning',
    'unknown': 'info'
  }
  return types[outcome] || 'info'
}

// 扣分项相关
const penaltyReasonLabel = (reason: string): string => {
  const labels: Record<string, string> = {
    'high_latency': '高延迟',
    'frequent_model_switch': '频繁模型切换',
    'compliance_issue': '合规问题',
    'per_error': '错误请求',
    'error_ended': '错误结束',
    'abandoned': '会话放弃',
    'prompt_injection': '提示注入',
    'pii_detected': 'PII 检测',
    'toxic_output': '毒性输出',
    'sensitive': '敏感内容'
  }
  return labels[reason] || reason
}

const penaltyColor = (deduction: number): string => {
  const abs = Math.abs(deduction)
  if (abs >= 20) return 'var(--el-color-danger)'
  if (abs >= 10) return 'var(--el-color-warning)'
  return 'var(--el-color-info)'
}

const hasPenaltyTarget = (reason: string): boolean => {
  return reason in penaltyJumpMap
}

const jumpToPenaltySource = (penalty: PenaltyItem) => {
  const target = penaltyJumpMap[penalty.reason]
  if (target) {
    emit('jumpTo', target)
    ElMessage.info(`跳转到 ${penaltyReasonLabel(penalty.reason)} 相关面板`)
  }
}

// 时间格式化
const formatTime = (timeStr: string): string => {
  if (!timeStr) return '—'
  const date = new Date(timeStr)
  return date.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
}

// Lifecycle
onMounted(() => {
  loadHealth()
})

// 暴露方法供父组件调用
defineExpose({
  reload: loadHealth
})
</script>

<style scoped>
.health-panel {
  margin-top: 16px;
}

.health-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
}

.loading-box {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 40px 20px;
  color: var(--el-text-color-secondary);
  gap: 8px;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 40px 20px;
  text-align: center;
}

.empty-state p {
  margin: 12px 0 4px;
  font-size: 16px;
  color: var(--el-text-color-primary);
}

.empty-state .muted {
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

.health-summary {
  padding: 8px;
}

/* 评分区域 */
.score-section {
  display: flex;
  justify-content: space-around;
  align-items: center;
  padding: 16px 0;
}

.score-value {
  text-align: center;
}

.score-number {
  font-size: 48px;
  font-weight: 700;
  color: var(--el-color-primary);
  line-height: 1;
}

.score-total {
  font-size: 24px;
  color: var(--el-text-color-secondary);
  margin-left: 4px;
}

.grade-badge {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 16px 24px;
  border-radius: 8px;
  gap: 4px;
}

.grade-A { background: var(--el-color-success-light-9); }
.grade-B { background: var(--el-color-primary-light-9); }
.grade-C { background: var(--el-color-warning-light-9); }
.grade-D { background: #fef0e6; }
.grade-F { background: var(--el-color-danger-light-9); }

.grade-icon {
  font-size: 32px;
  line-height: 1;
}

.grade-text {
  font-size: 18px;
  font-weight: 600;
  margin-top: 4px;
}

.grade-label {
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

/* 结果区域 */
.outcome-section {
  padding: 12px 0;
}

.outcome-line {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.outcome-label {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.outcome-detail {
  color: var(--el-text-color-secondary);
  font-size: 14px;
}

.metrics-line {
  display: flex;
  gap: 24px;
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

.metric-item strong {
  color: var(--el-text-color-primary);
  font-weight: 600;
}

/* 扣分明细区域 */
.penalties-section {
  padding: 12px 0;
}

.penalties-header {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 12px;
}

.penalties-title {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.penalties-count {
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

.penalties-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.penalty-item {
  display: flex;
  gap: 12px;
  padding: 12px;
  background: var(--el-fill-color-light);
  border-radius: 6px;
  border-left: 3px solid var(--el-color-warning);
  transition: all 0.2s;
}

.penalty-item.clickable {
  cursor: pointer;
}

.penalty-item.clickable:hover {
  background: var(--el-fill-color);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.08);
  transform: translateX(2px);
}

.penalty-icon {
  flex-shrink: 0;
  padding-top: 2px;
}

.penalty-content {
  flex: 1;
  min-width: 0;
}

.penalty-title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.penalty-reason {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

.penalty-deduction {
  font-size: 16px;
  font-weight: 700;
  color: var(--el-color-warning);
}

.jump-icon {
  margin-left: auto;
  color: var(--el-color-primary);
}

.penalty-detail {
  font-size: 13px;
  color: var(--el-text-color-secondary);
  word-break: break-all;
}

.no-penalties {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 24px;
  text-align: center;
  color: var(--el-text-color-secondary);
}

/* 计算时间 */
.computed-time {
  padding-top: 12px;
  text-align: right;
}

.muted {
  color: var(--el-text-color-secondary);
}
</style>
