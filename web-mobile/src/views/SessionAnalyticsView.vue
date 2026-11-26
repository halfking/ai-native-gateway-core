<script setup lang="ts">
// 客户端和任务画像展示当前请求投影的日期范围及同步时间。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, fmtUsd, relativeTime } from '@/utils/format'
import {
  fetchAnalyticsClients,
  fetchAnalyticsTasks,
  analyticsRefreshedAtIsZero,
  analyticsRowsWereDropped,
  analyticsSuccessRate,
  ANALYTICS_LIMIT_DEFAULT,
  ANALYTICS_LIMIT_MAX,
  ANALYTICS_CLIENT_ORDER_BYS,
  type AnalyticsOrderBy,
  type AnalyticsClientSummary,
  type AnalyticsTaskSummary,
} from '@/api/sessionAnalytics'

useHyperPage({ title: () => t('sa.title') })

type Dimension = 'clients' | 'tasks'

const dim = ref<Dimension>('clients')
const orderBy = ref<AnalyticsOrderBy | ''>('')
const offset = ref(0)

const rows = ref<Array<AnalyticsClientSummary | AnalyticsTaskSummary>>([])
const total = ref(0)
const refreshedAt = ref('')
const period = ref('')
const projectionPending = ref(false)
const error = ref<string | null>(null)
const loading = ref(false)
let requestVersion = 0

