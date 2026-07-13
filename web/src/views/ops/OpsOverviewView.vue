<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  getCenterStats,
  getLicenses,
  getOfflineActivationRequests,
  getUpgradeLogs,
  getFaultStats,
  getFaultEvents,
  type CenterStats,
  type FaultEvent,
  type OfflineActivationRequest,
  type UpgradeLog,
} from '../../api/ops'

const { t } = useI18n()
const router = useRouter()

const loading = ref(false)
const centerStats = ref<CenterStats | null>(null)
const licenseTotal = ref(0)
const pendingOffline = ref(0)
const openFaults = ref(0)
const todayUpgrades = ref(0)
const recentLogs = ref<UpgradeLog[]>([])
const recentFaults = ref<FaultEvent[]>([])
const pendingRequests = ref<OfflineActivationRequest[]>([])

const quickLinks = computed(() => [
  { path: '/ops/center', icon: '🖥️', label: t('ops.center.title') },
  { path: '/ops/licenses', icon: '🔑', label: t('ops.license.title') },
  { path: '/ops/autoupdate', icon: '🚀', label: t('ops.autoupdate.title') },
  { path: '/ops/faults', icon: '⚠️', label: t('ops.fault.title') },
])

function isToday(dateStr: string) {
  if (!dateStr) return false
  const d = new Date(dateStr)
  const now = new Date()
  return d.toDateString() === now.toDateString()
}

function formatDate(date: string) {
  if (!date) return '-'
  return new Date(date).toLocaleString()
}

function faultSeverityType(severity: string) {
  const map: Record<string, 'danger' | 'warning' | 'info'> = {
    critical: 'danger',
    error: 'danger',
    warning: 'warning',
    info: 'info',
  }
  return map[severity] || 'info'
}

