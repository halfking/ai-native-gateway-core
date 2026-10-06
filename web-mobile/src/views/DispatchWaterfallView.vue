<script setup lang="ts">
// DispatchWaterfallView — 「不报错但很慢」时，把时间拆成几段看。
//
// 数据源：
//   GET /api/admin/dispatch/waterfall   瀑布本体
//   GET /api/admin/dispatch/queues      队列深度（独立端点）
// 鉴权：两条都是 wrapAdmin = AdminMiddleware ⇒ tenant_admin 可用。
//
// ⚠️★ 本面**没有 degraded 字段**。不可观测由两个信号表达：
//   · wired === false        → 队列投影没接上（main_dispatch_projection.go:66-75）
//   · source === 'none'      → 没有数据源（waterfall_db.go:90-96）
//   这两种都不是「当前没有请求」。把它们显示成空列表，等于告诉运维
//   「一切正常」——而真相是**这个观测面根本没在工作**，恰恰是排障最需要的信号。
//   ⇒ 反向锁定：不写 isWaterfallUnavailable 的视图测试见 routingLog.test.ts。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage, ContinuousListController } from '@/hyper'
import HyperList from '@/components/common/HyperList.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { relativeTime } from '@/utils/format'
import {
  fetchWaterfall,
  fetchDispatchQueues,
  isWaterfallUnavailable,
  laneLabel,
  spanMs,
  WATERFALL_DEFAULT_LIMIT,
  type WaterfallRequest,
  type DispatchQueuesResponse,
} from '@/api/dispatchWaterfall'

useHyperPage({ title: () => t('waterfall.title') })

const waterfall = ref<{ wired: boolean; source?: string; bottleneck: string; suggestion?: string } | null>(null)
const queues = ref<DispatchQueuesResponse | null>(null)
const queuesError = ref<string | null>(null)
const expanded = ref<string | null>(null)

const controller = new ContinuousListController<WaterfallRequest>({
  fetchPage: async () => {
    const resp = await fetchWaterfall({ limit: WATERFALL_DEFAULT_LIMIT })
    waterfall.value = {
      wired: resp.wired,
      source: resp.source,
      bottleneck: resp.bottleneck_diagnosis?.bottleneck ?? '',
      suggestion: resp.bottleneck_diagnosis?.suggestion,
    }
    const items = resp.requests ?? []
    // 本端点无分页（后端不 clamp 也不 offset），恒第 1 页 ⇒ total 设成 items.length
    return { items, total: items.length }
  },
  stableKey: (r) => `wf-${r.request_id}`,
  scopeKey: 'dispatch-waterfall',
})

const unavailable = computed(() => isWaterfallUnavailable(waterfall.value))

/**
 * 队列深度是**独立端点**，桌面端用 `.catch(() => null)` 静默吞掉失败。
 * ★ 移动端不照抄：静默吞掉会让「队列指标挂了」和「队列是空的」长得一样。
 *   这里把失败存下来单独提示。
 */
async function loadQueues(): Promise<void> {
  try {
    queues.value = await fetchDispatchQueues()
    queuesError.value = null
  } catch (err) {
    queues.value = null
    queuesError.value = (err as Error)?.message ?? null
  }
}
void loadQueues()

onBeforeUnmount(() => {
  controller.dispose()
})

function tone(r: WaterfallRequest): 'success' | 'warning' | 'danger' | 'muted' {
  if (r.result === 'error' || r.result === 'failed') return 'danger'
  if (r.result === 'retry') return 'warning'
  return 'success'
}

interface Stage {
  key: string
  label: string
  ms: number | null
}

/**
 * 时序分段。
 *
 * ★ 每一段都用 spanMs（缺端返回 null）而不是后端的预聚合字段，
 *   因为预聚合字段在缺一端时会显示 0，而「没测到」和「耗时 0ms」在排障里
 *   是**相反**的指示。拿不到就留空显示「—」。
 */
