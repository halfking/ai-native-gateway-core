<script setup lang="ts">
// RequestAnomaliesView — 请求侧异常（/api/admin/request-anomalies{,/count}，**superAdmin 档**）。
//
// 它答的是「**上游在拒绝我们的什么请求**」——例如某个供应商开始拒绝
// `reasoning_effort` 参数。与 `/node-audit`（节点健康）、`/pending-responses`
// （卡住的请求）、`response_format_anomalies`（另一族，PG 表）互不重叠。
//
// ⚠️★★★ 九个后端语义（详见 api/requestAnomalies.ts 文件头）：
//
// (1) ★★★★ **同一筛选面板里四个字段的大小写敏感度不一致**：
//     provider/model 大小写不敏感，day/trigger 大小写敏感。
//     ⇒ `trigger=PARAM_REJECTED` 静默返空。页面只给后端那三个小写选项。
// (2) ★★★★ `model` 筛的是「客户端模型 **或** 出站模型」的 OR
//     ⇒ 两个模型都必须显示，否则用户以为筛错了。
// (3) ★★★★ 指纹里含 `day` ⇒ **同一问题每天一行**，`occurrences` 只是**今天**的次数。
// (4) ★★★ `day` 是 `FirstSeen` 的**本地**日期（网关进程时区，不是浏览器时区）。
// (5) ★★★ `counts` **只统计未解决**的，与列表条数天然对不上（列表默认返回全部）。
// (6) ★★★ `trigger` 只有三个取值。
// (7) ★★ limit clamp [1,500]、永不报错；`loadAll` 是 HGETALL 全量，**不被截断**。
// (8) ★★ `param` 是**逗号连接的多个**参数名；多数可选字段键可能整个不存在。
// (9) ★ 错误信封是 `{"error":{"detail":…}}`（第三个信封族）。
//
// ★ 写操作 `POST /{id}/resolve` 与 `POST /batch-resolve` 不在本页。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import AppIcon from '@/components/common/AppIcon.vue'
import StatusDot from '@/components/common/StatusDot.vue'
import { t } from '@/i18n'
import { fmtInt, relativeTime } from '@/utils/format'
import {
  fetchAnomalyList,
  fetchAnomalyCounts,
  anomalyParams,
  ANOMALY_TRIGGERS,
  ANOMALY_LIMIT_DEFAULT,
  ANOMALY_LIMIT_MAX,
  type AnomalyRecord,
  type AnomalyCounts,
} from '@/api/requestAnomalies'

useHyperPage({ title: () => t('anomalies.title') })

const LIMIT_CHOICES = [ANOMALY_LIMIT_DEFAULT, 100, ANOMALY_LIMIT_MAX]

const limit = ref(ANOMALY_LIMIT_DEFAULT)
const offset = ref(0)
const day = ref('')
const provider = ref('')
const model = ref('')
const trigger = ref<string>('')
const unresolvedOnly = ref(false)

const items = ref<AnomalyRecord[]>([])
const total = ref(0)
const counts = ref<AnomalyCounts | null>(null)

const loading = ref(false)
const error = ref<string | null>(null)
const countsError = ref<string | null>(null)

async function loadCounts(): Promise<void> {
  try {
    counts.value = await fetchAnomalyCounts()
    countsError.value = null
  } catch (e) {
    counts.value = null
    countsError.value = (e as Error)?.message || t('common.error')
  }
}

async function loadList(): Promise<void> {
  loading.value = true
  error.value = null
  try {
    const r = await fetchAnomalyList({
      limit: limit.value,
      offset: offset.value,
      day: day.value.trim() || undefined,
      provider: provider.value.trim() || undefined,
      model: model.value.trim() || undefined,
      // ★ 只发后端常量里的三个小写字面值（见坑 1）
      trigger: trigger.value || undefined,
      unresolvedOnly: unresolvedOnly.value,
    })
    items.value = r.anomalies
    total.value = r.count
  } catch (e) {
    items.value = []
    total.value = 0
    error.value = (e as Error)?.message || t('common.error')
  } finally {
    loading.value = false
  }
}

