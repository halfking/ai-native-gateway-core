<script setup lang="ts">
// RoutingOptView — 路由优化器读面（/routing-opt/{stats,accuracy,parameters,metrics}，**admin 档**）。
//
// 它答的是「**优化器自己调得准不准、现在用的是哪套参数**」，与已上移的
//   /overrides（规则是什么）  /routing-audit（谁改的）  /funnel（请求漏斗）
//   /matrix（热力矩阵）        /model-task-index（5 分钟桶表现）
// 互不重叠 —— 这一族是**效果与参数**面。
//
// ⚠️★★★ 六个后端语义（详见 api/routingOpt.ts 文件头）：
//
// (1) ★★★★ 准确率是 **1 条人工标注算 2 条**的加权平均，不是合并准确率；
//     而且响应**只给样本量、不给命中数** ⇒ 客户端**无法验证**这个值。
// (2) ★★★★ auto 与 human 的「正确」是**两种定义**（请求成功 vs 预测命中人工真值）。
// (3) ★★★★ `overall_accuracy` / `accuracy` 是 **0..1 比率**（量纲第 5 处）⇒ ×100。
// (4) ★★★ accuracy 的桶**恒有样本** ⇒ 桶里 0% 是真的 0%，不是「没数据」。
// (5) ★★★ `accuracy_source` 三种来源语义完全不同：
//     实时 / 持久化旧值回落 / 完全没有 ⇒ 必须分别渲染。
// (6) ★★★ `hours` 静默回落 24、静默 clamp 720；而 `stats` 的窗口**写死 24**、
//     **没有参数** ⇒ 两个端点的窗口不能共用一个控件。
//
// ★ `metrics` 的后端 LIMIT 2000 在长窗口下**必然命中**（720h ⇒ 8640 个时间桶），
//   丢的是**最旧**的数据；好在后端**自带 truncated 标记**。
// ★ `parameters` 没有激活版本时是 **404** ⇒ 「还没配置」，不是错误。
// ★ 本页的 metrics 只渲染前 50 行，并**明说**只显示了前 50 行。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchRoutingOptStats,
  fetchRoutingOptAccuracy,
  fetchRoutingOptParameters,
  fetchRoutingOptMetrics,
  formatRoutingOptAccuracy,
  accuracyIsLive,
  accuracyIsStaleFallback,
  accuracyHasNoSource,
  accuracySourceKnown,
  metricsLikelyTruncated,
  metricsRowDim,
  metricsRowIsAggregate,
  ROUTING_OPT_HOURS_DEFAULT,
  ROUTING_OPT_HOURS_MAX,
  ROUTING_OPT_STATS_WINDOW_HOURS,
  ROUTING_OPT_METRICS_ROW_CAP,
  type RoutingOptStats,
  type RoutingOptParameters,
  type RoutingOptAccuracyResponse,
  type RoutingOptMetricsResponse,
} from '@/api/routingOpt'

useHyperPage({ title: () => t('routingOpt.title') })

const HOUR_CHOICES = [1, 24, 168, ROUTING_OPT_HOURS_MAX]
const METRICS_DISPLAY_CAP = 50

const accHours = ref<number>(ROUTING_OPT_HOURS_DEFAULT)
const mHours = ref<number>(ROUTING_OPT_HOURS_DEFAULT)
const mTaskType = ref('')
const mProvider = ref('')

const stats = ref<RoutingOptStats | null>(null)
const params = ref<RoutingOptParameters | null>(null)
const accuracy = ref<RoutingOptAccuracyResponse | null>(null)
const metrics = ref<RoutingOptMetricsResponse | null>(null)

const loading = ref(false)
const error = ref<string | null>(null)
/** `parameters` 的 404 = 「没有激活版本」，与错误分开。 */
const paramsMissing = ref(false)
const paramsError = ref<string | null>(null)
const accError = ref<string | null>(null)
const metricsError = ref<string | null>(null)

