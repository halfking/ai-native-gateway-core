<script setup lang="ts">
// ReconciliationToolbar.vue — view, snapshot date range, provider/model filters, actions.
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Download, Filter, Refresh } from '@element-plus/icons-vue'
import { ElBadge, ElButton, ElOption, ElRadioButton, ElRadioGroup, ElSelect } from 'element-plus'
import type { DimensionOptions, ReportFilter, ReportView } from '../../api/reportrollup'
import { UNASSIGNED_ID } from '../../api/reportrollup'
import ModelPicker from '../ModelPicker.vue'
import KxDateRangePicker from '../ui/KxDateRangePicker.vue'
import type { KxDateRange } from '../ui/kx-date-types'
import { fmtInt } from './format'
import { snapshotDatePresets, snapshotNotAfter } from './snapshotRange'

const props = defineProps<{
  view: ReportView
  range: KxDateRange
  filters: ReportFilter
  dims: DimensionOptions | null
  dimsLoading: boolean
  loading: boolean
  exporting: boolean
  coverage: string
}>()

const emit = defineEmits<{
  'update:view': [ReportView]
  applyRange: [KxDateRange]
  patch: [Partial<ReportFilter>]
  refresh: []
  export: []
  clear: []
}>()

const { t } = useI18n()
const moreOpen = ref(false)
// 367 inclusive days matches reportRange: end-start > 366 days is rejected.
const presets = computed(() => snapshotDatePresets())
const notAfter = computed(() => snapshotNotAfter())

const extraCount = computed(() => {
  const f = props.filters
  return [f.credential_id, f.api_key_id, f.tenant_id, f.person].filter((v) => v != null && v !== '').length
})

function labelOf(key: string, name: string | undefined, requests: number): string {
  const base = name ? `${name} (${key})` : key
  return `${base} · ${fmtInt(requests)}`
}

/**
 * id 类维度（供应商 / 凭据 / apikey）下拉的展示名：-1 是「未落定」哨兵
 * （未路由到 provider / 未落到具体凭据 / 未落到具体 apikey 的失败行）。
 *
 * 它在快照里是 NULL，读面映射成 -1 显示成一行（真库实测未落定桶：供应商
 * 38,035 笔 = 全量 30.8%、凭据 38,035 笔、apikey 16 笔）。候选侧用同一个键
 * （-1），所以这一行**可筛**——但键是 -1，直接显示 "-1 · 38,035" 对账人
 * 读不懂，得说人话。
 */
function idLabelOf(key: string, name: string | undefined, requests: number): string {
  if (key === String(UNASSIGNED_ID)) {
    return `${t('reports.unassigned')} · ${fmtInt(requests)}`
  }
  return labelOf(key, name, requests)
}

/**
 * ModelPicker 的 emit 签名是 `string | string[]`（single / multi 共用），
 * 这里收窄成单值：数组形态取首项（多选模式不会被本页触发）。
 */
function onModelPicked(value: string | string[]) {
  const name = typeof value === 'string' ? value : (value[0] ?? '')
  emit('patch', { model: name || undefined })
}

/** 「本区间有请求的模型」清单：按请求数降序（后端已排序，这里不再二次排序）。 */
const rankedModels = computed(() =>
  (props.dims?.models ?? []).map((item) => ({ name: item.key, requests: item.requests })),
)

/**
 * 候选被硬上限砍掉时的披露。旧实现按 500 项静默截断（30 天就有 527 个模型名），
 * 被砍掉的正是请求数靠后的那批——「数据不见了」没有任何提示。现在后端回报
 * 砍了几项，这里说出来。
 */
const truncatedHint = computed(() => {
  const cut = props.dims?.truncated ?? {}
  const total = Object.values(cut).reduce((sum, n) => sum + n, 0)
  if (total <= 0) return ''
  return t('reports.candidatesTruncated', { n: total })
})

function onView(value: string | number | boolean | undefined) {
  if (value === 'provider' || value === 'internal') emit('update:view', value)
}
</script>

<template>
  <div class="toolbar">
    <el-radio-group :model-value="view" @update:model-value="onView">
      <el-radio-button value="provider">{{ t('reports.providerView') }}</el-radio-button>
      <el-radio-button value="internal">{{ t('reports.internalView') }}</el-radio-button>
    </el-radio-group>
    <el-button
      v-if="view === 'provider'"
      data-testid="reconciliation-business-scope"
      :aria-pressed="filters.traffic_scope === 'business'"
      @click="emit('patch', { traffic_scope: filters.traffic_scope === 'business' ? undefined : 'business' })"
    >{{ t('sessions.stats.attribution.businessOnly') }}</el-button>
    <KxDateRangePicker
      :model-value="range"
      :presets="presets"
      :max-span-days="367"
      :not-after="notAfter"
      :disabled="loading"
      @apply="emit('applyRange', $event)"
    />
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
        :label="idLabelOf(item.key, item.name, item.requests)"
      />
    </el-select>
    <!--
      模型选择器改用与 /routing-v2?tab=resolve 同一个 ModelPicker（2026-11-16 审计轮）。

      为什么不能留在 el-select 上：el-select 的候选来自 dims.models（快照里的
      raw_model_name 字面值），而路由页的选择器给的是 canonical 名——两套名字
      字面不等，「选了模型却筛不出行」。改成同一个 ModelPicker 后，canonical
      名由后端展开成 raw 名集合（见 domains/reportrollup/modelnames.go），
      而 raw 名（含目录外的 `<unknown>`）原样命中，一行都不丢。

      ModelPicker 里多挂一段「本区间有请求的模型」（request-ranked，置顶、按
      请求数降序）：目录描述的是**能路由的模型**，对账要看的却是**这个区间
      真跑过的模型**——两者的差集正是审计要抓的（配了却没流量 / 目录里没有
      却有流量）。这段就是「有请求的优先」的落点。
    -->
    <div class="model-picker-slot" data-testid="filter-model-picker">
      <ModelPicker
        :model-value="filters.model ?? ''"
        :request-ranked="rankedModels"
        :request-ranked-title="t('reports.modelsWithRequests')"
        :request-ranked-limit="24"
        :placeholder="t('reports.allModels')"
        :title="t('reports.modelPickerTitle')"
        @update:model-value="onModelPicked"
      />
    </div>
    <span v-if="truncatedHint" class="hint">{{ truncatedHint }}</span>
    <el-button type="primary" :icon="Refresh" :loading="loading" @click="emit('refresh')">
      {{ t('common.refresh') }}
    </el-button>
    <el-button :icon="Download" :loading="exporting" @click="emit('export')">
      {{ t('reports.exportExcel') }}
    </el-button>
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
            :label="idLabelOf(item.key, item.name, item.requests)"
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
            :label="idLabelOf(item.key, item.name, item.requests)"
          />
        </el-select>
      </label>
    </div>
  </div>
</template>

<style scoped>
.toolbar, .more { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; margin-bottom: 12px; }
.coverage { color: var(--text-muted, var(--el-text-color-secondary)); font-size: 12px; }
.hint { color: var(--text-muted, var(--el-text-color-secondary)); font-size: 12px; }
.model-picker-slot { width: 220px; }
.filter-badge { margin-left: 4px; }
.filter-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 8px;
  width: 100%;
}
.filter-grid label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; color: var(--text-muted, var(--el-text-color-secondary)); }
</style>