function reload(): void {
  void loadList()
  void loadCounts()
}

function applyLimit(n: number): void {
  limit.value = n
  offset.value = 0
  reload()
}

function applyTrigger(v: string): void {
  trigger.value = trigger.value === v ? '' : v
  offset.value = 0
  reload()
}

function submit(): void {
  offset.value = 0
  reload()
}

function clearFilters(): void {
  day.value = ''
  provider.value = ''
  model.value = ''
  trigger.value = ''
  unresolvedOnly.value = false
  offset.value = 0
  reload()
}

function goto(next: number): void {
  offset.value = Math.max(0, next)
  reload()
}

void loadList()
void loadCounts()

const hasMore = computed(() => offset.value + items.value.length < total.value)

function triggerTone(tr: string): 'warning' | 'danger' | 'muted' {
  if (tr === 'param_rejected') return 'warning'
  if (tr === 'mode_mismatch') return 'danger'
  return 'muted'
}

/** ★ 把逗号连接的 param 拆成逐个 chip（见坑 8）。 */
function paramsOf(r: AnomalyRecord): string[] {
  return anomalyParams(r.param)
}

/** ★ 两个模型可能不同（网关重写过），都要显示（见坑 2）。 */
function modelRewritten(r: AnomalyRecord): boolean {
  return !!r.client_model && !!r.outbound_model && r.client_model !== r.outbound_model
}

onBeforeUnmount(() => {
  items.value = []
  counts.value = null
  error.value = null
  countsError.value = null
})
</script>