function stages(r: WaterfallRequest): Stage[] {
  return [
    { key: 'queue', label: t('waterfall.queueWait'), ms: r.queue_wait_ms ?? null },
    { key: 'model', label: t('waterfall.waitingModel'), ms: r.waiting_in_model_ms ?? null },
    { key: 'node', label: t('waterfall.waitingNode'), ms: r.waiting_in_node_ms ?? null },
    { key: 'routing', label: t('waterfall.routing'), ms: r.routing_ms ?? null },
    { key: 'acquire', label: t('waterfall.acquire'), ms: r.acquire_ms ?? null },
    { key: 'upstream', label: t('waterfall.upstream'), ms: r.upstream_latency_ms ?? null },
    { key: 'streaming', label: t('waterfall.streaming'), ms: r.streaming_duration_ms ?? null },
  ].filter((s) => s.ms != null) as Stage[]
}

function spanText(from?: string | null, to?: string | null): string {
  const ms = spanMs(from, to)
  // ★ null 渲染成 '—' 而不是 0
  return ms == null ? '—' : `${ms}ms`
}

function attemptsText(r: WaterfallRequest): string | null {
  const n = r.attempts?.length
  return n == null || n === 0 ? null : t('waterfall.attempts', { n })
}

function toggle(id: string): void {
  expanded.value = expanded.value === id ? null : id
}

function laneTone(l: { full?: boolean; depth: number }): 'danger' | 'warning' | 'muted' {
  if (l.full) return 'danger'
  if (l.depth > 0) return 'warning'
  return 'muted'
}

/**
 * 进度条宽度百分比。
 *
 * ⚠️ 缺 total（null / 0 / 非有限）时返回 0 而不是 NaN —— `NaN%` 会让整个
 *   width 声明失效，条形直接消失，看起来像「这一段不存在」。
 *   负数也夹到 0：负耗时说明上游时钟不一致，不该画成反向的条。
 */
function barWidth(ms: number | null, total: number | null | undefined): number {
  if (ms == null) return 0
  if (total == null || !Number.isFinite(total) || total <= 0) return 0
  const pct = (ms / total) * 100
  if (!Number.isFinite(pct)) return 0
  return Math.max(0, Math.min(100, pct))
}
</script>

