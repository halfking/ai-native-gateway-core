<script setup lang="ts">
// ProviderResponsePanel.vue —— 右侧「供应商端响应」面板（2026-10-17）
//
// 维护者要求：右侧显示供应商端响应的请求，**可以点击放大**。
// 放大不做成 el-dialog/el-drawer（本仓 ui-audit 对弹层有形态契约，且弹窗会遮挡
// 拓扑），而是发出 `zoom` 事件由父层渲染**内联放大面板**（覆盖式、可关），
// 这样拓扑图始终在视野里，放大的是「细节」而不是「另一个页面」。

import { useI18n } from 'vue-i18n'
import type { PanelEntry } from '../../composables/useLiveTopology'

defineProps<{
  responses: PanelEntry[]
  activeRequestId?: string
}>()

const emit = defineEmits<{
  (e: 'select-request', requestId: string): void
  (e: 'zoom', entry: PanelEntry): void
}>()

const { t } = useI18n()

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

function tokens(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '—'
  return value >= 1000 ? `${(value / 1000).toFixed(1)}K` : String(value)
}
</script>

<template>
  <aside class="pp" aria-label="provider responses">
    <header class="pp__head">
      <span class="pp__title">{{ t('liveTopology.panel.providers') }}</span>
      <span class="pp__hint">{{ t('liveTopology.panel.zoomHint') }}</span>
    </header>
    <ul class="pp__list">
      <li v-for="entry in responses" :key="entry.requestId">
        <div class="pp__row" :class="[statusClass(entry.status), { 'is-active': entry.requestId === activeRequestId, 'is-probe': entry.isProbe }]">
          <button type="button" class="pp__main" @click="emit('select-request', entry.requestId)">
            <span class="pp__line">
              <span class="pp__time">{{ clock(entry.ts) }}</span>
              <span class="pp__provider">{{ entry.provider }}</span>
              <span class="pp__latency">{{ latency(entry.latencyMs) }}</span>
            </span>
            <span class="pp__line pp__line--sub">
              <span class="pp__model">{{ entry.model }}</span>
              <span class="pp__tokens">{{ tokens(entry.tokens) }} tok</span>
            </span>
            <span v-if="entry.errorKind" class="pp__error">{{ entry.errorKind }}</span>
          </button>
          <button
            type="button"
            class="pp__zoom"
            :aria-label="`${t('liveTopology.panel.zoom')} ${entry.requestId}`"
            :title="t('liveTopology.panel.zoom')"
            @click="emit('zoom', entry)"
          >
            ⤢
          </button>
        </div>
      </li>
    </ul>
    <p v-if="!responses.length" class="pp__empty">{{ t('liveTopology.panel.empty') }}</p>
  </aside>
</template>

<style scoped>
.pp {
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

.pp__head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 6px;
  padding: 0 4px 6px;
  border-bottom: 1px solid var(--kx-border);
}

.pp__title {
  font-size: var(--kx-font-sm, 12px);
  color: var(--kx-text);
}

.pp__hint {
  font-size: 10px;
  color: var(--kx-muted);
}

.pp__list {
  flex: 1;
  min-height: 0;
  margin: 0;
  padding: 0;
  list-style: none;
  overflow-y: auto;
}

.pp__row {
  display: flex;
  align-items: stretch;
  gap: 2px;
  margin: 3px 0;
  border: 1px solid transparent;
  border-left-width: 3px;
  border-radius: 7px;
  background: var(--kx-bg-accent);
}

.pp__row.is-success {
  border-left-color: var(--kx-success);
}

.pp__row.is-failure {
  border-left-color: var(--kx-danger);
}

.pp__row.is-in-progress {
  border-left-color: var(--kx-primary);
}

.pp__row:hover,
.pp__row.is-active {
  border-color: var(--kx-primary);
}

/* 探测流量：与普通请求区分开（探针失败不该被当成真实业务故障看） */
.pp__row.is-probe {
  opacity: 0.82;
  border-left-style: dashed;
}

.pp__main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
  padding: 5px 6px;
  border: 0;
  background: transparent;
  cursor: pointer;
  text-align: left;
  color: inherit;
}

.pp__line {
  display: flex;
  align-items: baseline;
  gap: 4px;
  font-size: 11px;
  color: var(--kx-text);
}

.pp__line--sub {
  color: var(--kx-muted);
}

.pp__time,
.pp__latency {
  font-variant-numeric: tabular-nums;
  color: var(--kx-muted);
}

.pp__provider,
.pp__model {
  flex: 1;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.pp__tokens {
  font-variant-numeric: tabular-nums;
}

.pp__error {
  font-size: 10px;
  color: var(--kx-danger);
}

.pp__zoom {
  padding: 0 7px;
  border: 0;
  background: transparent;
  color: var(--kx-muted);
  font-size: 13px;
  cursor: pointer;
}

.pp__zoom:hover {
  color: var(--kx-primary);
}

.pp__empty {
  margin: 8px 4px 0;
  font-size: 11px;
  color: var(--kx-muted);
}
</style>
