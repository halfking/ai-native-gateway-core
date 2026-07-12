<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { Line } from 'vue-chartjs'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
} from 'chart.js'
import {
  getCenterInstances,
  getCenterStats,
  getHeartbeatHistory,
  sendCommand,
  type CenterInstance,
  type CenterStats,
  type HeartbeatRecord,
} from '../../api/ops'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend)

const { t } = useI18n()

const instances = ref<CenterInstance[]>([])
const stats = ref<CenterStats | null>(null)
const loading = ref(false)
const expandedRows = ref<string[]>([])
const heartbeatData = ref<Record<string, HeartbeatRecord[]>>({})

const page = ref(1)
const pageSize = ref(20)

// Command dialog state
const showCommandDialog = ref(false)
const commandForm = ref({
  instanceId: '',
  command: 'restart',
})
const commandParamsText = ref('{}')

const commandOptions = computed(() => [
  { value: 'restart', label: t('ops.center.cmd.restart') },
  { value: 'upgrade', label: t('ops.center.cmd.upgrade') },
  { value: 'config_update', label: t('ops.center.cmd.configUpdate') },
  { value: 'health_check', label: t('ops.center.cmd.healthCheck') },
  { value: 'collect_logs', label: t('ops.center.cmd.collectLogs') },
])

