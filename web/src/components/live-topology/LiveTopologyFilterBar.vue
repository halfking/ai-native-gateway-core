<script setup lang="ts">
// LiveTopologyFilterBar.vue —— 实时拓扑的**过滤栏**（2026-11-16 从 /live 视图抽出）
//
// 为什么抽成组件：owner 2026-11-16 要求「过滤条件还原到上一步，放在顶层」，
// 于是同一份过滤栏要同时出现在两个实时页：
//   · `/live`   —— 旧版三栏拓扑（过滤栏在顶栏第二行）
//   · `/live2`  —— 新版请求阶段树（过滤栏在顶层，统计改为点开全页浮层）
//
// ★ **同源纪律**：两个页面的过滤栏**必须是一份实现**。复制一份 markup 意味着
// 以后改一个忘另一个 ⇒ 两页的「可见计数」与「图里内容」会对不上（仓内踩过：
// 「图里没有、右栏却有」的矛盾画面）。本组件是唯一实现，两页都调它。
//
// 语义沿用 /live 既有口径（不改）：
//   · 四维多选（客户端/供应商/模型/节点）—— **同维 OR、跨维 AND**；
//   · 状态、时间窗；
//   · 两个数据开关（探测请求 / 无效节点，**默认不勾 = 默认去除**）。
//
// data-testid 全部沿用 /live 原值（既有钉测按它们选元素，**不得改名**）。

import { useI18n } from 'vue-i18n'
import { ElOption, ElSelect } from 'element-plus'
import type { FilterOption } from '../../lib/liveTopologyFilter'
import { TOPO_WINDOW_PRESETS, type TopoFilters } from '../../types/liveTopology'

const props = withDefaults(
  defineProps<{
    filters: TopoFilters
    options: {
      clients: FilterOption[]
      providers: FilterOption[]
      models: FilterOption[]
      nodes: FilterOption[]
    }
    /** 已生效的过滤条件数（>0 时显示「清除过滤」）。 */
    activeFilterCount?: number
    /** 是否显示常驻全屏按钮（/live 与 live2 都要）。 */
    showFullscreen?: boolean
    isFullscreen?: boolean
  }>(),
  { activeFilterCount: 0, showFullscreen: false, isFullscreen: false },
)

const emit = defineEmits<{
  /** 局部改写过滤态（调用方合并进自己的 ref）。 */
  (e: 'patch', patch: Partial<TopoFilters>): void
  (e: 'clear'): void
  (e: 'toggle-fullscreen'): void
}>()

const { t } = useI18n()

function patch(next: Partial<TopoFilters>) {
  emit('patch', next)
}

function onClients(v: string[] | undefined) {
  patch({ clients: v ?? [] })
}
function onProviders(v: string[] | undefined) {
  patch({ providers: v ?? [] })
}
function onModels(v: string[] | undefined) {
  patch({ models: v ?? [] })
}
function onNodes(v: string[] | undefined) {
  patch({ nodes: v ?? [] })
}
function onStatus(v: string) {
  patch({ status: v as TopoFilters['status'] })
}
function onWindow(v: number) {
  patch({ windowMs: v })
}
function onShowProbe(v: boolean) {
  patch({ showProbe: v })
}
function onShowInvalid(v: boolean) {
  patch({ showInvalidNodes: v })
}
</script>

