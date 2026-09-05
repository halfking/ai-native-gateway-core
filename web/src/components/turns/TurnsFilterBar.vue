<script setup lang="ts">
import { computed } from 'vue'
import { ElDatePicker, ElOption, ElSelect } from 'element-plus'
import type { TurnsFilterOptions } from '../../api/turns'
import {
  activeFilterChips,
  localDatetime,
  type TurnsFilterState,
  type TurnsGroupBy,
} from './turnsListHelpers'

const props = defineProps<{
  state: TurnsFilterState
  filterOptions: TurnsFilterOptions
  groupBy: TurnsGroupBy
  advancedExpanded: boolean
}>()

const emit = defineEmits<{
  'update:state': [TurnsFilterState]
  'update:groupBy': [TurnsGroupBy]
  'update:advancedExpanded': [boolean]
  query: []
  reset: []
  clearChip: [string]
}>()

const timePresets = [
  { value: 'all', label: '时间不限' },
  { value: 'h1', label: '最近 1 小时' },
  { value: 'h24', label: '最近 24 小时' },
  { value: 'd3', label: '最近 3 天' },
  { value: 'd7', label: '最近 7 天' },
  { value: 'custom', label: '自定义时间段' },
]

const groupModes: { value: TurnsGroupBy; label: string }[] = [
  { value: 'project', label: '按项目' },
  { value: 'owner', label: '按用户' },
  { value: 'client', label: '按客户端' },
  { value: 'apikey', label: '按 API Key' },
  { value: 'flat', label: '平铺' },
]

const chips = computed(() => activeFilterChips(props.state))
const showCustomRange = computed(() => props.state.timePreset === 'custom')
const advancedCount = computed(() => chips.value.filter(c =>
  !['search', 'model', 'time'].includes(c.key)).length)

const customRange = computed({
  get(): [Date, Date] | null {
    const from = props.state.dateFrom ? new Date(props.state.dateFrom) : null
    const to = props.state.dateTo ? new Date(props.state.dateTo) : null
    if (from && to && !Number.isNaN(from.getTime()) && !Number.isNaN(to.getTime())) {
      return [from, to]
    }
    if (from && !Number.isNaN(from.getTime())) return [from, from]
    return null
  },
  set(range: [Date, Date] | null) {
    if (!range) {
      patch({ dateFrom: '', dateTo: '', timePreset: 'custom' })
      return
    }
    patch({
      timePreset: 'custom',
      dateFrom: localDatetime(range[0]),
      dateTo: localDatetime(range[1]),
    })
  },
})

function patch(partial: Partial<TurnsFilterState>) {
  emit('update:state', { ...props.state, ...partial })
}

function onPresetChange(value: string) {
  if (value === 'custom') {
    patch({ timePreset: 'custom' })
    return
  }
  if (value === 'all') {
    patch({ timePreset: 'all', dateFrom: '', dateTo: '' })
    emit('query')
    return
  }
  const hours: Record<string, number> = { h1: 1, h24: 24, d3: 72, d7: 168 }
  const h = hours[value] || 0
  const from = new Date(Date.now() - h * 3600 * 1000)
  patch({ timePreset: value, dateFrom: localDatetime(from), dateTo: '' })
  emit('query')
}

function onCustomRangeChange(range: [Date, Date] | null) {
  customRange.value = range
  if (range) emit('query')
}
</script>

