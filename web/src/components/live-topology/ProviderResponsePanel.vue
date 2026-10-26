<script setup lang="ts">
// ProviderResponsePanel.vue —— 右侧「供应商端响应」面板（2026-10-18 分页版）
//
// 右侧显示供应商端响应的请求，**可以点击放大**（内联覆盖面板，不是弹窗 ——
// 本仓 ui-audit 对弹层有形态契约，且弹窗会遮挡拓扑）。
// 2026-10-18 owner 拍板：分页输出，单页 ≤50 条；点行即看详情（⤢ 保留）。

import { computed, ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PanelEntry } from '../../composables/useLiveTopology'
import { clientDisplayName } from '../../lib/liveTopologyGraph'
import { PANEL_PAGE_SIZE } from '../../types/liveTopology'
import { CaretLeft, CaretRight, FullScreen, TopRight } from '@element-plus/icons-vue'

const props = defineProps<{
  responses: PanelEntry[]
  activeRequestId?: string
}>()

const emit = defineEmits<{
  (e: 'select-request', requestId: string): void
  (e: 'zoom', entry: PanelEntry): void
  (e: 'open-page', entry: PanelEntry): void
}>()

/** 客户端易读名（owner 2026-10-18）。 */
function displayName(client: string): string {
  return clientDisplayName(client)
}

const { t } = useI18n()

const page = ref(1)
const pageCount = computed(() => Math.max(1, Math.ceil(props.responses.length / PANEL_PAGE_SIZE)))
const pagedResponses = computed(() =>
  props.responses.slice((page.value - 1) * PANEL_PAGE_SIZE, page.value * PANEL_PAGE_SIZE),
)
watch(pageCount, (pages) => {
  if (page.value > pages) page.value = pages
})

const listRef = ref<HTMLElement | null>(null)
watch(page, async () => {
  await nextTick()
  if (listRef.value) listRef.value.scrollTop = 0
})

function prevPage() {
  if (page.value > 1) page.value -= 1
}

function nextPage() {
  if (page.value < pageCount.value) page.value += 1
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

/** token 量。缺省返回 `—`（**不带单位**）：拼成「— tok」等于把
 *  「没有数据」说成一个零值，违反本仓的假零禁令。 */
function tokens(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '—'
  return value >= 1000 ? `${(value / 1000).toFixed(1)}K tok` : `${value} tok`
}
</script>

<template>
  <aside class="pp" aria-label="provider responses">
    <header class="pp__head">
      <span class="pp__title">{{ t('liveTopology.panel.providers') }}</span>
      <span class="pp__hint">{{ t('liveTopology.panel.zoomHint') }}</span>
    </header>
    <ul ref="listRef" class="pp__list" data-testid="provider-response-list">
      <li v-for="entry in pagedResponses" :key="entry.requestId">
        <div class="pp__row" :class="[statusClass(entry.status), { 'is-active': entry.requestId === activeRequestId, 'is-probe': entry.isProbe }]">
          <button type="button" class="pp__main" @click="emit('zoom', entry)">
            <span class="pp__line">
              <span class="pp__time">{{ clock(entry.ts) }}</span>
              <span class="pp__provider">{{ entry.provider }}</span>
              <span class="pp__latency">{{ latency(entry.latencyMs) }}</span>
            </span>
            <span class="pp__line pp__line--sub">
              <span class="pp__model">{{ entry.model }}</span>
              <span class="pp__tokens">{{ tokens(entry.tokens) }}</span>
            </span>
            <span v-if="entry.errorKind" class="pp__error">{{ entry.errorKind }}</span>
          </button>
          <button
            type="button"
            class="pp__page"
            :data-testid="`provider-open-page-${entry.requestId}`"
            :aria-label="t('liveTopology.zoom.openPage')"
            :title="t('liveTopology.zoom.openPage')"
            @click.stop="emit('open-page', entry)"
          ><TopRight /></button>
          <button
            type="button"
            class="pp__zoom"
            :aria-label="`${t('liveTopology.panel.zoom')} ${entry.requestId}`"
            :title="t('liveTopology.panel.zoom')"
            @click="emit('zoom', entry)"
          >
            <FullScreen />
          </button>
        </div>
      </li>
    </ul>
    <p v-if="!responses.length" class="pp__empty">{{ t('liveTopology.panel.empty') }}</p>

    <footer v-if="responses.length" class="pp__pager" data-testid="provider-response-pager">
      <button
        type="button"
        class="pp__page-btn"
        :disabled="page <= 1"
        data-testid="provider-page-prev"
        @click="prevPage"
      ><CaretLeft /></button>
      <span class="pp__page-info">{{ t('liveTopology.panel.page', { page, pages: pageCount }) }} · {{ t('liveTopology.panel.total', { n: responses.length }) }}</span>
      <button
        type="button"
        class="pp__page-btn"
        :disabled="page >= pageCount"
        data-testid="provider-page-next"
        @click="nextPage"
      ><CaretRight /></button>
    </footer>
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
  border-radius: 7px;
  background: var(--kx-bg-accent);
}

.pp__row.is-success {
  border-color: var(--kx-success);
}

.pp__row.is-failure {
  border-color: var(--kx-danger);
}

.pp__row.is-in-progress {
  border-color: var(--kx-primary);
}

.pp__row:hover,
.pp__row.is-active {
  border-color: var(--kx-primary);
}

/* 探测流量：与普通请求区分开（探针失败不该被当成真实业务故障看）。
   原来靠左沿 3px 虚线区分；左沿粗边已移除，改为整块虚线描边。 */
.pp__row.is-probe {
  opacity: 0.82;
  border-style: dashed;
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

.pp__page {
  padding: 0 5px;
  border: 0;
  background: transparent;
  color: var(--kx-muted);
  font-size: 12px;
  cursor: pointer;
}

.pp__page:hover {
  color: var(--kx-primary);
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

.pp__pager {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 5px 4px 0;
  border-top: 1px solid var(--kx-border);
}

.pp__page-btn {
  min-width: 22px;
  height: 20px;
  padding: 0 6px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: transparent;
  color: var(--kx-text);
  font-size: 11px;
  line-height: 1;
  cursor: pointer;
}

.pp__page-btn:disabled {
  opacity: 0.4;
  cursor: default;
}

.pp__page-info {
  font-size: 10px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。
   尺寸沿用原字形的 font-size 观感（字形墨迹 ≈ 0.7em）。 */
.pp__open > svg { width: 11px; height: 11px; }
.pp__zoom > svg { width: 12px; height: 12px; }
.pp__page-btn > svg { width: 10px; height: 10px; }
</style>
