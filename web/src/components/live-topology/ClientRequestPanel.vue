<script setup lang="ts">
// ClientRequestPanel.vue —— 左侧「客户端请求」面板（2026-10-18 分页版）
//
// 单段结构：逐条请求明细（最新在前）——**分页输出**，单页 ≤50 条（owner 拍板），
// 点条目 = 选中高亮 + 弹出摘要浮层（RequestZoomPanel，两步交互第一步）；
// 浮层里的「查看全景」/行尾 ↗ 才开新页（owner 2026-10-27 口径）。
// 按客户端聚合的摘要 2026-10-28 上移到头部三块汇总（SummaryHeader），
// 本面板从此只做明细这一件事。
//
// 滚动语义：列表本体可滚动；翻页后回到列表顶部。第 1 页是最新数据，
// 实时新请求到达时第 1 页自然滚动输出（prepend 最新在前）。

import { computed, ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PanelEntry } from '../../composables/useLiveTopology'
import { clientDisplayName } from '../../lib/liveTopologyGraph'
import { PANEL_PAGE_SIZE } from '../../types/liveTopology'
import { useNewArrivals } from '../../composables/useNewArrivals'
import { CaretLeft, CaretRight, TopRight, ArrowRight } from '@element-plus/icons-vue'

const props = defineProps<{
  requests: PanelEntry[]
  activeRequestId?: string
}>()

const emit = defineEmits<{
  (e: 'select-request', requestId: string): void
  (e: 'zoom', entry: PanelEntry): void
  (e: 'open-page', entry: PanelEntry): void
}>()

const { t } = useI18n()

const page = ref(1)
const pageCount = computed(() => Math.max(1, Math.ceil(props.requests.length / PANEL_PAGE_SIZE)))
/** 当前页条目（最新在前 ⇒ 第 1 页 = 最新 50 条）。 */
const pagedRequests = computed(() =>
  props.requests.slice((page.value - 1) * PANEL_PAGE_SIZE, page.value * PANEL_PAGE_SIZE),
)
/** 数据更新导致页数收缩时，把越界的当前页收回最后一页。 */
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

function onItem(entry: PanelEntry) {
  emit('select-request', entry.requestId)
  emit('zoom', entry)
}

/** 客户端易读名（owner 2026-10-18）：identity 键 → 品牌写法。 */
function displayName(client: string): string {
  return clientDisplayName(client)
}

/** 表头计数 = 请求明细总数（与分页器的「共 N 条」同源；聚合摘要已上移头部）。 */
const headerCount = computed(() => String(props.requests.length))

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

/** 新到达的请求行做入场动画（只有新行动，见 useNewArrivals 注释）。 */
const { fresh: freshIds } = useNewArrivals(() => props.requests.map((r) => r.requestId))
</script>

<template>
  <aside class="cp" aria-label="client requests">
    <header class="cp__head">
      <span class="cp__title">{{ t('liveTopology.panel.clients') }}</span>
      <span class="cp__count" data-testid="client-request-count">{{ headerCount }}</span>
    </header>

    <ul ref="listRef" class="cp__list" data-testid="client-request-list">
      <li v-for="entry in pagedRequests" :key="entry.requestId">
        <div
          class="cp__row"
          :class="[statusClass(entry.status), { 'is-active': entry.requestId === activeRequestId, 'is-probe': entry.isProbe, 'is-new': freshIds.has(entry.requestId) }]"
        >
          <button type="button" class="cp__item" @click="onItem(entry)">
            <span class="cp__line">
              <span class="cp__time">{{ clock(entry.ts) }}</span>
              <span class="cp__client">{{ displayName(entry.client) }}</span>
              <span class="cp__latency">{{ latency(entry.latencyMs) }}</span>
            </span>
            <span class="cp__line cp__line--sub">
              <!-- auto 请求：点名的是 auto，最终模型由网关按任务选出 —— 面板上
                   用「auto → 最终模型」把这一步表达出来（箭头用 SVG 图标：
                   行是 button，V3 门禁按钮位的 → 字形）。 -->
              <span class="cp__model">
                <template v-if="entry.isAuto">
                  <span
                    class="cp__auto-chip"
                    :data-testid="`auto-chip-${entry.requestId}`"
                    :title="entry.taskType ? `${t('liveTopology.panel.autoChip')} · ${entry.taskType}` : t('liveTopology.panel.autoChip')"
                  >auto</span>
                  <ArrowRight class="cp__auto-arrow" aria-hidden="true" />
                </template>
                <span class="cp__model-name">{{ entry.model }}</span>
              </span>
              <span class="cp__provider">{{ entry.provider }}</span>
            </span>
            <span v-if="entry.errorKind" class="cp__error">{{ entry.errorKind }}</span>
          </button>
          <button
            type="button"
            class="cp__open"
            :data-testid="`client-open-page-${entry.requestId}`"
            :aria-label="t('liveTopology.zoom.openPage')"
            :title="t('liveTopology.zoom.openPage')"
            @click.stop="emit('open-page', entry)"
          ><TopRight /></button>
        </div>
      </li>
    </ul>
    <p v-if="!requests.length" class="cp__empty">{{ t('liveTopology.panel.empty') }}</p>

    <footer v-if="requests.length" class="cp__pager" data-testid="client-request-pager">
      <button
        type="button"
        class="cp__page-btn"
        :disabled="page <= 1"
        data-testid="client-page-prev"
        @click="prevPage"
      ><CaretLeft /></button>
      <span class="cp__page-info">{{ t('liveTopology.panel.page', { page, pages: pageCount }) }} · {{ t('liveTopology.panel.total', { n: requests.length }) }}</span>
      <button
        type="button"
        class="cp__page-btn"
        :disabled="page >= pageCount"
        data-testid="client-page-next"
        @click="nextPage"
      ><CaretRight /></button>
    </footer>
  </aside>