async function load() {
  loading.value = true
  try {
    const [
      center,
      licenses,
      offlineReqs,
      upgradeRes,
      faultStats,
      faultEvents,
    ] = await Promise.all([
      getCenterStats(),
      getLicenses({ limit: 1 }),
      getOfflineActivationRequests(),
      getUpgradeLogs({ limit: 20 }),
      getFaultStats(),
      getFaultEvents({ status: 'new', limit: 5 }),
    ])

    centerStats.value = center
    licenseTotal.value = licenses.total
    pendingOffline.value = offlineReqs.filter((r) => r.status === 'pending').length
    openFaults.value = faultStats.open_events
    pendingRequests.value = offlineReqs.filter((r) => r.status === 'pending').slice(0, 5)
    recentLogs.value = (upgradeRes.items || []).slice(0, 5)
    recentFaults.value = faultEvents.events || []
    todayUpgrades.value = (upgradeRes.items || []).filter((log) =>
      isToday(log.completed_at || log.started_at)
    ).length
  } catch (error) {
    ElMessage.error(t('ops.overview.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

function goTo(path: string) {
  router.push(path)
}

onMounted(load)
</script>

<template>
  <div class="ops-overview-view">
    <div class="page-header">
      <h1>{{ t('ops.overview.title') }}</h1>
      <el-button type="primary" :loading="loading" @click="load">
        {{ t('common.refresh') }}
      </el-button>
    </div>

    <div v-loading="loading" class="stats-grid">
      <el-card shadow="hover" class="stat-card" @click="goTo('/ops/center')">
        <div class="stat-item">
          <div class="stat-value stat-success">{{ centerStats?.online_instances ?? '—' }}</div>
          <div class="stat-label">{{ t('ops.overview.onlineInstances') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover" class="stat-card" @click="goTo('/ops/licenses')">
        <div class="stat-item">
          <div class="stat-value stat-primary">{{ licenseTotal }}</div>
          <div class="stat-label">{{ t('ops.overview.totalLicenses') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover" class="stat-card" @click="goTo('/ops/licenses')">
        <div class="stat-item">
          <div class="stat-value stat-warning">{{ pendingOffline }}</div>
          <div class="stat-label">{{ t('ops.overview.pendingApprovals') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover" class="stat-card" @click="goTo('/ops/autoupdate')">
        <div class="stat-item">
          <div class="stat-value stat-info">{{ todayUpgrades }}</div>
          <div class="stat-label">{{ t('ops.overview.todayUpgrades') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover" class="stat-card" @click="goTo('/ops/faults')">
        <div class="stat-item">
          <div class="stat-value stat-danger">{{ openFaults }}</div>
          <div class="stat-label">{{ t('ops.overview.openFaults') }}</div>
        </div>
      </el-card>
    </div>

    <div class="quick-links">
      <el-card
        v-for="link in quickLinks"
        :key="link.path"
        shadow="hover"
        class="quick-link-card"
        @click="goTo(link.path)"
      >
        <span class="quick-icon">{{ link.icon }}</span>
        <span class="quick-label">{{ link.label }}</span>
      </el-card>
    </div>

    <div class="panels-grid">
      <el-card shadow="never">
        <template #header>
          <div class="panel-header">
            <span>{{ t('ops.overview.recentUpgrades') }}</span>
            <el-button link type="primary" @click="goTo('/ops/autoupdate')">
              {{ t('ops.overview.viewAll') }}
            </el-button>
          </div>
        </template>
        <el-table :data="recentLogs" size="small" empty-text="—">
          <el-table-column prop="instance_id" :label="t('ops.center.instanceId')" width="160" show-overflow-tooltip />
          <el-table-column prop="version" :label="t('ops.autoupdate.version')" width="100" />
          <el-table-column prop="status" :label="t('common.status')" width="100">
            <template #default="{ row = {} } = {}">
              <el-tag size="small">{{ row.status }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="started_at" :label="t('ops.autoupdate.startedAt')">
            <template #default="{ row = {} } = {}">{{ formatDate(row.started_at) }}</template>
          </el-table-column>
        </el-table>
      </el-card>

      <el-card shadow="never">
        <template #header>
          <div class="panel-header">
            <span>{{ t('ops.overview.recentFaults') }}</span>
            <el-button link type="primary" @click="goTo('/ops/faults')">
              {{ t('ops.overview.viewAll') }}
            </el-button>
          </div>
        </template>
        <el-table :data="recentFaults" size="small" empty-text="—">
          <el-table-column prop="severity" :label="t('ops.fault.severityLabel')" width="90">
            <template #default="{ row = {} } = {}">
              <el-tag :type="faultSeverityType(row.severity)" size="small">{{ row.severity }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="title" :label="t('ops.fault.titleLabel')" show-overflow-tooltip />
          <el-table-column prop="detected_at" :label="t('ops.fault.detectedAt')" width="160">
            <template #default="{ row = {} } = {}">{{ formatDate(row.detected_at) }}</template>
          </el-table-column>
        </el-table>
      </el-card>

      <el-card v-if="pendingRequests.length > 0" shadow="never" class="full-width">
        <template #header>
          <div class="panel-header">
            <span>{{ t('ops.overview.pendingOffline') }}</span>
            <el-button link type="primary" @click="goTo('/ops/licenses')">
              {{ t('ops.overview.viewAll') }}
            </el-button>
          </div>
        </template>
        <el-table :data="pendingRequests" size="small">
          <el-table-column prop="license_key" :label="t('ops.license.licenseKey')" width="180" show-overflow-tooltip />
          <el-table-column prop="instance_id" :label="t('ops.license.deviceId')" width="140" />
          <el-table-column prop="request_id" :label="t('ops.license.requestCode')" show-overflow-tooltip />
          <el-table-column prop="timestamp" :label="t('common.createdAt')" width="160">
            <template #default="{ row = {} } = {}">{{ formatDate(row.timestamp) }}</template>
          </el-table-column>
        </el-table>
      </el-card>
    </div>
  </div>
</template>

<style scoped>
.ops-overview-view {
  padding: 20px;
}

.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 20px;
}

.page-header h1 {
  margin: 0;
  font-size: 22px;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
  gap: 16px;
  margin-bottom: 20px;
}

.stat-card {
  cursor: pointer;
}

.stat-item {
  text-align: center;
  padding: 8px 0;
}

.stat-value {
  font-size: 28px;
  font-weight: 700;
  line-height: 1.2;
}

.stat-label {
  margin-top: 6px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.stat-success { color: var(--el-color-success); }
.stat-primary { color: var(--el-color-primary); }
.stat-warning { color: var(--el-color-warning); }
.stat-danger { color: var(--el-color-danger); }
.stat-info { color: var(--el-color-info); }

.quick-links {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}

.quick-link-card {
  cursor: pointer;
  text-align: center;
  padding: 12px 0;
}

.quick-icon {
  display: block;
  font-size: 24px;
  margin-bottom: 6px;
}

.quick-label {
  font-size: 13px;
}

.panels-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
  gap: 16px;
}

.panel-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.full-width {
  grid-column: 1 / -1;
}
</style>
