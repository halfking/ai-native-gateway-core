<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  getCenterInstances,
  getCenterStats,
  sendCommand,
  getCommandStatus,
  getCenterAlerts,
  acknowledgeCenterAlert,
  resolveCenterAlert,
  suppressCenterAlert,
  type CenterInstance,
  type CenterStats,
  type IssuedCommand,
  type OpsAlert,
} from '../../api/ops'

const { t } = useI18n()

const instances = ref<CenterInstance[]>([])
const stats = ref<CenterStats | null>(null)
const alerts = ref<OpsAlert[]>([])
const alertsOpen = ref(0)
const loading = ref(false)
const alertsLoading = ref(false)
const page = ref(1)
const pageSize = ref(20)

// Command dialog state
const showCommandDialog = ref(false)
const commandForm = ref({
  instanceId: '',
  command: 'restart',
})
const commandParamsText = ref('{}')
const lastCommand = ref<IssuedCommand | null>(null)
const commandPollTimer = ref<ReturnType<typeof setInterval> | null>(null)

const commandOptions = computed(() => [
  { value: 'restart', label: t('ops.center.cmd.restart') },
  { value: 'upgrade', label: t('ops.center.cmd.upgrade') },
  { value: 'config_update', label: t('ops.center.cmd.configUpdate') },
  { value: 'health_check', label: t('ops.center.cmd.healthCheck') },
  { value: 'collect_logs', label: t('ops.center.cmd.collectLogs') },
])

async function load() {
  loading.value = true
  alertsLoading.value = true
  try {
    const [instancesData, statsData, alertsData] = await Promise.all([
      getCenterInstances(),
      getCenterStats(),
      getCenterAlerts(),
    ])
    instances.value = instancesData
    stats.value = statsData
    alerts.value = alertsData.items
    alertsOpen.value = alertsData.open
  } catch (error) {
    ElMessage.error(t('ops.center.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
    alertsLoading.value = false
  }
}

function canManageAlert(alert: OpsAlert) {
  return alert.id.startsWith('runtime-')
}

function alertSeverityType(severity: string) {
  const map: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
    critical: 'danger',
    error: 'danger',
    warning: 'warning',
    info: 'info',
  }
  return map[severity] || 'info'
}

function alertStatusType(status: string) {
  const map: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
    triggered: 'danger',
    acknowledged: 'warning',
    suppressed: 'info',
    resolved: 'success',
  }
  return map[status] || 'info'
}

async function handleAcknowledgeAlert(alert: OpsAlert) {
  try {
    await acknowledgeCenterAlert(alert.id)
    ElMessage.success(t('ops.center.alerts.ackSuccess'))
    await load()
  } catch (error) {
    ElMessage.error(t('ops.center.alerts.actionFailed'))
    console.error(error)
  }
}

async function handleResolveAlert(alert: OpsAlert) {
  try {
    await resolveCenterAlert(alert.id)
    ElMessage.success(t('ops.center.alerts.resolveSuccess'))
    await load()
  } catch (error) {
    ElMessage.error(t('ops.center.alerts.actionFailed'))
    console.error(error)
  }
}