</template>

<style scoped>
.cp {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  padding: 8px 4px;
  border: 1px solid var(--kx-border);
  border-radius: 12px;
  background: var(--kx-surface);
  overflow: hidden;
}

.cp__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 4px 4px;
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

.cp__list {
  flex: 1;
  min-height: 0;
  margin: 0;
  padding: 0;
  list-style: none;
  overflow-y: auto;
}

/* 行容器（主按钮 + 全页打开 ↗）—— 状态色挂在整块 1px 描边上，
   主按钮本身保持透明（button 嵌 button 不合法，拆成兄弟）。
   ★ 原来左沿是 3px 粗边（`border-left-width: 3px`），那是 craft-floor
     硬禁的「卡片单边色条」—— 状态改由整块描边表达。 */
.cp__row {
  display: flex;
  align-items: stretch;
  gap: 2px;
  margin: 3px 0;
  border: 1px solid transparent;
  border-radius: 7px;
  background: var(--kx-bg-accent);
}

.cp__row.is-success {
  border-color: var(--kx-success);
}

.cp__row.is-failure {
  border-color: var(--kx-danger);
}

.cp__row.is-in-progress {
  border-color: var(--kx-primary);
}

/* 探测流量：与普通请求区分开（探针失败不该被当成真实业务故障看）。
   原来靠左沿 3px 虚线区分；左沿粗边已移除，改为整块虚线描边。 */
.cp__row.is-probe {
  opacity: 0.82;
  border-style: dashed;
}

.cp__row:hover,
.cp__row.is-active {
  border-color: var(--kx-primary);
}

.cp__item {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 1px;
  padding: 4px 4px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  cursor: pointer;
  text-align: left;
  color: inherit;
}

.cp__open {
  flex: none;
  padding: 0 4px;
  border: 0;
  background: transparent;
  color: var(--kx-muted);
  font-size: 12px;
  cursor: pointer;
}

.cp__open:hover {
  color: var(--kx-primary);
}

.cp__line {
  display: flex;
  align-items: baseline;
  gap: 4px;
  font-size: 11px;
  color: var(--kx-text);
}

.cp__line--sub {
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

/* auto 请求的「auto → 最终模型」前缀（2026-10-28 选路层轮）。
   chip 是产品术语（model=auto 的字面值，不进语言包）；箭头是 SVG 图标 ——
   按钮位禁用 → 字形（V3），且裸 <svg viewBox> 必须显式定尺寸。 */
.cp__model {
  display: flex;
  align-items: center;
  gap: 3px;
}

.cp__auto-chip {
  flex: none;
  padding: 0 4px;
  border-radius: 999px;
  font-size: 11px;
  line-height: 14px;
  color: var(--kx-primary);
  border: 1px solid color-mix(in srgb, var(--kx-primary) 45%, var(--kx-border));
  background: color-mix(in srgb, var(--kx-surface) 82%, transparent);
}

.cp__auto-arrow {
  flex: none;
  color: var(--kx-muted);
}

.cp__auto-arrow > svg {
  width: 10px;
  height: 10px;
}

.cp__model-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cp__error {
  font-size: 11px;
  color: var(--kx-danger);
}

.cp__empty {
  margin: 8px 4px 0;
  font-size: 11px;
  color: var(--kx-muted);
}

.cp__pager {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 4px 4px 0;
  border-top: 1px solid var(--kx-border);
}

.cp__page-btn {
  min-width: 22px;
  height: 20px;
  padding: 0 4px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: transparent;
  color: var(--kx-text);
  font-size: 11px;
  line-height: 1;
  cursor: pointer;
}

.cp__page-btn:disabled {
  opacity: 0.4;
  cursor: default;
}

.cp__page-info {
  font-size: 11px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

/* 新请求到达：只有新出现的行做入场（useNewArrivals 保证旧行不动）。
   300ms 一次性入场（≤800ms 上限），指数减速；起始帧 opacity:0
   ⇒ 入场类 ⇒ 已在 design-motion-decisions.json 登记。 */
.cp__row.is-new {
  animation: cp-row-in 0.3s var(--kx-motion-ease-out);
}

@keyframes cp-row-in {
  from { opacity: 0; transform: translateY(-8px); }
  to { opacity: 1; transform: none; }
}

@media (prefers-reduced-motion: reduce) {
  .cp__row.is-new {
    animation: none;
  }
}

/* ── 2026-10-27 第 17 增量（AI 痕迹门 V3）：图标尺寸 ──
   element-plus 图标是裸 <svg viewBox="0 0 1024 1024">，**不带 width/height**
   ⇒ 浏览器按包含块宽度渲染（会撑爆布局）⇒ 必须显式定尺寸。
   尺寸沿用原字形的 font-size 观感（字形墨迹 ≈ 0.7em）。 */
.cp__open > svg { width: 11px; height: 11px; }
.cp__page-btn > svg { width: 10px; height: 10px; }

/* ── 手机（≤768，2026-10-28 手机观察轮）：行是触控目标 ──
   抬高行高到 44px、加宽 ↗ 按钮的命中区（padding 0 4px → 0 10px），
   手指才点得中；桌面鼠标不受影响。 */
@media (max-width: 768px) {
  .cp__row { min-height: 44px; }

  .cp__open { padding: 0 8px; }
}
</style>