async function load() {
  loading.value = true
  try {
    const [instancesData, statsData] = await Promise.all([
      getCenterInstances(),
      getCenterStats(),
    ])
    instances.value = instancesData
    stats.value = statsData
  } catch (error) {
    ElMessage.error(t('ops.center.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function handleExpandChange(row: CenterInstance) {
  const idx = expandedRows.value.indexOf(row.instance_id)
  if (idx > -1) {
    expandedRows.value.splice(idx, 1)
    return
  }

  expandedRows.value.push(row.instance_id)
  if (!heartbeatData.value[row.instance_id]) {
    try {
      heartbeatData.value[row.instance_id] = await getHeartbeatHistory(row.instance_id, 24)
    } catch (error) {
      ElMessage.error(t('ops.center.loadHeartbeatFailed'))
      console.error(error)
    }
  }
}

function openCommandDialog(instance: CenterInstance) {
  commandForm.value = {
    instanceId: instance.instance_id,
    command: 'restart',
  }
  commandParamsText.value = '{}'
  showCommandDialog.value = true
}

async function handleSendCommand() {
  let args: Record<string, string> = {}
  try {
    args = JSON.parse(commandParamsText.value)
    if (typeof args !== 'object' || args === null || Array.isArray(args)) {
      throw new Error('params must be a JSON object')
    }
  } catch (e) {
    ElMessage.warning(t('ops.center.paramsInvalidJSON'))
    return
  }

  loading.value = true
  try {
    await sendCommand(
      commandForm.value.instanceId,
      commandForm.value.command,
      args,
      'admin'
    )
    ElMessage.success(t('ops.center.commandSent'))
    showCommandDialog.value = false
  } catch (error) {
    ElMessage.error(t('ops.center.commandFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function statusType(status: string) {
  const map: Record<string, 'success' | 'warning' | 'danger'> = {
    online: 'success',
    degraded: 'warning',
    offline: 'danger',
  }
  return map[status] || 'info'
}

function formatDate(date: string) {
  if (!date) return '-'
  return new Date(date).toLocaleString()
}

function formatUptime(seconds: number) {
  if (!seconds && seconds !== 0) return '-'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

function allocMbColor(mb: number) {
  if (mb > 500) return '#F56C6C'
  if (mb > 200) return '#E6A23C'
  return '#67C23A'
}

function getChartData(instanceId: string) {
  const history = heartbeatData.value[instanceId] || []
  return {
    labels: history.map((h) => new Date(h.timestamp).toLocaleTimeString()),
    datasets: [
      {
        label: t('ops.center.cmd.healthCheck'),
        data: history.map((h) => h.alloc_mb),
        borderColor: 'rgb(75, 192, 192)',
        backgroundColor: 'rgba(75, 192, 192, 0.2)',
        tension: 0.4,
        yAxisID: 'y',
      },
      {
        label: 'Goroutines',
        data: history.map((h) => h.num_goroutine),
        borderColor: 'rgb(255, 99, 132)',
        backgroundColor: 'rgba(255, 99, 132, 0.2)',
        tension: 0.4,
        yAxisID: 'y1',
      },
    ],
  }
}

const chartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const },
  plugins: {
    legend: { position: 'top' as const },
  },
  scales: {
    y: {
      beginAtZero: true,
      title: { display: true, text: 'Alloc MB' },
    },
    y1: {
      beginAtZero: true,
      position: 'right' as const,
      grid: { display: false },
      title: { display: true, text: 'Goroutines' },
    },
  },
}

const paginatedInstances = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return instances.value.slice(start, start + pageSize.value)
})

onMounted(load)
</script>

<template>
  <div class="center-ops-view">
    <div class="page-header">
      <h1>{{ t('ops.center.title') }}</h1>
      <el-button type="primary" @click="load">
        {{ t('common.refresh') }}
      </el-button>
    </div>

    <!-- Stats Dashboard -->
    <div v-if="stats" class="stats-grid">
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-primary">{{ stats.total_instances }}</div>
          <div class="stat-label">{{ t('ops.center.totalInstances') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-success">{{ stats.online_instances }}</div>
          <div class="stat-label">{{ t('ops.center.onlineInstances') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-warning">{{ stats.degraded_instances }}</div>
          <div class="stat-label">{{ t('ops.center.degradedInstances') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-danger">{{ stats.offline_instances }}</div>
          <div class="stat-label">{{ t('ops.center.offlineInstances') }}</div>
        </div>
      </el-card>
    </div>

    <!-- Instances Table -->
    <el-card class="main-card" shadow="never">
      <el-table
        v-loading="loading"
        :data="paginatedInstances"
        :row-key="(row: CenterInstance) => row.instance_id"
        :expand-row-keys="expandedRows"
        @expand-change="handleExpandChange"
      >
        <el-table-column type="expand">
          <template #default="{ row = {} } = {}">
            <div class="expanded-content">
              <div v-if="heartbeatData[row.instance_id]?.length" class="metrics-grid">
                <div class="metric-item">
                  <span class="metric-label">{{ t('ops.center.cpuUsage') }}:</span>
                  <span class="metric-value">{{ formatUptime(heartbeatData[row.instance_id][0].uptime_secs) }}</span>
                </div>
                <div class="metric-item">
                  <span class="metric-label">{{ t('ops.center.memoryUsage') }}:</span>
                  <el-progress :percentage="Math.min(heartbeatData[row.instance_id][0].alloc_mb / 10, 100)" :stroke-width="8" :color="allocMbColor(heartbeatData[row.instance_id][0].alloc_mb)" />
                  <span class="metric-detail">{{ heartbeatData[row.instance_id][0].alloc_mb.toFixed(1) }} MB</span>
                </div>
                <div class="metric-item">
                  <span class="metric-label">Goroutines:</span>
                  <span class="metric-value">{{ heartbeatData[row.instance_id][0].num_goroutine }}</span>
                </div>
              </div>
              <div class="chart-container">
                <h4>{{ t('ops.center.heartbeatHistory') }}</h4>
                <Line :data="getChartData(row.instance_id)" :options="chartOptions" />
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="instance_id" :label="t('ops.center.instanceId')" width="200" />
        <el-table-column prop="hostname" :label="t('ops.center.hostname')" width="150" />
        <el-table-column prop="ip_address" :label="t('ops.center.ipAddress')" width="130" />
        <el-table-column prop="region" :label="t('ops.center.region')" width="100" />
        <el-table-column prop="version" :label="t('ops.center.version')" width="120" />
        <el-table-column prop="build_seq" :label="t('ops.center.buildSeq')" width="80" />
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row = {} } = {}">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.center.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.center.uptime')" width="100">
          <template #default="{ row = {} } = {}">
            <span v-if="heartbeatData[row.instance_id]?.length">{{ formatUptime(heartbeatData[row.instance_id][0].uptime_secs) }}</span>
            <span v-else>-</span>
          </template>
        </el-table-column>
        <el-table-column prop="last_heartbeat" :label="t('ops.center.lastHeartbeat')" width="160">
          <template #default="{ row = {} } = {}">{{ formatDate(row.last_heartbeat) }}</template>
        </el-table-column>
        <el-table-column prop="started_at" :label="t('ops.center.startedAt')" width="160">
          <template #default="{ row = {} } = {}">{{ formatDate(row.started_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="200" fixed="right">
          <template #default="{ row = {} } = {}">
            <el-button type="primary" size="small" @click="openCommandDialog(row)">
              {{ t('ops.center.sendCommand') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="instances.length > pageSize" class="pagination-wrap">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="instances.length"
          :page-sizes="[10, 20, 50]"
          layout="sizes, prev, pager, next"
        />
      </div>
    </el-card>

    <!-- Command Dialog -->
    <el-dialog
      v-model="showCommandDialog"
      :title="t('ops.center.sendCommandTitle')"
      width="500px"
    >
      <el-form :model="commandForm" label-width="120px">
        <el-form-item :label="t('ops.center.instanceId')">
          <el-input v-model="commandForm.instanceId" disabled />
        </el-form-item>
        <el-form-item :label="t('ops.center.command')" required>
          <el-select v-model="commandForm.command" style="width: 100%">
            <el-option
              v-for="cmd in commandOptions"
              :key="cmd.value"
              :label="cmd.label"
              :value="cmd.value"
            />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('ops.center.parameters')">
          <el-input
            v-model="commandParamsText"
            type="textarea"
            :rows="3"
            :placeholder="t('ops.center.parametersPlaceholder')"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCommandDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleSendCommand">
          {{ t('common.send') }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.center-ops-view {
  padding: 20px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 20px;
}

.page-header h1 {
  font-size: 24px;
  margin: 0;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 16px;
  margin-bottom: 20px;
}

.stat-item {
  text-align: center;
  padding: 8px;
}

.stat-value {
  font-size: 32px;
  font-weight: bold;
  color: var(--el-color-primary);
  margin-bottom: 8px;
}

.stat-value.stat-primary {
  color: var(--el-color-primary);
}

.stat-value.stat-success {
  color: var(--el-color-success);
}

.stat-value.stat-warning {
  color: var(--el-color-warning);
}

.stat-value.stat-danger {
  color: var(--el-color-danger);
}

.metric-detail {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.pagination-wrap {
  margin-top: 16px;
  display: flex;
  justify-content: flex-end;
}

.stat-label {
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

.main-card {
  margin-top: 20px;
}

.expanded-content {
  padding: 20px;
  background-color: var(--el-fill-color-light);
}

.metrics-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 16px;
  margin-bottom: 20px;
}

.metric-item {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.metric-label {
  font-size: 14px;
  font-weight: 500;
  color: var(--el-text-color-primary);
}

.chart-container {
  margin-top: 20px;
}

.chart-container h4 {
  margin: 0 0 12px 0;
  font-size: 14px;
  font-weight: 600;
}

.chart-container canvas {
  height: 300px !important;
}

.loading-chart {
  text-align: center;
  padding: 20px;
  color: var(--el-text-color-secondary);
}
</style>