async function handleSuppressAlert(alert: OpsAlert) {
  try {
    await suppressCenterAlert(alert.id, 24)
    ElMessage.success(t('ops.center.alerts.suppressSuccess'))
    await load()
  } catch (error) {
    ElMessage.error(t('ops.center.alerts.actionFailed'))
    console.error(error)
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
    const issued = await sendCommand(
      commandForm.value.instanceId,
      commandForm.value.command,
      args,
      'admin'
    )
    lastCommand.value = issued
    ElMessage.success(t('ops.center.commandSent'))
    showCommandDialog.value = false
    startCommandPoll(issued.command_id)
  } catch (error) {
    ElMessage.error(t('ops.center.commandFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function startCommandPoll(commandId: string) {
  if (commandPollTimer.value) clearInterval(commandPollTimer.value)
  let attempts = 0
  commandPollTimer.value = setInterval(async () => {
    attempts += 1
    try {
      const st = await getCommandStatus(commandId)
      if (lastCommand.value?.command_id === commandId) {
        lastCommand.value = { ...lastCommand.value, status: st.status }
      }
      if (st.status !== 'pending' || attempts >= 30) {
        if (commandPollTimer.value) clearInterval(commandPollTimer.value)
        commandPollTimer.value = null
      }
    } catch {
      if (attempts >= 30 && commandPollTimer.value) {
        clearInterval(commandPollTimer.value)
        commandPollTimer.value = null
      }
    }
  }, 2000)
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

    <el-alert
      v-if="lastCommand"
      :title="t('ops.center.lastCommand', { id: lastCommand.command_id, status: lastCommand.status })"
      type="info"
      show-icon
      :closable="false"
      class="last-cmd-alert"
    />

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

    <!-- Alerts -->
    <el-card class="alerts-card" shadow="never">
      <template #header>
        <div class="alerts-header">
          <span>{{ t('ops.center.alerts.title') }}</span>
          <el-tag type="danger" size="small">{{ t('ops.center.alerts.openCount', { count: alertsOpen }) }}</el-tag>
        </div>
      </template>
      <el-table v-loading="alertsLoading" :data="alerts" empty-text="-" row-key="id">
        <template #empty>
          <span>{{ t('ops.center.alerts.empty') }}</span>
        </template>
        <el-table-column prop="severity" :label="t('ops.center.alerts.severity')" width="100">
          <template #default="{ row }">
            <el-tag :type="alertSeverityType(row.severity)" size="small">{{ row.severity }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="title" :label="t('ops.center.alerts.alertTitle')" min-width="140" />
        <el-table-column prop="message" :label="t('ops.center.alerts.message')" min-width="220" show-overflow-tooltip />
        <el-table-column prop="source" :label="t('ops.center.alerts.source')" width="120" />
        <el-table-column prop="status" :label="t('common.table.status')" width="110">
          <template #default="{ row }">
            <el-tag :type="alertStatusType(row.status)" size="small">
              {{ t(`ops.center.alerts.status.${row.status}`, row.status) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="instance_id" :label="t('ops.center.instanceId')" width="180" />
        <el-table-column prop="detected_at" :label="t('ops.center.alerts.detectedAt')" width="170">
          <template #default="{ row }">{{ formatDate(row.detected_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="220" fixed="right">
          <template #default="{ row }">
            <template v-if="canManageAlert(row)">
              <el-button
                v-if="row.status === 'triggered'"
                size="small"
                type="primary"
                @click="handleAcknowledgeAlert(row)"
              >
                {{ t('ops.center.alerts.acknowledge') }}
              </el-button>
              <el-button
                v-if="row.status === 'triggered' || row.status === 'acknowledged'"
                size="small"
                type="success"
                @click="handleResolveAlert(row)"
              >
                {{ t('ops.center.alerts.resolve') }}
              </el-button>
              <el-button
                v-if="row.status === 'triggered' || row.status === 'acknowledged'"
                size="small"
                @click="handleSuppressAlert(row)"
              >
                {{ t('ops.center.alerts.suppress') }}
              </el-button>
            </template>
            <span v-else class="muted-action">—</span>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Instances Table -->
    <el-card class="main-card" shadow="never">
      <el-table v-loading="loading" :data="paginatedInstances" row-key="instance_id">
        <el-table-column prop="instance_id" :label="t('ops.center.instanceId')" width="200" />
        <el-table-column prop="hostname" :label="t('ops.center.hostname')" width="150" />
        <el-table-column prop="ip_address" :label="t('ops.center.ipAddress')" width="130" />
        <el-table-column prop="region" :label="t('ops.center.region')" width="100" />
        <el-table-column prop="version" :label="t('ops.center.version')" width="120" />
        <el-table-column prop="build_seq" :label="t('ops.center.buildSeq')" width="80" />
        <el-table-column prop="status" :label="t('common.table.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.center.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="last_heartbeat" :label="t('ops.center.lastHeartbeat')" width="160">
          <template #default="{ row }">{{ formatDate(row.last_heartbeat) }}</template>
        </el-table-column>
        <el-table-column prop="started_at" :label="t('ops.center.startedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.started_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="200" fixed="right">
          <template #default="{ row }">
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

.alerts-card {
  margin-bottom: 20px;
}

.alerts-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.muted-action {
  color: var(--el-text-color-placeholder);
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

.history-table-wrap {
  margin-top: 20px;
}

.history-table-wrap h4 {
  margin: 0 0 12px 0;
  font-size: 14px;
  font-weight: 600;
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
