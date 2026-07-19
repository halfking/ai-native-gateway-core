<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import {
  getOpsOverviewBundle,
  getLicenseHealth,
  getLicenseStatus,
  type CenterStats,
  type FaultEvent,
  type LicenseHealth,
  type LicenseStatus,
  type OfflineActivationRequest,
  type RegionStats,
  type UpgradeLog,
  type CenterInstance,
  type RuntimeMetricsSummary,
} from '../../api/ops'
import type { DownloadStats } from '../../api/public'

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
// License subsystem health (v2 Phase 3B-1) — separate from the
// license-management metrics: this is about daemon self-state
// (refresh daemon, grace period) not customer license catalogue.
const licenseHealth = ref<LicenseHealth | null>(null)
const licenseStatus = ref<LicenseStatus | null>(null)
const downloadStats = ref<DownloadStats | null>(null)
const regionStats = ref<RegionStats[]>([])
const deploymentNodes = ref<CenterInstance[]>([])
const dataPlaneTables = ref<Record<string, number>>({})
const runtimeMetrics = ref<RuntimeMetricsSummary[]>([])

const quickLinks = computed(() => [
  { path: '/ops/center', icon: '🖥️', label: t('ops.center.title') },
  { path: '/ops/blocklist', icon: '🚫', label: t('ops.blocklist.title') },
  { path: '/ops/licenses', icon: '🔑', label: t('ops.license.title') },
  { path: '/ops/autoupdate', icon: '🚀', label: t('ops.autoupdate.title') },
  { path: '/ops/faults', icon: '⚠️', label: t('ops.fault.title') },
  { path: '/download', icon: '📦', label: t('ops.overview.publicPortal') },
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
    const [bundle, licHealth, licStatus] = await Promise.all([
      getOpsOverviewBundle(),
      getLicenseHealth().catch(() => null),
      getLicenseStatus().catch(() => null),
    ])

    centerStats.value = bundle.center_stats ?? null
    licenseTotal.value = bundle.license_total ?? 0
    const offlineReqs = bundle.offline_requests ?? []
    pendingOffline.value = offlineReqs.filter((r) => r.status === 'pending').length
    openFaults.value = bundle.fault_stats?.open_events ?? 0
    pendingRequests.value = offlineReqs.filter((r) => r.status === 'pending').slice(0, 5)
    recentLogs.value = (bundle.recent_upgrades?.items || []).slice(0, 5)
    recentFaults.value = bundle.recent_faults?.events || []
    todayUpgrades.value = (bundle.recent_upgrades?.items || []).filter((log) =>
      isToday(log.completed_at || log.started_at)
    ).length
    licenseHealth.value = licHealth
    licenseStatus.value = licStatus
    downloadStats.value = bundle.download_stats ? {
      today_downloads: bundle.download_stats.today_downloads || 0,
      week_downloads: bundle.download_stats.week_downloads || 0,
      total_downloads: bundle.download_stats.total_downloads || 0,
      today_donations: 0,
      total_donations: 0,
      donation_amount_cents: 0,
      activation_rate_pct: 0,
      supporter_count: bundle.download_stats.supporter_count || 0
    } : null
    regionStats.value = bundle.region_stats ?? []
    deploymentNodes.value = bundle.deployment_nodes ?? []
    dataPlaneTables.value = bundle.data_plane_tables ?? {}
    runtimeMetrics.value = bundle.runtime_metrics_summary ?? []
  } catch (error) {
    ElMessage.error(t('ops.overview.loadFailed'))
    console.error(error)
  } finally {
    loading.value = false
  }
}

// License-subsection summary colour: green if healthy & no grace, amber
// if in-grace, red if restricted / unhealthy.
const licenseStatusTone = computed(() => {
  if (!licenseStatus.value) return 'info'
  if (licenseStatus.value.mode === 'restricted') return 'danger'
  if (licenseStatus.value.mode === 'in_grace') return 'warning'
  if (licenseHealth.value && !licenseHealth.value.healthy) return 'warning'
  return 'success'
})

const licenseStatusLabel = computed(() => {
  if (!licenseStatus.value) return '—'
  const s = licenseStatus.value
  if (s.mode === 'restricted') return t('ops.overview.licenseModeRestricted')
  if (s.mode === 'in_grace') {
    const hours = Math.max(0, Math.round(s.grace_remaining_seconds / 3600))
    return t('ops.overview.licenseModeGrace', { hours })
  }
  return t('ops.overview.licenseModeNormal')
})

function formatRel(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  const diff = Date.now() - d.getTime()
  if (diff < 0) return t('ops.overview.justNow')
  const mins = Math.round(diff / 60000)
  if (mins < 1) return t('ops.overview.justNow')
  if (mins < 60) return t('ops.overview.minutesAgo', { n: mins })
  const hours = Math.round(mins / 60)
  if (hours < 24) return t('ops.overview.hoursAgo', { n: hours })
  const days = Math.round(hours / 24)
  return t('ops.overview.daysAgo', { n: days })
}