async function loadAll(): Promise<void> {
  loading.value = true
  error.value = null
  // ★ 四个端点**独立**取：一个失败不许清空其它三个。
  const [s, p, a, m] = await Promise.allSettled([
    fetchRoutingOptStats(),
    fetchRoutingOptParameters(),
    fetchRoutingOptAccuracy({ hours: accHours.value }),
    fetchRoutingOptMetrics({
      hours: mHours.value,
      taskType: mTaskType.value.trim() || undefined,
      provider: mProvider.value.trim() || undefined,
    }),
  ])

  if (s.status === 'fulfilled') stats.value = s.value
  else {
    stats.value = null
    error.value = (s.reason as Error)?.message || t('common.error')
  }

  if (p.status === 'fulfilled') {
    params.value = p.value
    paramsMissing.value = false
    paramsError.value = null
  } else {
    params.value = null
    // ★ 404 = 没有激活版本（不是错误）；其它才是错误。
    // ★★ 错误写进**本面板自己的** paramsError，不去覆盖顶部那个
    //    `error` —— 否则 parameters 的失败会把 stats 的失败盖掉，
    //    两个端点就又不是独立的了。
    if ((p.reason as { status?: number })?.status === 404) {
      paramsMissing.value = true
      paramsError.value = null
    } else {
      paramsMissing.value = false
      paramsError.value = (p.reason as Error)?.message || t('common.error')
    }
  }

  if (a.status === 'fulfilled') {
    accuracy.value = a.value
    accError.value = null
  } else {
    accuracy.value = null
    accError.value = (a.reason as Error)?.message || t('common.error')
  }

  if (m.status === 'fulfilled') {
    metrics.value = m.value
    metricsError.value = null
  } else {
    metrics.value = null
    metricsError.value = (m.reason as Error)?.message || t('common.error')
  }

  loading.value = false
}
void loadAll()

function reload(): void {
  void loadAll()
}

const buckets = computed(() => accuracy.value?.buckets ?? [])
const metricsRows = computed(() => metrics.value?.rows ?? [])
const displayedRows = computed(() => metricsRows.value.slice(0, METRICS_DISPLAY_CAP))
const hiddenRowCount = computed(() => Math.max(0, metricsRows.value.length - displayedRows.value.length))

function toneOf(v: number | undefined): 'success' | 'warning' | 'danger' | 'muted' {
  if (v === undefined) return 'muted'
  if (v >= 0.95) return 'success'
  if (v >= 0.8) return 'warning'
  return 'danger'
}

onBeforeUnmount(() => {
  stats.value = null
  params.value = null
  accuracy.value = null
  metrics.value = null
  error.value = null
  accError.value = null
  metricsError.value = null
  paramsMissing.value = false
  paramsError.value = null
})
</script>

