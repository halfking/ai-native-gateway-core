<template>
  <div class="top-sessions-table">
    <el-card shadow="never">
      <template #header>
        <div class="table-header">
          <span class="table-title">热门会话</span>
          <el-radio-group v-model="metric" size="small">
            <el-radio-button label="cost">成本最高</el-radio-button>
            <el-radio-button label="tokens">Token 最多</el-radio-button>
            <el-radio-button label="latency">延迟最高</el-radio-button>
            <el-radio-button label="duration">时长最长</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <el-table
        v-loading="loading"
        :data="displayData"
        style="width: 100%"
        :default-sort="{ prop: sortField, order: 'descending' }"
        @row-click="handleRowClick"
      >
        <el-table-column prop="title" label="标题" min-width="200" show-overflow-tooltip>
          <template #default="scope">
            <div class="session-title">
              {{ scope?.row?.title || '(未命名)' }}
            </div>
          </template>
        </el-table-column>

        <el-table-column
          v-if="showTenant"
          prop="tenantId"
          label="租户"
          width="120"
          show-overflow-tooltip
        />

        <el-table-column prop="requestCount" label="请求数" width="90" align="right">
          <template #default="scope">
            {{ scope?.row?.requestCount }}
          </template>
        </el-table-column>

        <el-table-column prop="totalCost" label="成本" width="110" align="right" sortable>
          <template #default="scope">
            <span :class="{ 'highlight-value': metric === 'cost' }">
              ${{ (scope?.row?.totalCost ?? 0).toFixed(4) }}
            </span>
          </template>
        </el-table-column>

        <el-table-column prop="totalTokens" label="Token" width="100" align="right" sortable>
          <template #default="scope">
            <span :class="{ 'highlight-value': metric === 'tokens' }">
              {{ formatNumber(scope?.row?.totalTokens) }}
            </span>
          </template>
        </el-table-column>

        <el-table-column prop="durationSeconds" label="时长" width="100" align="right" sortable>
          <template #default="scope">
            <span :class="{ 'highlight-value': metric === 'duration' }">
              {{ formatDuration(scope?.row?.durationSeconds) }}
            </span>
          </template>
        </el-table-column>

        <el-table-column prop="avgLatency" label="平均延迟" width="110" align="right" sortable>
          <template #default="scope">
            <span :class="{ 'highlight-value': metric === 'latency' }">
              {{ formatLatency(scope?.row?.avgLatency) }}
            </span>
          </template>
        </el-table-column>

        <el-table-column prop="healthGrade" label="健康" width="80" align="center">
          <template #default="scope">
            <el-tag
              v-if="scope?.row?.healthGrade"
              :type="getHealthTagType(scope?.row?.healthGrade)"
              size="small"
            >
              {{ scope?.row?.healthGrade }}
            </el-tag>
            <span v-else class="text-muted">-</span>
          </template>
        </el-table-column>

        <el-table-column prop="primaryModel" label="主模型" width="140" show-overflow-tooltip />

        <el-table-column label="操作" width="100" align="center" fixed="right">
          <template #default="scope">
            <el-button
              type="primary"
              link
              size="small"
              @click.stop="handleViewPanorama(scope?.row?.gwSessionId)"
            >
              全景图
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div v-if="!loading && displayData.length === 0" class="table-empty">
        <el-empty description="所选范围内无会话" />
      </div>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ref } from 'vue'
import { useRouter } from 'vue-router'

export interface TopSession {
  gwSessionId: string
  title: string
  tenantId?: string
  requestCount: number
  totalCost: number
  totalTokens: number
  durationSeconds: number
  avgLatency: number
  healthGrade: string | null
  primaryModel: string
}

const props = defineProps<{
  data: TopSession[]
  loading?: boolean
  showTenant?: boolean
}>()

const router = useRouter()
const metric = ref<'cost' | 'tokens' | 'latency' | 'duration'>('cost')

// 根据指标排序
const sortField = computed(() => {
  switch (metric.value) {
    case 'tokens':
      return 'totalTokens'
    case 'latency':
      return 'avgLatency'
    case 'duration':
      return 'durationSeconds'
    default:
      return 'totalCost'
  }
})

// 展示数据（排序后的前10条）
const displayData = computed(() => {
  const sorted = [...props.data].sort((a, b) => {
    switch (metric.value) {
      case 'tokens':
        return b.totalTokens - a.totalTokens
      case 'latency':
        return b.avgLatency - a.avgLatency
      case 'duration':
        return b.durationSeconds - a.durationSeconds
      default:
        return b.totalCost - a.totalCost
    }
  })
  return sorted.slice(0, 10)
})

// 格式化数字
const formatNumber = (value: number): string => {
  if (value >= 1000000) {
    return (value / 1000000).toFixed(1) + 'M'
  } else if (value >= 1000) {
    return (value / 1000).toFixed(1) + 'K'
  }
  return value.toString()
}

// 格式化时长
const formatDuration = (seconds: number): string => {
  if (seconds >= 3600) {
    const hours = Math.floor(seconds / 3600)
    const mins = Math.floor((seconds % 3600) / 60)
    return `${hours}h ${mins}m`
  } else if (seconds >= 60) {
    const mins = Math.floor(seconds / 60)
    const secs = Math.floor(seconds % 60)
    return `${mins}m ${secs}s`
  }
  return `${Math.floor(seconds)}s`
}

// 格式化延迟
const formatLatency = (ms: number): string => {
  if (ms >= 1000) {
    return (ms / 1000).toFixed(2) + 's'
  }
  return ms.toFixed(0) + 'ms'
}

// 健康等级标签类型
const getHealthTagType = (grade: string): string => {
  const typeMap: Record<string, string> = {
    A: 'success',
    B: 'primary',
    C: 'warning',
    D: 'danger',
    F: 'danger'
  }
  return typeMap[grade] || 'info'
}

// 行点击
const handleRowClick = (row: TopSession) => {
  handleViewPanorama(row.gwSessionId)
}

// 查看全景图
const handleViewPanorama = (sessionId: string) => {
  router.push(`/admin/session-analytics/${sessionId}/panorama`)
}
</script>

<style scoped>
.top-sessions-table {
  height: 100%;
}

.table-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.table-title {
  font-weight: 600;
  font-size: 15px;
}

.session-title {
  cursor: pointer;
  color: var(--accent-h);
}

.session-title:hover {
  text-decoration: underline;
}

.highlight-value {
  font-weight: 600;
  color: var(--accent-h);
}

.text-muted {
  color: var(--muted);
}

.table-empty {
  padding: 40px 20px;
  text-align: center;
}

/* 暗色系：让 el-card / el-table 与全局深色面板保持一致 */
:deep(.el-card) {
  background: var(--card);
  border-color: var(--border);
  color: var(--text);
}

:deep(.el-table) {
  background: transparent;
  color: var(--text);
  --el-table-bg-color: transparent;
  --el-table-tr-bg-color: transparent;
  --el-table-header-bg-color: var(--bg-subtle);
  --el-table-border-color: var(--border);
  --el-table-row-hover-bg-color: rgba(255, 255, 255, 0.04);
  --el-table-text-color: var(--text);
  --el-table-header-text-color: var(--muted);
}

:deep(.el-table__row) {
  cursor: pointer;
}

:deep(.el-table__row:hover) {
  background-color: rgba(255, 255, 255, 0.04) !important;
}

:deep(.el-table__cell) {
  border-bottom-color: var(--border);
}

:deep(.el-empty__description p) {
  color: var(--muted);
}
</style>
