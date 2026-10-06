<script setup lang="ts">
// SystemMonitorView — 系统监控（/api/admin/system-monitor/{stats,recent-runs}，**admin 档**）。
//
// 它答的是「**系统探针这层**现在在不在干活、干得怎么样」，与同族分清：
//   /probe /probe-model /probe-health  答「凭据与模型健不健康」
//   本页                              答「监控器本身的状态 + 最近结束的探针运行明细」
//
// ⚠️★★★ 五个后端语义（详见 api/systemMonitor.ts 文件头）：
//
// (1) ★★★ **每个 0 都可能是「不知道」。**
//     · 监控器未接线 ⇒ queue/running 留 0、in_fallback 留 false
//       （不是「队列空、没在降级」）
//     · 读不到 settings 行 ⇒ monitor_concurrency 恒为 5
//     · ★★ 近 1 小时四个计数的 **Scan 错误被显式丢弃**（`_ = ...Scan(...)`）
//       ⇒ 四项全 0 时**分不清**「一小时零次」与「查询失败」
//     ⇒ 四项同时为 0 时，本页加一条「可能没取到」的显式说明。
//
// (2) ★★★ **completed + failed + skipped ≠ 总运行数。**
//     `status` 的 CHECK 允许 6 个值（success/failed/expired/skipped/
//     timeout/network_error），而后端 failed 口径**不含 `expired`**
//     ⇒ 三者相加漏掉 expired。
//     ⇒ 本页**不显示**「一小时共 N 次」，只显示三个分项，并常驻说明。
//
// (3) ★★ `recent-runs` 的 `total` 取自 `len(out)`（跳行之后）
//     ⇒ 它是**本页返回行数**，不是数据库计数。
//
// (4) ★★ 清单**只含已结束的运行**：两处写入方都在运行结束后才 INSERT，
//     `finished_at` 是 NOT NULL 且恒为真实时间。
//     ⇒ 页面不说「最新的探针运行」，说「最近**已结束**的运行」；
//       进行中的探测**不会**出现在这里。
//
// (5) ★ `limit` 默认 50、上限 200、越界静默回落；但 limit 被回显
//     ⇒ `total >= limit` 是**精确**的截断信号。
//
// ★ 同族的 `by-credential` / `by-provider` / `by-model` / `concurrency` 是
//   **superAdmin 档**，且 `concurrency` 是 **PATCH 写操作** ⇒ 本页都不碰。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchSystemMonitorStats,
  fetchRecentProbeRuns,
  recentRunsTruncated,
  statsAccountedRuns,
  statusIsUnaccounted,
  runTone,
  runDurationMs,
  RECENT_RUNS_LIMIT_DEFAULT,
  RECENT_RUNS_LIMIT_MAX,
  STATS_CONCURRENCY_FALLBACK,
  STATS_MAY_NOT_BE_WIRED,
  STATS_1H_COUNTS_MAY_BE_UNAVAILABLE,
  PROBE_RUNS_ONLY_SETTLED,
  type SystemMonitorStats,
  type RecentRunsResponse,
} from '@/api/systemMonitor'

useHyperPage({ title: () => t('sysmon.title') })

const LIMIT_CHOICES = [RECENT_RUNS_LIMIT_DEFAULT, 100, RECENT_RUNS_LIMIT_MAX]

const limit = ref<number>(RECENT_RUNS_LIMIT_DEFAULT)
const stats = ref<SystemMonitorStats | null>(null)
const runs = ref<RecentRunsResponse | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const statsError = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  statsError.value = null
  // ★ 两个端点**独立**取：stats 挂了不该让明细也消失，反之亦然。
  //   各自独立解包，不共用一个 try。
  const [s, r] = await Promise.allSettled([
    fetchSystemMonitorStats(),
    fetchRecentProbeRuns({ limit: limit.value }),
  ])
  if (s.status === 'fulfilled') stats.value = s.value
  else {
    stats.value = null
    statsError.value = (s.reason as Error)?.message || t('common.error')
  }
  if (r.status === 'fulfilled') runs.value = r.value
  else {
    runs.value = null
    error.value = (r.reason as Error)?.message || t('common.error')
  }
  loading.value = false
  loaded.value = true
}
void load()

