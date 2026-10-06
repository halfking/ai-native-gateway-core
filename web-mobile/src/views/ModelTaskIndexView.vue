<script setup lang="ts">
// ModelTaskIndexView — 自动路由的「模型 × 任务」表现索引
// （/api/admin/auto-route/analytics/model-task-index，**superAdmin 档**）。
//
// 它补的是同族另外两份答不了的一问：
//   /funnel  = 单个模型在 7d/24h 窗口里的漏斗与可信度
//   /matrix  = 模型 × 任务的热力矩阵（count / success_rate / p95 / cost）
//   本页     = **按 5 分钟桶滚动**的表现排行，带 primary credential（主用凭据）
//
// ⚠️★★★ 五个后端语义（详见 api/modelTaskIndex.ts 文件头）：
//
// (1) ★★★ **只统计自动路由的请求。** 生产者 SQL 里有
//     `AND rl.is_auto_request = TRUE` ⇒ 人工/直连流量完全不进这张表。
//     ⇒ 排行榜天然偏小众模型，必须在页首说清口径。
//
// (2) ★★ **只返回最新一个 5 分钟桶**，不是窗口聚合，也不是趋势。
//     刷新器每 5 分钟跑一次 ⇒ 数据可能**滞后约 5~10 分钟**。
//     ⇒ 页首常驻「数据截至 <bucket>」+ 滞后提示。
//
// (3) ★★★ **量纲第 4 处**：`success_rate` 是 0..1 比率（不是百分数）
//     ⇒ 一律走 `formatModelTaskRatePct`，与 timeline 那份 0..100 的
//       `formatSuccessRatePct` **刻意不同名**。
//
// (4) ★★★ **三个数值列的「0」/「1000」是生产者 COALESCE 兜底值**：
//     `COALESCE(AVG(latency_ms), 0)`、`COALESCE(percentile_cont(...), 1000)`、
//     `SUM(total_tokens) > 0 ? ... : 0`。
//     ⇒ **0ms / 1000ms / $0 都不能当成真实测量**。数值照显，但加弱化标记，
//       并在页脚常驻图例 —— 后端给不出判据。
//
// (5) ★★ **`bucket === null` 是「后台刷新器尚未首刷」**，不是「没有数据」。
//     ⇒ 独立状态，不落进空态。
//
// ★ `top` 越界**静默回落 20**（不是 400）⇒ 只发 1..500 的整数；
//   因为 top 是客户端自己发的，`items.length === top` 是**精确**的截断信号
//   （不像时间线的写死 500 / 缓存的 4096）。
//
// 整条 auto-route 线是 superAdmin（handler.go:1430），导航已按 requiresRole 挡住。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchModelTaskIndex,
  isAwaitingFirstRefresh,
  modelTaskIndexTruncated,
  formatModelTaskRatePct,
  formatMs,
  formatCostPer1k,
  avgLatencyMayBeDefault,
  p95LatencyMayBeDefault,
  costMayBeNoData,
  modelTaskIndexKeyOf,
  taskLabel,
  MODEL_TASK_INDEX_TOP_DEFAULT,
  MODEL_TASK_INDEX_TOP_MAX,
  type ModelTaskIndexResponse,
} from '@/api/modelTaskIndex'

useHyperPage({ title: () => t('taskIndex.title') })

const TOP_CHOICES = [MODEL_TASK_INDEX_TOP_DEFAULT, 50, 100, MODEL_TASK_INDEX_TOP_MAX]

const taskType = ref('')
const top = ref<number>(MODEL_TASK_INDEX_TOP_DEFAULT)
const data = ref<ModelTaskIndexResponse | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchModelTaskIndex({
      // ★ 只 trim，**不**改大小写：后端 SQL 是 `task_type = $1` 大小写敏感。
      taskType: taskType.value.trim() || undefined,
      top: top.value,
    })
  } catch (err) {
    data.value = null
    const st = (err as { status?: number })?.status
    if (st === 403) error.value = t('taskIndex.errForbidden')
    else error.value = (err as Error)?.message || t('common.error')
  } finally {
    loading.value = false
    loaded.value = true
  }
}
void load()

function onSearch(): void {
  void load()
}

function onClear(): void {
  taskType.value = ''
  void load()
}