<template>
  <div class="view-root wf">
    <!-- ★ 不可观测横幅：与空列表严格区分 -->
    <div v-if="unavailable" class="wf__banner wf__banner--warn">
      <p class="wf__banner-title">
        <AppIcon name="alert" :size="16" />
        <span>
          {{ waterfall?.wired === false ? t('waterfall.unwired') : t('waterfall.sourceNone') }}
        </span>
      </p>
      <p v-if="waterfall?.source" class="wf__banner-sub">{{ t('waterfall.sourceLabel', { s: waterfall.source }) }}</p>
    </div>

    <p v-if="waterfall?.bottleneck" class="wf__bottleneck">
      {{ t('waterfall.bottleneck', { b: waterfall.bottleneck }) }}
    </p>
    <p v-if="waterfall?.suggestion" class="wf__suggestion">{{ t('waterfall.suggestion', { s: waterfall.suggestion }) }}</p>

    <!-- 队列深度：独立端点，失败要显式说，不能静默 -->
    <section class="wf__queues">
      <h2 class="wf__section-title">{{ t('waterfall.queues') }}</h2>
      <p v-if="queuesError" class="wf__queues-err">{{ queuesError }}</p>
      <p v-else-if="!queues || !queues.enabled" class="wf__queues-err">{{ t('waterfall.queuesDisabled') }}</p>
      <template v-else>
        <div v-if="queues.models.length === 0 && queues.credentials.length === 0" class="wf__queues-err">
          {{ t('waterfall.queuesDisabled') }}
        </div>
        <div v-for="(lane, i) in queues.models" :key="`m${i}`" class="wf__lane">
          <StatusDot :tone="laneTone(lane)" />
          <span class="wf__lane-name">{{ laneLabel(lane) ?? '—' }}</span>
          <span class="wf__lane-depth">{{ t('waterfall.depth', { d: lane.depth }) }}</span>
          <span v-if="lane.full" class="badge badge--danger">{{ t('waterfall.full') }}</span>
          <span v-else-if="lane.limit != null" class="wf__lane-limit">{{ t('waterfall.limit', { l: lane.limit }) }}</span>
        </div>
        <div v-for="(lane, i) in queues.credentials" :key="`c${i}`" class="wf__lane">
          <StatusDot :tone="laneTone(lane)" />
          <span class="wf__lane-name">{{ laneLabel(lane) ?? '—' }}</span>
          <span class="wf__lane-depth">{{ t('waterfall.depth', { d: lane.depth }) }}</span>
          <span v-if="lane.full" class="badge badge--danger">{{ t('waterfall.full') }}</span>
        </div>
      </template>
    </section>

    <HyperList
      :controller="controller"
      :item-key="(r: WaterfallRequest) => `wf-${r.request_id}`"
      :on-refresh="async () => { controller.loadFirst('requery'); await loadQueues() }"
      :empty-hint="t('waterfall.empty')"
    >
      <template #item="{ item: r }">
        <div class="data-card wf-card">
          <button type="button" class="wf-card__head" @click="toggle(r.request_id)">
            <div class="card-row">
              <span class="wf-card__model">
                <StatusDot :tone="tone(r)" />
                {{ r.model || '—' }}
              </span>
              <span class="badge" :class="`badge--${tone(r) === 'muted' ? 'muted' : tone(r)}`">{{ r.result }}</span>
            </div>
            <div class="wf-card__fields">
              <span v-if="r.arrived_at" class="wf-card__field">{{ relativeTime(r.arrived_at) }}</span>
              <span v-if="r.vendor" class="wf-card__field">{{ r.vendor }}</span>
              <span v-if="r.total_ms != null" class="wf-card__field">{{ t('waterfall.total') }} {{ r.total_ms }}ms</span>
            </div>
            <p v-if="attemptsText(r)" class="wf-card__attempts">{{ attemptsText(r) }}</p>
          </button>

          <div v-if="expanded === r.request_id" class="wf-card__detail">
            <p class="wf-card__section-title">{{ t('waterfall.stageTimeline') }}</p>
            <div v-for="s in stages(r)" :key="s.key" class="wf-stage">
              <span class="wf-stage__label">{{ s.label }}</span>
              <span class="wf-stage__bar">
                <span
                  class="wf-stage__fill"
                  :style="{ width: `${barWidth(s.ms, r.total_ms)}%` }"
                />
              </span>
              <span class="wf-stage__ms">{{ s.ms }}ms</span>
            </div>

            <p class="wf-card__section-title">{{ t('waterfall.arriveTitle') }}</p>
            <div class="card-field">
              <span>{{ t('waterfall.arrived') }}</span>
              <span class="card-field__value">{{ r.arrived_at ?? '—' }}</span>
            </div>
            <div class="card-field">
              <span>{{ t('waterfall.firstByte') }}</span>
              <span class="card-field__value">{{ spanText(r.arrived_at, r.response_start_at) }}</span>
            </div>
            <div class="card-field">
              <span>{{ t('waterfall.ended') }}</span>
              <span class="card-field__value">{{ spanText(r.arrived_at, r.response_end_at) }}</span>
            </div>

            <template v-if="r.attempts && r.attempts.length > 0">
              <p class="wf-card__section-title">{{ t('waterfall.attemptsTitle') }}</p>
              <div v-for="a in r.attempts" :key="a.attempt_id" class="wf-attempt">
                <span class="wf-attempt__no">#{{ a.attempt_no }}</span>
                <span class="wf-attempt__model">{{ a.model ?? '—' }}</span>
                <span v-if="a.vendor" class="wf-attempt__vendor">{{ a.vendor }}</span>
                <span v-if="a.outcome" class="badge badge--muted">{{ a.outcome }}</span>
                <span v-if="a.error_kind" class="wf-attempt__err">{{ a.error_kind }}</span>
              </div>
            </template>
          </div>
        </div>
      </template>
    </HyperList>
  </div>