async function load(): Promise<void> {
  const version = ++requestVersion
  loading.value = true
  error.value = null
  try {
    const params = { orderBy: orderBy.value || undefined, offset: offset.value, limit: ANALYTICS_LIMIT_DEFAULT }
    const r = dim.value === 'clients' ? await fetchAnalyticsClients(params) : await fetchAnalyticsTasks(params)
    if (version !== requestVersion) return
    rows.value = (r as { clients?: AnalyticsClientSummary[]; tasks?: AnalyticsTaskSummary[] }).clients ??
      (r as { tasks?: AnalyticsTaskSummary[] }).tasks ?? []
    total.value = r.total
    refreshedAt.value = r.refreshed_at ?? ''
    period.value = r.start_date && r.end_date ? `${r.start_date} → ${r.end_date}` : ''
    projectionPending.value = !!r.pending_since
  } catch (e) {
    if (version !== requestVersion) return
    // ★★ 抛错**不许**退化成空列表：那会把「查询失败」显示成「没有数据」。
    rows.value = []
    total.value = 0
    refreshedAt.value = ''
    period.value = ''
    projectionPending.value = false
    error.value = (e as Error)?.message || t('common.error')
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

function pickDimension(d: Dimension): void {
  dim.value = d
  offset.value = 0
  void load()
}

function pickOrder(v: AnalyticsOrderBy): void {
  orderBy.value = orderBy.value === v ? '' : v
  offset.value = 0
  void load()
}

function goto(next: number): void {
  offset.value = Math.max(0, next)
  void load()
}

/** ★ 有 total，可以做**精确**分页（与本仓那些没有 total 的族不同）。 */
const hasPrev = computed(() => offset.value > 0)
const hasNext = computed(() => offset.value + rows.value.length < total.value)

/** ★★ 坏行被 continue 丢掉时为 true —— 这不是分页 bug，页面必须说出来。见坑 1。 */
const rowsDropped = computed(() => analyticsRowsWereDropped(rows.value.length, total.value))

/** ★ 空列表时 refreshed_at 是 Go 零值，那一栏要换个说法。见坑 2。 */
const refreshedIsZero = computed(() => analyticsRefreshedAtIsZero(refreshedAt.value))

function keyOf(r: AnalyticsClientSummary | AnalyticsTaskSummary): string {
  return 'task_id' in r ? r.task_id : r.client_id
}

function titleOf(r: AnalyticsClientSummary | AnalyticsTaskSummary): string {
  return 'task_id' in r ? t('sa.taskId') : t('sa.clientId')
}

function gradeKeys(): Array<{ k: string; label: string; tone: 'success' | 'warning' | 'danger' | 'muted' }> {
  return [
    { k: 'a', label: 'A', tone: 'success' },
    { k: 'b', label: 'B', tone: 'success' },
    { k: 'c', label: 'C', tone: 'warning' },
    { k: 'd', label: 'D', tone: 'danger' },
    { k: 'f', label: 'F', tone: 'danger' },
  ]
}

function gradeCount(r: AnalyticsClientSummary | AnalyticsTaskSummary, k: string): number {
  return (r.health_distribution as unknown as Record<string, number>)[k] ?? 0
}

function successPct(r: AnalyticsClientSummary | AnalyticsTaskSummary): number | null {
  return analyticsSuccessRate({ total_requests: r.total_requests, total_success: r.total_success })
}

function optionalNum(v: number | null | undefined): string {
  return typeof v === 'number' ? fmtInt(v) : t('sa.noValue')
}

onBeforeUnmount(() => {
  requestVersion++
  rows.value = []
  total.value = 0
  refreshedAt.value = ''
  error.value = null
})

void load()
</script>

<template>
  <div class="view-root sa">
    <!-- ══════ 数据新鲜度（本页头号问题，必须置顶） ══════ -->
    <section class="sa__panel">
      <span class="sa__panel-title">{{ t('sa.freshness') }}</span>

      <p class="sa__note">{{ t('sa.staleSourceNote') }}</p>
      <p v-if="period && !loading" class="sa__note">{{ period }}</p>
      <p v-if="projectionPending && !loading" class="sa__note sa__note--warn" role="status">{{ t('rr.projectedPending') }}</p>

      <div v-if="!loading && !error" class="sa__fresh">
        <template v-if="refreshedIsZero">
          <p class="sa__note sa__note--warn">
            <AppIcon name="alert" :size="14" />
            <span>{{ t('sa.refreshedZero') }}</span>
          </p>
        </template>
        <template v-else>
          <span class="sa__cell">
            <span class="sa__cell-l">{{ t('sa.refreshedAt') }}</span>
            <span class="sa__cell-v">{{ relativeTime(refreshedAt) }}</span>
          </span>
          <span class="sa__cell">
            <span class="sa__cell-l">{{ t('sa.refreshedRaw') }}</span>
            <span class="sa__cell-v sa__mono">{{ refreshedAt }}</span>
          </span>
        </template>
      </div>
    </section>

    <!-- ══════ 筛选 ══════ -->
    <section class="sa__panel">
      <span class="sa__panel-title">{{ t('sa.filters') }}</span>

      <div class="sa__chips" role="group" :aria-label="t('sa.dimensionLabel')">
        <button
          v-for="d in (['clients', 'tasks'] as Dimension[])"
          :key="d"
          type="button"
          class="sa__chip"
          :class="{ 'sa__chip--on': dim === d }"
          @click="pickDimension(d)"
        >
          {{ t('sa.dim_' + d) }}
        </button>
      </div>

      <div class="sa__chips" role="group" :aria-label="t('sa.orderLabel')">
        <button
          v-for="v in ANALYTICS_CLIENT_ORDER_BYS"
          :key="v"
          type="button"
          class="sa__chip"
          :class="{ 'sa__chip--on': orderBy === v }"
          @click="pickOrder(v)"
        >
          {{ t('sa.order_' + v) }}
        </button>
      </div>
      <!-- ★ 不发 order_by 时后端用**各自**的默认（clients=cost / tasks=sessions） -->
      <p class="sa__note">
        <AppIcon name="key" :size="13" />
        <span>{{ orderBy ? t('sa.orderExplicit', { v: t('sa.order_' + orderBy) }) : t('sa.orderImplicit') }}</span>
      </p>
    </section>

    <!-- ══════ 列表 ══════ -->
    <section class="sa__panel">
      <p v-if="loading" class="sa__msg">{{ t('common.loading') }}</p>
      <p v-if="error" class="sa__msg sa__msg--err">
        {{ error }}
        <span class="sa__sub">{{ t('sa.errorHint') }}</span>
      </p>
      <p v-else-if="!rows.length" class="sa__msg">{{ t('sa.empty') }}</p>

      <!-- ★★ 坏行被丢弃时要说清楚：不是分页 bug -->
      <p v-if="rowsDropped && rows.length" class="sa__note sa__note--warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('sa.rowsDropped', { shown: rows.length, total }) }}</span>
      </p>

      <ul v-if="rows.length" class="sa__list">
        <li v-for="r in rows" :key="keyOf(r)" class="sa__item">
          <div class="sa__item-head">
            <span class="sa__title">{{ titleOf(r) }}</span>
            <span class="sa__id">{{ keyOf(r) }}</span>
          </div>

          <div class="sa__grid">
            <span class="sa__cell">
              <span class="sa__cell-l">{{ t('sa.sessions') }}</span>
              <span class="sa__cell-v">{{ fmtInt(r.session_count) }}</span>
            </span>
            <span class="sa__cell">
              <span class="sa__cell-l">{{ t('sa.active24h') }}</span>
              <span class="sa__cell-v">{{ fmtInt(r.active_sessions_24h) }}</span>
            </span>
            <span class="sa__cell">
              <span class="sa__cell-l">{{ t('sa.requests') }}</span>
              <span class="sa__cell-v">{{ fmtInt(r.total_requests) }}</span>
            </span>
            <span class="sa__cell">
              <span class="sa__cell-l">{{ t('sa.cost') }}</span>
              <span class="sa__cell-v">{{ fmtUsd(r.total_cost_usd) }}</span>
            </span>
          </div>

          <div class="sa__grid">
            <span class="sa__cell">
              <span class="sa__cell-l">{{ t('sa.avgCost') }}</span>
              <span class="sa__cell-v">{{ fmtUsd(r.avg_cost_per_session) }}</span>
            </span>
            <!-- ★ omitempty 指针 ⇒ 键可能不存在，显示「—」而不是 0 -->
            <span class="sa__cell">
              <span class="sa__cell-l">{{ t('sa.avgHealth') }}</span>
              <span class="sa__cell-v">{{ optionalNum(r.avg_health_score) }}</span>
            </span>
            <span class="sa__cell">
              <span class="sa__cell-l">{{ t('sa.avgLatency') }}</span>
              <span class="sa__cell-v">{{ optionalNum(r.avg_latency_ms) }}<template v-if="typeof r.avg_latency_ms === 'number'"> ms</template></span>
            </span>
            <span class="sa__cell">
              <span class="sa__cell-l">{{ t('sa.successRate') }}</span>
              <span class="sa__cell-v">{{ successPct(r) === null ? t('sa.noValue') : successPct(r)!.toFixed(1) + '%' }}</span>
            </span>
          </div>

          <!-- ★★ health_distribution 只有 5 个计数键，没有 total、没有百分比 -->
          <div class="sa__grades" role="group" :aria-label="t('sa.gradeLabel')">
            <span v-for="g in gradeKeys()" :key="g.k" class="sa__grade">
              <StatusDot :tone="g.tone" />
              <span class="sa__grade-k">{{ g.label }}</span>
              <span class="sa__grade-v">{{ fmtInt(gradeCount(r, g.k)) }}</span>
            </span>
          </div>
          <p class="sa__sub">{{ t('sa.gradeNote') }}</p>

          <p class="sa__meta">
            {{ t('sa.models') }}:
            <span class="sa__mono">{{ r.models_used.length ? r.models_used.join(' · ') : t('sa.noValue') }}</span>
          </p>
          <!-- ★ tasks 比 clients 多这一行 -->
          <p v-if="'clients_used' in r" class="sa__meta">
            {{ t('sa.clients') }}:
            <span class="sa__mono">{{ r.clients_used.length ? r.clients_used.join(' · ') : t('sa.noValue') }}</span>
          </p>
          <p class="sa__meta">
            {{ t('sa.seen') }}: {{ relativeTime(r.first_seen_at) }} → {{ relativeTime(r.last_seen_at) }}
          </p>
        </li>
      </ul>

      <!-- ★ 这一族**有** total ⇒ 分页是精确的（与 review-queue/feedback 不同） -->
      <div v-if="rows.length || offset > 0" class="sa__row">
        <button type="button" class="sa__btn" :disabled="!hasPrev" @click="goto(offset - ANALYTICS_LIMIT_DEFAULT)">
          {{ t('sa.prev') }}
        </button>
        <span class="sa__meta">
          {{ t('sa.page', { from: offset + 1, to: offset + rows.length, total }) }}
        </span>
        <button type="button" class="sa__btn" :disabled="!hasNext" @click="goto(offset + ANALYTICS_LIMIT_DEFAULT)">
          {{ t('sa.next') }}
        </button>
      </div>
      <p class="sa__sub">{{ t('sa.pageSizeNote', { n: ANALYTICS_LIMIT_DEFAULT, max: ANALYTICS_LIMIT_MAX }) }}</p>
    </section>
  </div>
</template>

<style scoped>
.sa__panel {
  background: var(--app-surface);
  border-radius: 12px;
  padding: 12px;
  margin-bottom: 12px;
}
.sa__panel-title {
  display: block;
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 8px;
}
.sa__note {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  font-size: 12px;
  line-height: 1.5;
  color: var(--app-text-secondary);
  margin: 6px 0;
}
.sa__note--warn { color: var(--app-warning); }
.sa__mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; word-break: break-all; }
.sa__msg { font-size: 13px; color: var(--app-text-secondary); padding: 8px 0; }
.sa__msg--err { color: var(--app-danger); }
.sa__sub { display: block; font-size: 11px; color: var(--app-text-muted); }

.sa__chips { display: flex; flex-wrap: wrap; gap: 8px; margin: 6px 0; }
/* ★ R1：新增触控控件 ≥48 CSS px */
.sa__chip {
  min-height: 48px;
  min-width: 48px;
  padding: 0 14px;
  border-radius: 8px;
  border: 1px solid var(--app-border);
  background: transparent;
  font-size: 13px;
}
.sa__chip--on { background: var(--app-primary); color: var(--app-on-primary); border-color: transparent; }

.sa__list { list-style: none; margin: 0; padding: 0; }
.sa__item { padding: 10px 0; border-top: 1px solid var(--app-border); }
.sa__item-head { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
.sa__title { font-size: 12px; color: var(--app-text-muted); }
.sa__id { font-size: 14px; font-weight: 600; word-break: break-all; }

.sa__grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 6px; margin-top: 6px; }
.sa__cell { display: flex; flex-direction: column; }
.sa__cell-l { font-size: 11px; color: var(--app-text-muted); }
.sa__cell-v { font-size: 14px; font-weight: 600; }
.sa__fresh { display: flex; flex-direction: column; gap: 6px; }

.sa__grades { display: flex; gap: 10px; margin-top: 8px; flex-wrap: wrap; }
.sa__grade { display: inline-flex; align-items: center; gap: 4px; font-size: 12px; }
.sa__grade-k { font-weight: 600; }
.sa__grade-v { color: var(--app-text-secondary); }

.sa__meta { font-size: 12px; color: var(--app-text-secondary); margin: 4px 0 0; }
.sa__row { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 12px; }
.sa__btn {
  min-height: 48px;
  min-width: 64px;
  padding: 0 16px;
  border-radius: 8px;
  border: 1px solid var(--app-border);
  background: transparent;
  font-size: 13px;
}
.sa__btn:disabled { opacity: 0.4; }
</style>
