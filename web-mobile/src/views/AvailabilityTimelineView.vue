<script setup lang="ts">
// AvailabilityTimelineView — 模型级可用性时间线（/api/admin/probe/availability-timeline，**admin 档**）。
//
// 它与已上移的各页构成「**模型为什么现在坏了**」的证据链：
//   /probe-model   = 现在这个模型整体什么状态（快照）
//   本页           = **它是什么时候开始坏的**（近 24 小时，按小时）
//   /node-health/:id = 单个凭据级的时间线
//
// ⚠️★★★ 四个后端语义（详见 api/probeTimelineCache.ts 文件头）：
//
// (1) ★★ **`LIMIT 500` 写死在 SQL 里，响应里没有任何截断标记。**
//     视图只保留近 24 小时且按小时聚合 ⇒ 24 行/模型
//     ⇒ 500 行 ≈ **20 个模型**。⇒ `total === 500` 只能说「至少这么多」，
//     不能说「共 N 条，全部如下」。
//
// (2) ★★★ **`success_rate` 在视图里已经乘过 100**（0..100）。
//     ★ 而**同一次会话**里 `probe/dashboard` 的 `avg_success_rate_7d` 是 0..1
//       必须 ×100。两个视图都是「成功率」，量纲相反，抄错差 100 倍。
//     ⇒ 一律走 `formatSuccessRatePct`，不自己写 `.toFixed(1) + '%'`。
//
// (3) ★ `avg_latency_ms` 是 `avg(...) FILTER (WHERE status='ok')`
//     ⇒ 那一小时**没有成功探测**时是 SQL NULL ⇒ omitempty ⇒ 键不存在。
//     ⇒ 「没有成功的探测」不是「延时 0ms」。
//
// (4) ★ `model` 是**精确匹配**（`raw_model_name = $1`），
//     而 `/probe-model` 那页是 **ILIKE 子串匹配**。
//     ⇒ 本页必须说明，否则用户会以为「那边搜得到、这边搜不到」是数据问题。
//
// (5) ★ `timeline` 是 **nil slice** ⇒ 空时是 `null` 不是 `[]`
//     （`tuning/proposals` 那个端点专门修过这个，这里没修）。
//
// (6) `outbound_model_name` 恒等于 `raw_model_name`（视图里的同一个别名）
//     ⇒ 不显示「raw → outbound」箭头，那会显示成「gpt-4o → gpt-4o」。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import { t } from '@/i18n'
import { fmtInt, fmtTime } from '@/utils/format'
import {
  fetchAvailabilityTimeline,
  timelineAtCap,
  formatSuccessRatePct,
  avgLatencyOf,
  groupByModel,
  AVAILABILITY_TIMELINE_ROW_CAP,
  type AvailabilityTimelineResponse,
} from '@/api/probeTimelineCache'

useHyperPage({ title: () => t('timeline.title') })

const model = ref('')
const data = ref<AvailabilityTimelineResponse | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)

async function load(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    data.value = await fetchAvailabilityTimeline(model.value.trim() ? { model: model.value.trim() } : {})
  } catch (err) {
    data.value = null
    error.value = describeError(err)
  } finally {
    loading.value = false
    loaded.value = true
  }
}
void load()

