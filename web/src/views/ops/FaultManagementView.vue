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
  triggerManualFix,
  type FaultEvent,
  type FaultRule,
  type FaultStats,
} from '../../api/ops'

const { t } = useI18n()

const events = ref<FaultEvent[]>([])
const rules = ref<FaultRule[]>([])
const stats = ref<FaultStats | null>(null)
const loading = ref(false)

// Rule dialog state
const showRuleDialog = ref(false)
const editingRule = ref<FaultRule | null>(null)
const ruleForm = ref({
  name: '',
  description: '',
  severity: 'warning' as 'critical' | 'warning' | 'info',
  enabled: true,
  condition: '',
  auto_fix: false,
})

// Filter state
const filterStatus = ref<string>('all')
const filterSeverity = ref<string>('all')

const filteredEvents = computed(() => {
  return events.value.filter((event) => {
    if (filterStatus.value !== 'all' && event.status !== filterStatus.value) {
      return false
    }
    if (filterSeverity.value !== 'all' && event.severity !== filterSeverity.value) {
      return false
    }
    return true
  })
})

async function load() {
  loading.value = true
  try {
    const [eventsData, rulesData, statsData] = await Promise.all([
      getFaultEvents(),
      getFaultRules(),
      getFaultStats(),
    ])
    events.value = eventsData
    rules.value = rulesData
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
    enabled: true,
    condition: '',
    auto_fix: false,
  }
  showRuleDialog.value = true
}

function openEditDialog(rule: FaultRule) {
  editingRule.value = rule
  ruleForm.value = {
    name: rule.name,
    description: rule.description,
    severity: rule.severity,
    enabled: rule.enabled,
    condition: rule.condition,
    auto_fix: rule.auto_fix,
  }
  showRuleDialog.value = true
}