<template>
  <div class="view-root ra">
    <p v-if="loading" class="ra__msg">{{ t('common.loading') }}</p>

    <!-- ══════ 徽标计数 ══════ -->
    <section class="ra__panel">
      <span class="ra__panel-title">{{ t('anomalies.counts') }}</span>

      <p v-if="countsError" class="ra__msg ra__msg--err">{{ countsError }}</p>
      <template v-else-if="counts">
        <div class="ra__grid">
          <span class="ra__cell">
            <span class="ra__cell-l">{{ t('anomalies.unresolved') }}</span>
            <span class="ra__cell-v">{{ fmtInt(counts.unresolved) }}</span>
          </span>
          <span class="ra__cell">
            <span class="ra__cell-l">{{ t('anomalies.newToday') }}</span>
            <span class="ra__cell-v">{{ fmtInt(counts.new_today) }}</span>
          </span>
        </div>
        <!-- ★★★ 计数只含未解决，列表默认返回全部 ⇒ 两者天然对不上 -->
        <p class="ra__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('anomalies.countsUnresolvedOnly') }}</span>
        </p>
        <!-- ★★ 「今天」是网关进程的本地日期，不是浏览器时区 -->
        <p class="ra__note">
          <AppIcon name="alert" :size="13" />
          <span>{{ t('anomalies.dayIsServerLocal') }}</span>
        </p>
      </template>
    </section>

    <!-- ══════ 筛选 ══════ -->
    <section class="ra__panel">
      <span class="ra__panel-title">{{ t('anomalies.filter') }}</span>

      <!-- ★★ 大小写敏感度不一致，必须写在筛选区 -->
      <p class="ra__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('anomalies.caseSensitivityNote') }}</span>
      </p>

      <div class="ra__chips" role="group" :aria-label="t('anomalies.triggerLabel')">
        <button
          v-for="tr in ANOMALY_TRIGGERS"
          :key="tr"
          type="button"
          class="ra__chip"
          :class="{ 'ra__chip--on': trigger === tr }"
          @click="applyTrigger(tr)"
        >
          {{ t('anomalies.trigger_' + tr) }}
        </button>
      </div>

      <form class="ra__form" @submit.prevent="submit">
        <label class="ra__field">
          <span>{{ t('anomalies.day') }}</span>
          <input v-model="day" class="ra__input" :placeholder="t('anomalies.dayHint')" autocomplete="off" spellcheck="false" />
        </label>
        <label class="ra__field">
          <span>{{ t('anomalies.provider') }}</span>
          <input v-model="provider" class="ra__input" :placeholder="t('anomalies.exactHint')" autocomplete="off" spellcheck="false" />
        </label>
        <label class="ra__field">
          <span>{{ t('anomalies.model') }}</span>
          <input v-model="model" class="ra__input" :placeholder="t('anomalies.modelHint')" autocomplete="off" spellcheck="false" />
        </label>
        <label class="ra__check">
          <input v-model="unresolvedOnly" type="checkbox" />
          <span>{{ t('anomalies.unresolvedOnly') }}</span>
        </label>
        <div class="ra__chips" role="group" :aria-label="t('anomalies.limitLabel')">
          <button
            v-for="n in LIMIT_CHOICES"
            :key="n"
            type="button"
            class="ra__chip"
            :class="{ 'ra__chip--on': limit === n }"
            @click="applyLimit(n)"
          >
            {{ t('anomalies.limitN', { n }) }}
          </button>
        </div>
        <div class="ra__row">
          <button type="submit" class="ra__btn ra__btn--go">{{ t('anomalies.query') }}</button>
          <button type="button" class="ra__btn" @click="clearFilters">{{ t('common.clearFilters') }}</button>
        </div>
      </form>
    </section>

    <!-- ══════ 列表 ══════ -->
    <section class="ra__panel">
      <span class="ra__panel-title">{{ t('anomalies.list') }}</span>

      <!-- ★★★ 指纹含 day ⇒ 同一问题每天一行 -->
      <p class="ra__note">
        <AppIcon name="alert" :size="13" />
        <span>{{ t('anomalies.oneRowPerDayNote') }}</span>
      </p>

      <p v-if="error" class="ra__msg ra__msg--err">{{ error }}</p>
      <p v-else-if="!items.length && !loading" class="ra__msg">{{ t('anomalies.noAnomalies') }}</p>

      <ul v-if="items.length" class="ra__list">
        <li v-for="r in items" :key="r.id" class="ra__item">
          <div class="ra__item-head">
            <StatusDot :tone="triggerTone(r.trigger)" />
            <span class="ra__trig">{{ t('anomalies.trigger_' + r.trigger) }}</span>
            <span class="ra__status">{{ r.http_status }}</span>
            <span v-if="r.resolved" class="ra__done">{{ t('anomalies.resolved') }}</span>
          </div>

          <!-- ★★ 客户端模型与出站模型可能不同（网关重写过），两个都要显示 -->
          <div class="ra__kv">
            <span class="ra__kv-item">
              <span class="ra__kv-l">{{ t('anomalies.providerCode') }}</span>
              <span class="ra__kv-v">{{ r.provider_code }}</span>
            </span>
            <span class="ra__kv-item">
              <span class="ra__kv-l">{{ t('anomalies.clientModel') }}</span>
              <span class="ra__kv-v">{{ r.client_model || '—' }}</span>
            </span>
            <span class="ra__kv-item">
              <span class="ra__kv-l">{{ t('anomalies.outboundModel') }}</span>
              <span class="ra__kv-v">{{ r.outbound_model || '—' }}</span>
            </span>
          </div>
          <p v-if="modelRewritten(r)" class="ra__note">
            <AppIcon name="alert" :size="13" />
            <span>{{ t('anomalies.modelRewrittenNote') }}</span>
          </p>

          <!-- ★★ param 是逗号连接的多个，逐个显示 -->
          <div v-if="paramsOf(r).length" class="ra__tags">
            <span class="ra__tags-l">{{ t('anomalies.rejectedParams') }}</span>
            <span v-for="p in paramsOf(r)" :key="p" class="ra__tag">{{ p }}</span>
          </div>
          <p v-if="r.suggest_mode" class="ra__meta">{{ t('anomalies.suggestMode', { mode: r.suggest_mode }) }}</p>

          <p class="ra__meta">
            {{ t('anomalies.occLine', { n: r.occurrences, rec: r.recovered_count }) }}
          </p>
          <p class="ra__meta">
            {{ t('anomalies.seenLine', { t: relativeTime(r.last_seen), f: r.day }) }}
          </p>
          <p v-if="r.error_kind" class="ra__meta">{{ t('anomalies.errorKind', { k: r.error_kind }) }}</p>
          <p v-if="r.error_sample" class="ra__sample">{{ r.error_sample }}</p>
          <p v-if="r.resolution_notes" class="ra__meta">{{ r.resolution_notes }}</p>
        </li>
      </ul>

      <div v-if="items.length || offset > 0" class="ra__row">
        <button type="button" class="ra__btn" :disabled="offset === 0" @click="goto(offset - limit)">
          {{ t('anomalies.prev') }}
        </button>
        <span class="ra__meta">{{ t('anomalies.pageInfo', { from: offset + 1, to: offset + items.length, total }) }}</span>
        <button type="button" class="ra__btn" :disabled="!hasMore" @click="goto(offset + limit)">
          {{ t('anomalies.next') }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.ra {
  padding: var(--app-space-3);
}
.ra__panel {
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
}
.ra__panel-title {
  display: block;
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
  margin-bottom: var(--app-space-2);
}
.ra__grid {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
}
.ra__cell {
  display: inline-flex;
  flex-direction: column;
  gap: 2px;
}
.ra__cell-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.ra__cell-v {
  font-size: 20px;
  font-weight: 700;
  color: var(--app-text);
  font-variant-numeric: tabular-nums;
}
.ra__msg {
  margin: var(--app-space-2) 0;
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
  color: var(--app-text-secondary);
}
.ra__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ra__note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin: var(--app-space-2) 0 0;
  color: var(--app-text-muted);
  font-size: 11px;
  line-height: 1.5;
}
.ra__meta {
  margin: 4px 0 0;
  font-size: 11px;
  color: var(--app-text-muted);
  word-break: break-word;
}
.ra__sample {
  margin: 4px 0 0;
  padding: 6px 8px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
  font-size: 11px;
  line-height: 1.5;
  word-break: break-word;
  white-space: pre-wrap;
}
.ra__form {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
.ra__field {
  display: block;
}
.ra__field > span {
  display: block;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: 4px;
}
.ra__input {
  width: 100%;
  min-height: 48px;
  padding: 0 var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
.ra__check {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 48px;
  font-size: 13px;
  color: var(--app-text-secondary);
}
.ra__check input {
  width: 20px;
  height: 20px;
}
.ra__chips {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin: var(--app-space-2) 0;
}
.ra__chip {
  min-height: 48px;
  min-width: 64px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ra__chip--on {
  border-color: var(--app-primary);
  color: var(--app-primary);
  font-weight: 700;
}
.ra__row {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  flex-wrap: wrap;
  margin-top: var(--app-space-2);
}
.ra__btn {
  min-height: 48px;
  min-width: 64px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-sm);
  border: 1px solid var(--app-border);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 13px;
}
.ra__btn--go {
  border-color: var(--app-primary);
  color: var(--app-primary);
}
.ra__btn[disabled] {
  opacity: 0.45;
}
.ra__list {
  list-style: none;
  margin: var(--app-space-2) 0 0;
  padding: 0;
}
.ra__item {
  padding: var(--app-space-2) 0;
  border-top: 1px solid var(--app-border);
}
.ra__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.ra__trig {
  font-size: 13px;
  font-weight: 700;
  color: var(--app-text);
}
.ra__status {
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-danger-soft);
  color: var(--app-danger);
  font-size: 11px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}
.ra__done {
  padding: 1px 6px;
  border-radius: var(--app-radius-pill);
  background: var(--app-success-soft);
  color: var(--app-success);
  font-size: 10px;
  line-height: 1.6;
}
.ra__kv {
  display: flex;
  gap: var(--app-space-3);
  flex-wrap: wrap;
  margin-top: 4px;
}
.ra__kv-item {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.ra__kv-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.ra__kv-v {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}
.ra__tags {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
  margin-top: 4px;
}
.ra__tags-l {
  font-size: 11px;
  color: var(--app-text-muted);
}
.ra__tag {
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-warning-soft);
  color: var(--app-warning);
  font-size: 11px;
  font-weight: 600;
  word-break: break-all;
}
</style>