<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import DrilldownPieChart from '../analytics/DrilldownPieChart.vue'
import { fetchBoardErrorDrill, type BoardPayload, type BoardPieItem } from '../../api/board'

const props = defineProps<{
  board: BoardPayload | null | undefined
  days: number
  loading?: boolean
}>()

const { t } = useI18n()
const pieMetric = ref<'requests' | 'tokens'>('requests')
const errorDrillKind = ref<string | null>(null)
const errorDrillDim = ref<'model' | 'provider' | 'client'>('model')
const errorDrillItems = ref<BoardPieItem[]>([])
const errorDrillLoading = ref(false)

async function onErrorClick(kind: string) {
  errorDrillKind.value = kind
  errorDrillLoading.value = true
  try {
    const res = await fetchBoardErrorDrill({
      error_kind: kind,
      days: props.days,
      dimension: errorDrillDim.value,
    })
    errorDrillItems.value = res.items
  } catch {
    errorDrillItems.value = []
  } finally {
    errorDrillLoading.value = false
  }
}

async function onDrillDimChange(dim: 'model' | 'provider' | 'client') {
  errorDrillDim.value = dim
  if (errorDrillKind.value) await onErrorClick(errorDrillKind.value)
}

const metricOptions = computed(() => [
  { key: 'requests' as const, label: t('dashboard.board.metricRequests') },
  { key: 'tokens' as const, label: t('dashboard.board.metricTokens') },
])
</script>

<template>
  <div class="pie-grid">
    <DrilldownPieChart
      :title="t('dashboard.board.pieClients')"
      :data="board?.pies?.clients ?? []"
      :metric="pieMetric"
      :loading="loading"
    >
      <template #metric-toggle>
        <div class="metric-radio" role="radiogroup" :aria-label="t('dashboard.board.metricToggle')">
          <label
            v-for="m in metricOptions"
            :key="m.key"
            class="metric-radio__item"
            :class="{ active: pieMetric === m.key }"
          >
            <input v-model="pieMetric" type="radio" name="board-pie-metric" :value="m.key" />
            <span>{{ m.label }}</span>
          </label>
        </div>
      </template>
    </DrilldownPieChart>

    <DrilldownPieChart
      :title="t('dashboard.board.pieVirtualIp')"
      :data="board?.pies?.virtual_ips ?? []"
      :metric="pieMetric"
      :loading="loading"
    />
    <DrilldownPieChart
      :title="t('dashboard.board.pieIdentity')"
      :data="board?.pies?.identity_hashes ?? []"
      :metric="pieMetric"
      :loading="loading"
      truncate-keys
    />
    <DrilldownPieChart
      :title="t('dashboard.board.pieModels')"
      :data="board?.pies?.models ?? []"
      :metric="pieMetric"
      :loading="loading"
    />
    <DrilldownPieChart
      :title="t('dashboard.board.pieErrors')"
      :data="board?.pies?.errors ?? []"
      metric="requests"
      :loading="loading"
      @slice-click="onErrorClick"
    />
    <DrilldownPieChart
      :title="t('dashboard.board.pieTenants')"
      :data="board?.pies?.tenants ?? []"
      :metric="pieMetric"
      :loading="loading"
    />
    <DrilldownPieChart
      :title="t('dashboard.board.pieProviders')"
      :data="board?.pies?.providers ?? []"
      :metric="pieMetric"
      :loading="loading"
    />

    <div v-if="errorDrillKind" class="drill-panel">
      <div class="drill-panel__header">
        <span>{{ t('dashboard.board.errorDrill', { kind: errorDrillKind }) }}</span>
        <div class="drill-tabs">
          <button type="button" :class="{ active: errorDrillDim === 'model' }" @click="onDrillDimChange('model')">{{ t('dashboard.board.drillModel') }}</button>
          <button type="button" :class="{ active: errorDrillDim === 'provider' }" @click="onDrillDimChange('provider')">{{ t('dashboard.board.drillProvider') }}</button>
          <button type="button" :class="{ active: errorDrillDim === 'client' }" @click="onDrillDimChange('client')">{{ t('dashboard.board.drillClient') }}</button>
          <button type="button" class="drill-close" @click="errorDrillKind = null">×</button>
        </div>
      </div>
      <DrilldownPieChart
        :title="''"
        :data="errorDrillItems"
        metric="requests"
        :loading="errorDrillLoading"
      />
    </div>
  </div>
</template>

<style scoped>
.pie-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.metric-radio {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  margin-left: auto;
  padding: 2px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--bg-subtle, var(--bg));
}
.metric-radio__item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 4px;
  cursor: pointer;
  color: var(--text-muted);
  user-select: none;
}
.metric-radio__item input {
  position: absolute;
  opacity: 0;
  width: 0;
  height: 0;
  margin: 0;
}
.metric-radio__item.active {
  background: color-mix(in srgb, var(--accent) 14%, transparent);
  color: var(--accent);
  font-weight: 600;
}
.drill-panel {
  grid-column: 1 / -1;
  border: 1px dashed var(--accent);
  border-radius: 8px;
  padding: 8px;
}
.drill-panel__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 13px;
  font-weight: 600;
}
.drill-tabs button {
  font-size: 12px;
  margin-right: 6px;
  padding: 4px 8px;
  border-radius: 4px;
  border: 1px solid var(--border);
  background: transparent;
  color: var(--text);
  cursor: pointer;
}
.drill-tabs button.active {
  border-color: var(--accent);
  color: var(--accent);
}
.drill-close {
  margin-left: 8px;
}
</style>