async function handleSaveRule() {
  if (!ruleForm.value.name || !ruleForm.value.condition) {
    ElMessage.warning(t('ops.fault.fillRequired'))
    return
  }

  loading.value = true
  try {
    if (editingRule.value) {
      await updateFaultRule(editingRule.value.id, ruleForm.value)
      ElMessage.success(t('ops.fault.updateSuccess'))
    } else {
      await createFaultRule(ruleForm.value)
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

async function handleManualFix(event: FaultEvent) {
  try {
    await ElMessageBox.confirm(
      t('ops.fault.fixConfirm'),
      t('common.confirm'),
      { type: 'info' }
    )
    await triggerManualFix(event.id)
    ElMessage.success(t('ops.fault.fixTriggered'))
    await load()
  } catch (error) {
    if (error !== 'cancel') {
      ElMessage.error(t('ops.fault.fixFailed'))
      console.error(error)
    }
  }
}

function severityType(severity: string) {
  const map: Record<string, 'danger' | 'warning' | 'info'> = {
    critical: 'danger',
    warning: 'warning',
    info: 'info',
  }
  return map[severity] || 'info'
}

function statusType(status: string) {
  const map: Record<string, 'danger' | 'warning' | 'success'> = {
    open: 'danger',
    resolving: 'warning',
    resolved: 'success',
  }
  return map[status] || 'info'
}

function formatDate(date: string) {
  return new Date(date).toLocaleString()
}

function formatDuration(minutes: number) {
  if (minutes < 60) return `${Math.round(minutes)}m`
  const hours = Math.floor(minutes / 60)
  const mins = Math.round(minutes % 60)
  return `${hours}h ${mins}m`
}

onMounted(load)
</script>

<template>
  <div class="fault-management-view">
    <div class="page-header">
      <h1>⚠️ {{ t('ops.fault.title') }}</h1>
      <el-button type="primary" @click="openCreateDialog">
        + {{ t('ops.fault.createRule') }}
      </el-button>
    </div>

    <!-- Stats Dashboard -->
    <div v-if="stats" class="stats-grid">
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value">{{ stats.total_events }}</div>
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
          <div class="stat-value stat-success">{{ stats.resolved_events }}</div>
          <div class="stat-label">{{ t('ops.fault.resolvedEvents') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover">
        <div class="stat-item">
          <div class="stat-value">{{ formatDuration(stats.avg_resolution_time_minutes) }}</div>
          <div class="stat-label">{{ t('ops.fault.avgResolutionTime') }}</div>
        </div>
      </el-card>
    </div>

    <!-- Filters -->
    <el-card class="filters-card" shadow="never">
      <el-space>
        <el-select v-model="filterStatus" :placeholder="t('common.status')" style="width: 150px">
          <el-option :label="t('common.all')" value="all" />
          <el-option :label="t('ops.fault.status.open')" value="open" />
          <el-option :label="t('ops.fault.status.resolving')" value="resolving" />
          <el-option :label="t('ops.fault.status.resolved')" value="resolved" />
        </el-select>
        <el-select v-model="filterSeverity" :placeholder="t('ops.fault.severity')" style="width: 150px">
          <el-option :label="t('common.all')" value="all" />
          <el-option :label="t('ops.fault.severity.critical')" value="critical" />
          <el-option :label="t('ops.fault.severity.warning')" value="warning" />
          <el-option :label="t('ops.fault.severity.info')" value="info" />
        </el-select>
      </el-space>
    </el-card>

    <!-- Events Table -->
    <el-card class="main-card" shadow="never">
      <template #header>
        <span>{{ t('ops.fault.events') }}</span>
      </template>
      <el-table v-loading="loading" :data="filteredEvents">
        <el-table-column prop="rule_name" :label="t('ops.fault.ruleName')" width="200" />
        <el-table-column prop="severity" :label="t('ops.fault.severity')" width="100">
          <template #default="{ row }">
            <el-tag :type="severityType(row.severity)" size="small">
              {{ t(`ops.fault.severity.${row.severity}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="status" :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">
              {{ t(`ops.fault.status.${row.status}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="message" :label="t('ops.fault.message')" min-width="200" />
        <el-table-column prop="detected_at" :label="t('ops.fault.detectedAt')" width="160">
          <template #default="{ row }">{{ formatDate(row.detected_at) }}</template>
        </el-table-column>
        <el-table-column prop="resolved_at" :label="t('ops.fault.resolvedAt')" width="160">
          <template #default="{ row }">
            {{ row.resolved_at ? formatDate(row.resolved_at) : '—' }}
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="120" fixed="right">
          <template #default="{ row }">
            <el-button
              v-if="row.status === 'open'"
              type="primary"
              size="small"
              @click="handleManualFix(row)"
            >
              {{ t('ops.fault.fix') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Rules Table -->
    <el-card class="rules-card" shadow="never">
      <template #header>
        <span>{{ t('ops.fault.rules') }}</span>
      </template>
      <el-table :data="rules" size="small">
        <el-table-column prop="name" :label="t('ops.fault.ruleName')" width="180" />
        <el-table-column prop="description" :label="t('common.description')" min-width="200" />
        <el-table-column prop="severity" :label="t('ops.fault.severity')" width="100">
          <template #default="{ row }">
            <el-tag :type="severityType(row.severity)" size="small">
              {{ t(`ops.fault.severity.${row.severity}`) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="enabled" :label="t('common.enabled')" width="80">
          <template #default="{ row }">
            <el-tag :type="row.enabled ? 'success' : 'info'" size="small">
              {{ row.enabled ? t('common.yes') : t('common.no') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="auto_fix" :label="t('ops.fault.autoFix')" width="100">
          <template #default="{ row }">
            <el-tag :type="row.auto_fix ? 'success' : 'info'" size="small">
              {{ row.auto_fix ? t('common.yes') : t('common.no') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="180" fixed="right">
          <template #default="{ row }">
            <el-button type="primary" size="small" @click="openEditDialog(row)">
              {{ t('common.edit') }}
            </el-button>
            <el-button type="danger" size="small" @click="handleDeleteRule(row)">
              {{ t('common.delete') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- Rule Dialog -->
    <el-dialog
      v-model="showRuleDialog"
      :title="editingRule ? t('ops.fault.editRule') : t('ops.fault.createRule')"
      width="600px"
    >
      <el-form :model="ruleForm" label-width="120px">
        <el-form-item :label="t('ops.fault.ruleName')" required>
          <el-input v-model="ruleForm.name" :placeholder="t('ops.fault.ruleNamePlaceholder')" />
        </el-form-item>
        <el-form-item :label="t('common.description')">
          <el-input
            v-model="ruleForm.description"
            type="textarea"
            :rows="2"
            :placeholder="t('ops.fault.descriptionPlaceholder')"
          />
        </el-form-item>
        <el-form-item :label="t('ops.fault.severity')" required>
          <el-radio-group v-model="ruleForm.severity">
            <el-radio label="critical">{{ t('ops.fault.severity.critical') }}</el-radio>
            <el-radio label="warning">{{ t('ops.fault.severity.warning') }}</el-radio>
            <el-radio label="info">{{ t('ops.fault.severity.info') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('ops.fault.condition')" required>
          <el-input
            v-model="ruleForm.condition"
            type="textarea"
            :rows="3"
            :placeholder="t('ops.fault.conditionPlaceholder')"
          />
        </el-form-item>
        <el-form-item :label="t('common.enabled')">
          <el-switch v-model="ruleForm.enabled" />
        </el-form-item>
        <el-form-item :label="t('ops.fault.autoFix')">
          <el-switch v-model="ruleForm.auto_fix" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showRuleDialog = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="loading" @click="handleSaveRule">
          {{ t('common.save') }}
        </el-button>
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

.stat-value.stat-danger {
  color: var(--el-color-danger);
}

.stat-value.stat-success {
  color: var(--el-color-success);
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