<template>
  <div class="view-root ro">
    <p v-if="error" class="ro__msg ro__msg--err">{{ error }}</p>
    <p v-if="loading" class="ro__msg">{{ t('common.loading') }}</p>

    <!-- ══════ 总体准确率 ══════ -->
    <section v-if="stats" class="ro__panel">
      <span class="ro__panel-title">{{ t('routingOpt.overall') }}</span>

      <div class="ro__headline">
        <StatusDot :tone="accuracyIsLive(stats.accuracy_source) ? toneOf(stats.overall_accuracy) : 'warning'" />
        <span class="ro__acc">{{ formatRoutingOptAccuracy(stats.overall_accuracy) }}</span>
      </div>

      <!-- ★★★ 三种来源语义完全不同，必须分别说明 -->
      <p v-if="accuracyIsStaleFallback(stats.accuracy_source)" class="ro__warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.sourceStale') }}</span>
      </p>
      <p v-else-if="accuracyHasNoSource(stats.accuracy_source)" class="ro__warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.sourceNone') }}</span>
      </p>
      <p v-else-if="!accuracySourceKnown(stats.accuracy_source)" class="ro__warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.sourceUnknown', { source: stats.accuracy_source }) }}</span>
      </p>

      <!-- ★★ 加权口径 + 两种「正确」定义 + 无法验证，三件事一次说清 -->
      <p class="ro__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.weightedNote') }}</span>
      </p>
      <p class="ro__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.twoDefinitionsNote') }}</span>
      </p>
      <p class="ro__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.notVerifiableNote') }}</span>
      </p>

      <!-- ★★★ 常量不是摆设：当响应的 window_hours 与「后端写死值」不一致时，
           说明后端改了常量而文档/常量没跟上，必须说出来而不是照抄响应。 -->
      <p v-if="stats.window_hours !== ROUTING_OPT_STATS_WINDOW_HOURS" class="ro__warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.windowMismatch', { got: stats.window_hours, expect: ROUTING_OPT_STATS_WINDOW_HOURS }) }}</span>
      </p>

      <div class="ro__grid">
        <span class="ro__cell">
          <span class="ro__cell-l">{{ t('routingOpt.window') }}</span>
          <span class="ro__cell-v">{{ t('routingOpt.windowFixed', { n: ROUTING_OPT_STATS_WINDOW_HOURS }) }}</span>
        </span>
        <span class="ro__cell">
          <span class="ro__cell-l">{{ t('routingOpt.paramVersion') }}</span>
          <span class="ro__cell-v">{{ stats.parameter_version }}</span>
        </span>
        <span class="ro__cell">
          <span class="ro__cell-l">{{ t('routingOpt.autoSamples') }}</span>
          <span class="ro__cell-v">{{ fmtInt(stats.auto_samples) }}</span>
        </span>
        <span class="ro__cell">
          <span class="ro__cell-l">{{ t('routingOpt.humanSamples') }}</span>
          <span class="ro__cell-v">{{ fmtInt(stats.human_samples) }}</span>
        </span>
      </div>
      <p v-if="stats.state_updated_at" class="ro__meta">
        {{ t('routingOpt.stateUpdatedAt', { t: relativeTime(stats.state_updated_at) }) }}
      </p>
    </section>

    <!-- ══════ 激活参数 ══════ -->
    <section class="ro__panel">
      <span class="ro__panel-title">{{ t('routingOpt.params') }}</span>
      <p v-if="paramsMissing" class="ro__msg">{{ t('routingOpt.noActiveParams') }}</p>
      <p v-else-if="paramsError" class="ro__msg ro__msg--err">{{ paramsError }}</p>
      <template v-else-if="params">
        <div class="ro__grid">
          <span class="ro__cell">
            <span class="ro__cell-l">{{ t('routingOpt.version') }}</span>
            <span class="ro__cell-v">{{ params.version }}</span>
          </span>
          <span class="ro__cell">
            <span class="ro__cell-l">{{ t('routingOpt.exploration') }}</span>
            <span class="ro__cell-v">{{ params.exploration_rate }}</span>
          </span>
          <span class="ro__cell">
            <span class="ro__cell-l">{{ t('routingOpt.learningRate') }}</span>
            <span class="ro__cell-v">{{ params.learning_rate }}</span>
          </span>
          <span class="ro__cell">
            <span class="ro__cell-l">{{ t('routingOpt.adaptation') }}</span>
            <span class="ro__cell-v">{{ params.adaptation_window }}</span>
          </span>
        </div>
        <p class="ro__meta">{{ t('routingOpt.activatedAt', { t: relativeTime(params.activated_at) }) }}</p>
        <p v-if="params.created_by" class="ro__meta">{{ t('routingOpt.createdBy', { who: params.created_by }) }}</p>
        <p v-if="params.notes" class="ro__meta ro__notes">{{ params.notes }}</p>
      </template>
    </section>

    <!-- ══════ 小时 × 任务 准确率桶 ══════ -->
    <section class="ro__panel">
      <div class="ro__panel-head">
        <span class="ro__panel-title">{{ t('routingOpt.byHour') }}</span>
      </div>
      <p class="ro__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.bucketHasSamplesNote') }}</span>
      </p>

      <div class="ro__hours" role="group" :aria-label="t('routingOpt.hoursLabel')">
        <button
          v-for="h in HOUR_CHOICES"
          :key="h"
          type="button"
          class="ro__chip"
          :class="{ 'ro__chip--on': accHours === h }"
          @click="((accHours = h), reload())"
        >
          {{ h }}h
        </button>
      </div>

      <p v-if="accError" class="ro__msg ro__msg--err">{{ accError }}</p>
      <p v-else-if="!buckets.length && !loading" class="ro__msg">{{ t('routingOpt.noBuckets') }}</p>

      <ul v-if="buckets.length" class="ro__list">
        <li v-for="b in buckets" :key="b.hour + '|' + b.task_type" class="ro__item">
          <div class="ro__item-head">
            <StatusDot :tone="toneOf(b.accuracy)" />
            <span class="ro__acc-small">{{ formatRoutingOptAccuracy(b.accuracy) }}</span>
            <span class="ro__task">{{ b.task_type }}</span>
          </div>
          <p class="ro__meta">{{ t('routingOpt.bucketAt', { t: relativeTime(b.hour) }) }}</p>
          <p class="ro__meta">
            {{ t('routingOpt.bucketSamples', { n: b.samples, h: b.human_samples }) }}
          </p>
        </li>
      </ul>
    </section>

    <!-- ══════ 5 分钟聚合明细 ══════ -->
    <section class="ro__panel">
      <div class="ro__panel-head">
        <span class="ro__panel-title">{{ t('routingOpt.metrics') }}</span>
      </div>

      <!-- ★★★★ 四个端点里只有这一个读**物化聚合表**（后台 sweep 写入），
           另外三个实时读 ⇒ sweep 落后时这一块会和上面的准确率对不上。 -->
      <p class="ro__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.metricsMaterializedNote') }}</span>
      </p>
      <!-- ★ 同一批数据会同时产出四类行（后端 GROUPING SETS），必须逐类标 -->
      <p class="ro__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.groupingSetsNote') }}</span>
      </p>

      <form class="ro__form" @submit.prevent="reload">
        <label class="ro__field">
          <span>{{ t('routingOpt.taskType') }}</span>
          <input v-model="mTaskType" class="ro__input" :placeholder="t('routingOpt.exactMatchHint')" autocomplete="off" autocapitalize="off" spellcheck="false" />
        </label>
        <label class="ro__field">
          <span>{{ t('routingOpt.provider') }}</span>
          <input v-model="mProvider" class="ro__input" :placeholder="t('routingOpt.exactMatchHint')" autocomplete="off" autocapitalize="off" spellcheck="false" />
        </label>
        <div class="ro__hours">
          <button
            v-for="h in HOUR_CHOICES"
            :key="h"
            type="button"
            class="ro__chip"
            :class="{ 'ro__chip--on': mHours === h }"
            @click="((mHours = h), reload())"
          >
            {{ h }}h
          </button>
        </div>
        <button type="submit" class="ro__btn ro__btn--go">{{ t('routingOpt.query') }}</button>
      </form>

      <!-- ★★★ 长窗口下 2000 上限必然命中，丢的是最旧的数据 -->
      <p v-if="metrics?.truncated" class="ro__warn">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.metricsTruncated', { cap: ROUTING_OPT_METRICS_ROW_CAP }) }}</span>
      </p>
      <p v-else-if="metricsLikelyTruncated(mHours)" class="ro__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('routingOpt.metricsLikelyTruncated', { hours: mHours, cap: ROUTING_OPT_METRICS_ROW_CAP }) }}</span>
      </p>

      <p v-if="metricsError" class="ro__msg ro__msg--err">{{ metricsError }}</p>
      <p v-else-if="!metricsRows.length && !loading" class="ro__msg">{{ t('routingOpt.noMetrics') }}</p>

      <template v-if="metricsRows.length">
        <!-- ★ 只渲染前 50 行，且明说 -->
        <p v-if="hiddenRowCount > 0" class="ro__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('routingOpt.displayCap', { shown: displayedRows.length, hidden: hiddenRowCount }) }}</span>
        </p>
        <ul class="ro__list">
          <li v-for="(r, i) in displayedRows" :key="r.time_bucket + '|' + i" class="ro__item">
            <div class="ro__item-head">
              <!-- ★ 只有 task×provider 两列都有的行才是「单个预测组合」，
                   其余三类都是 GROUPING SETS 产出的**汇总行**，不能同色。 -->
              <StatusDot :tone="metricsRowIsAggregate(r.task_type, r.predicted_provider) ? 'muted' : 'success'" />
              <span class="ro__task">{{ t('routingOpt.dim_' + metricsRowDim(r.task_type, r.predicted_provider)) }}</span>
              <span class="ro__dimvals">
                <span v-if="r.task_type" class="ro__task">{{ r.task_type }}</span>
                <span v-if="r.predicted_provider" class="ro__prov">{{ r.predicted_provider }}</span>
              </span>
              <!-- ★ 汇总行标出来：否则同一批数据的汇总会看起来像「又一条预测」 -->
              <span v-if="metricsRowIsAggregate(r.task_type, r.predicted_provider)" class="ro__agg">{{ t('routingOpt.aggregateRow') }}</span>
            </div>
            <p class="ro__meta">{{ t('routingOpt.bucketAt', { t: relativeTime(r.time_bucket) }) }}</p>
            <div class="ro__kv">
              <span class="ro__kv-item">
                <span class="ro__kv-l">{{ t('routingOpt.requests') }}</span>
                <span class="ro__kv-v">{{ fmtInt(r.total_requests) }}</span>
              </span>
              <!-- ★ 可空键缺失 = NULL ⇒ 显示「—」 -->
              <span class="ro__kv-item">
                <span class="ro__kv-l">{{ t('routingOpt.accuracyRate') }}</span>
                <span class="ro__kv-v">{{ formatRoutingOptAccuracy(r.accuracy_rate) }}</span>
              </span>
              <span class="ro__kv-item">
                <span class="ro__kv-l">{{ t('routingOpt.p95') }}</span>
                <span class="ro__kv-v">{{ r.p95_latency_ms === undefined ? '—' : `${r.p95_latency_ms}ms` }}</span>
              </span>
              <span class="ro__kv-item">
                <span class="ro__kv-l">{{ t('routingOpt.humanCorrections') }}</span>
                <span class="ro__kv-v">{{ fmtInt(r.human_corrections) }}</span>
              </span>
            </div>
          </li>
        </ul>
      </template>
    </section>
  </div>