const runList = computed(() => runs.value?.runs ?? [])
const atCap = computed(() => recentRunsTruncated(runs.value))
const runsEmpty = computed(() => loaded.value && !loading.value && !error.value && runList.value.length === 0)

/** ★★ 四项同时为 0 ⇒ 可能是 Scan 失败，不是「一小时零次」。见 (1)。 */
const countsMayBeUnavailable = computed(
  () =>
    STATS_1H_COUNTS_MAY_BE_UNAVAILABLE &&
    !!stats.value &&
    stats.value.completed_total_1h === 0 &&
    stats.value.failed_total_1h === 0 &&
    stats.value.skipped_total_1h === 0 &&
    stats.value.total_tokens_1h === 0,
)
/** ★ 监控器未接线时队列恒 0 ⇒ 不能说「队列为空，一切正常」。 */
const queueMayBeNotWired = computed(
  () => STATS_MAY_NOT_BE_WIRED && !!stats.value && stats.value.queue_size === 0 && stats.value.running_size === 0,
)
const concurrencyIsFallback = computed(() => stats.value?.monitor_concurrency === STATS_CONCURRENCY_FALLBACK)

function fmtDuration(run: (typeof runList.value)[number]): string {
  const ms = runDurationMs(run)
  if (ms === null) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

onBeforeUnmount(() => {
  stats.value = null
  runs.value = null
  error.value = null
  statsError.value = null
})
</script>

<template>
  <div class="view-root sm">
    <!-- ══════ 监控器状态 ══════ -->
    <section class="sm__panel">
      <div class="sm__panel-head">
        <span class="sm__panel-title">{{ t('sysmon.monitorState') }}</span>
        <StatusDot :tone="stats?.in_fallback ? 'warning' : 'success'" />
      </div>

      <p v-if="loading" class="sm__msg">{{ t('common.loading') }}</p>
      <p v-else-if="statsError" class="sm__msg sm__msg--err">{{ statsError }}</p>

      <template v-else-if="stats">
        <p v-if="stats.in_fallback" class="sm__warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('sysmon.inFallback') }}</span>
        </p>

        <div class="sm__grid">
          <span class="sm__cell">
            <span class="sm__cell-l">{{ t('sysmon.queue') }}</span>
            <span class="sm__cell-v">{{ fmtInt(stats.queue_size) }}</span>
          </span>
          <span class="sm__cell">
            <span class="sm__cell-l">{{ t('sysmon.running') }}</span>
            <span class="sm__cell-v">{{ fmtInt(stats.running_size) }}</span>
          </span>
          <span class="sm__cell">
            <span class="sm__cell-l">{{ t('sysmon.concurrency') }}</span>
            <span class="sm__cell-v">{{ stats.monitor_concurrency }}</span>
          </span>
        </div>

        <!-- ★★ 队列与运行都是 0：可能是**没接线**，不能说「一切正常」 -->
        <p v-if="queueMayBeNotWired" class="sm__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('sysmon.mayNotBeWired') }}</span>
        </p>

        <!-- ★ 并发恒等于兜底值 ⇒ 该值可能是**读配置失败**的产物 -->
        <p v-if="concurrencyIsFallback" class="sm__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('sysmon.concurrencyFallback', { n: STATS_CONCURRENCY_FALLBACK }) }}</span>
        </p>

        <p class="sm__meta">{{ t('sysmon.snapshotAt', { t: relativeTime(stats.snapshot_at) }) }}</p>
      </template>
    </section>

    <!-- ══════ 近 1 小时计数 ══════ -->
    <section v-if="stats" class="sm__panel">
      <span class="sm__panel-title">{{ t('sysmon.lastHour') }}</span>
      <div class="sm__grid">
        <span class="sm__cell">
          <span class="sm__cell-l">{{ t('sysmon.completed') }}</span>
          <span class="sm__cell-v sm__cell-v--ok">{{ fmtInt(stats.completed_total_1h) }}</span>
        </span>
        <span class="sm__cell">
          <span class="sm__cell-l">{{ t('sysmon.failed') }}</span>
          <span class="sm__cell-v sm__cell-v--bad">{{ fmtInt(stats.failed_total_1h) }}</span>
        </span>
        <span class="sm__cell">
          <span class="sm__cell-l">{{ t('sysmon.skipped') }}</span>
          <span class="sm__cell-v">{{ fmtInt(stats.skipped_total_1h) }}</span>
        </span>
        <span class="sm__cell">
          <span class="sm__cell-l">{{ t('sysmon.tokens') }}</span>
          <span class="sm__cell-v">{{ fmtInt(stats.total_tokens_1h) }}</span>
        </span>
      </div>

      <!-- ★★ 三项之和**不是**总数：expired 不被任何一项统计 -->
      <p class="sm__note sm__note--strong">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('sysmon.accounted', { n: statsAccountedRuns(stats) }) }}</span>
      </p>

      <!-- ★★ 四项全 0 ⇒ Scan 可能失败 -->
      <p v-if="countsMayBeUnavailable" class="sm__warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('sysmon.countsUnavailable') }}</span>
      </p>
    </section>

    <!-- ══════ 最近已结束的运行 ══════ -->
    <div class="sm__limits" role="group" :aria-label="t('sysmon.limitLabel')">
      <button
        v-for="n in LIMIT_CHOICES"
        :key="n"
        type="button"
        class="sm__chip"
        :class="{ 'sm__chip--on': limit === n }"
        @click="((limit = n), load())"
      >
        {{ t('sysmon.limitN', { n }) }}
      </button>
    </div>

    <!-- ★★ 只含已结束的运行 -->
    <p v-if="PROBE_RUNS_ONLY_SETTLED" class="sm__note">
      <AppIcon name="alert" :size="13" />
      <span>{{ t('sysmon.settledOnly') }}</span>
    </p>

    <p v-if="error" class="sm__msg sm__msg--err">{{ error }}</p>
    <p v-if="loading" class="sm__msg">{{ t('common.loading') }}</p>
    <p v-else-if="runsEmpty" class="sm__msg">{{ t('sysmon.empty') }}</p>

    <p v-if="atCap" class="sm__note sm__note--strong">
      <AppIcon name="alert" :size="13" />
      <span>{{ t('sysmon.truncated', { n: runs?.total ?? 0, limit: runs?.limit ?? limit }) }}</span>
    </p>
    <p v-else-if="runList.length" class="sm__count">{{ t('sysmon.count', { n: runs?.total ?? runList.length }) }}</p>

    <ul v-if="runList.length" class="sm__list">
      <li v-for="r in runList" :key="r.id" class="sm__item">
        <div class="sm__item-head">
          <StatusDot :tone="runTone(r)" />
          <span class="sm__status">{{ r.status }}</span>
          <span class="sm__task">{{ r.task_type }}</span>
          <span class="badge" :class="`badge--${runTone(r)}`">#{{ r.credential_id }}</span>
        </div>

        <p class="sm__model">{{ r.raw_model }}</p>

        <div class="sm__kv">
          <span class="sm__kv-item">
            <span class="sm__kv-l">{{ t('sysmon.attempt') }}</span>
            <span class="sm__kv-v">{{ r.attempt }}</span>
          </span>
          <span class="sm__kv-item">
            <span class="sm__kv-l">{{ t('sysmon.httpStatus') }}</span>
            <!-- ★ 键缺失 = SQL NULL，不是 0 -->
            <span class="sm__kv-v">{{ r.http_status === undefined ? '—' : r.http_status }}</span>
          </span>
          <span class="sm__kv-item">
            <span class="sm__kv-l">{{ t('sysmon.latency') }}</span>
            <span class="sm__kv-v">{{ r.latency_ms === undefined ? '—' : `${r.latency_ms}ms` }}</span>
          </span>
          <span class="sm__kv-item">
            <span class="sm__kv-l">{{ t('sysmon.duration') }}</span>
            <span class="sm__kv-v">{{ fmtDuration(r) }}</span>
          </span>
        </div>

        <!-- ★★ expired 是唯一「后端三个计数都没统计」的状态 -->
        <p v-if="statusIsUnaccounted(r.status)" class="sm__warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('sysmon.expiredUnaccounted') }}</span>
        </p>

        <p v-if="r.err_code" class="sm__reason">
          {{ t('sysmon.errCode', { code: r.err_code }) }}
        </p>
        <p v-if="r.skip_reason" class="sm__reason">
          {{ t('sysmon.skipReason', { reason: r.skip_reason }) }}
        </p>
        <p class="sm__meta">{{ t('sysmon.finishedAt', { t: relativeTime(r.finished_at) }) }}</p>
      </li>
    </ul>

    <!-- ★★ 图例常驻：三个「可能未知」的来源一次说清 -->
    <section v-if="runList.length" class="sm__legend">
      <p class="sm__legend-title">{{ t('sysmon.legendTitle') }}</p>
      <p class="sm__legend-line">{{ t('sysmon.legendNotWired') }}</p>
      <p class="sm__legend-line">{{ t('sysmon.legendTotal') }}</p>
      <p class="sm__legend-line">{{ t('sysmon.legendTotalIsPageRows', { n: runs?.total ?? 0 }) }}</p>
    </section>
  </div>
