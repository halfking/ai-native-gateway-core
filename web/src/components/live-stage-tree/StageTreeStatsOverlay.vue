<script setup lang="ts">
// StageTreeStatsOverlay.vue —— 统计的**全屏统计层**（2026-11-16 起；2026-11-17 按设计补全）
//
// owner 2026-11-16 原话：「统计点开可以展开全页形式，盖在动态显示的上层」；
// owner 2026-11-17（设计第 10 轮）追加：「显示的层应该**占满全屏**，并显示相关的数据，
// 统计数据要**完整**，并且**动态**。每个模型、每个供应商、每个用户、top5 的 apikey
// 等分类统计数据包括请求数、token 数据、成功率、费用估算、成本估算、及缓存率等等。
// 同时要有**周期统计**数据的展示。」
//
// ⇒ 本层的四段式版面（与设计一致）：
//   ① 总览 KPI（请求数 / 成功率 / 在途 / 失败 / 耗时 / token / 成本 / 缓存 / 维度计数）
//   ② 周期统计（按分钟分桶的请求数与成功率，空桶也保留 ⇒ 断流看得见）
//   ③ 分类统计（模型 / 供应商 / 客户端 / 凭据 / 状态 五个维度 × 请求数/成功率/
//      在途/token/成本/缓存率/平均耗时）
//   ④ 口径区（把「这些数是怎么来的」写在界面上，不藏在角标里）
//   ＋ 折叠区：**数据库口径**（近 5 分钟 / 1 小时直查、当天日桶）—— 原 StatsPanel。
//
// ★ **两把尺的关系**（必须写清，否则读者会把两套口径当成互相矛盾）：
//   · 本层主体 = **内存实时窗口**（顶部过滤条当前可见的请求）⇒ 与树上的带**同源**；
//   · 折叠区 = **数据库直查**（历史全量）⇒ 覆盖更长时间窗，但与内存窗口**不是同一口径**。
//   ⇒ 两者都在界面上标了自己的口径，不混算、不互相替代。
//
// 浮层自身的三条纪律：
//   1. 有 Esc 出口（不是只能点关闭按钮）；
//   2. 内容超长时**限高可滚**（统计不能被裁掉 —— 口径行必须看得见）；
//   3. 关闭后焦点回到触发它的按钮（键盘用户不至于迷路）。

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Close } from '@element-plus/icons-vue'
import StatsPanel from '../live-topology/StatsPanel.vue'
import {
  cacheAggregate,
  computeStats,
  dimRows,
  trendBuckets,
  type StatDim,
} from '../../lib/liveStageTreeStats'
import type { StageBand } from '../../types/liveStageTree'

const props = defineProps<{
  open: boolean
  /** 在途数（实时口径，来自阶段树数据）。 */
  inFlight?: number | null
  /** 队列深度（未接线时 null —— 隐藏，不冒充 0）。 */
  queueDepth?: number | null
  /** 当前可见的请求带（与树同源 ⇒ 统计与画面对得上）。 */
  bands: StageBand[]
  /** 时间窗文案（顶部过滤条当前窗口）。 */
  windowLabel?: string
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'select-client', key: string): void
  (e: 'select-provider', code: string): void
  (e: 'select-model', model: string): void
}>()

const { t } = useI18n()
const panelRef = ref<HTMLElement | null>(null)
/** 分类统计当前维度。 */
const dim = ref<StatDim>('model')
/** 数据库口径区是否展开（默认收起：它是**另一把尺**，不该抢主口径的注意力）。 */
const dbOpen = ref(false)

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.stopPropagation()
    emit('close')
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      document.addEventListener('keydown', onKeydown)
    } else {
      document.removeEventListener('keydown', onKeydown)
    }
  },
)

onMounted(() => {
  if (props.open) document.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))

