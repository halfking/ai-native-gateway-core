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
  type HeartbeatHistory,
} from '../../api/ops'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend)

const { t } = useI18n()

const instances = ref<CenterInstance[]>([])
const stats = ref<CenterStats | null>(null)
const loading = ref(false)
const expandedRows = ref<string[]>([])
const heartbeatData = ref<Record<string, HeartbeatHistory[]>>({})

// Command dialog state
const showCommandDialog = ref(false)
const commandForm = ref({
  instanceId: '',
  command: 'restart',
  params: {} as Record<string, unknown>,
})

const commandOptions = [
  { value: 'restart', label: 'Restart Service' },
  { value: 'reload', label: 'Reload Config' },
  { value: 'update', label: 'Update Version' },
  { value: 'clear_cache', label: 'Clear Cache' },
  { value: 'health_check', label: 'Health Check' },
]

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
    params: {},
  }
  showCommandDialog.value = true
}

async function handleSendCommand() {
  loading.value = true
  try {
    await sendCommand(commandForm.value.instanceId, commandForm.value.command, commandForm.value.params)
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
  return new Date(date).toLocaleString()
}

function formatUptime(seconds: number) {
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days}d ${hours}h`
  if (hours > 0) return `${hours}h ${minutes}m`
  return `${minutes}m`
}

function getChartData(instanceId: string) {
  const history = heartbeatData.value[instanceId] || []
  return {
    labels: history.map((h) => new Date(h.timestamp).toLocaleTimeString()),
    datasets: [
      {
        label: 'CPU %',
        data: history.map((h) => h.cpu_usage),
        borderColor: 'rgb(75, 192, 192)',
        backgroundColor: 'rgba(75, 192, 192, 0.2)',
        tension: 0.4,
      },
      {
        label: 'Memory %',
        data: history.map((h) => h.memory_usage),
        borderColor: 'rgb(255, 99, 132)',
        backgroundColor: 'rgba(255, 99, 132, 0.2)',
        tension: 0.4,
      },
      {
        label: 'Disk %',
        data: history.map((h) => h.disk_usage),
        borderColor: 'rgb(255, 205, 86)',
        backgroundColor: 'rgba(255, 205, 86, 0.2)',
        tension: 0.4,
      },
    ],
  }
}

const chartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      position: 'top' as const,
    },
  },
  scales: {
    y: {
      beginAtZero: true,
      max: 100,
    },
  },
}

onMounted(load)
</script>

<template>
  <div class="center-ops-view">
    <div class="page-header">
      <h1>🖥️ {{ t('ops.center.title') }}</h1>
      <el-button type="primary" @click="load">
        {{ t('common.refresh') }}
      </el-button>
    </div>

    <!-- Stats Dashboard -->
    <div v-if="stats" class="stats-grid">
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-success">{{ stats.online_count }}</div>
          <div class="stat-label">{{ t('ops.center.onlineInstances') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-warning">{{ stats.degraded_count }}</div>
          <div class="stat-label">{{ t('ops.center.degradedInstances') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-danger">{{ stats.offline_count }}</div>
          <div class="stat-label">{{ t('ops.center.offlineInstances') }}</div>
        </div>
      </el-card>
    </div>

    <!-- Instances Table -->
    <el-card class="main-card" shadow="never">
      <el-table
        v-loading="loading"
        :data="instances"
        :row-key="(row: CenterInstance) => row.instance_id"
        :expand-row-keys="expandedRows"
        @expand-change="handleExpandChange"
      >
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="expanded-content">
              <div class="metrics-grid">
                <div class="metric-item">
                  <span class="metric-label">{{ t('ops.center.cpuUsage') }}:</span>
                  <el-progress :percentage="row.cpu_usage" :stroke-width="8" />
                </div>
                <div class="metric-item">
                  <span class="metric-label">{{ t('ops.center.memoryUsage') }}:</span>
                  <el-progress :percentage="row.memory_usage" :stroke-width="8" :color="row.memory_usage > 80 ? '#F56C6C' : '#67C23A'" />
                </div>
                <div class="metric-item">
                  <span class="metric-label">{{ t('ops.center.diskUsage') }}:</span>
                  <el-progress :percentage="row.disk_usage" :stroke-width="8" :color="row.disk_usage > 80 ? '#F56C6C' : '#67C23A'" />
                </div>
              </div>
              <div v-if="heartbeatData[row.instance_id]" class="chart-container">
                <h4>{{ t('ops.center.heartbeatHistory') }}</h4>
                <Line :data="getChartData(row.instance_id)" :options="chartOptions" />
              </div>
              <div v-else class="loading-chart">{{ t('common.loading') }}</div>
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="instance_id" :label="t('ops.center.instanceId')" width="200" />
        <el-table-column prop="hostname" :label="t('ops.center.hostname')" width="150" />
        <el-table-column prop="version" :label="t('ops.center.version')" width="120" />
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.center.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="uptime_seconds" :label="t('ops.center.uptime')" width="100">
          <template #default="{ row }">{{ formatUptime(row.uptime_seconds) }}</template>
        </el-table-column>
        <el-table-column prop="last_heartbeat" :label="t('ops.center.lastHeartbeat')" width="160">
          <template #default="{ row }">{{ formatDate(row.last_heartbeat) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="140" fixed="right">
          <template #default="{ row }">
            <el-button type="primary" size="small" @click="openCommandDialog(row)">
              {{ t('ops.center.sendCommand') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
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
            v-model="commandForm.params"
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

.stat-value.stat-success {
  color: var(--el-color-success);
}

.stat-value.stat-warning {
  color: var(--el-color-warning);
}

.stat-value.stat-danger {
  color: var(--el-color-danger);
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