</template>

<style scoped>
.sm {
  padding: var(--app-space-3);
}
.sm__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.sm__panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  margin-bottom: var(--app-space-2);
}
.sm__panel-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.sm__grid {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
}
.sm__cell {
  display: inline-flex;
  flex-direction: column;
  gap: 2px;
}
.sm__cell-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.sm__cell-v {
  font-size: 16px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.sm__cell-v--ok {
  color: var(--app-success);
}
.sm__cell-v--bad {
  color: var(--app-danger);
}
.sm__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.sm__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.sm__warn {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  padding: 6px 8px;
  border-radius: var(--app-radius-sm);
  background: var(--app-warning-soft);
  color: var(--app-warning);
  font-size: 11px;
  line-height: 1.5;
}
.sm__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.sm__note--strong {
  color: var(--app-warning);
  font-weight: 600;
}
.sm__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
.sm__limits {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: var(--app-space-2);
}
.sm__chip {
  min-height: 48px;
  min-width: 60px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.sm__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.sm__count {
  margin: var(--app-space-2) 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.sm__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.sm__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.sm__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.sm__status {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.sm__task {
  font-size: 11px;
  color: var(--app-text-secondary);
}
.sm__model {
  margin: 4px 0 0;
  font-size: 13px;
  color: var(--app-text);
  word-break: break-word;
}
.sm__kv {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.sm__kv-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.sm__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.sm__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.sm__reason {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-secondary);
  word-break: break-word;
}
.sm__legend {
  margin-top: var(--app-space-3);
  padding: var(--app-space-3);
  border: 1px dashed var(--app-border);
  border-radius: var(--app-radius);
}
.sm__legend-title {
  margin: 0 0 6px;
  font-size: 12px;
  font-weight: 700;
  color: var(--app-text-secondary);
}
.sm__legend-line {
  margin: 0 0 4px;
  font-size: 11px;
  color: var(--app-text-muted);
  line-height: 1.6;
}
</style>