/** 统计总览（内存窗口口径）。 */
const stats = computed(() => computeStats(props.bands))
/** 缓存命中率聚合（不跨口径合并）。 */
const cache = computed(() => cacheAggregate(props.bands))
/** 周期统计（分钟桶）。 */
const buckets = computed(() => trendBuckets(props.bands, 60_000))
/** 分类统计行。 */
const rows = computed(() => dimRows(props.bands, dim.value))
/** 周期统计里的最大请求数（画柱状图用）。 */
const maxBucket = computed(() =>
  Math.max(1, ...buckets.value.map(b => b.requests)),
)

/** 维度标签（★ 逐字面量 t()：动态键是引用扫描器的盲区）。 */
function dimLabel(d: StatDim): string {
  if (d === 'model') return t('liveStageTree.stats.dim.model')
  if (d === 'provider') return t('liveStageTree.stats.dim.provider')
  if (d === 'client') return t('liveStageTree.stats.dim.client')
  if (d === 'credential') return t('liveStageTree.stats.dim.credential')
  return t('liveStageTree.stats.dim.status')
}

const DIMS: StatDim[] = ['model', 'provider', 'client', 'credential', 'status']

/** 状态维度的行名（成功 / 进行中 / 失败）。 */
function statusLabel(key: string): string {
  if (key === 'success') return t('liveStageTree.band.status.success')
  if (key === 'in_progress') return t('liveStageTree.band.status.in_progress')
  if (key === 'failure') return t('liveStageTree.band.status.failure')
  return key
}