function goTo(path: string) {
  router.push(path)
}

function nodeStatusType(status: string) {
  const map: Record<string, 'success' | 'warning' | 'danger' | 'info'> = {
    online: 'success',
    degraded: 'warning',
    offline: 'danger',
  }
  return map[status] || 'info'
}

function regionStatusType(row: RegionStats) {
  if (row.missing) return 'info'
  if (row.online_instances > 0) return 'success'
  if (row.degraded_instances > 0) return 'warning'
  return 'danger'
}

function regionStatusLabel(row: RegionStats) {
  if (row.missing) return t('ops.overview.regionMissing')
  if (row.online_instances > 0) return t('ops.overview.regionOnline')
  if (row.degraded_instances > 0) return t('ops.overview.regionDegraded')
  return t('ops.overview.regionOffline')
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
      <el-card shadow="hover" class="stat-card" @click="goTo('/ops/licenses')">
        <div class="stat-item">
          <div class="stat-value" :class="`stat-${licenseStatusTone}`">{{ licenseStatusLabel }}</div>
          <div class="stat-label">{{ t('ops.overview.licenseSubsystem') }}</div>
        </div>
      </el-card>
      <el-card shadow="hover" class="stat-card" @click="goTo('/ops/faults')">
        <div class="stat-item">
          <div class="stat-value stat-danger">{{ openFaults }}</div>
          <div class="stat-label">{{ t('ops.overview.openFaults') }}</div>
        </div>
      </el-card>
      <el-card v-if="downloadStats" shadow="hover" class="stat-card" @click="goTo('/download')">
        <div class="stat-item">
          <div class="stat-value stat-info">{{ downloadStats.today_downloads }}</div>
          <div class="stat-label">{{ t('ops.overview.todayDownloads') }}</div>
        </div>
      </el-card>
      <el-card v-if="downloadStats" shadow="hover" class="stat-card" @click="goTo('/download')">
        <div class="stat-item">
          <div class="stat-value stat-primary">{{ downloadStats.supporter_count }}</div>
          <div class="stat-label">{{ t('ops.overview.supporterCount') }}</div>
        </div>
      </el-card>
    </div>

    <el-card shadow="never" class="deployment-nodes-card">
      <template #header>
        <div class="panel-header">
          <span>{{ t('ops.overview.deploymentNodes') }}</span>
          <el-button link type="primary" @click="goTo('/ops/center')">
            {{ t('ops.overview.viewAll') }}
          </el-button>
        </div>
      </template>
      <div class="region-grid">
        <div
          v-for="row in regionStats"
          :key="row.region"
          class="region-chip"
          :class="{ 'region-chip-missing': row.missing }"
        >
          <div class="region-name">{{ row.region }}</div>
          <el-tag size="small" :type="regionStatusType(row)">{{ regionStatusLabel(row) }}</el-tag>
          <div class="region-meta">
            {{ t('ops.overview.regionOnlineCount', { n: row.online_instances }) }}
            <span v-if="row.last_heartbeat"> · {{ formatRel(row.last_heartbeat) }}</span>
          </div>
        </div>
      </div>
      <el-table :data="deploymentNodes" size="small" empty-text="—" style="margin-top: 12px">
        <el-table-column prop="region" :label="t('ops.center.region')" width="90" />
        <el-table-column prop="hostname" :label="t('ops.license.hostname')" width="180" show-overflow-tooltip />
        <el-table-column prop="version" :label="t('ops.autoupdate.version')" width="120" show-overflow-tooltip />
        <el-table-column prop="status" :label="t('common.table.status')" width="100">
          <template #default="{ row = {} } = {}">
            <el-tag size="small" :type="nodeStatusType(row.status)">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="last_heartbeat" :label="t('ops.center.lastHeartbeat')">
          <template #default="{ row = {} } = {}">{{ formatDate(row.last_heartbeat) }}</template>
        </el-table-column>
      </el-table>
      <div v-if="Object.keys(dataPlaneTables).length" class="data-plane-tables">
        <span class="data-plane-label">{{ t('ops.overview.dataPlaneTables') }}:</span>
        <el-tag
          v-for="(count, table) in dataPlaneTables"
          :key="table"
          size="small"
          :type="count > 0 ? 'success' : 'info'"
          class="table-tag"
        >
          {{ table }}={{ count < 0 ? '?' : count }}
        </el-tag>
      </div>
    </el-card>

    <!-- v2 Phase 3B-1: License subsystem detail card. Shows the
         daemon's most recent refresh outcome + consecutive-failure
         count. Hidden until the API is reachable so older deployments
         don't show a "never" string. -->
    <div v-if="licenseHealth" class="license-health-card">
      <el-card shadow="hover">
        <div class="health-row">
          <div class="health-label">{{ t('ops.overview.lastRefresh') }}</div>
          <div class="health-value">
            <span :class="`health-pill health-${licenseStatusTone}`">{{ formatRel(licenseHealth.last_cycle_at) }}</span>
          </div>
        </div>
        <div class="health-row">
          <div class="health-label">{{ t('ops.overview.consecutiveFailures') }}</div>
          <div class="health-value">
            <span class="health-num">{{ licenseHealth.consecutive_fails }}</span>
            <span class="health-meta">{{ t('ops.overview.totalCycles', { n: licenseHealth.total_cycles }) }}</span>
          </div>
        </div>
        <div v-if="licenseHealth.last_error" class="health-row error-row">
          <div class="health-label">{{ t('ops.overview.lastError') }}</div>
          <div class="health-value error-text">{{ licenseHealth.last_error }}</div>
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
          <el-table-column prop="status" :label="t('common.table.status')" width="100">
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

      <!-- Runtime Metrics Summary Card -->
      <el-card v-if="runtimeMetrics.length > 0" shadow="never" class="full-width runtime-metrics-card">
        <template #header>
          <div class="panel-header">
            <span>实例性能（最近 24 小时）</span>
          </div>
        </template>
        <el-table :data="runtimeMetrics" size="small">
          <el-table-column prop="hostname" label="实例" width="150" show-overflow-tooltip />
          <el-table-column prop="region" label="区域" width="80" />
          <el-table-column label="CPU">
            <template #default="{ row }">
              <el-progress 
                :percentage="Math.min(100, Math.round(row.avg_cpu_pct))" 
                :color="row.avg_cpu_pct > 80 ? '#f56c6c' : row.avg_cpu_pct > 60 ? '#e6a23c' : '#67c23a'"
                :format="() => `${row.avg_cpu_pct.toFixed(1)}%`"
              />
            </template>
          </el-table-column>
          <el-table-column label="内存">
            <template #default="{ row }">
              <el-progress 
                :percentage="Math.min(100, Math.round(row.avg_mem_pct))"
                :color="row.avg_mem_pct > 90 ? '#f56c6c' : row.avg_mem_pct > 70 ? '#e6a23c' : '#67c23a'"
                :format="() => `${row.avg_mem_pct.toFixed(1)}%`"
              />
            </template>
          </el-table-column>
          <el-table-column prop="avg_tps" label="TPS" width="80">
            <template #default="{ row }">{{ row.avg_tps.toFixed(2) }}</template>
          </el-table-column>
          <el-table-column label="P99 延迟" width="100">
            <template #default="{ row }">
              <span :class="{ 'slow-p99': row.max_p99_ms > 30000 }">
                {{ (row.max_p99_ms / 1000).toFixed(1) }}s
              </span>
            </template>
          </el-table-column>
          <el-table-column prop="version" label="版本" width="100" />
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag :type="row.status === 'online' ? 'success' : 'danger'" size="small">
                {{ row.status }}
              </el-tag>
            </template>
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

/* v2 Phase 3B-1: License subsystem detail card */
.license-health-card {
  margin-bottom: 20px;
}
.health-row {
  display: flex;
  align-items: baseline;
  gap: 12px;
  padding: 6px 0;
}
.health-label {
  width: 160px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
  flex-shrink: 0;
}
.health-value {
  flex: 1;
}
.health-num {
  font-weight: 700;
  font-size: 16px;
}
.health-meta {
  margin-left: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.health-pill {
  padding: 2px 10px;
  border-radius: 4px;
  font-weight: 600;
  font-size: 13px;
}
.health-success { background: var(--el-color-success-light-7); color: var(--el-color-success); }
.health-warning { background: var(--el-color-warning-light-7); color: var(--el-color-warning); }
.health-danger  { background: var(--el-color-danger-light-7);  color: var(--el-color-danger); }
.health-info    { color: var(--el-text-color-secondary); }
.error-row { border-top: 1px dashed var(--el-color-danger-light-7); padding-top: 8px; margin-top: 6px; }
.error-text {
  font-family: monospace;
  font-size: 12px;
  color: var(--el-color-danger);
  word-break: break-all;
}

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

.deployment-nodes-card {
  margin-bottom: 20px;
}

.region-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 12px;
}

.region-chip {
  border: 1px solid var(--el-border-color-light);
  border-radius: 8px;
  padding: 10px 12px;
}

.region-chip-missing {
  opacity: 0.75;
  border-style: dashed;
}

.region-name {
  font-weight: 700;
  margin-bottom: 6px;
}

.region-meta {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.data-plane-tables {
  margin-top: 12px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.data-plane-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.table-tag {
  font-family: monospace;
}
</style>

.runtime-metrics-card {
  margin-top: 20px;
}

.slow-p99 {
  color: #f56c6c;
  font-weight: bold;
}
