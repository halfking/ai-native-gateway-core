<script setup lang="ts">
// ClientRequestPanel.vue —— 左侧「客户端请求摘要」面板（2026-10-17）
//
// 维护者要求：左侧显示**客户端的请求摘要列表**。
// 两段结构（先看聚合、再看明细），因为只给明细的话客户端一多就成流水账：
//   · 顶部按客户端聚合的摘要（请求数 / 成功率 / 在途 / 主要模型）
//   · 下面是这些客户端的逐条请求（最新在前），点条目 → 主图高亮该请求链路

import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ClientSummary, PanelEntry } from '../../composables/useLiveTopology'

const props = defineProps<{
  summaries: ClientSummary[]
  requests: PanelEntry[]
  activeRequestId?: string
}>()

const emit = defineEmits<{
  (e: 'select-request', requestId: string): void
}>()

const { t } = useI18n()

const visibleSummaries = computed(() => props.summaries.slice(0, 8))

/**
 * 表头的客户端数。
 * **必须与下面实际渲染的行数一致**：只显示 `summaries.length`（上限 60）
 * 而正文只画 8 行的话，表头就在宣传不存在的行，同时把真实总数藏起来。
 * 超出时补成「8 / N」，两边对得上账。
 */
const headerCount = computed(() =>
  props.summaries.length > visibleSummaries.value.length
    ? `${visibleSummaries.value.length} / ${props.summaries.length}`
    : String(visibleSummaries.value.length),
)

function rate(summary: ClientSummary): string {
  if (!summary.requests) return '—'
  return `${((summary.success / summary.requests) * 100).toFixed(0)}%`
}

function statusClass(status: string): string {
  switch (status) {
    case 'success':
      return 'is-success'
    case 'failure':
    case 'rate_limited':
      return 'is-failure'
    default:
      return 'is-in-progress'
  }
}

function clock(ts: string): string {
  const at = Date.parse(ts)
  return Number.isFinite(at) ? new Date(at).toLocaleTimeString([], { hour12: false }) : '--:--:--'
}

function latency(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '—'
  return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${Math.round(value)}ms`
}
</script>

<template>
  <aside class="cp" aria-label="client request summary">
    <header class="cp__head">
      <span class="cp__title">{{ t('liveTopology.panel.clients') }}</span>
      <span class="cp__count">{{ headerCount }}</span>
    </header>

    <ul class="cp__summary">
      <li v-for="s in visibleSummaries" :key="s.client" class="cp__summary-row">
        <span class="cp__summary-name">{{ s.client }}</span>
        <span class="cp__summary-stats">
          <b>{{ s.requests }}</b>
          <i>{{ rate(s) }}</i>
          <em v-if="s.inFlight">{{ s.inFlight }}↻</em>
        </span>
        <span class="cp__summary-model">{{ s.topModel || '—' }}</span>
      </li>
    </ul>

    <ul class="cp__list">
      <li v-for="entry in requests" :key="entry.requestId">
        <button
          type="button"
          class="cp__item"
          :class="[statusClass(entry.status), { 'is-active': entry.requestId === activeRequestId, 'is-probe': entry.isProbe }]"
          @click="emit('select-request', entry.requestId)"
        >
          <span class="cp__row">
            <span class="cp__time">{{ clock(entry.ts) }}</span>
            <span class="cp__client">{{ entry.client }}</span>
            <span class="cp__latency">{{ latency(entry.latencyMs) }}</span>
          </span>
          <span class="cp__row cp__row--sub">
            <span class="cp__model">{{ entry.model }}</span>
            <span class="cp__provider">{{ entry.provider }}</span>
          </span>
          <span v-if="entry.errorKind" class="cp__error">{{ entry.errorKind }}</span>
        </button>
      </li>
    </ul>
    <p v-if="!requests.length" class="cp__empty">{{ t('liveTopology.panel.empty') }}</p>
  </aside>
</template>

<style scoped>
.cp {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  padding: 8px 6px;
  border: 1px solid var(--kx-border);
  border-radius: 12px;
  background: var(--kx-surface);
  overflow: hidden;
}

.cp__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 4px 6px;
  border-bottom: 1px solid var(--kx-border);
}

.cp__title {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
}

.cp__count {
  font-size: 11px;
  color: var(--kx-muted);
}

.cp__summary {
  margin: 0 0 6px;
  padding: 0;
  list-style: none;
}

.cp__summary-row {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 1px 6px;
  padding: 4px;
  border-bottom: 1px dashed color-mix(in srgb, var(--kx-border) 60%, transparent);
}

.cp__summary-name {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.cp__summary-stats {
  display: flex;
  gap: 5px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--kx-text);
}

.cp__summary-stats i {
  color: var(--kx-muted);
  font-style: normal;
}

.cp__summary-stats em {
  color: var(--kx-primary);
  font-style: normal;
}

.cp__summary-model {
  grid-column: 1 / -1;
  font-size: 10px;
  color: var(--kx-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.cp__list {
  flex: 1;
  min-height: 0;
  margin: 0;
  padding: 0;
  list-style: none;
  overflow-y: auto;
}

.cp__item {
  display: flex;
  flex-direction: column;
  gap: 1px;
  width: 100%;
  margin: 3px 0;
  padding: 5px 6px;
  border: 1px solid transparent;
  border-left-width: 3px;
  border-radius: 7px;
  background: var(--kx-bg-accent);
  cursor: pointer;
  text-align: left;
  color: inherit;
}

.cp__item:hover,
.cp__item.is-active {
  border-color: var(--kx-primary);
}

.cp__item.is-success {
  border-left-color: var(--kx-success);
}

.cp__item.is-failure {
  border-left-color: var(--kx-danger);
}

.cp__item.is-in-progress {
  border-left-color: var(--kx-primary);
}

.cp__row {
  display: flex;
  align-items: baseline;
  gap: 4px;
  font-size: 11px;
  color: var(--kx-text);
}

.cp__row--sub {
  color: var(--kx-muted);
}

.cp__time,
.cp__latency {
  font-variant-numeric: tabular-nums;
  color: var(--kx-muted);
}

.cp__client,
.cp__model,
.cp__provider {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.cp__client {
  flex: 1;
}

.cp__model {
  flex: 1;
}

.cp__error {
  font-size: 10px;
  color: var(--kx-danger);
}

.cp__empty {
  margin: 8px 4px 0;
  font-size: 11px;
  color: var(--kx-muted);
}
</style>
