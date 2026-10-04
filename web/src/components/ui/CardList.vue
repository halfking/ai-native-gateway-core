<script setup lang="ts" generic="T extends Record<string, any>">
/**
 * CardList — 描述驱动的卡片列表（docs/UI规范/00 §5.2 · H3，参考规范 03 §3）。
 *
 * ## 为什么要卡片而不是把表格缩小
 *
 * 网关控制台的核心作业是**对照多列数字**（模型、耗时、成功率、token、错误）。
 * 320px 竖屏无法同时展示这些列。把表格缩小会得到一堆截断的列头和横向滚动，
 * 那不是移动端可用性。卡片把「一行」重排成「一块」：主体识别放大、次要字段堆叠、
 * 主指标突出、动作收敛到 1–2 个。
 *
 * ## 字段政策红线
 *
 * **卡片不得因为换了形态就露出表格里被脱敏/隐藏的字段。**
 * 本组件是**纯呈现**：它渲染什么完全由调用方传入的 `fields` 决定。
 * 因此脱敏责任在页面侧 —— 页面必须让 table 的列与 card 的 fields 来自
 * **同一套字段策略**（`ResponsiveDataView` 的两个插槽由同一页面提供）。
 * 组件层再加一道保险：未在 `fields` 中声明的键**永远不会被渲染**，
 * 即便行对象里带着它。
 *
 * ## 交互约定
 *
 * - 整卡点击**仅当**与「进入详情」同一意图时才启用，且必须同时提供可见的
 *   详情控件/文案（见 `primaryField` 的 `action` 说明）。避免误触写操作。
 * - `actions` 最多 2 个。禁止在卡片上复制整行的 6 个操作。
 * - 触控目标 ≥48px（新增 Android 控件基线）。
 */
import { computed } from 'vue'

export type CardFieldAlign = 'start' | 'end'
export type CardTone = 'neutral' | 'good' | 'warn' | 'danger'

export interface CardField {
  /** 行对象上的键。未声明的键不会被渲染。 */
  key: string
  /** 已翻译的标签文本。 */
  label: string
  /** 展示形态。 */
  type?: 'text' | 'metric' | 'badge'
  /** metric 型右对齐（金额/数字惯例）。 */
  align?: CardFieldAlign
  /**
   * `metric` / `badge` 型的强调级别（着色）。`text` 型不读它。
   *
   * 可以给函数：**逐行求值**。状态色天生是行属性 ——
   * 一张列表里「待支付 / 已支付 / 已取消」可以共存，字段级常量
   * 会把整列表按同一个色上色，等于把桌面徽章那层信息丢掉。
   */
  tone?: CardTone | ((row: Record<string, unknown>) => CardTone | undefined)
  /**
   * 自定义格式化。**没有它就只能渲染原始字符串字段** ——
   * 数字千分位、日期本地化、状态译名、货币符号都做不了，
   * 于是调用方要么放弃卡片形态，要么往领域对象里塞 `xxx_text` 字段（更脏）。
   *
   * 返回 `null` / `undefined` 时按「无值」渲染成 `—`，与缺字段一致。
   * 抛错由调用方负责处理 —— 本组件不吞异常。
   */
  format?: (value: unknown, row: Record<string, unknown>) => string | null | undefined
}

const props = withDefaults(
  defineProps<{
    rows: T[]
    /** 主体识别字段的键（姓名/项目名/单号）。 */
    titleKey: string
    /** 主体下方的次要文本字段键（可多个）。 */
    subtitleKeys?: string[]
    /** 状态/元信息字段键。 */
    metaKeys?: string[]
    /** 主指标字段。 */
    primaryField?: CardField
    /** 次要字段（label:value 网格）。 */
    fields?: CardField[]
    /** 行操作插槽名空间下的可用动作数上限（仅用于文档与断言）。 */
    loading?: boolean
    empty?: boolean
    emptyText?: string
    /**
     * 卡头文本的格式化钩子。
     *
     * 为什么必须有它：`titleKey` 只给键，卡头走**原始值**。以「时间」或
     * 「动作码」为主识别的列表（请求日志、审计日志）于是会把裸 ISO 串
     * `2026-10-05T01:15:30.000Z` 或 `user.delete` 直接甩给用户 ——
     * 本地化与译名都做不了。`fields` 上的 `format` 救不了卡头：它们渲染在卡的下方。
     *
     * 返回 `null` / `''` 时回落到 `titleKey` 的原始值（而不是渲染成空白），
     * 这样「钩子算不出标题」不会变成一张没有标题的卡。
     */
    titleFormat?: (row: Record<string, unknown>) => string | null | undefined
    /** 整卡是否可点。仅在与「进入详情」同一意图时传 true。 */
    clickable?: boolean
    /** 整卡可点时的可访问名（必须指向同一意图）。 */
    clickableLabel?: string
  }>(),
  {
    subtitleKeys: () => [],
    metaKeys: () => [],
    fields: () => [],
    loading: false,
    empty: false,
    emptyText: '',
    clickable: false,
    clickableLabel: '',
  },
)

