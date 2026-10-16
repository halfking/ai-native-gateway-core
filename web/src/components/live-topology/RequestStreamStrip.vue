<script setup lang="ts">
// RequestStreamStrip.vue — 最右侧窄条请求流水（2026-10-16）
//
// 需求原话：「在最右侧显示一个窄条的请求」。窄 = 不抢主图的注意力，
// 但每条必须带足定位信息：谁（客户端）→ 走哪（供应商/模型）→ 成没成（状态）
// → 多快（延迟）。点击把该请求的链路在主图上高亮。

import { useI18n } from 'vue-i18n'
import type { StripEntry } from '../../composables/useLiveTopology'

defineProps<{
  entries: StripEntry[]
  activeRequestId?: string
}>()

const emit = defineEmits<{
  (e: 'select-request', requestId: string): void
}>()

const { t } = useI18n()

function statusClass(status: string): string {
  switch (status) {
    case 'success':
      return 'is-success'
    case 'failure':
      return 'is-failure'
    case 'rate_limited':
      return 'is-rate-limited'
    default:
      return 'is-in-progress'
  }
}

function latencyLabel(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '—'
  return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${Math.round(value)}ms`
}

function clock(ts: string): string {
  const at = Date.parse(ts)
  return Number.isFinite(at) ? new Date(at).toLocaleTimeString([], { hour12: false }) : '--:--:--'
}
</script>

<template>
  <aside class="strip" aria-label="live request stream">
    <header class="strip__head">
      <span class="strip__title">{{ t('liveTopology.strip.title') }}</span>
      <span class="strip__count">{{ entries.length }}</span>
    </header>
    <ul class="strip__list">
      <li v-for="entry in entries" :key="entry.requestId">
        <button
          type="button"
          class="strip__item"
          :class="[statusClass(entry.status), { 'is-active': entry.requestId === activeRequestId, 'is-probe': entry.isProbe }]"
          @click="emit('select-request', entry.requestId)"
        >
          <span class="strip__row">
            <span class="strip__time">{{ clock(entry.ts) }}</span>
            <span class="strip__model">{{ entry.model }}</span>
            <span class="strip__latency">{{ latencyLabel(entry.latencyMs) }}</span>
          </span>
          <span class="strip__row strip__row--sub">
            <span class="strip__client">{{ entry.label }}</span>
            <span class="strip__provider">{{ entry.provider }}</span>
          </span>
          <span v-if="entry.errorKind" class="strip__error">{{ entry.errorKind }}</span>
        </button>
      </li>
    </ul>
    <p v-if="!entries.length" class="strip__empty">{{ t('liveTopology.strip.empty') }}</p>
  </aside>
</template>

<style scoped>
.strip {
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

.strip__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 4px 6px;
  border-bottom: 1px solid var(--kx-border);
}

.strip__title {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
}

.strip__count {
  font-size: 11px;
  color: var(--kx-muted);
}

.strip__list {
  flex: 1;
  min-height: 0;
  margin: 0;
  padding: 0;
  list-style: none;
  overflow-y: auto;
}

.strip__item {
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

.strip__item:hover,
.strip__item.is-active {
  border-color: var(--kx-primary);
}

.strip__item.is-success {
  border-left-color: var(--kx-success);
}

.strip__item.is-failure {
  border-left-color: var(--kx-danger);
}

.strip__item.is-rate-limited {
  border-left-color: var(--kx-warning);
}

.strip__item.is-in-progress {
  border-left-color: var(--kx-primary);
}

.strip__row {
  display: flex;
  align-items: baseline;
  gap: 4px;
  font-size: 11px;
  color: var(--kx-text);
}

.strip__row--sub {
  color: var(--kx-muted);
}

.strip__time {
  font-variant-numeric: tabular-nums;
  color: var(--kx-muted);
}

.strip__model {
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.strip__latency {
  font-variant-numeric: tabular-nums;
  color: var(--kx-muted);
}

.strip__client,
.strip__provider {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.strip__error {
  font-size: 10px;
  color: var(--kx-danger);
}

.strip__empty {
  margin: 8px 4px 0;
  font-size: 11px;
  color: var(--kx-muted);
}
</style>
