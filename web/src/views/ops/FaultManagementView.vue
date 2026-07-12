<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getFaultEvents,
  getFaultRules,
  createFaultRule,
  updateFaultRule,
  deleteFaultRule,
  getFaultStats,
  acknowledgeFaultEvent,
  resolveFaultEvent,
  type FaultEvent,
  type FaultRule,
  type FaultStats,
} from '../../api/ops'

const { t } = useI18n()

const events = ref<FaultEvent[]>([])
const rules = ref<FaultRule[]>([])
const stats = ref<FaultStats | null>(null)
const loading = ref(false)
const selectedEvent = ref<FaultEvent | null>(null)
const showEventDetail = ref(false)
const eventDetailLoading = ref(false)

// Rule dialog
const showRuleDialog = ref(false)
const editingRule = ref<FaultRule | null>(null)
const ruleForm = ref({
  name: '',
  description: '',
  severity: 'warning' as 'critical' | 'error' | 'warning' | 'info',
  metric: '',
  operator: 'gte' as 'gte' | 'lte' | 'eq' | 'ne',
  threshold: 0,
  duration: '5m',
  action: 'notify' as 'restart' | 'scale_up' | 'notify' | 'failover' | 'auto_recover' | 'run_script',
  action_config: '',
  enabled: true,
  cooldown: '5m',
})

// Filters
const filterStatus = ref<string>('all')
const filterSeverity = ref<string>('all')

const filteredEvents = computed(() => {
  return events.value.filter((event) => {
    if (filterStatus.value !== 'all' && event.status !== filterStatus.value) return false
    if (filterSeverity.value !== 'all' && event.severity !== filterSeverity.value) return false
    return true
  })
})

