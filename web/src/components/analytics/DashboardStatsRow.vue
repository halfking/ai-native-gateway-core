<template>
  <div class="dashboard-stats-row" :aria-busy="loading || undefined">
    <div class="stat-card">
      <div class="stat-content">
        <div class="stat-icon icon-accent">
          <el-icon :size="24" color="var(--accent-h)"><ChatDotRound /></el-icon>
        </div>
        <div class="stat-info">
          <div class="stat-label">{{ t('dashboard.statsRow.totalSessions') }}</div>
          <div class="stat-value">{{ formatNumber(props.stats.totalSessions) }}</div>
          <div v-if="props.stats.totalSessionsChange !== null" :class="['stat-change', changeClass(props.stats.totalSessionsChange, false)]">
            <el-icon><component :is="changeIcon(props.stats.totalSessionsChange)" /></el-icon>
            {{ Math.abs(props.stats.totalSessionsChange).toFixed(1) }}%
          </div>
        </div>
      </div>
    </div>

    <div class="stat-card">
      <div class="stat-content">
        <div class="stat-icon icon-success">
          <el-icon :size="24" color="var(--success)"><Connection /></el-icon>
        </div>
        <div class="stat-info">
          <div class="stat-label">{{ t('dashboard.statsRow.activeSessions') }}</div>
          <div class="stat-value">{{ formatNumber(props.stats.activeSessions) }}</div>
          <div class="stat-subtext">{{ t('dashboard.statsRow.activeHint') }}</div>
        </div>
      </div>
    </div>

    <div class="stat-card">
      <div class="stat-content">
        <div class="stat-icon icon-danger">
          <el-icon :size="24" color="var(--danger)"><Money /></el-icon>
        </div>
        <div class="stat-info">
          <div class="stat-label">{{ t('dashboard.statsRow.totalCost') }}</div>
          <div class="stat-value">${{ formatCost(props.stats.totalCost) }}</div>
          <div v-if="props.stats.totalCostChange !== null" :class="['stat-change', changeClass(props.stats.totalCostChange, true)]">
            <el-icon><component :is="changeIcon(props.stats.totalCostChange)" /></el-icon>
            {{ Math.abs(props.stats.totalCostChange).toFixed(1) }}%
          </div>
        </div>
      </div>
    </div>

    <div class="stat-card">
      <div class="stat-content">
        <div class="stat-icon icon-success">
          <el-icon :size="24" color="var(--success)"><CircleCheck /></el-icon>
        </div>
        <div class="stat-info">
          <div class="stat-label">{{ t('dashboard.statsRow.complianceRate') }}</div>
          <div class="stat-value">{{ props.stats.complianceRate.toFixed(1) }}%</div>
          <div v-if="props.stats.complianceRateChange !== null" :class="['stat-change', changeClass(props.stats.complianceRateChange, false)]">
            <el-icon><component :is="changeIcon(props.stats.complianceRateChange)" /></el-icon>
            {{ Math.abs(props.stats.complianceRateChange).toFixed(1) }}%
          </div>
        </div>
      </div>
    </div>

    <div class="stat-card">
      <div class="stat-content">
        <div class="stat-icon icon-warning">
          <el-icon :size="24" color="var(--warning)"><Odometer /></el-icon>
        </div>
        <div class="stat-info">
          <div class="stat-label">{{ t('dashboard.statsRow.avgHealthScore') }}</div>
          <div class="stat-value">{{ props.stats.avgHealthScore != null ? props.stats.avgHealthScore.toFixed(1) : '—' }}</div>
          <div v-if="props.stats.avgHealthScore != null && props.stats.avgHealthScoreChange !== null" :class="['stat-change', changeClass(props.stats.avgHealthScoreChange, false)]">
            <el-icon><component :is="changeIcon(props.stats.avgHealthScoreChange)" /></el-icon>
            {{ Math.abs(props.stats.avgHealthScoreChange).toFixed(1) }}%
          </div>
          <div v-else class="stat-subtext">{{ t('dashboard.statsRow.healthHint') }}</div>
        </div>
      </div>
    </div>

    <div class="stat-card">
      <div class="stat-content">
        <div class="stat-icon icon-warning">
          <el-icon :size="24" color="var(--warning)"><Timer /></el-icon>
        </div>
        <div class="stat-info">
          <div class="stat-label">{{ t('dashboard.statsRow.avgLatency') }}</div>
          <div class="stat-value">{{ formatLatency(props.stats.avgLatency) }}</div>
          <div v-if="props.stats.avgLatencyChange !== null" :class="['stat-change', changeClass(props.stats.avgLatencyChange, true)]">
            <el-icon><component :is="changeIcon(props.stats.avgLatencyChange)" /></el-icon>
            {{ Math.abs(props.stats.avgLatencyChange).toFixed(1) }}%
          </div>
        </div>
      </div>
    </div>

    <div class="stat-card">
      <div class="stat-content">
        <div class="stat-icon icon-muted">
          <el-icon :size="24" color="var(--muted)"><DocumentCopy /></el-icon>
        </div>
        <div class="stat-info">
          <div class="stat-label">{{ t('dashboard.statsRow.totalRequests') }}</div>
          <div class="stat-value">{{ formatNumber(props.stats.totalRequests) }}</div>
          <div v-if="props.stats.totalRequestsChange !== null" :class="['stat-change', changeClass(props.stats.totalRequestsChange, false)]">
            <el-icon><component :is="changeIcon(props.stats.totalRequestsChange)" /></el-icon>
            {{ Math.abs(props.stats.totalRequestsChange).toFixed(1) }}%
          </div>
        </div>
      </div>
    </div>

    <div v-if="props.showTokens" class="stat-card">
      <div class="stat-content">
        <div class="stat-icon icon-accent">
          <el-icon :size="24" color="var(--accent-h)"><Coin /></el-icon>
        </div>
        <div class="stat-info">
          <div class="stat-label">{{ t('dashboard.statsRow.totalTokens') }}</div>
          <div class="stat-value">{{ formatNumber(props.stats.totalTokens) }}</div>
          <div v-if="props.stats.totalTokensChange !== null" :class="['stat-change', changeClass(props.stats.totalTokensChange, false)]">
            <el-icon><component :is="changeIcon(props.stats.totalTokensChange)" /></el-icon>
            {{ Math.abs(props.stats.totalTokensChange).toFixed(1) }}%
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import {
  ChatDotRound, Connection, Money, CircleCheck, Odometer, Timer, DocumentCopy, Coin, ArrowUp, ArrowDown,
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

const { t } = useI18n()

const props = withDefaults(defineProps<{
  stats: DashboardStats
  loading?: boolean
  showTokens?: boolean
}>(), {
  showTokens: true,
})

const formatNumber = (value: number): string => {
  if (value >= 1000000) return (value / 1000000).toFixed(1) + 'M'
  if (value >= 1000) return (value / 1000).toFixed(1) + 'K'
  return value.toString()
}

const formatCost = (value: number): string => value.toFixed(4)

const formatLatency = (ms: number): string => {
  if (ms >= 1000) return (ms / 1000).toFixed(2) + 's'
  return ms.toFixed(0) + 'ms'
}

const changeIcon = (change: number) => (change >= 0 ? ArrowUp : ArrowDown)

const changeClass = (change: number, isNegative: boolean) => {
  if (change === 0) return 'stat-change-neutral'
  const isIncrease = change > 0
  if (isNegative) return isIncrease ? 'stat-change-bad' : 'stat-change-good'
  return isIncrease ? 'stat-change-good' : 'stat-change-bad'
}
</script>

<style scoped>
.dashboard-stats-row {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 16px;
  width: 100%;
  margin-bottom: 20px;
}
.stat-card {
  height: 100%;
  cursor: default;
  transition: transform 0.2s;
  background: var(--card, var(--on-primary));
  border: 1px solid var(--border, var(--surface-secondary));
  border-radius: 10px;
  color: var(--text, var(--kx-text));
  padding: 16px;
}
.stat-card:hover { transform: translateY(-2px); }
.stat-content { display: flex; align-items: center; gap: 12px; }
.stat-icon { width: 48px; height: 48px; border-radius: 8px; display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
.icon-accent  { background: color-mix(in srgb, var(--accent, #6366f1) 16%, transparent); }
.icon-success { background: rgba(63, 185, 80, 0.16); }
.icon-danger  { background: rgba(248, 81, 73, 0.16); }
.icon-warning { background: rgba(210, 153, 34, 0.16); }
.icon-muted   { background: var(--neutral-bg); }
.stat-info { flex: 1; min-width: 0; }
.stat-label { font-size: 13px; color: var(--muted, var(--text-secondary)); margin-bottom: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.stat-value { font-size: 24px; font-weight: 600; color: var(--text, var(--kx-text)); line-height: 1.2; margin-bottom: 2px; }
.stat-change { font-size: 12px; display: flex; align-items: center; gap: 2px; }
.stat-change-good { color: var(--success, var(--success)); }
.stat-change-bad { color: var(--danger, var(--danger)); }
.stat-change-neutral { color: var(--muted, var(--text-secondary)); }
.stat-subtext { font-size: 12px; color: var(--muted, var(--text-secondary)); }
@media (max-width: 1600px) { .stat-value { font-size: 20px; } }
@media (max-width: 768px) {
  .dashboard-stats-row { grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; }
  .stat-content { gap: 8px; }
  .stat-icon { width: 40px; height: 40px; }
  .stat-value { font-size: 18px; }
}
</style>
