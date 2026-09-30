<script setup lang="ts">
// ReconciliationToolbar.vue — view, range chips, provider/model filters, actions.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Download, Filter, Refresh } from '@element-plus/icons-vue'
import { ElBadge, ElButton, ElOption, ElRadioButton, ElRadioGroup, ElSelect } from 'element-plus'
import type { DimensionOptions, ReportFilter, ReportView } from '../../api/reportrollup'
import KxDateRangePicker from '../ui/KxDateRangePicker.vue'
import type { KxDateRange } from '../ui/kx-date-types'
import { fmtInt, type QuickRange } from './format'

const props = defineProps<{
  view: ReportView
  range: KxDateRange
  filters: ReportFilter
  dims: DimensionOptions | null
  dimsLoading: boolean
  loading: boolean
  exporting: boolean
  rerunning: boolean
  coverage: string
  activeQuick: QuickRange | ''
}>()

const emit = defineEmits<{
  'update:view': [ReportView]
  applyRange: [KxDateRange]
  quick: [QuickRange]
  patch: [Partial<ReportFilter>]
  refresh: []
  export: []
  rerun: []
  clear: []
}>()

const { t } = useI18n()
const moreOpen = ref(false)

const extraCount = computed(() => {
  const f = props.filters
  return [f.credential_id, f.api_key_id, f.tenant_id, f.person].filter((v) => v != null && v !== '').length
})

function labelOf(key: string, name: string | undefined, requests: number): string {
  const base = name ? `${name} (${key})` : key
  return `${base} · ${fmtInt(requests)}`
}

function onView(value: string | number | boolean | undefined) {
  if (value === 'provider' || value === 'internal') emit('update:view', value)
}

const quicks = computed(() => [
  { id: 'yesterday' as const, label: t('reports.quickYesterday') },
  { id: '7d' as const, label: t('reports.quick7d') },
  { id: '30d' as const, label: t('reports.quick30d') },
  { id: 'month' as const, label: t('reports.quickMonth') },
])
</script>

<template>
  <div class="toolbar">
    <el-radio-group :model-value="view" @update:model-value="onView">
      <el-radio-button value="provider">{{ t('reports.providerView') }}</el-radio-button>
      <el-radio-button value="internal">{{ t('reports.internalView') }}</el-radio-button>
    </el-radio-group>
    <KxDateRangePicker :model-value="range" :presets="[]" @apply="emit('applyRange', $event)" />
    <div class="chips">
      <button
        v-for="chip in quicks"
        :key="chip.id"
        type="button"
        class="chip"
        :class="{ on: activeQuick === chip.id }"
        :data-quick="chip.id"
        @click="emit('quick', chip.id)"
      >{{ chip.label }}</button>
    </div>
    <el-select
      :model-value="filters.provider_id"
      clearable
      filterable
      data-testid="filter-provider"
      :loading="dimsLoading"
      :placeholder="t('reports.allProviders')"
      style="width: 200px"
      @update:model-value="emit('patch', { provider_id: $event ?? undefined })"
    >
      <el-option
        v-for="item in dims?.providers ?? []"
        :key="item.key"
        :value="Number(item.key)"
        :label="labelOf(item.key, item.name, item.requests)"
      />
    </el-select>
    <el-select
      :model-value="filters.model"
      clearable
      filterable
      data-testid="filter-model"
      :loading="dimsLoading"
      :placeholder="t('reports.allModels')"
      style="width: 200px"
      @update:model-value="emit('patch', { model: $event || undefined })"
    >
      <el-option
        v-for="item in dims?.models ?? []"
        :key="item.key"
        :value="item.key"
        :label="labelOf(item.key, item.name, item.requests)"
      />
    </el-select>
    <el-button type="primary" :icon="Refresh" :loading="loading" @click="emit('refresh')">
      {{ t('common.refresh') }}
    </el-button>
    <el-button :icon="Download" :loading="exporting" @click="emit('export')">
      {{ t('reports.exportExcel') }}
    </el-button>
    <el-button :loading="rerunning" @click="emit('rerun')">{{ t('reports.rerun') }}</el-button>
    <span v-if="coverage" class="coverage">{{ coverage }}</span>
  </div>

  <div class="more">
    <el-button :icon="Filter" @click="moreOpen = !moreOpen">
      {{ t('reports.filters') }}
      <el-badge v-if="extraCount > 0" :value="extraCount" class="filter-badge" />
    </el-button>
    <el-button v-if="extraCount > 0" link @click="emit('clear')">{{ t('reports.clearFilters') }}</el-button>
    <div v-show="moreOpen" class="filter-grid">
      <label>
        {{ t('reports.credential') }}
        <el-select
          :model-value="filters.credential_id"
          clearable
          filterable
          :placeholder="t('reports.all')"
          @update:model-value="emit('patch', { credential_id: $event ?? undefined })"
        >
          <el-option
            v-for="item in dims?.credentials ?? []"
            :key="item.key"
            :value="Number(item.key)"
            :label="labelOf(item.key, item.name, item.requests)"
          />
        </el-select>
      </label>
      <label>
        {{ t('reports.tenant') }}
        <el-select
          :model-value="filters.tenant_id"
          clearable
          filterable
          :placeholder="t('reports.all')"
          @update:model-value="emit('patch', { tenant_id: $event || undefined })"
        >
          <el-option v-for="item in dims?.tenants ?? []" :key="item.key" :value="item.key" :label="labelOf(item.key, item.name, item.requests)" />
        </el-select>
      </label>
      <label>
        {{ t('reports.person') }}
        <el-select
          :model-value="filters.person"
          clearable
          filterable
          :placeholder="t('reports.all')"
          @update:model-value="emit('patch', { person: $event || undefined })"
        >
          <el-option v-for="item in dims?.persons ?? []" :key="item.key" :value="item.key" :label="labelOf(item.key, item.name, item.requests)" />
        </el-select>
      </label>
      <label>
        {{ t('reports.apiKey') }}
        <el-select
          :model-value="filters.api_key_id"
          clearable
          filterable
          :placeholder="t('reports.all')"
          @update:model-value="emit('patch', { api_key_id: $event ?? undefined })"
        >
          <el-option
            v-for="item in dims?.api_keys ?? []"
            :key="item.key"
            :value="Number(item.key)"
            :label="labelOf(item.key, item.name, item.requests)"
          />
        </el-select>
      </label>
    </div>
  </div>
</template>

<style scoped>
.toolbar, .more { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 12px; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip {
  border: 1px solid var(--border);
  background: transparent;
  color: inherit;
  border-radius: 999px;
  padding: 2px 10px;
  cursor: pointer;
  font: inherit;
}
.chip.on { border-color: var(--accent); color: var(--accent); }
.coverage { color: var(--text-muted, var(--el-text-color-secondary)); font-size: 12px; }
.filter-badge { margin-left: 6px; }
.filter-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 8px;
  width: 100%;
}
.filter-grid label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text-muted, var(--el-text-color-secondary)); }
</style>
