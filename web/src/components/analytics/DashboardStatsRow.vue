<template>
  <div class="dashboard-stats-row">
    <el-row :gutter="16">
      <!-- 会话总数 -->
      <el-col :xs="24" :sm="12" :md="8" :lg="6" :xl="3">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-content">
            <div class="stat-icon" style="background: #ecf5ff">
              <el-icon :size="24" color="#409eff">
                <ChatDotRound />
              </el-icon>
            </div>
            <div class="stat-info">
              <div class="stat-label">会话总数</div>
              <div class="stat-value">{{ formatNumber(stats.totalSessions) }}</div>
              <div v-if="stats.totalSessionsChange !== null" :class="['stat-change', changeClass(stats.totalSessionsChange, false)]">
                <el-icon><component :is="changeIcon(stats.totalSessionsChange)" /></el-icon>
                {{ Math.abs(stats.totalSessionsChange).toFixed(1) }}%
              </div>
            </div>
          </div>
        </el-card>
      </el-col>

      <!-- 活跃会话 -->
      <el-col :xs="24" :sm="12" :md="8" :lg="6" :xl="3">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-content">
            <div class="stat-icon" style="background: #f0f9ff">
              <el-icon :size="24" color="#67c23a">
                <Connection />
              </el-icon>
            </div>
            <div class="stat-info">
              <div class="stat-label">活跃会话</div>
              <div class="stat-value">{{ formatNumber(stats.activeSessions) }}</div>
              <div class="stat-subtext">实时在线</div>
            </div>
          </div>
        </el-card>
      </el-col>

      <!-- 总成本 -->
      <el-col :xs="24" :sm="12" :md="8" :lg="6" :xl="3">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-content">
            <div class="stat-icon" style="background: #fef0f0">
              <el-icon :size="24" color="#f56c6c">
                <Money />
              </el-icon>
            </div>
            <div class="stat-info">
              <div class="stat-label">总成本</div>
              <div class="stat-value">${{ formatCost(stats.totalCost) }}</div>
              <div v-if="stats.totalCostChange !== null" :class="['stat-change', changeClass(stats.totalCostChange, true)]">
                <el-icon><component :is="changeIcon(stats.totalCostChange)" /></el-icon>
                {{ Math.abs(stats.totalCostChange).toFixed(1) }}%
              </div>
            </div>
          </div>
        </el-card>
      </el-col>

      <!-- 合规率 -->
      <el-col :xs="24" :sm="12" :md="8" :lg="6" :xl="3">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-content">
            <div class="stat-icon" style="background: #f0f9ff">
              <el-icon :size="24" color="#67c23a">
                <CircleCheck />
              </el-icon>
            </div>
            <div class="stat-info">
              <div class="stat-label">合规率</div>
              <div class="stat-value">{{ stats.complianceRate.toFixed(1) }}%</div>
              <div v-if="stats.complianceRateChange !== null" :class="['stat-change', changeClass(stats.complianceRateChange, false)]">
                <el-icon><component :is="changeIcon(stats.complianceRateChange)" /></el-icon>
                {{ Math.abs(stats.complianceRateChange).toFixed(1) }}%
              </div>
            </div>
          </div>
        </el-card>
      </el-col>

      <!-- 平均健康分 -->
      <el-col :xs="24" :sm="12" :md="8" :lg="6" :xl="3">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-content">
            <div class="stat-icon" style="background: #f4f4f5">
              <el-icon :size="24" color="#e6a23c">
                <Odometer />
              </el-icon>
            </div>
            <div class="stat-info">
              <div class="stat-label">平均健康分</div>
              <div class="stat-value">{{ stats.avgHealthScore ? stats.avgHealthScore.toFixed(1) : '-' }}</div>
              <div v-if="stats.avgHealthScore && stats.avgHealthScoreChange !== null" :class="['stat-change', changeClass(stats.avgHealthScoreChange, false)]">
                <el-icon><component :is="changeIcon(stats.avgHealthScoreChange)" /></el-icon>
                {{ Math.abs(stats.avgHealthScoreChange).toFixed(1) }}%
              </div>
              <div v-else class="stat-subtext">建设中</div>
            </div>
          </div>
        </el-card>
      </el-col>

      <!-- 平均延迟 -->
      <el-col :xs="24" :sm="12" :md="8" :lg="6" :xl="3">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-content">
            <div class="stat-icon" style="background: #fdf6ec">
              <el-icon :size="24" color="#e6a23c">
                <Timer />
              </el-icon>
            </div>
            <div class="stat-info">
              <div class="stat-label">平均延迟</div>
              <div class="stat-value">{{ formatLatency(stats.avgLatency) }}</div>
              <div v-if="stats.avgLatencyChange !== null" :class="['stat-change', changeClass(stats.avgLatencyChange, true)]">
                <el-icon><component :is="changeIcon(stats.avgLatencyChange)" /></el-icon>
                {{ Math.abs(stats.avgLatencyChange).toFixed(1) }}%
              </div>
            </div>
          </div>
        </el-card>
      </el-col>

      <!-- 总请求数 -->
      <el-col :xs="24" :sm="12" :md="8" :lg="6" :xl="3">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-content">
            <div class="stat-icon" style="background: #f4f4f5">
              <el-icon :size="24" color="#909399">
                <DocumentCopy />
              </el-icon>
            </div>
            <div class="stat-info">
              <div class="stat-label">总请求数</div>
              <div class="stat-value">{{ formatNumber(stats.totalRequests) }}</div>
              <div v-if="stats.totalRequestsChange !== null" :class="['stat-change', changeClass(stats.totalRequestsChange, false)]">
                <el-icon><component :is="changeIcon(stats.totalRequestsChange)" /></el-icon>
                {{ Math.abs(stats.totalRequestsChange).toFixed(1) }}%
              </div>
            </div>
          </div>
        </el-card>
      </el-col>

      <!-- 总 Token 数 -->
      <el-col :xs="24" :sm="12" :md="8" :lg="6" :xl="3">
        <el-card shadow="hover" class="stat-card">
          <div class="stat-content">
            <div class="stat-icon" style="background: #f0f9ff">
              <el-icon :size="24" color="#409eff">
                <Coin />
              </el-icon>
            </div>
            <div class="stat-info">
              <div class="stat-label">总 Token</div>
              <div class="stat-value">{{ formatNumber(stats.totalTokens) }}</div>
              <div v-if="stats.totalTokensChange !== null" :class="['stat-change', changeClass(stats.totalTokensChange, false)]">
                <el-icon><component :is="changeIcon(stats.totalTokensChange)" /></el-icon>
                {{ Math.abs(stats.totalTokensChange).toFixed(1) }}%
              </div>
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import {
  ChatDotRound,
  Connection,
  Money,
  CircleCheck,
  Odometer,
  Timer,
  DocumentCopy,
  Coin,
  ArrowUp,
  ArrowDown
} from '@element-plus/icons-vue'