<template>
  <div class="live-topology__filters">
    <el-select
      :model-value="props.filters.clients"
      multiple
      collapse-tags
      clearable
      class="live-topology__select"
      :placeholder="t('liveTopology.filter.allClients')"
      :aria-label="t('liveTopology.filter.client')"
      data-testid="filter-clients"
      @update:model-value="onClients"
    >
      <el-option v-for="opt in props.options.clients" :key="opt.value" :label="`${opt.label} (${opt.count})`" :value="opt.value" />
    </el-select>

    <el-select
      :model-value="props.filters.providers"
      multiple
      collapse-tags
      clearable
      class="live-topology__select"
      :placeholder="t('liveTopology.filter.allProviders')"
      :aria-label="t('liveTopology.filter.provider')"
      data-testid="filter-providers"
      @update:model-value="onProviders"
    >
      <el-option v-for="opt in props.options.providers" :key="opt.value" :label="`${opt.label} (${opt.count})`" :value="opt.value" />
    </el-select>

    <el-select
      :model-value="props.filters.models"
      multiple
      collapse-tags
      clearable
      class="live-topology__select"
      :placeholder="t('liveTopology.filter.allModels')"
      :aria-label="t('liveTopology.filter.model')"
      data-testid="filter-models"
      @update:model-value="onModels"
    >
      <el-option v-for="opt in props.options.models" :key="opt.value" :label="`${opt.label} (${opt.count})`" :value="opt.value" />
    </el-select>

    <el-select
      :model-value="props.filters.nodes"
      multiple
      collapse-tags
      clearable
      class="live-topology__select"
      :placeholder="t('liveTopology.filter.allNodes')"
      :aria-label="t('liveTopology.filter.node')"
      data-testid="filter-nodes"
      @update:model-value="onNodes"
    >
      <el-option v-for="opt in props.options.nodes" :key="opt.value" :label="`${opt.label} (${opt.count})`" :value="opt.value" />
    </el-select>

    <select
      :value="props.filters.status"
      :aria-label="t('liveTopology.filter.status')"
      @change="onStatus(($event.target as HTMLSelectElement).value)"
    >
      <option value="">{{ t('liveTopology.filter.allStatuses') }}</option>
      <option value="success">{{ t('liveTopology.filter.success') }}</option>
      <option value="in_progress">{{ t('liveTopology.filter.inProgress') }}</option>
      <option value="failure">{{ t('liveTopology.filter.failure') }}</option>
      <option value="rate_limited">{{ t('liveTopology.filter.rateLimited') }}</option>
    </select>

    <select
      :value="props.filters.windowMs"
      :aria-label="t('liveTopology.filter.window')"
      @change="onWindow(Number(($event.target as HTMLSelectElement).value))"
    >
      <option v-for="preset in TOPO_WINDOW_PRESETS" :key="preset.value" :value="preset.value">
        {{ t(preset.labelKey) }}
      </option>
    </select>

    <!-- 两个数据开关（owner 2026-10-18：默认不勾 = 默认去除）。 -->
    <label class="live-topology__toggle" data-testid="toggle-probe">
      <input
        type="checkbox"
        :checked="props.filters.showProbe"
        @change="onShowProbe(($event.target as HTMLInputElement).checked)"
      />
      <span>{{ t('liveTopology.filter.showProbe') }}</span>
    </label>
    <label class="live-topology__toggle" data-testid="toggle-invalid">
      <input
        type="checkbox"
        :checked="props.filters.showInvalidNodes"
        @change="onShowInvalid(($event.target as HTMLInputElement).checked)"
      />
      <span>{{ t('liveTopology.filter.showInvalid') }}</span>
    </label>

    <button
      v-if="props.activeFilterCount"
      type="button"
      class="live-topology__clear"
      data-testid="filter-clear"
      @click="emit('clear')"
    >
      {{ t('liveTopology.filter.clear') }}
    </button>

    <!-- 常驻全屏按钮：与清空过滤同族，放在过滤栏右端。 -->
    <button
      v-if="props.showFullscreen"
      type="button"
      class="live-topology__fullscreen"
      :data-testid="props.isFullscreen ? 'fullscreen-exit' : 'fullscreen-enter'"
      :title="props.isFullscreen ? t('liveTopology.fullscreen.exit') : t('liveTopology.fullscreen.enter')"
      :aria-label="props.isFullscreen ? t('liveTopology.fullscreen.exit') : t('liveTopology.fullscreen.enter')"
      :aria-pressed="props.isFullscreen"
      @click="emit('toggle-fullscreen')"
    >
      {{ props.isFullscreen ? t('liveTopology.fullscreen.exit') : t('liveTopology.fullscreen.enter') }}
    </button>
  </div>
</template>

<style scoped>
.live-topology__filters {
  display: flex;
  align-items: center;
  gap: 4px;
  /* 独占一行（width:100%）、永不换行；极窄兜底横向滚动（丢滚动好过丢控件）。
     owner 2026-10-18：筛选条件一行放下，字号更小且各控件大小一致。 */
  width: 100%;
  flex-wrap: nowrap;
  overflow-x: auto;
  scrollbar-width: thin;
  font-size: 11px;
}

.live-topology__select {
  width: 132px;
  /* el-select 内部字号统一为 11px，与旁边的原生 select/按钮一致。 */
  --el-font-size-base: 11px;
  flex: none;
}

.live-topology__filters select {
  max-width: 120px;
  padding: 0 4px;
  height: 24px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-text);
  font-size: 11px;
  flex: none;
}

/* 数据开关：与其它控件同高同字号（checkbox + label，一行内不凸起）。 */
.live-topology__toggle {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  height: 24px;
  padding: 0 4px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  color: var(--kx-text);
  font-size: 11px;
  white-space: nowrap;
  cursor: pointer;
  user-select: none;
  flex: none;
}

.live-topology__toggle input {
  margin: 0;
  width: 12px;
  height: 12px;
  accent-color: var(--kx-primary);
}

.live-topology__clear {
  height: 24px;
  padding: 0 8px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: transparent;
  color: var(--kx-muted);
  font-size: 11px;
  cursor: pointer;
  flex: none;
}

/* 常驻全屏按钮：与「清空过滤」同族同尺寸。 */
.live-topology__fullscreen {
  height: 24px;
  padding: 0 8px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: transparent;
  color: var(--kx-text);
  font-size: 11px;
  cursor: pointer;
  white-space: nowrap;
  flex: none;
}
</style>
