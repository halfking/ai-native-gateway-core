<script setup lang="ts">
defineProps<{
  online: number | string
  licenses: number
  pending: number
  openFaults: number
  todayUpgrades: number
  todayDownloads?: number
  supporters?: number
  licenseLabel: string
  licenseTone: string
}>()

const emit = defineEmits<{
  (e: 'navigate', path: string): void
}>()
</script>

<template>
  <div class="kpi-strip">
    <button type="button" class="kpi-card" @click="emit('navigate', '/ops/center')">
      <div class="kpi-value success">{{ online }}</div>
      <div class="kpi-label">{{ $t('ops.overview.onlineInstances') }}</div>
    </button>
    <button type="button" class="kpi-card" @click="emit('navigate', '/ops/licenses')">
      <div class="kpi-value primary">{{ licenses }}</div>
      <div class="kpi-label">{{ $t('ops.overview.totalLicenses') }}</div>
    </button>
    <button type="button" class="kpi-card" @click="emit('navigate', '/ops/licenses')">
      <div class="kpi-value" :class="pending > 0 ? 'warning' : 'muted'">{{ pending }}</div>
      <div class="kpi-label">{{ $t('ops.overview.pendingApprovals') }}</div>
    </button>
    <button type="button" class="kpi-card" @click="emit('navigate', '/ops/faults')">
      <div class="kpi-value" :class="openFaults > 0 ? 'danger' : 'muted'">{{ openFaults }}</div>
      <div class="kpi-label">{{ $t('ops.overview.openFaults') }}</div>
    </button>
  </div>

  <div class="kpi-pills">
    <button type="button" class="pill" @click="emit('navigate', '/ops/autoupdate')">
      <span class="pill-k">{{ $t('ops.overview.todayUpgrades') }}</span>
      <span class="pill-v">{{ todayUpgrades }}</span>
    </button>
    <button v-if="todayDownloads != null" type="button" class="pill" @click="emit('navigate', '/download')">
      <span class="pill-k">{{ $t('ops.overview.todayDownloads') }}</span>
      <span class="pill-v">{{ todayDownloads }}</span>
    </button>
    <button v-if="supporters != null" type="button" class="pill" @click="emit('navigate', '/download')">
      <span class="pill-k">{{ $t('ops.overview.supporterCount') }}</span>
      <span class="pill-v">{{ supporters }}</span>
    </button>
    <button type="button" class="pill" @click="emit('navigate', '/ops/licenses')">
      <span class="pill-k">{{ $t('ops.overview.licenseSubsystem') }}</span>
      <span class="pill-v" :class="`tone-${licenseTone}`">{{ licenseLabel }}</span>
    </button>
  </div>
</template>

<style scoped>
.kpi-strip {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 12px;
}

.kpi-card {
  appearance: none;
  border: 1px solid var(--el-border-color-lighter);
  background: var(--el-bg-color-overlay);
  border-radius: 12px;
  padding: 16px 12px;
  text-align: center;
  cursor: pointer;
  transition: border-color 0.15s ease, transform 0.15s ease;
}

.kpi-card:hover {
  border-color: var(--el-color-primary-light-5);
  transform: translateY(-1px);
}

.kpi-value {
  font-size: 28px;
  font-weight: 700;
  line-height: 1.15;
  font-variant-numeric: tabular-nums;
}

.kpi-label {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}

.success { color: var(--el-color-success); }
.primary { color: var(--el-color-primary); }
.warning { color: var(--el-color-warning); }
.danger { color: var(--el-color-danger); }
.muted { color: var(--el-text-color-regular); }

.kpi-pills {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 20px;
}

.pill {
  appearance: none;
  border: 1px solid var(--el-border-color-lighter);
  background: transparent;
  border-radius: 999px;
  padding: 6px 12px;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  font-size: 12px;
}

.pill:hover {
  border-color: var(--el-color-primary-light-5);
}

.pill-k {
  color: var(--el-text-color-secondary);
}

.pill-v {
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.tone-success { color: var(--el-color-success); }
.tone-warning { color: var(--el-color-warning); }
.tone-danger { color: var(--el-color-danger); }
.tone-info { color: var(--el-text-color-secondary); }

@media (max-width: 900px) {
  .kpi-strip {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
</style>
