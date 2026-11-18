<script setup lang="ts">
/**
 * CollapsibleFilterSection.vue — 通用折叠筛选容器（2026-11-18 筛选收纳轮）。
 *
 * 解决的问题：多数列表页筛选控件平铺 1-2 行（最重的页面 6 个 select 排两行，
 * 竖屏/小屏能占半屏），数据区被压到底部。业界收敛口径（GitHub「More filters」
 * / Google Flights / Linear filter bar）：控件默认收拢、生效条件常显、
 * 头部一行常驻。
 *
 * 用法（slot 式，页面已有筛选控件时迁移成本最低——原控件原样搬进 slot）：
 *   <CollapsibleFilterSection :chips="chips" @reset="clearAll">
 *     <select v-model="foo">…</select>
 *     <input v-model="bar" />
 *   </CollapsibleFilterSection>
 *
 * 形态：
 * - 头部一行常驻：⚙️ 筛选（N）开关 + 生效条件 chips + 操作区 slot（查询/清空等）；
 *   chips 单击移除单项，收拢时也能看到当前范围。
 * - 面板默认收拢；有生效条件时首次自动展开一次（首次使用者能看到控件长什么样；
 *   用户手动收拢后不再自动展开——用户显式动作优先于自动行为）。
 * - v-show（不销毁控件，保留未提交的输入状态）。
 *
 * 与 FilterBar.vue 的分工：FilterBar 面向「声明式 definitions + v-model:filters」
 * 的页面（7 个页面在用）；本组件面向「页面已有散装 select/input」的 50+ 页面，
 * 不改数据绑定，只负责收纳。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { FilterChip } from '../../composables/useFilterChips'
import { useFilterChips } from '../../composables/useFilterChips'
import { CaretBottom } from '@element-plus/icons-vue'

const props = defineProps<{
  /** 生效条件 chips（label + 单击移除回调），来自 useFilterChips */
  chips?: FilterChip[]
  /** 生效条件计数；缺省时用 chips.length。页面自定义计数（如排除默认时间条件）时传入 */
  activeCount?: number
  /** 折叠开关文案；缺省用 common.button.filter */
  label?: string
}>()

const emit = defineEmits<{
  /** 头部「清空」按钮：页面自己实现清空逻辑（清条件 + 触发重查） */
  reset: []
  /** 展开状态变化（页面需要同步动作时监听） */
  toggle: [open: boolean]
}>()

const { t } = useI18n()

const chips = useFilterChips(() => props.chips ?? [])
const count = computed(() => props.activeCount ?? chips.value.length)

const expanded = ref(false)

// 有生效条件时自动展开一次，让首次使用者看到控件；
// 用户手动收拢后（manualTouched）不再自动展开——用户显式动作优先。
// immediate：挂载时已带生效条件（URL query 进入 / 状态恢复）也要展开，
// 否则 watch 只捕获"挂载后的跳变"，初始有条件的场景永远不触发。
const manualTouched = ref(false)
watch(
  () => count.value > 0,
  (hasActive, wasActive) => {
    if (hasActive && !wasActive && !manualTouched.value) expanded.value = true
    if (!hasActive) manualTouched.value = false
  },
  { immediate: true },
)

function toggle() {
  expanded.value = !expanded.value
  manualTouched.value = true
  emit('toggle', expanded.value)
}
</script>

<template>
  <div class="cfs">
    <!-- 头部一行常驻：开关 + chips + 操作 -->
    <div class="cfs__header" @click="toggle">
      <button
        type="button"
        class="cfs__toggle"
        :aria-expanded="expanded"
        :aria-controls="`cfs-panel-${$.uid}`"
        @click.stop="toggle"
      >
        <span class="cfs__gear" aria-hidden="true">⚙️</span>
        {{ label ?? t('common.button.filter') }}
        <span v-if="count > 0" class="cfs__badge">{{ count }}</span>
        <span class="cfs__caret" :class="{ 'cfs__caret--open': expanded }" aria-hidden="true"><CaretBottom /></span>
      </button>

      <div v-if="chips.length" class="cfs__chips" @click.stop>
        <button
          v-for="chip in chips"
          :key="chip.key"
          type="button"
          class="cfs__chip"
          :class="chip.className"
          :title="chip.label"
          @click="chip.onRemove()"
        >{{ chip.label }} ✕</button>
      </div>

      <span class="cfs__spacer"></span>

      <div class="cfs__actions" @click.stop>
        <slot name="actions"></slot>
        <button
          v-if="count > 0"
          type="button"
          class="btn btn-sm cfs__reset"
          :title="t('common.button.clear')"
          @click="emit('reset')"
        >⟲</button>
      </div>
    </div>

    <!-- 面板：v-show 保留控件状态（未提交的输入不丢） -->
    <div v-show="expanded" class="cfs__panel">
      <slot></slot>
      <slot name="panel-footer"></slot>
    </div>
  </div>
</template>

<style scoped>
.cfs {
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface-primary);
  margin-bottom: 12px;
  overflow: hidden;
}

/* 头部一行常驻（≈36px），取代原先 1-2 行平铺（80-140px+） */
.cfs__header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 10px;
  background: var(--surface-secondary);
  cursor: pointer;
  min-height: 36px;
}

.cfs__toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: none;
  background: transparent;
  font-weight: 600;
  font-size: 13px;
  color: var(--text-primary);
  cursor: pointer;
  white-space: nowrap;
  padding: 2px 4px;
  user-select: none;
}

.cfs__badge {
  background: var(--primary, var(--el-color-primary, #409eff));
  color: #fff;
  border-radius: 999px;
  font-size: 11px;
  padding: 1px 7px;
  font-weight: 600;
  line-height: 1.4;
}

.cfs__caret {
  display: inline-flex;
  color: var(--text-muted, var(--muted));
  transition: transform 0.15s ease;
}
/* element-plus 图标是裸 svg，必须显式定尺寸（同 ActiveFilterChips 口径） */
.cfs__caret > svg { width: 12px; height: 12px; }
.cfs__caret--open { transform: rotate(180deg); }

/* chips：收拢时也可见——生效条件不失联 */
.cfs__chips {
  display: flex;
  flex-wrap: nowrap;
  gap: 6px;
  overflow-x: auto;
  min-width: 0;
  scrollbar-width: thin;
}
.cfs__chip {
  border: 1px solid var(--border);
  background: var(--bg, var(--surface-primary));
  border-radius: 999px;
  padding: 2px 8px;
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  color: var(--text-primary);
}

.cfs__spacer { flex: 1; }

.cfs__actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
}
.cfs__reset {
  cursor: pointer;
}

/* 面板：控件流式排布，放满自动折行 */
.cfs__panel {
  padding: 10px 12px;
  border-top: 1px solid var(--border);
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.cfs__panel > * {
  min-width: 0;
}
/* 面板内通栏元素（错误提示等）独占一行 */
.cfs__panel > .filter-error {
  flex-basis: 100%;
}
</style>
