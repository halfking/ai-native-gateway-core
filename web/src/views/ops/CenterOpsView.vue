<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  getCenterInstances,
  getCenterStats,
  getHeartbeatHistory,
  sendCommand,
  type CenterInstance,
  type CenterStats,
  type HeartbeatHistory,
} from '../../api/ops'

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
  params: '{}',
})

const commandOptions = [
  { value: 'restart', label: 'Restart Service' },
  { value: 'upgrade', label: 'Update Version' },
  { value: 'config_update', label: 'Update Config' },
  { value: 'health_check', label: 'Health Check' },
  { value: 'collect_logs', label: 'Collect Logs' },
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
      heartbeatData.value[row.instance_id] = await getHeartbeatHistory(row.instance_id)
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
    params: '{}',
  }
  showCommandDialog.value = true
}

async function handleSendCommand() {
  loading.value = true
  try {
    const args = commandForm.value.params.trim()
      ? JSON.parse(commandForm.value.params) as Record<string, string>
      : {}
    await sendCommand(commandForm.value.instanceId, commandForm.value.command, args)
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
  const map: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
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
                  <span class="metric-label">{{ t('ops.center.uptime') }}:</span>
                  <span>{{ formatUptime(row.uptime_seconds) }}</span>
                </div>
                <div class="metric-item">
                  <span class="metric-label">{{ t('ops.center.version') }}:</span>
                  <span>{{ row.version }}</span>
                </div>
                <div class="metric-item">
                  <span class="metric-label">{{ t('ops.center.lastHeartbeat') }}:</span>
                  <span>{{ formatDate(row.last_heartbeat) }}</span>
                </div>
              </div>
              <div v-if="heartbeatData[row.instance_id]" class="chart-container">
                <h2>{{ t('ops.center.heartbeatHistory') }}</h2>
                <el-table :data="heartbeatData[row.instance_id]" size="small">
                  <el-table-column prop="timestamp" :label="t('ops.center.lastHeartbeat')">
                    <template #default="{ row: heartbeat }">{{ formatDate(heartbeat.timestamp) }}</template>
                  </el-table-column>
                  <el-table-column prop="uptime_secs" :label="t('ops.center.uptime')">
                    <template #default="{ row: heartbeat }">{{ formatUptime(heartbeat.uptime_secs) }}</template>
                  </el-table-column>
                  <el-table-column prop="alloc_mb" label="Alloc MB" />
                  <el-table-column prop="num_goroutine" label="Goroutines" />
                </el-table>
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

.chart-container h2 {
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