/** 时间桶标签（HH:MM）。 */
function bucketLabel(tsMs: number): string {
  const d = new Date(tsMs)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}`
}
</script>

<template>
  <div
    v-if="props.open"
    ref="panelRef"
    class="stage-stats-overlay"
    role="dialog"
    aria-modal="true"
    :aria-label="t('liveStageTree.stats.title')"
    data-testid="stage-stats-overlay"
  >
    <header class="stage-stats-overlay__bar">
      <h2 class="stage-stats-overlay__title">{{ t('liveStageTree.stats.title') }}</h2>
      <span class="stage-stats-overlay__scope" data-testid="stage-stats-scope">
        {{ t('liveStageTree.stats.scope', {
          window: props.windowLabel ?? '—',
          n: stats.total,
        }) }}
      </span>
      <span class="stage-stats-overlay__hint">{{ t('liveStageTree.stats.hint') }}</span>
      <button
        type="button"
        class="stage-stats-overlay__close"
        data-testid="stage-stats-close"
        :aria-label="t('liveStageTree.stats.close')"
        :title="t('liveStageTree.stats.close')"
        @click="emit('close')"
      >
        <Close />
      </button>
    </header>

    <div class="stage-stats-overlay__body">
      <!-- ① 总览 KPI -->
      <section class="stage-stats-overlay__sec">
        <div class="stage-stats-overlay__sechd">
          <b>{{ t('liveStageTree.stats.kpi.title') }}</b>
        </div>
        <div class="stage-stats-overlay__kpis" data-testid="stage-stats-kpis">
          <div class="kpi">
            <i>{{ t('liveStageTree.stats.kpi.requests') }}</i>
            <b>{{ stats.total }}</b>
          </div>
          <div class="kpi" :class="stats.successRate === null ? 'is-dim' : 'is-ok'">
            <i>{{ t('liveStageTree.stats.kpi.successRate') }}</i>
            <b>{{ stats.successRate === null ? '—' : `${stats.successRate}%` }}</b>
          </div>
          <div class="kpi is-live">
            <i>{{ t('liveStageTree.stats.kpi.inFlight') }}</i>
            <b>{{ stats.inFlight }}</b>
          </div>
          <div class="kpi" :class="stats.failed > 0 ? 'is-bad' : 'is-dim'">
            <i>{{ t('liveStageTree.stats.kpi.failed') }}</i>
            <b>{{ stats.failed }}</b>
          </div>
          <div class="kpi">
            <i>{{ t('liveStageTree.stats.kpi.avgLatency') }}</i>
            <b>{{ stats.avgLatencyMs === null ? '—' : `${stats.avgLatencyMs}ms` }}</b>
          </div>
          <div class="kpi">
            <i>{{ t('liveStageTree.stats.kpi.p95Latency') }}</i>
            <b>{{ stats.p95LatencyMs === null ? '—' : `${stats.p95LatencyMs}ms` }}</b>
          </div>
          <div class="kpi">
            <i>{{ t('liveStageTree.stats.kpi.totalTokens') }}</i>
            <b>{{ stats.totalTokens === null ? '—' : stats.totalTokens }}</b>
          </div>
          <div class="kpi">
            <i>{{ t('liveStageTree.stats.kpi.inputTokens') }}</i>
            <b>{{ stats.inputTokens === null ? '—' : stats.inputTokens }}</b>
          </div>
          <div class="kpi">
            <i>{{ t('liveStageTree.stats.kpi.outputTokens') }}</i>
            <b>{{ stats.outputTokens === null ? '—' : stats.outputTokens }}</b>
          </div>
          <div class="kpi">
            <i>{{ t('liveStageTree.stats.kpi.cost') }}</i>
            <b>{{ stats.costUsd === null ? '—' : `$${stats.costUsd.toFixed(4)}` }}</b>
          </div>
          <div class="kpi" :class="cache.pct === null ? 'is-dim' : 'is-ok'">
            <i>{{ t('liveStageTree.stats.kpi.cacheRate') }}</i>
            <b>{{ cache.pct === null ? '—' : `${cache.pct}%` }}</b>
          </div>
          <div class="kpi is-dim">
            <i>{{ t('liveStageTree.stats.kpi.providers') }}</i>
            <b>{{ stats.providers }}</b>
          </div>
          <div class="kpi is-dim">
            <i>{{ t('liveStageTree.stats.kpi.models') }}</i>
            <b>{{ stats.models }}</b>
          </div>
          <div class="kpi is-dim">
            <i>{{ t('liveStageTree.stats.kpi.clients') }}</i>
            <b>{{ stats.clients }}</b>
          </div>
          <div class="kpi is-dim">
            <i>{{ t('liveStageTree.stats.kpi.credentials') }}</i>
            <b>{{ stats.credentials }}</b>
          </div>
          <div class="kpi is-dim">
            <i>{{ t('liveStageTree.stats.kpi.attempts') }}</i>
            <b>{{ stats.attempts }}</b>
          </div>
        </div>
      </section>

      <!-- ② 周期统计 -->
      <section class="stage-stats-overlay__sec">
        <div class="stage-stats-overlay__sechd">
          <b>{{ t('liveStageTree.stats.trend.title') }}</b>
          <span class="stage-stats-overlay__hint">
            {{ t('liveStageTree.stats.trend.hint') }}
          </span>
        </div>
        <div v-if="buckets.length" class="stage-stats-overlay__chart" data-testid="stage-stats-trend">
          <div
            v-for="b in buckets"
            :key="b.tsMs"
            class="trend__col"
            :title="`${bucketLabel(b.tsMs)} · ${b.requests}`"
          >
            <span class="trend__bar" :style="{ height: `${Math.round((b.requests / maxBucket) * 100)}%` }"></span>
          </div>
        </div>
        <p v-else class="stage-stats-overlay__hint">{{ t('liveStageTree.stats.trend.empty') }}</p>
      </section>

      <!-- ③ 分类统计 -->
      <section class="stage-stats-overlay__sec">
        <div class="stage-stats-overlay__sechd">
          <b>{{ t('liveStageTree.stats.dim.title') }}</b>
          <span class="stage-stats-overlay__hint">
            {{ t('liveStageTree.stats.dim.hint') }}
          </span>
        </div>
        <div class="stage-stats-overlay__tabs">
          <button
            v-for="d in DIMS"
            :key="d"
            type="button"
            class="stage-stats-overlay__tab"
            :class="{ 'is-on': dim === d }"
            :data-testid="`stage-stats-dim-${d}`"
            @click="dim = d"
          >
            {{ dimLabel(d) }}
          </button>
        </div>
        <div v-if="rows.length" class="stage-stats-overlay__tablewrap">
          <table class="stage-stats-overlay__table" data-testid="stage-stats-table">
            <thead>
              <tr>
                <th>{{ t('liveStageTree.stats.dim.name') }}</th>
                <th>{{ t('liveStageTree.stats.dim.requests') }}</th>
                <th>{{ t('liveStageTree.stats.dim.successRate') }}</th>
                <th>{{ t('liveStageTree.stats.dim.inFlight') }}</th>
                <th>{{ t('liveStageTree.stats.dim.tokens') }}</th>
                <th>{{ t('liveStageTree.stats.dim.cost') }}</th>
                <th>{{ t('liveStageTree.stats.dim.cache') }}</th>
                <th>{{ t('liveStageTree.stats.dim.avgLatency') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in rows" :key="row.key">
                <td>{{ dim === 'status' ? statusLabel(row.key) : row.label }}</td>
                <td>{{ row.requests }}</td>
                <td>{{ row.successRate === null ? '—' : `${row.successRate}%` }}</td>
                <td>{{ row.inFlight }}</td>
                <td>{{ row.tokens === null ? '—' : row.tokens }}</td>
                <td>{{ row.costUsd === null ? '—' : `$${row.costUsd.toFixed(4)}` }}</td>
                <td>{{ row.cachePct === null ? '—' : `${row.cachePct}%` }}</td>
                <td>{{ row.avgLatencyMs === null ? '—' : `${row.avgLatencyMs}ms` }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="stage-stats-overlay__hint">{{ t('liveStageTree.stats.dim.empty') }}</p>
      </section>

      <!-- ④ 口径区：把「这些数是怎么来的」写在界面上 -->
      <section class="stage-stats-overlay__sec stage-stats-overlay__notes">
        <div class="stage-stats-overlay__sechd">
          <b>{{ t('liveStageTree.stats.notes.title') }}</b>
        </div>
        <ul>
          <li>{{ t('liveStageTree.stats.notes.scope') }}</li>
          <li>{{ t('liveStageTree.stats.notes.coverage') }}</li>
          <li>{{ t('liveStageTree.stats.notes.cost') }}</li>
          <li>{{ t('liveStageTree.stats.notes.cache') }}</li>
          <li>{{ t('liveStageTree.stats.notes.db') }}</li>
        </ul>
      </section>

      <!-- 数据库口径（另一把尺：历史全量直查）—— 默认折叠，不与主口径混算 -->
      <section class="stage-stats-overlay__sec">
        <button
          type="button"
          class="stage-stats-overlay__disclosure"
          data-testid="stage-stats-db-toggle"
          :aria-expanded="dbOpen"
          @click="dbOpen = !dbOpen"
        >
          {{ dbOpen ? '▾' : '▸' }} {{ t('liveStageTree.stats.db.title') }}
        </button>
        <div v-if="dbOpen" class="stage-stats-overlay__db">
          <StatsPanel
            :in-flight="props.inFlight ?? null"
            :queue-depth="props.queueDepth ?? null"
            @select-client="(k: string) => emit('select-client', k)"
            @select-provider="(c: string) => emit('select-provider', c)"
            @select-model="(m: string) => emit('select-model', m)"
          />
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.stage-stats-overlay {
  position: fixed;
  inset: 0;
  z-index: 30;
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  background: var(--kx-bg);
}

.stage-stats-overlay__bar {
  display: flex;
  align-items: center;
  gap: var(--kx-space-3);
  flex: none;
  padding: var(--kx-space-3) var(--kx-space-4);
  border-bottom: 1px solid var(--kx-border);
  background: var(--kx-surface);
}

.stage-stats-overlay__title {
  margin: 0;
  font-size: var(--kx-text-lg);
  font-weight: 650;
  color: var(--kx-text);
}

.stage-stats-overlay__scope {
  padding: 1px 8px;
  border: 1px solid var(--kx-border);
  border-radius: 999px;
  background: var(--kx-bg-container);
  color: var(--kx-text-secondary);
  font-size: var(--kx-text-2xs);
}

.stage-stats-overlay__hint {
  flex: 1;
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

.stage-stats-overlay__close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  background: transparent;
  color: var(--kx-text-secondary);
  cursor: pointer;
}

.stage-stats-overlay__close:hover {
  color: var(--kx-primary);
  border-color: var(--kx-primary);
}

/* element-plus 图标是裸 <svg viewBox>，必须显式定尺寸。 */
.stage-stats-overlay__close > svg {
  width: 12px;
  height: 12px;
}

.stage-stats-overlay__body {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: var(--kx-space-4);
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-3);
}

.stage-stats-overlay__sec {
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-surface);
  padding: var(--kx-space-3);
}

.stage-stats-overlay__sechd {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  margin-bottom: var(--kx-space-2);
  color: var(--kx-muted);
  font-size: var(--kx-text-xs);
}

.stage-stats-overlay__sechd b {
  color: var(--kx-text);
  font-weight: 650;
}

.stage-stats-overlay__kpis {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: var(--kx-space-2);
}

.kpi {
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
  padding: 4px 8px;
  background: var(--kx-bg-container);
}

.kpi i {
  display: block;
  font-style: normal;
  font-size: var(--kx-text-2xs);
  color: var(--kx-muted);
}

.kpi b {
  display: block;
  font-size: var(--kx-text-lg);
  font-weight: 650;
  color: var(--kx-text);
  font-variant-numeric: tabular-nums;
}

.kpi.is-ok b {
  color: var(--kx-success);
}
.kpi.is-bad b {
  color: var(--kx-danger);
}
.kpi.is-live b {
  color: var(--kx-primary);
}
.kpi.is-dim b {
  color: var(--kx-muted);
}

.stage-stats-overlay__chart {
  display: flex;
  align-items: flex-end;
  gap: 2px;
  height: 120px;
}

.trend__col {
  flex: 1 1 0;
  height: 100%;
  display: flex;
  align-items: flex-end;
  min-width: 2px;
}

.trend__bar {
  display: block;
  width: 100%;
  min-height: 1px;
  background: var(--kx-primary);
  border-radius: 2px 2px 0 0;
}

.stage-stats-overlay__tabs {
  display: flex;
  flex-wrap: wrap;
  gap: var(--kx-space-1);
  margin-bottom: var(--kx-space-2);
}

.stage-stats-overlay__tab {
  padding: 2px 10px;
  border: 1px solid var(--kx-border);
  border-radius: 999px;
  background: var(--kx-bg-container);
  color: var(--kx-text-secondary);
  font-size: var(--kx-text-2xs);
  cursor: pointer;
}

.stage-stats-overlay__tab.is-on {
  background: var(--kx-primary-soft);
  border-color: var(--kx-primary);
  color: var(--kx-text);
  font-weight: 600;
}

.stage-stats-overlay__tablewrap {
  max-height: 320px;
  overflow: auto;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm);
}

.stage-stats-overlay__table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--kx-text-2xs);
  font-variant-numeric: tabular-nums;
}

.stage-stats-overlay__table th {
  position: sticky;
  top: 0;
  z-index: 1;
  padding: 4px 8px;
  text-align: right;
  background: var(--kx-surface);
  color: var(--kx-muted);
  font-weight: 600;
  white-space: nowrap;
}

.stage-stats-overlay__table th:first-child,
.stage-stats-overlay__table td:first-child {
  text-align: left;
  position: sticky;
  left: 0;
  background: var(--kx-surface);
}

.stage-stats-overlay__table td {
  padding: 4px 8px;
  text-align: right;
  border-bottom: 1px solid var(--kx-border);
  color: var(--kx-text);
  white-space: nowrap;
}

.stage-stats-overlay__table tbody tr:last-child td {
  border-bottom: none;
}

.stage-stats-overlay__notes ul {
  margin: 0;
  padding-left: 18px;
  color: var(--kx-muted);
  font-size: var(--kx-text-2xs);
  line-height: 1.7;
}

.stage-stats-overlay__disclosure {
  border: none;
  background: transparent;
  color: var(--kx-text-secondary);
  font-size: var(--kx-text-xs);
  cursor: pointer;
  padding: 0;
}

.stage-stats-overlay__db {
  margin-top: var(--kx-space-2);
}
</style>
