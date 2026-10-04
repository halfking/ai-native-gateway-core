<script setup lang="ts" generic="T extends Record<string, any>">
/**
 * ResponsiveDataView — 表格/卡片双模板容器（docs/UI规范/00 §5.2 · H3，参考规范 03 §1–§3）。
 *
 * ```
 * <ResponsiveDataView :rows="rows" :title-key="id" :fields="fields"
 *                     :loading="loading" :empty="!rows.length" empty-text="…">
 *   <template #table><table>…既有表格结构不动…</table></template>
 *   <template #cards="{ row }"><CardList … /></template>  <!-- 或直接用默认卡片 -->
 * </ResponsiveDataView>
 * ```
 *
 * ## 三条不可省的规则
 *
 * 1. **compact 强制卡片，且不渲染切换钮。** 切换钮由 `canSwitch` 门控 ——
 *    compact 档它压根不存在，用户无从点到表格形态。
 * 2. **切换视图模式不重新打接口。** 本组件不发任何请求；`rows` 只有一个来源。
 *    卡片与表格共用同一份行数据与字段政策。
 * 3. **loading / 空态 / 错误在两种形态间一致。** 不允许出现
 *    「表格有骨架、卡片空白一片」。三态统一由本组件在外层裁定。
 *
 * ## 为什么不把 DataTable 改造进来
 *
 * 既有 `DataTable.vue` 是**包裹模式**（给存量手写 `<table>` 加横滚容器），
 * 服务于 60 个未重构视图；本组件是**双模板**。两者形态与适用面不同，
 * 强行合并会让 DataTable 的零重构优势消失。两者可嵌套使用。
 *
 * ## 四个插槽
 *
 * - `#table` — 桌面表格结构原样放进来。
 * - `#cards` — 要完全自己控制卡片（props / 布局）时用它，内部已含 `#actions` 透传。
 * - `#actions` — 2026-10-06 补的直通口：调用方只想加**逐行动作**
 *   （表格里那种「仅待支付才出的行内链接」）时，不必为了用 `#actions`
 *   而把 CardList 的整串 props 在 `#cards` 里重写一遍。
 *   两条互斥，`#cards` 优先。
 * - `#empty` — 2026-10-06 切片十四补的空态富内容口。
 *
 *   `EmptyState` 本来就有默认插槽（它自己的注释写明「也可用默认插槽放富内容」），
 *   但本组件此前只把 `text` 传下去。**为什么需要这个口子**：有的页面的桌面空态
 *   **不是一句话**，而是「一句话 + 一个行动入口」—— `MaaSUsageView` 的账本空态里
 *   就有一个「去购买额度」的 `RouterLink`。`emptyText` 只能装纯文本，
 *   把它降级成一句话 = **在 compact 下删掉该屏唯一的 CTA**。
 *
 *   ⚠️ **不传 `#empty` 时行为逐字不变**：`EmptyState` 的 `<slot>{{ text }}</slot>`
 *   在插槽无内容时走 fallback，仍然出 `emptyText`。
 *   这条不变式由 `ResponsiveDataView.test.ts` 的
 *   「不传 `#empty` 时空态仍是 `emptyText`」用例钉住 —— 它是本次改动的**唯一风险面**，
 *   因为本组件已被十三条切片共用。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useDataViewMode, type DataViewMode } from '../../composables/useDataViewMode'
import CardList, { type CardField } from './CardList.vue'
import EmptyState from '../EmptyState.vue'
import AppSpinner from '../AppSpinner.vue'

const props = withDefaults(
  defineProps<{
    rows: T[]
    /** 卡片主体字段键 */
    titleKey: string
    subtitleKeys?: string[]
    metaKeys?: string[]
    primaryField?: CardField
    fields?: CardField[]
    loading?: boolean
    empty?: boolean
    emptyText?: string
    /**
     * 空态内边距。默认与 `EmptyState` 一致（40px）。
     *
     * 为什么要有这个 prop：迁入本组件的页面原本各写各的空态 padding
     * （`ApprovalListView` 是 64px），而本组件统一用 `EmptyState` 的默认值 ——
     * 不给这个口子，**桌面空态的观感会在迁移当天静默变化**。
     * 各页面显式传自己原来的值，桌面像素不变。
     */
    emptyPadding?: string
    /**
     * 表格的最小宽度。超出部分在本组件的容器内横向滚动，而非挤压列。
     *
     * 默认与 `DataTable` 的 `minWidth` 同值（720px），这样从 DataTable
     * 迁移过来的页面不需要额外指定就能拿到同样的窄屏列宽保护。
     * **不要同时用 `DataTable` 包这张表** —— 两个 `overflow-x` 容器嵌套
     * 会出现双横向滚动条。
     */
    tableMinWidth?: string
    /** 整卡可点（仅当与「进入详情」同一意图） */
    clickable?: boolean
    clickableLabel?: string
    /** 卡头格式化钩子。透传给 CardList —— 见该组件同名 prop 的说明。 */
    titleFormat?: (row: Record<string, unknown>) => string | null | undefined
    /** 是否允许用户切换形态。默认跟随档位（compact 不可切）。 */
    allowSwitch?: boolean
  }>(),
  {
    subtitleKeys: () => [],
    metaKeys: () => [],
    fields: () => [],
    loading: false,
    empty: false,
    emptyText: '',
    emptyPadding: '40px',
    tableMinWidth: '720px',
    clickable: false,
    clickableLabel: '',
    allowSwitch: true,
  },
)