function describeError(err: unknown): string {
  const status = (err as { status?: number })?.status
  if (status === 403) return t('timeline.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

function onSearch(): void {
  void load()
}

function onClear(): void {
  model.value = ''
  void load()
}

/** ★ nil slice 兜底。见文件头 (5)。 */
const groups = computed(() => groupByModel(data.value?.timeline))
const atCap = computed(() => timelineAtCap(data.value))
const total = computed(() => data.value?.total ?? 0)
const isEmpty = computed(() => loaded.value && !loading.value && groups.value.length === 0 && !error.value)

/** 一个小时桶的粗判：全成功 / 有失败 / 没有探测。 */
function hourTone(p: { total_probes: number; failed_probes: number }): 'success' | 'warning' | 'muted' {
  if (p.total_probes === 0) return 'muted'
  if (p.failed_probes === 0) return 'success'
  return 'warning'
}

function latencyText(p: Parameters<typeof avgLatencyOf>[0]): string {
  const ms = avgLatencyOf(p)
  return ms === null ? t('timeline.noSuccessProbe') : t('timeline.avgLatency', { ms })
}

onBeforeUnmount(() => {
  data.value = null
})
</script>

<template>
  <div class="view-root tl">
    <form class="tl__form" @submit.prevent="onSearch">
      <label class="tl__field">
        <span>{{ t('timeline.searchLabel') }}</span>
        <input
          v-model="model"
          class="tl__input"
          :placeholder="t('timeline.searchPlaceholder')"
          autocomplete="off"
          autocapitalize="off"
          spellcheck="false"
        />
      </label>
      <div class="tl__actions">
        <button type="submit" class="tl__btn tl__btn--go">{{ t('timeline.search') }}</button>
        <button v-if="model" type="button" class="tl__btn" @click="onClear">{{ t('common.clearFilters') }}</button>
      </div>
    </form>

    <!-- ★ 本页是精确匹配，与 /probe-model 的子串匹配不同 —— 必须说清 -->
    <p class="tl__note">
      <AppIcon name="alert" :size="13" />
      <span>{{ t('timeline.exactMatchNote') }}</span>
    </p>

    <p v-if="error" class="tl__msg tl__msg--err">{{ error }}</p>
    <p v-if="loading" class="tl__msg">{{ t('common.loading') }}</p>
    <p v-if="isEmpty" class="tl__msg">{{ t('timeline.empty') }}</p>

    <!-- ★★ 撞上写死的 LIMIT 500 ⇒ 只能说「可能被截断」，不能说「全部」 -->
    <p v-if="atCap" class="tl__note tl__note--warn">
      <AppIcon name="alert" :size="13" />
      <span>{{ t('timeline.truncated', { n: total, cap: AVAILABILITY_TIMELINE_ROW_CAP }) }}</span>
    </p>
    <p v-else-if="total" class="tl__count">{{ t('timeline.count', { n: total }) }}</p>

    <section v-for="g in groups" :key="g.model" class="tl__group">
      <h2 class="tl__group-title">{{ g.model }}</h2>
      <ul class="tl__hours">
        <li v-for="p in g.points" :key="p.hour_bucket" class="tl__hour">
          <span class="tl__hour-time">{{ fmtTime(p.hour_bucket) }}</span>
          <!-- ★ 不用「6px 彩色进度条」编码成功率：那是一个亚 48px 的装饰元素，
               触控门会拦，而把它抬到 48px 毫无意义（行高已经是 48px）。
               改为**行左边框着色 + 数值**，信息量不减且没有多余元素。 -->
          <span class="tl__hour-rate" :class="'tl__hour-rate--' + hourTone(p)">
            {{ formatSuccessRatePct(p.success_rate) }}
          </span>
          <span class="tl__hour-n">{{ fmtInt(p.total_probes) }}</span>
          <!-- ★ 没有成功探测的那一小时显示专门文案，不是 0ms -->
          <span class="tl__hour-lat" :class="{ 'tl__unknown': avgLatencyOf(p) === null }">
            {{ latencyText(p) }}
          </span>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.tl {
  padding: var(--app-space-3);
}
.tl__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
.tl__field {
  display: block;
}
.tl__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.tl__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.tl__actions {
  display: flex;
  gap: var(--app-space-2);
}
.tl__btn {
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.tl__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.tl__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.tl__note--warn {
  color: var(--app-warning);
}
.tl__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.tl__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.tl__count {
  margin: var(--app-space-2) 0;
  color: var(--app-text-muted);
  font-size: 12px;
}
.tl__group {
  margin-bottom: var(--app-space-3);
}
.tl__group-title {
  margin: 0 0 var(--app-space-2);
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  word-break: break-word;
}
.tl__hours {
  list-style: none;
  margin: 0;
  padding: 0;
}
.tl__hour {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  min-height: 48px;
  padding: 4px 0;
  border-bottom: 1px solid var(--app-border);
}
.tl__hour:last-child {
  border-bottom: 0;
}
.tl__hour-time {
  font-size: 11px;
  color: var(--app-text-muted);
  min-width: 42px;
  font-variant-numeric: tabular-nums;
}
.tl__hour {
  /* ★ 左边框编码该小时是否全成功（success_rate 的定性档位）。 */
  border-left: 3px solid var(--app-success);
  padding-left: 6px;
}
.tl__hour-rate {
  font-size: 12px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
  min-width: 52px;
  text-align: right;
}
.tl__hour-rate--warning {
  color: var(--app-warning);
}
.tl__hour-rate--muted {
  color: var(--app-text-muted);
}
.tl__hour-n {
  font-size: 11px;
  color: var(--app-text-muted);
  font-variant-numeric: tabular-nums;
}
.tl__hour-lat {
  margin-left: auto;
  font-size: 11px;
  color: var(--app-text-secondary);
  font-variant-numeric: tabular-nums;
}
/* ★ 「没有成功探测」必须与真实延时视觉上分得开。 */
.tl__unknown {
  color: var(--app-text-muted) !important;
  font-style: italic;
}
</style>
