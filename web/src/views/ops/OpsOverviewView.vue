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
  type LicenseHealth,
  type LicenseStatus,
  type RegionStats,
  type CenterInstance,
} from '../../api/ops'
import type { DownloadStats } from '../../api/public'
import OverviewKpiStrip from './components/OverviewKpiStrip.vue'
import RegionTopology from './components/RegionTopology.vue'
import InstanceDetailDrawer from './components/InstanceDetailDrawer.vue'

const { t } = useI18n()
const router = useRouter()

const loading = ref(false)
const centerStats = ref<CenterStats | null>(null)
const licenseTotal = ref(0)
const pendingOffline = ref(0)
const openFaults = ref(0)
const todayUpgrades = ref(0)
const licenseHealth = ref<LicenseHealth | null>(null)
const licenseStatus = ref<LicenseStatus | null>(null)
const downloadStats = ref<DownloadStats | null>(null)
const regionStats = ref<RegionStats[]>([])
const deploymentNodes = ref<CenterInstance[]>([])

const drawerOpen = ref(false)
const selectedInstanceId = ref<string | null>(null)

const quickLinks = computed(() => [
  { path: '/ops/center', label: t('ops.center.title') },
  { path: '/ops/licenses', label: t('ops.license.title') },
  { path: '/ops/faults', label: t('ops.fault.title') },
  { path: '/ops/autoupdate', label: t('ops.autoupdate.title') },
  { path: '/ops/blocklist', label: t('ops.blocklist.title') },
  { path: '/download', label: t('ops.overview.publicPortal') },
])

function isToday(dateStr: string) {
  if (!dateStr) return false
  const d = new Date(dateStr)
  return d.toDateString() === new Date().toDateString()
}

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
    todayUpgrades.value = (bundle.recent_upgrades?.items || []).filter((log) =>
      isToday(log.completed_at || log.started_at),
    ).length
    licenseHealth.value = licHealth
    licenseStatus.value = licStatus
    downloadStats.value = bundle.download_stats
      ? {
          today_downloads: bundle.download_stats.today_downloads || 0,
          week_downloads: bundle.download_stats.week_downloads || 0,
          total_downloads: bundle.download_stats.total_downloads || 0,
          today_donations: 0,
          total_donations: 0,
          donation_amount_cents: 0,
          activation_rate_pct: 0,
          supporter_count: bundle.download_stats.supporter_count || 0,
        }
      : null
    regionStats.value = bundle.region_stats ?? []
    deploymentNodes.value = bundle.deployment_nodes ?? []
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

function openNode(node: CenterInstance) {
  selectedInstanceId.value = node.instance_id
  drawerOpen.value = true
}

onMounted(load)
</script>

<template>
  <div class="ops-overview-view">
    <div class="page-header">
      <div>
        <h1>{{ t('ops.overview.title') }}</h1>
        <p class="page-sub">{{ t('ops.overview.subtitle') }}</p>
      </div>
      <el-button type="primary" :loading="loading" @click="load">
        {{ t('common.refresh') }}
      </el-button>
    </div>

    <div v-loading="loading">
      <OverviewKpiStrip
        :online="centerStats?.online_instances ?? '—'"
        :licenses="licenseTotal"
        :pending="pendingOffline"
        :open-faults="openFaults"
        :today-upgrades="todayUpgrades"
        :today-downloads="downloadStats?.today_downloads"
        :supporters="downloadStats?.supporter_count"
        :license-label="licenseStatusLabel"
        :license-tone="licenseStatusTone"
        @navigate="goTo"
      />

      <RegionTopology
        :region-stats="regionStats"
        :nodes="deploymentNodes"
        @select-node="openNode"
      >
        <template #actions>
          <el-button link type="primary" @click="goTo('/ops/center')">
            {{ t('ops.overview.viewAll') }}
          </el-button>
        </template>
      </RegionTopology>

      <nav class="quick-nav">
        <button
          v-for="link in quickLinks"
          :key="link.path"
          type="button"
          class="quick-link"
          @click="goTo(link.path)"
        >
          {{ link.label }}
        </button>
      </nav>
    </div>

    <InstanceDetailDrawer
      v-model="drawerOpen"
      :instance-id="selectedInstanceId"
    />
  </div>
</template>

<style scoped>
.ops-overview-view {
  padding: 20px;
  max-width: 1280px;
}

.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}

.page-header h1 {
  margin: 0;
  font-size: 22px;
}

.page-sub {
  margin: 6px 0 0;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}

.quick-nav {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding-top: 4px;
}

.quick-link {
  appearance: none;
  border: 1px solid var(--el-border-color-lighter);
  background: transparent;
  border-radius: 8px;
  padding: 8px 12px;
  font-size: 13px;
  cursor: pointer;
  color: var(--el-text-color-regular);
}

.quick-link:hover {
  border-color: var(--el-color-primary-light-5);
  color: var(--el-color-primary);
}
</style>