const emit = defineEmits<{
  (e: 'row-click', row: T): void
}>()

const { t } = useI18n()
const { effective, canSwitch, setMode } = useDataViewMode()

const showCards = computed(() => effective.value === 'cards')
const switchVisible = computed(() => props.allowSwitch && canSwitch.value)

function onSwitch(to: DataViewMode) {
  setMode(to)
}
</script>

<template>
  <div
    class="responsive-data-view"
    :data-view="effective"
    :style="{ '--rdv-table-min-width': tableMinWidth }"
  >
    <!--
      切换钮：compact 档**不渲染**（canSwitch=false）。用 v-if 而非 CSS 隐藏，
      这样「compact 下不存在切换入口」可被 querySelector 直接断言。
    -->
    <div v-if="switchVisible" class="responsive-data-view__switch" role="group">
      <button
        type="button"
        class="responsive-data-view__switch-btn"
        :aria-pressed="effective === 'table'"
        @click="onSwitch('table')"
      >
        {{ t('hyper.dataView.table') }}
      </button>
      <button
        type="button"
        class="responsive-data-view__switch-btn"
        :aria-pressed="effective === 'cards'"
        @click="onSwitch('cards')"
      >
        {{ t('hyper.dataView.cards') }}
      </button>
    </div>

    <!--
      三态统一：loading / empty 在两种形态之外裁定。
      这样切形态时不会出现「表格有骨架、卡片空白」的错位。
    -->
    <div v-if="loading" class="responsive-data-view__state">
      <AppSpinner />
    </div>
    <EmptyState v-else-if="empty" :text="emptyText || t('hyper.list.empty')" :padding="emptyPadding">
      <slot name="empty" />
    </EmptyState>

    <template v-else>
      <div v-if="!showCards" class="responsive-data-view__table">
        <slot name="table" :rows="rows" />
      </div>
      <CardList
        v-else
        class="responsive-data-view__cards"
        :rows="rows"
        :title-key="titleKey"
        :subtitle-keys="subtitleKeys"
        :meta-keys="metaKeys"
        :primary-field="primaryField"
        :fields="fields"
        :title-format="titleFormat"
        :clickable="clickable"
        :clickable-label="clickableLabel"
        @row-click="(r) => emit('row-click', r)"
      >
        <template v-if="$slots.cards" #actions="slotProps">
          <slot name="cards" v-bind="slotProps" />
        </template>
        <template v-else-if="$slots.actions" #actions="slotProps">
          <slot name="actions" v-bind="slotProps" />
        </template>
      </CardList>
    </template>
  </div>
</template>

<style scoped>
.responsive-data-view {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-2);
}

.responsive-data-view__switch {
  display: flex;
  gap: 4px;
  justify-content: flex-end;
}

.responsive-data-view__switch-btn {
  /* R1（2026-10-04 吸收）：≥48px。本文件是本专题 H3 新建的（未被 git 跟踪），
     所以适用「新控件」标准而不是存量 44px 下限。此处原本 44px。 */
  min-height: 48px;
  padding: 0 12px;
  background: var(--kx-bg);
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  color: var(--kx-muted);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

.responsive-data-view__switch-btn[aria-pressed='true'] {
  color: var(--kx-text);
  border-color: var(--kx-primary);
}

.responsive-data-view__state {
  padding: var(--kx-space-4) 0;
}

.responsive-data-view__table {
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
}

/*
 * 窄屏列宽保护：表格至少 min-width，超出部分在容器内滚动而非挤压。
 * 与 `DataTable` 的 `--dt-min-width` 同一套做法，**不要两者同时用**
 * —— 嵌套的 overflow-x 容器会出现双横向滚动条。
 */
.responsive-data-view__table > :slotted(table) {
  min-width: var(--rdv-table-min-width, 720px);
}
</style>