<template>
  <div class="filter-section">
    <div class="view-toggle" aria-label="层级查看模式">
      <button
        v-for="mode in groupModes"
        :key="mode.value"
        type="button"
        class="toggle-btn"
        :class="{ active: groupBy === mode.value }"
        @click="emit('update:groupBy', mode.value)"
      >{{ mode.label }}</button>
    </div>

    <div class="filter-row-topic-time">
      <input
        :value="state.search"
        type="text"
        placeholder="主题 / 标题 / 摘要"
        class="filter-input filter-topic"
        @input="patch({ search: ($event.target as HTMLInputElement).value })"
        @keyup.enter="emit('query')"
      />
      <select
        :value="state.timePreset"
        class="filter-input filter-preset"
        @change="onPresetChange(($event.target as HTMLSelectElement).value)"
      >
        <option v-for="p in timePresets" :key="p.value" :value="p.value">{{ p.label }}</option>
      </select>
      <el-date-picker
        v-if="showCustomRange"
        :model-value="customRange"
        type="datetimerange"
        range-separator="~"
        start-placeholder="开始"
        end-placeholder="结束"
        format="YYYY-MM-DD HH:mm"
        class="filter-date-range"
        @update:model-value="onCustomRangeChange"
      />
    </div>

    <div class="filter-bar">
      <el-select
        :model-value="state.model"
        filterable allow-create default-first-option clearable
        placeholder="模型"
        class="filter-select"
        @update:model-value="(v: string) => { patch({ model: v || '' }); emit('query') }"
      >
        <el-option v-for="v in filterOptions.models" :key="v" :label="v" :value="v" />
      </el-select>
      <button class="btn btn-primary" type="button" @click="emit('query')">查询</button>
      <button class="btn btn-secondary" type="button" @click="emit('reset')">清空</button>
      <button class="btn btn-secondary" type="button" @click="emit('update:advancedExpanded', !advancedExpanded)">
        更多筛选
        <span v-if="advancedCount" class="adv-count">{{ advancedCount }}</span>
        <span class="filter-caret" :class="{ open: advancedExpanded }">▸</span>
      </button>
    </div>

    <div v-if="advancedExpanded" class="filter-bar filter-advanced">
      <el-select :model-value="state.provider" filterable allow-create clearable placeholder="供应商" class="filter-select" @update:model-value="(v: string) => { patch({ provider: v || '' }); emit('query') }">
        <el-option v-for="v in filterOptions.providers" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select :model-value="state.statusCode" filterable allow-create clearable placeholder="状态码" class="filter-select filter-select-short" @update:model-value="(v: string) => { patch({ statusCode: v || '' }); emit('query') }">
        <el-option v-for="v in filterOptions.status_codes" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select :model-value="state.projectId" filterable allow-create clearable placeholder="项目" class="filter-select filter-select-short" @update:model-value="(v: string) => { patch({ projectId: v || '' }); emit('query') }">
        <el-option v-for="v in filterOptions.projects" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select :model-value="state.taskId" filterable allow-create clearable placeholder="任务" class="filter-select filter-select-short" @update:model-value="(v: string) => { patch({ taskId: v || '' }); emit('query') }">
        <el-option v-for="v in filterOptions.tasks" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select :model-value="state.ownerUser" filterable allow-create clearable placeholder="属主用户" class="filter-select" @update:model-value="(v: string) => { patch({ ownerUser: v || '' }); emit('query') }">
        <el-option v-for="v in filterOptions.owners" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select :model-value="state.client" filterable allow-create clearable placeholder="客户端 / 智能体" class="filter-select" @update:model-value="(v: string) => { patch({ client: v || '' }); emit('query') }">
        <el-option v-for="v in filterOptions.clients" :key="v" :label="v" :value="v" />
      </el-select>
      <el-select :model-value="state.apiKeyId" filterable clearable placeholder="API Key" class="filter-select" @update:model-value="(v: string) => { patch({ apiKeyId: v || '' }); emit('query') }">
        <el-option
          v-for="opt in filterOptions.api_keys || []"
          :key="opt.id"
          :label="opt.label"
          :value="String(opt.id)"
        />
      </el-select>
      <el-select :model-value="state.status" clearable placeholder="会话状态" class="filter-select filter-select-short" @update:model-value="(v: string) => { patch({ status: v || '' }); emit('query') }">
        <el-option label="进行中" value="active" />
        <el-option label="已关闭" value="closed" />
        <el-option label="已归档" value="archived" />
        <el-option label="已删除" value="deleted" />
      </el-select>
      <el-select :model-value="state.tags" multiple filterable allow-create clearable collapse-tags placeholder="标签" class="filter-select filter-select-tags" @update:model-value="(v: string[]) => { patch({ tags: v || [] }); emit('query') }">
        <el-option v-for="v in filterOptions.tags" :key="v" :label="v" :value="v" />
      </el-select>
    </div>

    <div v-if="chips.length" class="chip-row">
      <button
        v-for="chip in chips"
        :key="chip.key"
        type="button"
        class="chip"
        @click="emit('clearChip', chip.key)"
      >{{ chip.label }} ×</button>
    </div>
  </div>