const items = computed(() => data.value?.items ?? [])
/** ★ 「尚未首刷」：bucket 为 null，与「有桶但这一桶没记录」是两件事。 */
const awaitingFirst = computed(() => isAwaitingFirstRefresh(data.value))
const atCap = computed(() => modelTaskIndexTruncated(data.value, top.value))
const isEmpty = computed(
  () => loaded.value && !loading.value && !awaitingFirst.value && !error.value && items.value.length === 0,
)

onBeforeUnmount(() => {
  data.value = null
  error.value = null
})
</script>

<template>
  <div class="view-root ti">
    <!-- ★★★ 口径说明常驻：三条会让「排行榜」被误读的事实 -->
    <section class="ti__scope">
      <p class="ti__scope-line">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('taskIndex.scopeNote') }}</span>
      </p>
      <p v-if="data?.bucket" class="ti__scope-line ti__scope-line--dim">
        <AppIcon name="clock" :size="13" />
        <span>{{ t('taskIndex.bucketAt', { t: relativeTime(data.bucket) }) }} · {{ t('taskIndex.lagNote') }}</span>
      </p>
    </section>

    <form class="ti__form" @submit.prevent="onSearch">
      <label class="ti__field">
        <span>{{ t('taskIndex.taskType') }}</span>
        <input
          v-model="taskType"
          class="ti__input"
          :placeholder="t('taskIndex.taskTypePlaceholder')"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
        />
      </label>
      <div class="ti__top" role="group" :aria-label="t('taskIndex.topLabel')">
        <button
          v-for="n in TOP_CHOICES"
          :key="n"
          type="button"
          class="ti__chip"
          :class="{ 'ti__chip--on': top === n }"
          @click="((top = n), onSearch())"
        >
          {{ t('taskIndex.topN', { n }) }}
        </button>
      </div>
      <div class="ti__actions">
        <button type="submit" class="ti__btn ti__btn--go">{{ t('taskIndex.search') }}</button>
        <button v-if="taskType" type="button" class="ti__btn" @click="onClear">
          {{ t('common.clearFilters') }}
        </button>
      </div>
    </form>

    <p v-if="error" class="ti__msg ti__msg--err">{{ error }}</p>

    <!-- ★★ 「尚未首刷新」与「这一桶没有记录」必须分开 -->
    <section v-if="awaitingFirst" class="ti__await">
      <div class="ti__await-head">
        <StatusDot tone="warning" />
        <span class="ti__await-title">{{ t('taskIndex.awaitingFirst') }}</span>
      </div>
      <p class="ti__await-hint">{{ t('taskIndex.awaitingFirstHint') }}</p>
    </section>

    <p v-if="loading" class="ti__msg">{{ t('common.loading') }}</p>
    <p v-else-if="isEmpty" class="ti__msg">{{ t('taskIndex.empty') }}</p>

    <p v-if="atCap" class="ti__note ti__note--warn">
      <AppIcon name="alert" :size="13" />
      <span>{{ t('taskIndex.truncated', { n: items.length, top }) }}</span>
    </p>
    <p v-else-if="items.length" class="ti__count">{{ t('taskIndex.count', { n: items.length }) }}</p>

    <ul v-if="items.length" class="ti__list">
      <li v-for="it in items" :key="modelTaskIndexKeyOf(it)" class="ti__item">
        <div class="ti__item-head">
          <StatusDot :tone="it.success_rate === undefined ? 'muted' : it.success_rate >= 0.99 ? 'success' : it.success_rate >= 0.9 ? 'warning' : 'danger'" />
          <!-- ★ canonical_name 来自 LEFT JOIN，未命中时**整个键不存在** ⇒ 回落成 id -->
          <span class="ti__item-model">{{ it.canonical_name || `#${it.canonical_id}` }}</span>
          <span class="badge badge--muted">{{ taskLabel(it.task_type, t) }}</span>
        </div>

        <div class="ti__kv">
          <span class="ti__kv-item">
            <span class="ti__kv-l">{{ t('taskIndex.samples') }}</span>
            <span class="ti__kv-v">{{ fmtInt(it.sample_count) }}</span>
          </span>
          <span class="ti__kv-item">
            <span class="ti__kv-l">{{ t('taskIndex.success') }}</span>
            <span class="ti__kv-v">{{ formatModelTaskRatePct(it.success_rate) }}</span>
          </span>
        </div>

        <div class="ti__kv">
          <!-- ★ 兜底值照显数值，但加弱化标记：后端给不出「真 0ms」与「没量到」的判据 -->
          <span class="ti__kv-item" :class="{ 'ti__kv-item--maybe': avgLatencyMayBeDefault(it.avg_latency_ms) }">
            <span class="ti__kv-l">{{ t('taskIndex.avgLatency') }}</span>
            <span class="ti__kv-v">{{ formatMs(it.avg_latency_ms) }}</span>
          </span>
          <span class="ti__kv-item" :class="{ 'ti__kv-item--maybe': p95LatencyMayBeDefault(it.p95_latency_ms) }">
            <span class="ti__kv-l">{{ t('taskIndex.p95Latency') }}</span>
            <span class="ti__kv-v">{{ formatMs(it.p95_latency_ms) }}</span>
          </span>
        </div>

        <div class="ti__kv">
          <span class="ti__kv-item" :class="{ 'ti__kv-item--maybe': costMayBeNoData(it.avg_cost_per_1k_usd) }">
            <span class="ti__kv-l">{{ t('taskIndex.cost') }}</span>
            <span class="ti__kv-v">{{ formatCostPer1k(it.avg_cost_per_1k_usd) }}</span>
          </span>
          <span v-if="it.primary_credential_id !== undefined" class="ti__kv-item">
            <span class="ti__kv-l">{{ t('taskIndex.primaryCred') }}</span>
            <span class="ti__kv-v">#{{ it.primary_credential_id }}</span>
          </span>
        </div>

        <!-- ★ 合成键 __specified__ 必须说清含义，否则用户以为那是真实任务类型 -->
        <p v-if="it.task_type === '__specified__'" class="ti__warn">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('taskIndex.specifiedHint') }}</span>
        </p>

        <p class="ti__meta">{{ t('taskIndex.updatedAt', { t: relativeTime(it.updated_at) }) }}</p>
      </li>
    </ul>

    <!-- ★★ 图例常驻：那三个兜底值是本表最容易误读的地方 -->
    <section v-if="items.length" class="ti__legend">
      <p class="ti__legend-title">{{ t('taskIndex.legendTitle') }}</p>
      <ul class="ti__legend-list">
        <li>{{ t('taskIndex.legendAvg') }}</li>
        <li>{{ t('taskIndex.legendP95') }}</li>
        <li>{{ t('taskIndex.legendCost') }}</li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.ti {
  padding: var(--app-space-3);
}
.ti__scope {
  border: 1px solid var(--app-warning);
  border-left-width: 3px;
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-2) var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.ti__scope-line {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: 0;
  color: var(--app-text-secondary);
  font-size: 11px;
  line-height: 1.5;
}
.ti__scope-line--dim {
  margin-top: 4px;
  color: var(--app-text-muted);
}
.ti__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-3);
}
.ti__field {
  display: block;
}
.ti__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.ti__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.ti__top {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.ti__chip {
  min-height: 48px;
  min-width: 56px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ti__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.ti__actions {
  display: flex;
  gap: var(--app-space-2);
}
.ti__btn {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ti__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.ti__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.ti__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ti__await {
  border: 1px solid var(--app-warning);
  border-left-width: 3px;
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.ti__await-head {
  display: flex;
  align-items: center;
  gap: 6px;
}
.ti__await-title {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.ti__await-hint {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-secondary);
  line-height: 1.5;
}
.ti__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.ti__note--warn {
  color: var(--app-warning);
}
.ti__count {
  margin: var(--app-space-2) 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.ti__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.ti__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.ti__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.ti__item-model {
  font-size: 14px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-word;
  min-width: 0;
}
.ti__kv {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.ti__kv-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.ti__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.ti__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
/* ★ 兜底值弱化：不是删数值（后端给不出判据），是让它不敢冒充真实测量 */
.ti__kv-item--maybe .ti__kv-v {
  color: var(--app-text-muted);
  font-style: italic;
}
.ti__warn {
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
.ti__meta {
  margin: 2px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
}
.ti__legend {
  margin-top: var(--app-space-3);
  padding: var(--app-space-3);
  border: 1px dashed var(--app-border);
  border-radius: var(--app-radius);
}
.ti__legend-title {
  margin: 0 0 6px;
  font-size: 12px;
  font-weight: 700;
  color: var(--app-text-secondary);
}
.ti__legend-list {
  margin: 0;
  padding-left: 16px;
  font-size: 11px;
  color: var(--app-text-muted);
  line-height: 1.6;
}
</style>