</template>

<style scoped>
.wf {
  padding: var(--app-space-3) var(--app-space-3) 0;
}
.wf__banner {
  margin: 0 0 var(--app-space-2);
  padding: var(--app-space-3);
  border-radius: var(--app-radius);
  font-size: 13px;
  line-height: 1.6;
}
.wf__banner--warn {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.wf__banner-title {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  font-weight: 600;
}
.wf__banner-sub {
  margin-top: 4px;
  font-size: 12px;
  opacity: 0.85;
}
.wf__bottleneck,
.wf__suggestion {
  margin: 0 0 var(--app-space-2);
  font-size: 13px;
  color: var(--app-text-secondary);
}
.wf__section-title {
  margin: var(--app-space-3) 0 var(--app-space-2);
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text-secondary);
}
.wf__queues-err {
  margin: 0 0 var(--app-space-2);
  font-size: 12px;
  color: var(--app-text-muted);
}
.wf__lane {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  padding: 10px 0;
  border-bottom: 1px solid var(--app-border);
  min-height: 48px;
}
.wf__lane-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 14px;
  color: var(--app-text);
}
.wf__lane-depth,
.wf__lane-limit {
  font-size: 12px;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
.wf-card {
  padding: 0;
}
.wf-card__head {
  display: block;
  width: 100%;
  padding: var(--app-space-3);
  text-align: left;
  background: none;
  border: none;
}
.wf-card__model {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 15px;
  font-weight: 600;
  color: var(--app-text);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wf-card__fields {
  display: flex;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: 6px;
}
.wf-card__field {
  font-size: 12px;
  color: var(--app-text-muted);
}
.wf-card__attempts {
  margin: 4px 0 0;
  font-size: 12px;
  color: var(--app-text-secondary);
}
.wf-card__detail {
  padding: 0 var(--app-space-3) var(--app-space-3);
}
.wf-card__section-title {
  margin: var(--app-space-3) 0 var(--app-space-2);
  font-size: 12px;
  font-weight: 600;
  color: var(--app-text-secondary);
}
.wf-stage {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  /* 48px：这是用户真正要读的 7 行分段（标签/条/数值），32px 在手机上太挤。
     R1 门扫的是「所有 min-height<48 的选择器」而非仅可点控件，所以只读行
     同样会红 —— 与其为它放松门禁，不如按可读性抬到标准值。 */
  min-height: 48px;
}
.wf-stage__label {
  width: 84px;
  flex: none;
  font-size: 12px;
  color: var(--app-text-muted);
}
.wf-stage__bar {
  flex: 1;
  height: 6px;
  border-radius: 3px;
  background: var(--app-surface-muted);
  overflow: hidden;
}
.wf-stage__fill {
  display: block;
  height: 100%;
  background: var(--app-primary);
}
.wf-stage__ms {
  width: 68px;
  flex: none;
  text-align: right;
  font-size: 12px;
  color: var(--app-text-secondary);
  font-variant-numeric: tabular-nums;
}
.wf-attempt {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  padding: 8px 0;
  border-bottom: 1px solid var(--app-border);
}
.wf-attempt__no {
  font-size: 12px;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
.wf-attempt__model {
  font-size: 13px;
  color: var(--app-text);
}
.wf-attempt__vendor {
  font-size: 12px;
  color: var(--app-text-muted);
}
.wf-attempt__err {
  font-size: 12px;
  color: var(--app-danger);
}
</style>