const emit = defineEmits<{
  (e: 'row-click', row: T): void
}>()

function val(row: T, key: string): unknown {
  // 只读白名单内的键：调用方没声明的字段这里也拿不到。
  return (row as Record<string, unknown>)[key]
}

/** 行主键：保证是 string/number，Vue :key 需要 PropertyKey。 */
function keyOf(row: T, key: string, index: number): string | number {
  const v = val(row, key)
  return typeof v === 'string' || typeof v === 'number' ? v : `idx-${index}`
}

function text(row: T, key: string): string {
  const v = val(row, key)
  if (v == null) return '—'
  return String(v)
}

/**
 * 卡头文本：优先走 `titleFormat`，其次按 `titleKey` 的原始值。
 * 钩子返回 null/'' 时回落 —— 一张没有标题的卡比一个不完美的卡头更难用。
 *
 * `keyOf` **不走这里**：`:key` 必须是稳定主键，用格式化后的标题当键
 * 会在标题重复或含时间戳时把整列表的复用打乱。
 */
function titleText(row: T): string {
  if (props.titleFormat) {
    const out = props.titleFormat(row as Record<string, unknown>)
    if (out != null && out !== '') return out
  }
  return text(row, props.titleKey)
}

/**
 * 字段文本：优先走 `format`，其次按原始值。
 * `format` 返回 null/undefined 时按「无值」渲染成 `—`，与缺字段表现一致 ——
 * 否则调用方要为了「空字符串 vs 无值」再写一层判断。
 */
function fieldText(row: T, f: CardField): string {
  if (f.format) {
    const out = f.format(val(row, f.key), row as Record<string, unknown>)
    return out == null || out === '' ? '—' : out
  }
  return text(row, f.key)
}

const visibleFields = computed(() => props.fields.slice(0, 8))

/**
 * 字段的 `data-tone`。
 *
 * ★ 2026-10-06（H6 第七条切片）才补对的两件事：
 * 1. `text` 型恒为 undefined（不读 tone）；`metric` **与 `badge`** 都读。
 *    此前只判 `metric`，于是 `type: 'badge'` 在 `fields` 里**整条是空操作** ——
 *    不加 class、不给颜色。徽章形状仍由 `metaKeys` 那一路（`.card__badge`）提供，
 *    这里补的是**状态色**：桌面上「待支付=黄 / 已支付=绿 / 已过期=红」这层信息，
 *    到了卡片上原本只剩文字。
 * 2. `tone` 可以是**函数**，按行求值。状态色天生是行属性 —— 同一张列表里
 *    「待支付 / 已支付 / 已取消」可以共存，字段级常量会把整列表按同一个色上色，
 *    等于把桌面徽章那层信息又丢一次。
 */
function fieldTone(row: T, f: CardField): CardTone | undefined {
  if (f.type !== 'metric' && f.type !== 'badge') return undefined
  const t = typeof f.tone === 'function' ? f.tone(row as Record<string, unknown>) : f.tone
  return t ?? 'neutral'
}
</script>