</template>

<style scoped>
.ro {
  padding: var(--app-space-3);
}
.ro__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.ro__panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}
.ro__panel-title {
  display: block;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  margin-bottom: var(--app-space-2);
}
.ro__headline {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ro__acc {
  font-size: 22px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.ro__acc-small {
  font-size: 14px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.ro__grid {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.ro__cell {
  display: inline-flex;
  flex-direction: column;
  gap: 2px;
}
.ro__cell-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.ro__cell-v {
  font-size: 15px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.ro__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.ro__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ro__warn {
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
.ro__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.ro__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.ro__notes {
  font-style: italic;
}
.ro__hours {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin: var(--app-space-2) 0;
}
.ro__chip {
  min-height: 48px;
  min-width: 56px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ro__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.ro__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
.ro__field {
  display: block;
}
.ro__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.ro__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.ro__btn {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ro__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.ro__list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}
.ro__item {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}
.ro__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.ro__task {
  font-size: 12px;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}
.ro__prov {
  font-size: 11px;
  color: var(--app-text-secondary);
  word-break: break-all;
}
.ro__dimvals {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
  flex-wrap: wrap;
}
.ro__agg {
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-muted);
  color: var(--app-text-muted);
  font-size: 10px;
  line-height: 1.6;
}
.ro__kv {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: 4px;
}
.ro__kv-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.ro__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.ro__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
</style>