async function load() {
  loading.value = true
  try {
    const [eventsRes, rulesRes, statsData] = await Promise.all([
      getFaultEvents({ limit: 200 }),
      getFaultRules({ limit: 200 }),
      getFaultStats(),
    ])
    events.value = eventsRes.events
    rules.value = rulesRes.rules
    stats.value = statsData
  } catch (error) {
    ElMessage.error(t('ops.fault.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function openCreateDialog() {
  editingRule.value = null
  ruleForm.value = {
    name: '',
    description: '',
    severity: 'warning',
    metric: '',
    operator: 'gte',
    threshold: 0,
    duration: '5m',
    action: 'notify',
    action_config: '',
    enabled: true,
    cooldown: '5m',
  }
  showRuleDialog.value = true
}

function openEditDialog(rule: FaultRule) {
  editingRule.value = rule
  ruleForm.value = {
    name: rule.name,
    description: rule.description,
    severity: rule.severity,
    metric: rule.metric,
    operator: rule.operator,
    threshold: rule.threshold,
    duration: rule.duration,
    action: rule.action,
    action_config: rule.action_config || '',
    enabled: rule.enabled,
    cooldown: rule.cooldown,
  }
  showRuleDialog.value = true
}

async function handleSaveRule() {
  if (!ruleForm.value.name || !ruleForm.value.metric) {
    ElMessage.warning(t('ops.fault.fillRequired'))
    return
  }

  loading.value = true
  try {
    const payload = { ...ruleForm.value }
    if (editingRule.value) {
      await updateFaultRule(editingRule.value.id, payload)
      ElMessage.success(t('ops.fault.updateSuccess'))
    } else {
      await createFaultRule(payload)
      ElMessage.success(t('ops.fault.createSuccess'))
    }
    showRuleDialog.value = false
    await load()
  } catch (error) {
    ElMessage.error(t('ops.fault.saveFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

async function handleDeleteRule(rule: FaultRule) {
  try {
    await ElMessageBox.confirm(
      t('ops.fault.deleteRuleConfirm', { name: rule.name }),
      t('common.warning'),
      { type: 'warning' }
    )
    await deleteFaultRule(rule.id)
    ElMessage.success(t('ops.fault.deleteSuccess'))
    await load()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.fault.deleteFailed'))
      console.error(error)
    }
  }
}

async function handleAcknowledge(event: FaultEvent) {
  try {
    await acknowledgeFaultEvent(event.id)
    ElMessage.success('Event acknowledged')
    await load()
  } catch (error) {
    ElMessage.error(t('ops.fault.fixFailed'))
    console.error(error)
  }
}

async function handleResolve(event: FaultEvent) {
  try {
    await ElMessageBox.confirm(
      t('ops.fault.fixConfirm'),
      t('common.confirm'),
      { type: 'info' }
    )
    await resolveFaultEvent(event.id)
    ElMessage.success('Event resolved')
    await load()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.fault.fixFailed'))
      console.error(error)
    }
  }
}

function showDetail(event: FaultEvent) {
  selectedEvent.value = event
  showEventDetail.value = true
}

function severityType(severity: string) {
  const map: Record<string, 'danger' | 'warning' | 'info'> = {
    critical: 'danger',
    error: 'danger',
    warning: 'warning',
    info: 'info',
  }
  return map[severity] || 'info'
}

function statusType(status: string) {
  const map: Record<string, 'danger' | 'warning' | 'success' | 'info'> = {
    new: 'danger',
    acknowledged: 'warning',
    resolving: 'warning',
    resolved: 'success',
    ignored: 'info',
  }
  return map[status] || 'info'
}

function formatDate(date: string) {
  if (!date) return '-'
  return new Date(date).toLocaleString()
}

function formatDuration(minutes: number) {
  if (!minutes && minutes !== 0) return '-'
  if (minutes < 60) return `${Math.round(minutes)}m`
  const hours = Math.floor(minutes / 60)
  const mins = Math.round(minutes % 60)
  return `${hours}h ${mins}m`
}

const operatorLabels: Record<string, string> = {
  gte: '>=',
  lte: '<=',
  eq: '==',
  ne: '!=',
}

onMounted(load)
</script>

<template>
  <div class="fault-management-view">
    <div class="page-header">
      <h1>{{ t('ops.fault.title') }}</h1>
      <el-button type="primary" @click="openCreateDialog">
        + {{ t('ops.fault.createRule') }}
      </el-button>
    </div>

    <!-- Stats Dashboard -->
    <div v-if="stats" class="stats-grid">
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-primary">{{ stats.total_events }}</div>
          <div class="stat-label">{{ t('ops.fault.totalEvents') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-danger">{{ stats.open_events }}</div>
          <div class="stat-label">{{ t('ops.fault.openEvents') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-success">{{ stats.resolved_24h }}</div>
          <div class="stat-label">{{ t('ops.fault.resolvedEvents24h') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value stat-primary">{{ formatDuration(stats.avg_resolve_mins) }}</div>
          <div class="stat-label">{{ t('ops.fault.avgResolutionTime') }}</div>
        </div>
      </el-card>
    </div>

    <!-- Filters -->
    <el-card class="filters-card" shadow="never">
      <el-space>
        <el-select v-model="filterStatus" :placeholder="t('common.status')" style="width: 150px">
          <el-option :label="t('common.all')" value="all" />
          <el-option :label="t('ops.fault.status.new')" value="new" />
          <el-option :label="t('ops.fault.status.acknowledged')" value="acknowledged" />
          <el-option :label="t('ops.fault.status.resolving')" value="resolving" />
          <el-option :label="t('ops.fault.status.resolved')" value="resolved" />
          <el-option :label="t('ops.fault.status.ignored')" value="ignored" />
        </el-select>
        <el-select v-model="filterSeverity" :placeholder="t('ops.fault.severityLabel')" style="width: 150px">
          <el-option :label="t('common.all')" value="all" />
          <el-option :label="t('ops.fault.severity.critical')" value="critical" />
          <el-option :label="t('ops.fault.severity.error')" value="error" />
          <el-option :label="t('ops.fault.severity.warning')" value="warning" />
          <el-option :label="t('ops.fault.severity.info')" value="info" />
        </el-select>
      </el-space>
    </el-card>

    <!-- Events Table -->
    <el-card class="main-card" shadow="never">
      <template #header>
        <span>{{ t('ops.fault.events') }} ({{ stats?.open_events || 0 }} open)</span>
      </template>
      <el-table v-loading="loading" :data="filteredEvents">
        <el-table-column prop="rule_name" :label="t('ops.fault.ruleName')" width="160" />
        <el-table-column prop="severity" :label="t('ops.fault.severityLabel')" width="80">
          <template #default="{ row = {} } = {}">
            <el-tag :type="severityType(row.severity)" size="small">
              {{ t(`ops.fault.severity.${row.severity}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row = {} } = {}">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.fault.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="title" :label="t('ops.fault.titleLabel')" min-width="220" show-overflow-tooltip />
        <el-table-column prop="source" :label="t('ops.fault.source')" width="100" />
        <el-table-column prop="detected_at" :label="t('ops.fault.detectedAt')" width="150">
          <template #default="{ row = {} } = {}">{{ formatDate(row.detected_at) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="200" fixed="right">
          <template #default="{ row = {} } = {}">
            <el-button size="small" @click="showDetail(row)">{{ t('common.view') }}</el-button>
            <el-button v-if="row.status === 'new'" size="small" type="primary" @click="handleAcknowledge(row)">{{ t('ops.fault.acknowledge') }}</el-button>
            <el-button v-if="row.status === 'acknowledged'" size="small" type="success" @click="handleResolve(row)">{{ t('ops.fault.resolve') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Event Detail Dialog -->
    <el-dialog v-model="showEventDetail" :title="t('ops.fault.eventDetail')" width="600px">
      <template v-if="selectedEvent">
        <el-descriptions :column="2" border>
          <el-descriptions-item :label="t('ops.fault.titleLabel')" :span="2">{{ selectedEvent.title }}</el-descriptions-item>
          <el-descriptions-item :label="t('ops.fault.ruleName')">{{ selectedEvent.rule_name }}</el-descriptions-item>
          <el-descriptions-item :label="t('common.status')">
            <el-tag :type="statusType(selectedEvent.status)" size="small">{{ t(`ops.fault.status.${selectedEvent.status}`) }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item :label="t('ops.fault.severityLabel')">
            <el-tag :type="severityType(selectedEvent.severity)" size="small">{{ t(`ops.fault.severity.${selectedEvent.severity}`) }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item :label="t('ops.fault.source')">{{ selectedEvent.source }}</el-descriptions-item>
          <el-descriptions-item :label="t('ops.fault.detectedAt')">{{ formatDate(selectedEvent.detected_at) }}</el-descriptions-item>
          <el-descriptions-item v-if="selectedEvent.acked_at" :label="t('ops.fault.ackedAt')">{{ formatDate(selectedEvent.acked_at) }} ({{ selectedEvent.acked_by }})</el-descriptions-item>
          <el-descriptions-item v-if="selectedEvent.resolved_at" :label="t('ops.fault.resolvedAt')">{{ formatDate(selectedEvent.resolved_at) }} ({{ selectedEvent.resolved_by }})</el-descriptions-item>
          <el-descriptions-item :label="t('common.description')" :span="2">{{ selectedEvent.description || '-' }}</el-descriptions-item>
          <el-descriptions-item v-if="selectedEvent.metadata" :label="t('ops.fault.metadata')" :span="2">
            <pre class="metadata-pre">{{ JSON.stringify(JSON.parse(selectedEvent.metadata), null, 2) }}</pre>
          </el-descriptions-item>
        </el-descriptions>
      </template>
    </el-dialog>

    <!-- Rules Table -->
    <el-card class="rules-card" shadow="never">
      <template #header>
        <span>{{ t('ops.fault.rules') }}</span>
      </template>
      <el-table :data="rules" size="small">
        <el-table-column prop="name" :label="t('ops.fault.ruleName')" width="160" />
        <el-table-column prop="severity" :label="t('ops.fault.severityLabel')" width="80">
          <template #default="{ row = {} } = {}">
            <el-tag :type="severityType(row.severity)" size="small">
              {{ t(`ops.fault.severity.${row.severity}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.fault.condition')" width="180">
          <template #default="{ row = {} } = {}">
            <code>{{ row.metric }} {{ operatorLabels[row.operator] || row.operator }} {{ row.threshold }} ({{ row.duration }})</code>
          </template>
        </el-table-column>
        <el-table-column prop="action" :label="t('ops.fault.action')" width="100" />
        <el-table-column prop="enabled" :label="t('common.enabled')" width="70">
          <template #default="{ row = {} } = {}">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
              {{ row.enabled ? t('common.yes') : t('common.no') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="160" fixed="right">
          <template #default="{ row = {} } = {}">
            <el-button type="primary" size="small" @click="openEditDialog(row)">{{ t('common.edit') }}</el-button>
            <el-button type="danger" size="small" @click="handleDeleteRule(row)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Rule Dialog -->
    <el-dialog
      v-model="showRuleDialog"
      :title="editingRule ? t('ops.fault.editRule') : t('ops.fault.createRule')"
      width="650px"
    >
      <el-form :model="ruleForm" label-width="120px">
        <el-form-item :label="t('ops.fault.ruleName')" required>
          <el-input v-model="ruleForm.name" :placeholder="t('ops.fault.ruleNamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('common.description')">
          <el-input v-model="ruleForm.description" type="textarea" :rows="2" :placeholder="t('ops.fault.descriptionPlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('ops.fault.severityLabel')" required>
          <el-radio-group v-model="ruleForm.severity">
            <el-radio label="critical">{{ t('ops.fault.severity.critical') }}</el-radio>
            <el-radio label="error">{{ t('ops.fault.severity.error') }}</el-radio>
            <el-radio label="warning">{{ t('ops.fault.severity.warning') }}</el-radio>
            <el-radio label="info">{{ t('ops.fault.severity.info') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('ops.fault.metric')" required>
          <el-input v-model="ruleForm.metric" placeholder="e.g. cpu_usage, memory_alloc, error_rate" />
        </el-form-item>
        <div class="rule-operator-row">
          <el-form-item :label="t('ops.fault.operator')" required>
            <el-select v-model="ruleForm.operator" style="width: 140px">
              <el-option label=">= (gte)" value="gte" />
              <el-option label="<= (lte)" value="lte" />
              <el-option label="== (eq)" value="eq" />
              <el-option label="!= (ne)" value="ne" />
            </el-select>
          </el-form-item>
          <el-form-item :label="t('ops.fault.threshold')" required>
            <el-input-number v-model="ruleForm.threshold" :min="0" :precision="2" :step="0.1" />
          </el-form-item>
          <el-form-item :label="t('ops.fault.duration')" required>
            <el-input v-model="ruleForm.duration" placeholder="e.g. 5m, 30s, 1h" style="width: 120px" />
          </el-form-item>
        </div>
        <el-form-item :label="t('ops.fault.action')" required>
          <el-select v-model="ruleForm.action" style="width: 100%">
            <el-option label="Notify" value="notify" />
            <el-option label="Restart Service" value="restart" />
            <el-option label="Scale Up" value="scale_up" />
            <el-option label="Failover" value="failover" />
            <el-option label="Auto Recover" value="auto_recover" />
            <el-option label="Run Script" value="run_script" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('ops.fault.actionConfig')">
          <el-input v-model="ruleForm.action_config" type="textarea" :rows="2" placeholder="Optional JSON config" />
        </el-form-item>
        <el-form-item :label="t('ops.fault.cooldown')">
          <el-input v-model="ruleForm.cooldown" placeholder="e.g. 5m, 10m" style="width: 200px" />
        </el-form-item>
        <el-form-item :label="t('common.enabled')">
          <el-switch v-model="ruleForm.enabled" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showRuleDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleSaveRule">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.fault-management-view {
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

.stat-value.stat-danger {
  color: var(--el-color-danger);
}

.stat-value.stat-success {
  color: var(--el-color-success);
}

.rule-operator-row {
  display: flex;
  gap: 12px;
}

.rule-operator-row :deep(.el-form-item) {
  flex: 1;
}

.metadata-pre {
  background: var(--el-fill-color-light);
  padding: 8px;
  border-radius: 4px;
  font-size: 12px;
  max-height: 200px;
  overflow: auto;
}

.stat-label {
  font-size: 14px;
  color: var(--el-text-color-secondary);
}

.filters-card {
  margin-bottom: 20px;
}

.main-card {
  margin-bottom: 20px;
}

.rules-card {
  margin-top: 20px;
}
</style>