<template>
  <div class="card-list" data-testid="card-list">
    <p v-if="loading" class="card-list__state" role="status" aria-live="polite">…</p>
    <p v-else-if="empty" class="card-list__state">{{ emptyText }}</p>

    <ul v-else class="card-list__items">
      <li
        v-for="(row, i) in rows"
        :key="keyOf(row, titleKey, i)"
        class="card"
        :class="{ 'card--clickable': clickable }"
        :data-title="titleText(row)"
      >
        <component
          :is="clickable ? 'button' : 'div'"
          class="card__head"
          :type="clickable ? 'button' : undefined"
          :aria-label="clickable ? clickableLabel || titleText(row) : undefined"
          @click="clickable && emit('row-click', row)"
        >
          <span class="card__title">{{ titleText(row) }}</span>
          <span v-if="subtitleKeys.length" class="card__subtitle">
            <template v-for="k in subtitleKeys" :key="k">{{ text(row, k) }}&nbsp;</template>
          </span>
          <span v-if="metaKeys.length" class="card__meta">
            <span v-for="k in metaKeys" :key="k" class="card__badge">{{ text(row, k) }}</span>
          </span>
        </component>

        <p v-if="primaryField" class="card__primary" :data-align="primaryField.align ?? 'end'">
          <span class="card__primary-value" :data-tone="primaryField.tone ?? 'neutral'">
            {{ fieldText(row, primaryField) }}
          </span>
          <span class="card__primary-label">{{ primaryField.label }}</span>
        </p>

        <dl v-if="visibleFields.length" class="card__fields">
          <div v-for="f in visibleFields" :key="f.key" class="card__field">
            <dt>{{ f.label }}</dt>
            <dd :data-align="f.align ?? 'start'" :data-tone="fieldTone(row, f)">
              {{ fieldText(row, f) }}
            </dd>
          </div>
        </dl>

        <div v-if="$slots.actions" class="card__actions">
          <slot name="actions" :row="row" :index="i" />
        </div>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.card-list__items {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-2);
}

.card {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  padding: 12px;
}

.card__head {
  display: flex;
  flex-direction: column;
  gap: 4px;
  width: 100%;
  background: none;
  border: 0;
  padding: 0;
  color: inherit;
  font: inherit;
  text-align: start;
}

.card--clickable .card__head {
  min-height: 48px;
  cursor: pointer;
}

.card__title {
  font-size: 16px;
  font-weight: 600;
}

.card__subtitle {
  font-size: 13px;
  color: var(--kx-muted);
}

.card__meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 2px;
}

.card__badge {
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--kx-bg);
  color: var(--kx-muted);
}

.card__primary {
  display: flex;
  align-items: baseline;
  justify-content: flex-end;
  gap: 6px;
  margin: 10px 0 0;
}

.card__primary[data-align='start'] {
  justify-content: flex-start;
}

.card__primary-value {
  font-size: 20px;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
}

.card__primary-value[data-tone='good'] { color: var(--kx-success); }
.card__primary-value[data-tone='warn'] { color: var(--kx-warning); }
.card__primary-value[data-tone='danger'] { color: var(--kx-danger); }

.card__primary-label {
  font-size: 12px;
  color: var(--kx-muted);
}

.card__fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px 10px;
  margin: 10px 0 0;
}

.card__field {
  min-width: 0;
}

.card__field dt {
  font-size: 11px;
  color: var(--kx-muted);
}

.card__field dd {
  margin: 0;
  font-size: 14px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.card__field dd[data-align='end'] {
  text-align: end;
  font-variant-numeric: tabular-nums;
}

.card__field dd[data-tone='good'] { color: var(--kx-success); }
.card__field dd[data-tone='warn'] { color: var(--kx-warning); }
.card__field dd[data-tone='danger'] { color: var(--kx-danger); }

.card__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

/*
 * ★ 间距挂在**子元素**上，不挂在容器上。2026-10-06 实测踩过的坑：
 *   容器是「槽存在就渲染」的，调用方按行条件给动作时，其余各行仍留下一个空容器 ——
 *   `margin-top` 挂在容器上就会让每张无动作的卡片底部凭空多 10px 空白。
 *
 *   曾想用 `.card__actions:empty { display: none }` 收口，**实测不成立**：
 *   空的插槽片段在 DOM 里留下的是两个 `nodeValue === ""` 的 `#text` 节点
 *   （不是空白、也不是注释节点），`:empty` 要求「一个子节点都没有」，
 *   于是永远不命中。改挂子元素后，空容器高度为 0、也不吃间距。
 */
.card__actions > * {
  margin-top: 10px;
}

/* 卡片内动作按钮的触控下限（新增 Android 控件基线） */
.card__actions :deep(button) {
  min-height: 48px;
}
</style>
