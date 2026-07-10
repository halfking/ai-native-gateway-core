<script setup lang="ts">
/**
 * EnhancedDashboardView.vue - 增强版Dashboard视图
 * 集成所有新的图表组件和API
 */

import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { RefreshRight, Grid, Timer, WarningFilled } from '@element-plus/icons-vue'
import { useDashboard } from '../composables/useDashboard'
import SessionStatsPanel from '../components/SessionStatsPanel.vue'
import ModuleStatsChart from '../components/analytics/ModuleStatsChart.vue'
import ErrorStatsChart from '../components/analytics/ErrorStatsChart.vue'
import PerformanceChart from '../components/analytics/PerformanceChart.vue'

const router = useRouter()
const { t } = useI18n()

const {
  loading,
  error,
  days,
  moduleStats,
  errorStats,
  performanceStats,
  refresh,
  changeDays,
} = useDashboard({ autoRefresh: true, refreshInterval: 60000 })

const timeRangeOptions = [
  { label: t('dashboard.range.today') || 'Today', value: 1 },
  { label: t('dashboard.range.last7d') || 'Last 7 days', value: 7 },
  { label: t('dashboard.range.last30d') || 'Last 30 days', value: 30 },
  { label: t('dashboard.range.last90d') || 'Last 90 days', value: 90 },
]

function handleTimeRangeChange(value: number) {
  changeDays(value)
}

function handleRefresh() {
  refresh()
}
</script>

<template>
  <div class="enhanced-dashboard">
    <!-- 头部工具栏 -->
    <el-card class="toolbar-card" shadow="never">
      <div class="toolbar">
        <div class="toolbar-left">
          <h2>{{ t('dashboard.title') || 'Dashboard' }}</h2>
        </div>
        <div class="toolbar-right">
          <el-select 
            :model-value="days" 
            @change="handleTimeRangeChange"
            style="width: 140px; margin-right: 12px;">
            <el-option
              v-for="option in timeRangeOptions"
              :key="option.value"
              :label="option.label"
              :value="option.value"
            />
          </el-select>
          <el-button 
            type="primary" 
            :icon="RefreshRight" 
            :loading="loading"
            @click="handleRefresh">
            {{ t('dashboard.refresh') || 'Refresh' }}
          </el-button>
        </div>
      </div>
    </el-card>

    <!-- 错误提示 -->
    <el-alert 
      v-if="error" 
      type="error" 
      :title="t('dashboard.loadError') || 'Load Error'"
      :description="error.message"
      show-icon
      :closable="false"
      style="margin-bottom: 20px;"
    />

    <!-- 会话统计面板 -->
    <SessionStatsPanel style="margin-bottom: 20px;" />

    <!-- 性能和错误统计 -->
    <el-row :gutter="20" style="margin-bottom: 20px;">
      <el-col :span="12">
        <PerformanceChart :data="performanceStats" :loading="loading" />
      </el-col>
      <el-col :span="12">
        <ErrorStatsChart :data="errorStats" :loading="loading" />
      </el-col>
    </el-row>

    <!-- 模块执行统计 -->
    <el-row :gutter="20">
      <el-col :span="24">
        <ModuleStatsChart 
          v-if="moduleStats" 
          :data="moduleStats.modules" 
          :loading="loading" 
        />
        <el-card v-else shadow="hover" v-loading="loading">
          <el-empty :description="t('dashboard.noData') || 'No Data'" />
        </el-card>
      </el-col>
    </el-row>

    <!-- 模块统计摘要 -->
    <el-card v-if="moduleStats" shadow="hover" style="margin-top: 20px;">
      <template #header>
        <span>{{ t('dashboard.moduleStats.title') || 'Module Statistics Summary' }}</span>
      </template>
      <el-row :gutter="20">
        <el-col :span="6">
          <el-statistic 
            :title="t('dashboard.moduleStats.totalModules') || 'Total Modules'" 
            :value="moduleStats.summary.total_modules">
            <template #prefix>
              <el-icon color="#409EFF"><Grid /></el-icon>
            </template>
          </el-statistic>
        </el-col>
        <el-col :span="6">
          <el-statistic 
            :title="t('dashboard.moduleStats.totalExecutions') || 'Total Executions'" 
            :value="moduleStats.summary.total_executions" />
        </el-col>
        <el-col :span="6">
          <el-statistic 
            :title="t('dashboard.moduleStats.avgCacheHitRate') || 'Avg Cache Hit Rate'" 
            :value="moduleStats.summary.avg_cache_hit_rate" 
            :precision="2" 
            suffix="%" />
        </el-col>
        <el-col :span="6">
          <el-statistic 
            :title="t('dashboard.moduleStats.avgDuration') || 'Avg Duration'" 
            :value="moduleStats.summary.avg_duration_ms" 
            :precision="0" 
            suffix="ms" />
        </el-col>
      </el-row>
    </el-card>

    <!-- Top 错误列表 -->
    <el-card v-if="errorStats && errorStats.top_errors.length > 0" shadow="hover" style="margin-top: 20px;">
      <template #header>
        <span>{{ t('dashboard.errors.topErrors') || 'Top Errors' }}</span>
      </template>
      <el-table :data="errorStats.top_errors" stripe>
        <el-table-column prop="module" :label="t('common.module') || 'Module'" width="150" />
        <el-table-column prop="error_message" :label="t('common.error') || 'Error'" show-overflow-tooltip />
        <el-table-column prop="count" :label="t('common.count') || 'Count'" width="100" align="right" />
        <el-table-column prop="last_occurred" :label="t('common.lastOccurred') || 'Last Occurred'" width="180">
          <template #default="{ row }">
            {{ new Date(row.last_occurred).toLocaleString() }}
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 慢查询列表 -->
    <el-card v-if="performanceStats && performanceStats.slow_queries.length > 0" shadow="hover" style="margin-top: 20px;">
      <template #header>
        <span>{{ t('dashboard.performance.slowQueries') || 'Slow Queries' }}</span>
      </template>
      <el-table :data="performanceStats.slow_queries" stripe>
        <el-table-column prop="session_key" :label="t('common.session') || 'Session'" width="200" />
        <el-table-column prop="module_name" :label="t('common.module') || 'Module'" width="150" />
        <el-table-column prop="duration_ms" :label="t('common.duration') || 'Duration'" width="120" align="right">
          <template #default="{ row }">
            <el-tag :type="row.duration_ms > 5000 ? 'danger' : 'warning'">
              {{ row.duration_ms }} ms
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="executed_at" :label="t('common.time') || 'Time'" width="180">
          <template #default="{ row }">
            {{ new Date(row.executed_at).toLocaleString() }}
          </template>
        </el-table-column>
        <el-table-column prop="error_message" :label="t('common.error') || 'Error'" show-overflow-tooltip />
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.enhanced-dashboard {
  padding: 20px;
  max-width: 1600px;
  margin: 0 auto;
}

.toolbar-card {
  margin-bottom: 20px;
}

.toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.toolbar-left h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  color: #303133;
}

.toolbar-right {
  display: flex;
  align-items: center;
  gap: 12px;
}

:deep(.el-card__body) {
  padding: 20px;
}

:deep(.el-card__header) {
  padding: 12px 20px;
  font-weight: 500;
}

:deep(.el-table) {
  font-size: 14px;
}

:deep(.el-statistic__head) {
  font-size: 14px;
  color: #909399;
  margin-bottom: 8px;
}

:deep(.el-statistic__content) {
  font-size: 24px;
  font-weight: 600;
}
</style>
