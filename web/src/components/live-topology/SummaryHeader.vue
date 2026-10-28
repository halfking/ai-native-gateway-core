<script setup lang="ts">
// SummaryHeader.vue —— 头部汇总条（2026-10-28 汇总上移轮）
//
// owner 口径：汇总信息放头部，分【客户端 / 供应商 / 模型】三块。
// 此前客户端聚合摘要在左面板顶部（2026-10-28 手机观察轮还给它限过 176px）
// —— 本轮上移到头部，左面板从此只做「逐条请求明细」这一件事。
//
// ★ 三块共用同一过滤子集（与图、两栏收敛 —— LiveTopologyView 顶部的
//   关键约束）；数据形状由父层映射成统一的 SummaryRow，本组件不感知
//   客户端易读名 / provider_code 等领域细节。
// ★ 截断计数纪律（同 ClientRequestPanel 旧表头）：只画前 maxRows 行时，
//   表头必须写「maxRows / 总数」—— 只写总数 = 宣传不存在的行；
//   只写行数 = 把真实总数藏起来。
// ★ 行内数字不带单位词（34 · 92%）：块的标题已经说明这是什么维度，
//   每行再写「34 个请求」是重复；in-flight 用 ↻ 标记（沿用左面板旧摘要
//   的记法）。

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

export interface SummaryRow {
  name: string
  requests: number
  success: number
  failure: number
  inFlight: number
}

const props = withDefaults(
  defineProps<{
    clients: SummaryRow[]
    providers: SummaryRow[]
    models: SummaryRow[]
    /** 每块最多渲染几行（超出截断，表头如实写「N / 总数」）。 */
    maxRows?: number
  }>(),
  { maxRows: 4 },
)

const { t } = useI18n()

/** 缺请求时成功率显示 em dash，不显示 0%（假零禁令：没数据 ≠ 零）。 */
function rate(row: SummaryRow): string {
  if (!row.requests) return '—'
  return `${Math.round((row.success / row.requests) * 100)}%`
}

function shown(rows: SummaryRow[]): SummaryRow[] {
  return rows.slice(0, props.maxRows)
}

/** 「N / 总数」或恰好放得下时的「N」。 */
function headCount(rows: SummaryRow[]): string {
  return rows.length > props.maxRows ? `${props.maxRows} / ${rows.length}` : String(rows.length)
}

const blocks = computed(() => [
  { key: 'clients', titleKey: 'liveTopology.summary.clients', rows: props.clients },
  { key: 'providers', titleKey: 'liveTopology.summary.providers', rows: props.providers },
  { key: 'models', titleKey: 'liveTopology.summary.models', rows: props.models },
])
</script>

<template>
  <section class="sumhead" aria-label="summary">
    <div v-for="block in blocks" :key="block.key" class="sumhead__block" :data-testid="`summary-block-${block.key}`">
      <header class="sumhead__head">
        <span class="sumhead__title">{{ t(block.titleKey) }}</span>
        <span class="sumhead__count">{{ headCount(block.rows) }}</span>
      </header>
      <ul class="sumhead__list">
        <li v-for="row in shown(block.rows)" :key="row.name" class="sumhead__row">
          <span class="sumhead__name" :title="row.name">{{ row.name }}</span>
          <span class="sumhead__stats">
            <b>{{ row.requests }}</b>
            <i>{{ rate(row) }}</i>
            <em v-if="row.inFlight">{{ row.inFlight }}↻</em>
          </span>
        </li>
      </ul>
      <p v-if="!block.rows.length" class="sumhead__empty">{{ t('liveTopology.panel.empty') }}</p>
    </div>
  </section>
</template>

<style scoped>
/* 头部三块汇总：桌面三列并排；窄屏单列堆叠（断点白名单 768）。
   高度要克制 —— 头部每长一像素，下面的请求明细区就矮一像素。 */
.sumhead {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
  gap: 4px 12px;
}

.sumhead__block {
  min-width: 0;
  padding: 4px 8px;
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  background: var(--kx-surface);
}

.sumhead__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 4px;
  border-bottom: 1px dashed color-mix(in srgb, var(--kx-border) 60%, transparent);
  padding-bottom: 2px;
}

.sumhead__title {
  font-size: var(--kx-font-sm, 12px);
  font-weight: 700;
  color: var(--kx-text);
}

.sumhead__count {
  font-size: 11px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.sumhead__list {
  margin: 2px 0 0;
  padding: 0;
  list-style: none;
}

.sumhead__row {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  padding: 1px 0;
  font-size: 11px;
}

.sumhead__name {
  min-width: 0;
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.sumhead__stats {
  display: flex;
  gap: 4px;
  flex: none;
  font-variant-numeric: tabular-nums;
  color: var(--kx-text);
}

.sumhead__stats i {
  color: var(--kx-muted);
  font-style: normal;
}

/* 在途标记沿用左面板旧摘要的记法（数字 + ↻，主色）。 */
.sumhead__stats em {
  color: var(--kx-primary);
  font-style: normal;
}

.sumhead__empty {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--kx-muted);
}

@media (min-width: 768px) {
  .sumhead {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}
</style>