export interface DashboardStats {
  totalSessions: number
  totalSessionsChange: number | null
  activeSessions: number
  totalCost: number
  totalCostChange: number | null
  complianceRate: number
  complianceRateChange: number | null
  avgHealthScore: number | null
  avgHealthScoreChange: number | null
  avgLatency: number
  avgLatencyChange: number | null
  totalRequests: number
  totalRequestsChange: number | null
  totalTokens: number
  totalTokensChange: number | null
}

const props = defineProps<{
  stats: DashboardStats
  loading?: boolean
}>()

// 格式化数字
const formatNumber = (value: number): string => {
  if (value >= 1000000) {
    return (value / 1000000).toFixed(1) + 'M'
  } else if (value >= 1000) {
    return (value / 1000).toFixed(1) + 'K'
  }
  return value.toString()
}

// 格式化成本
const formatCost = (value: number): string => {
  return value.toFixed(4)
}

// 格式化延迟
const formatLatency = (ms: number): string => {
  if (ms >= 1000) {
    return (ms / 1000).toFixed(2) + 's'
  }
  return ms.toFixed(0) + 'ms'
}

// 变化图标
const changeIcon = (change: number) => {
  return change >= 0 ? ArrowUp : ArrowDown
}

// 变化样式类（isNegative=true 表示增长是坏事，如成本、延迟）
const changeClass = (change: number, isNegative: boolean) => {
  if (change === 0) return 'stat-change-neutral'
  
  const isIncrease = change > 0
  
  if (isNegative) {
    // 对于成本、延迟，增长是坏事
    return isIncrease ? 'stat-change-bad' : 'stat-change-good'
  } else {
    // 对于其他指标，增长是好事
    return isIncrease ? 'stat-change-good' : 'stat-change-bad'
  }
}
</script>

<style scoped>
.dashboard-stats-row {
  margin-bottom: 20px;
}

.stat-card {
  height: 100%;
  cursor: default;
  transition: all 0.3s;
}

.stat-card:hover {
  transform: translateY(-4px);
}

.stat-content {
  display: flex;
  align-items: center;
  gap: 12px;
}

.stat-icon {
  width: 48px;
  height: 48px;
  border-radius: 8px;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.stat-info {
  flex: 1;
  min-width: 0;
}

.stat-label {
  font-size: 13px;
  color: #909399;
  margin-bottom: 4px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.stat-value {
  font-size: 24px;
  font-weight: 600;
  color: #303133;
  line-height: 1.2;
  margin-bottom: 2px;
}

.stat-change {
  font-size: 12px;
  display: flex;
  align-items: center;
  gap: 2px;
}

.stat-change-good {
  color: #67c23a;
}

.stat-change-bad {
  color: #f56c6c;
}

.stat-change-neutral {
  color: #909399;
}

.stat-subtext {
  font-size: 12px;
  color: #c0c4cc;
}

:deep(.el-card__body) {
  padding: 16px;
}

/* 响应式调整 */
@media (max-width: 1600px) {
  .stat-value {
    font-size: 20px;
  }
}

@media (max-width: 768px) {
  .stat-content {
    gap: 8px;
  }

  .stat-icon {
    width: 40px;
    height: 40px;
  }

  .stat-value {
    font-size: 18px;
  }
}
</style>