</template>

<style scoped>
.filter-section { margin-bottom: 16px; }
.view-toggle { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.toggle-btn { height: 30px; padding: 0 14px; border: 1px solid var(--border); border-radius: 15px; background: var(--surface-primary); color: var(--text-secondary); cursor: pointer; font-size: 13px; }
.toggle-btn.active { background: var(--accent); border-color: var(--accent); color: white; }
.filter-row-topic-time { display: flex; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; align-items: center; }
.filter-bar { display: flex; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; align-items: center; }
.filter-advanced { padding: 8px; border: 1px dashed var(--border); border-radius: 6px; background: var(--surface-secondary); }
.filter-input { height: 36px; padding: 4px 12px; background: var(--surface-primary); border: 1px solid var(--border); border-radius: 6px; font-size: 14px; min-width: 140px; box-sizing: border-box; }
.filter-topic { min-width: 260px; flex: 1 1 260px; }
.filter-preset { min-width: 130px; flex: 0 0 auto; }
.filter-date-range { flex: 1 1 320px; max-width: 420px; }
.filter-date-range :deep(.el-input__wrapper) {
  min-height: 36px;
  height: 36px;
  box-sizing: border-box;
}
.filter-select { width: 170px; max-width: 170px; flex: 0 0 auto; }
.filter-select-short { width: 130px; max-width: 130px; }
.filter-select-tags { width: 180px; max-width: 180px; }
.filter-select :deep(.el-select__wrapper) {
  min-height: 36px;
  height: 36px;
  box-sizing: border-box;
}
.filter-select :deep(.el-select__selection) { overflow: hidden; }
.filter-select :deep(.el-select__suffix),
.filter-select :deep(.el-select__caret),
.filter-select :deep(.el-icon) {
  width: 14px !important;
  height: 14px !important;
  font-size: 14px !important;
  line-height: 14px !important;
}
.filter-select :deep(svg) {
  width: 14px !important;
  height: 14px !important;
  max-width: 14px !important;
  max-height: 14px !important;
}
.btn { height: 36px; padding: 0 16px; border-radius: 6px; font-size: 14px; font-weight: 500; cursor: pointer; }
.btn-primary { background: var(--accent); color: white; border: 0; }
.btn-secondary { background: var(--surface-primary); color: var(--text-primary); border: 1px solid var(--border); }
.adv-count { display: inline-block; min-width: 16px; padding: 0 4px; border-radius: 8px; background: var(--accent); color: white; font-size: 11px; line-height: 16px; }
.filter-caret { display: inline-block; transition: transform .15s; font-size: 12px; }
.filter-caret.open { transform: rotate(90deg); }
.chip-row { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 4px; }
/* .chip / .chip:hover / .chip--button 从全局 styles/pill-chip.css 继承（P1-8）。
 * 本组件原 padding 2px 10px 与全局 --chip-padding-y/x = 3px 9px 略有差异，
 * scoped 内仅覆盖 padding。 */
.chip { padding: 2px 10px; }
@media (max-width: 760px) {
  .filter-topic, .filter-preset, .filter-date-range, .filter-select, .filter-select-short, .filter-select-tags { width: 100%; max-width: none; min-width: 0; flex-basis: 100%; }
}
</style>
