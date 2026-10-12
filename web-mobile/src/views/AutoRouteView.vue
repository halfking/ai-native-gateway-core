<script setup lang="ts">
// AutoRouteView — 自动路由读面（/auto-route，**superAdmin 档**）。
//
// 数据源（五条，全部 superAdmin 档，见 api/autoRouteRead.ts 文件头）：
//   GET /api/admin/auto-route/audit              7 天路由审计聚合
//   GET /api/admin/auto-route/index              凭据 × 模型实时索引快照
//   GET /api/admin/auto-route/cost/customer      按 API Key 的成本
//   GET /api/admin/auto-route/cost/model         按模型的成本
//   GET /api/admin/auto-route/tuning/accuracy    调优准确率（按 task_type × classifier）
//
// 与既有页面的关系：/proposals 答「有哪些建议、批了没」（api/autoRouteInsights.ts），
// 本页答「现在跑得怎么样、花了多少钱」—— 建议与效果之间缺的就是这一页。
//
// ## ⚠️ 五段**全部按需加载**
// index 是全网 credential × model 的笛卡尔积，cost/model 是全网模型名，
// audit 要扫 7 天窗口 —— 自动拉会让每次进这一页都付这三份代价。
//
// ## ★★★★★ 五处「不能都渲染成同一个东西」
//
// (1) ★★★ **稀疏键**：三段列表的每一行只有 1~4 个键无条件，其余 DB 为 NULL
//      就不写这个键（auto_route.go:357-398 / :917-945 / :1009-1025）。
//      ⇒ 键缺失必须渲染成「无数据」，**不能**渲染成 0。
//      判据落在 class 上（.ar__cell--nodata），不只看字形。
//
// (2) ★★★ **audit 的三个块查询失败时键直接缺失，HTTP 仍 200**
//      （:627 / :665 / :724 / :758 全在 `if err == nil` 里）。
//      ⇒ 「这一块没有数据」与「这一块的查询挂了」在响应里长得一样。
//      缺键渲染成「查询失败」（danger 态），有键但为空才渲染「无数据」。
//
// (3) ★★★ **outcome_source.stale** ⇒ 这一屏的成功率是**停止产生**的数字，
//      不是「数字低」。必须挂免责句，否则运维会把一次采集故障读成业务暴跌。
//
// (4) ★★ **空索引返回异构哨兵** `[{warning}]` 而不是 `[]`
//      （auto_route.go:294-297）⇒ 渲染「等待首轮刷新」，不是「没有数据」。
//
// (5) ★★ **accuracy 的五个 avg_* 全是 COALESCE(…,0)** 编造的 0，
//      且按窗口换物化视图（≤7 走 5m、8..90 走天桶）⇒ 跨窗口的 avg_* 不可直接比。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtInt, fmtNum, fmtUsd, fmtCompact } from '@/utils/format'
import {
  fetchAutoRouteAudit,
  fetchAutoRouteIndex,
  fetchAutoRouteCustomerCost,
  fetchAutoRouteModelCost,
  fetchAutoRouteAccuracy,
  autoRouteAuditMissingBlocks,
  autoRouteAuditHasNoTraffic,
  autoRouteAuditNumbersStale,
  autoRouteIndexAwaitingFirstRefresh,
  autoRouteCustomerCostContradicts,
  autoRouteModelCostPerRequest,
  autoRouteAccuracyBucketGranularity,
  autoRouteAccuracyAveragesMayBeZeroPlaceholders,
  type AutoRouteAuditResponse,
  type AutoRouteIndexRow,
  type AutoRouteCustomerCostRow,
  type AutoRouteModelCostRow,
  type AutoRouteAccuracyResponse,
} from '@/api/autoRouteRead'

useHyperPage({ title: () => t('autoRoute.title') })

/** 无数据占位。★ 与数字 0 的字形不同，且带独立 class（判据锚在原因上）。 */
const NO_DATA = '—'

type SectionKey = 'audit' | 'index' | 'customerCost' | 'modelCost' | 'accuracy'

const audit = ref<AutoRouteAuditResponse | null>(null)
const indexRows = ref<AutoRouteIndexRow[]>([])
const customerRows = ref<AutoRouteCustomerCostRow[]>([])
const modelRows = ref<AutoRouteModelCostRow[]>([])
const accuracy = ref<AutoRouteAccuracyResponse | null>(null)

const loading = ref<SectionKey | null>(null)
const loaded = ref<Record<SectionKey, boolean>>({
  audit: false,
  index: false,
  customerCost: false,
  modelCost: false,
  accuracy: false,
})
const error = ref<Record<SectionKey, string | null>>({
  audit: null,
  index: null,
  customerCost: null,
  modelCost: null,
  accuracy: null,
})

const days = ref(7)

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('autoRoute.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

async function load(section: SectionKey): Promise<void> {
  loading.value = section
  error.value[section] = null
  try {
    if (section === 'audit') {
      audit.value = await fetchAutoRouteAudit()
    } else if (section === 'index') {
      indexRows.value = await fetchAutoRouteIndex()
    } else if (section === 'customerCost') {
      customerRows.value = await fetchAutoRouteCustomerCost()
    } else if (section === 'modelCost') {
      modelRows.value = await fetchAutoRouteModelCost()
    } else {
      accuracy.value = await fetchAutoRouteAccuracy({ days: days.value })
    }
  } catch (err) {
    if (section === 'audit') audit.value = null
    if (section === 'index') indexRows.value = []
    if (section === 'customerCost') customerRows.value = []
    if (section === 'modelCost') modelRows.value = []
    if (section === 'accuracy') accuracy.value = null
    error.value[section] = describeError(err)
  } finally {
    loading.value = null
    loaded.value[section] = true
  }
}

/** ★ 失败 ≠ 零行：失败时不渲染任何「空」文案，只渲染错误。 */
function isEmpty(section: SectionKey): boolean {
  if (!loaded.value[section] || error.value[section]) return false
  if (section === 'audit') return false
  if (section === 'index') return indexRows.value.length === 0
  if (section === 'customerCost') return customerRows.value.length === 0
  if (section === 'modelCost') return modelRows.value.length === 0
  return (accuracy.value?.breakdown.length ?? 0) === 0
}

/* ── audit ──────────────────────────────────────────────────────── */

const auditMissingBlocks = computed(() =>
  audit.value ? autoRouteAuditMissingBlocks(audit.value) : [],
)
/** ★ 三个块各自：有键（可能为空数组）vs 缺键（查询失败）。二者不能共用文案。 */
function blockState(key: string): 'ok' | 'failed' {
  if (!audit.value) return 'failed'
  return key in (audit.value as unknown as Record<string, unknown>) ? 'ok' : 'failed'
}

const auditStale = computed(() => (audit.value ? autoRouteAuditNumbersStale(audit.value) : false))
const auditNoTraffic = computed(() => (audit.value ? autoRouteAuditHasNoTraffic(audit.value) : false))

/** ★ success_rate 的 0 有两种来源，分开显示（后端在 total=0 时强制写 0.0）。 */
const successRateText = computed(() => {
  if (!audit.value) return NO_DATA
  if (auditNoTraffic.value) return t('autoRoute.audit.noTraffic')
  return fmtNum(audit.value.success_rate * 100, 2) + '%'
})

const outcomeReasonKey = computed(() => {
  const reason = audit.value?.outcome_source.reason ?? ''
  const known = ['live', 'no_rows', 'stale', 'absent', 'query_failed']
  return known.includes(reason) ? 'autoRoute.outcome.' + reason : 'autoRoute.outcome.unknown'
})

function outcomeAgeText(): string {
  const os = audit.value?.outcome_source
  if (!os || os.age_seconds === undefined) return NO_DATA
  return fmtCompact(os.age_seconds) + 's'
}

/* ── index ──────────────────────────────────────────────────────── */

const indexAwaiting = computed(() => autoRouteIndexAwaitingFirstRefresh(indexRows.value))

/** ★ 稀疏键单元格：缺失 → 无数据（独立 class），不显示 0。 */
function sparse(value: number | string | undefined, render: (v: never) => string): { text: string; nodata: boolean } {
  if (value === undefined || value === null) return { text: NO_DATA, nodata: true }
  return { text: render(value as never), nodata: false }
}
const n = (v: number | undefined) => fmtNum(v, 2)
const i = (v: number | undefined) => fmtInt(v)
const s = (v: string | undefined) => v as string

/* ── cost ───────────────────────────────────────────────────────── */

/** ★ 分母缺失时不显示单价（否则会算出 NaN/Infinity，渲染成空白而非报错）。 */
function perRequestText(r: AutoRouteModelCostRow): { text: string; nodata: boolean } {
  const v = autoRouteModelCostPerRequest(r)
  return v === null ? { text: NO_DATA, nodata: true } : { text: fmtUsd(v), nodata: false }
}

function contradictionText(r: AutoRouteCustomerCostRow): string | null {
  return autoRouteCustomerCostContradicts(r) ? t('autoRoute.contradiction') : null
}

/* ── accuracy ───────────────────────────────────────────────────── */

const accuracyGranularity = computed(() => t('autoRoute.accuracy.granularity.' + autoRouteAccuracyBucketGranularity(days.value)))

/** ★ 整表是否需要挂「0 可能是编造的」免责。 */
const accuracyAnyPlaceholder = computed(
  () => (accuracy.value?.breakdown ?? []).some((r) => autoRouteAccuracyAveragesMayBeZeroPlaceholders(r)),
)

onBeforeUnmount(() => {
  audit.value = null
  indexRows.value = []
  customerRows.value = []
  modelRows.value = []
  accuracy.value = null
})
</script>

<template>
  <div class="view-root ar">
    <!-- ══ 1. 路由审计聚合 ══════════════════════════════════════════ -->
    <section class="ar__section">
      <header class="ar__head">
        <h2 class="ar__title">{{ t('autoRoute.audit.title') }}</h2>
        <button
          type="button"
          class="ar__load"
          :disabled="loading === 'audit'"
          @click="load('audit')"
        >
          {{ loaded.audit && !error.audit ? t('autoRoute.reload') : t('autoRoute.load') }}
        </button>
      </header>

      <p v-if="error.audit" class="ar__msg ar__msg--err">{{ error.audit }}</p>
      <p v-if="loading === 'audit'" class="ar__msg">{{ t('common.loading') }}</p>

      <div v-else-if="audit" class="ar__kpis">
        <div class="ar__kpi">
          <span class="ar__kpi-label">{{ t('autoRoute.audit.total') }}</span>
          <span class="ar__kpi-value">{{ fmtInt(audit.total_requests) }}</span>
        </div>
        <div class="ar__kpi">
          <span class="ar__kpi-label">{{ t('autoRoute.audit.auto') }}</span>
          <span class="ar__kpi-value">{{ fmtInt(audit.total_auto_requests) }}</span>
        </div>
        <div class="ar__kpi">
          <span class="ar__kpi-label">{{ t('autoRoute.audit.specified') }}</span>
          <span class="ar__kpi-value">{{ fmtInt(audit.specified_model_requests) }}</span>
        </div>
        <div class="ar__kpi">
          <span class="ar__kpi-label">{{ t('autoRoute.audit.successRate') }}</span>
          <!-- ★ 两个来源的 0 分开渲染：无流量 ≠ 0% 成功率 -->
          <span
            class="ar__kpi-value"
            :class="{ 'ar__kpi-value--notraffic': auditNoTraffic }"
          >{{ successRateText }}</span>
        </div>
      </div>

      <template v-if="audit">
        <!-- ★ stale：数字停止产生，不是数字低 -->
        <p v-if="auditStale" class="ar__msg ar__msg--warn ar__stale">
          {{ t('autoRoute.audit.staleWarn') }}
        </p>
        <p class="ar__echo">
          {{ t('autoRoute.outcomeLine', {
            reason: t(outcomeReasonKey),
            age: outcomeAgeText(),
            available: audit.outcome_source.available ? t('common.yes') : t('common.no'),
          }) }}
        </p>

        <!-- ★ 三个条件块：缺键 = 查询失败，有键为空 = 无数据 -->
        <p
          v-for="key in auditMissingBlocks"
          :key="key"
          class="ar__msg ar__msg--err"
        >{{ t('autoRoute.audit.blockFailed', { block: key }) }}</p>

        <dl class="ar__kv">
          <div v-for="key in ['task_distribution', 'profile_distribution', 'top_chosen_models']" :key="key" class="ar__kv-row">
            <dt>{{ t('autoRoute.audit.block.' + key) }}</dt>
            <dd>
              <template v-if="blockState(key) === 'failed'">
                <span class="ar__cell--nodata ar__block-failed">{{ t('autoRoute.audit.blockFailedShort') }}</span>
              </template>
              <template v-else-if="key === 'top_chosen_models'">
                <span v-if="(audit.top_chosen_models ?? []).length === 0" class="ar__cell--nodata">{{ t('autoRoute.empty') }}</span>
                <span v-else class="ar__block-ok">
                  <span v-for="m in audit.top_chosen_models ?? []" :key="m.model" class="ar__chip">
                    {{ m.model }} · {{ fmtInt(m.count) }}
                  </span>
                </span>
              </template>
              <template v-else>
                <span
                  v-if="Object.keys((key === 'task_distribution' ? audit.task_distribution : audit.profile_distribution) ?? {}).length === 0"
                  class="ar__cell--nodata"
                >{{ t('autoRoute.empty') }}</span>
                <span v-else class="ar__block-ok">
                  <span
                    v-for="(v, name) in (key === 'task_distribution' ? audit.task_distribution : audit.profile_distribution) ?? {}"
                    :key="name"
                    class="ar__chip"
                  >{{ name }} · {{ fmtInt(v) }}</span>
                </span>
              </template>
            </dd>
          </div>
        </dl>
      </template>
    </section>

    <!-- ══ 2. 凭据 × 模型索引 ══════════════════════════════════════ -->
    <section class="ar__section">
      <header class="ar__head">
        <h2 class="ar__title">{{ t('autoRoute.index.title') }}</h2>
        <button
          type="button"
          class="ar__load"
          :disabled="loading === 'index'"
          @click="load('index')"
        >
          {{ loaded.index && !error.index ? t('autoRoute.reload') : t('autoRoute.load') }}
        </button>
      </header>

      <p v-if="error.index" class="ar__msg ar__msg--err">{{ error.index }}</p>
      <p v-if="loading === 'index'" class="ar__msg">{{ t('common.loading') }}</p>
      <!-- ★ 哨兵与真·空索引是两回事 -->
      <p v-else-if="indexAwaiting" class="ar__msg ar__msg--warn">{{ t('autoRoute.index.awaiting') }}</p>
      <p v-else-if="isEmpty('index')" class="ar__msg">{{ t('autoRoute.empty') }}</p>

      <ul v-if="indexRows.length && !indexAwaiting" class="ar__list">
        <li v-for="r in indexRows" :key="r.credential_id + '|' + r.raw_model" class="ar__item">
          <div class="ar__item-head">
            <span class="ar__item-title">{{ r.raw_model }}</span>
            <span class="badge badge--muted">#{{ r.credential_id }}</span>
            <span class="ar__item-time">{{ r.bucket }}</span>
          </div>
          <dl class="ar__kv">
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.index.successRate') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.success_rate, n).nodata }">
                {{ sparse(r.success_rate, n).nodata ? t('autoRoute.noData') : sparse(r.success_rate, n).text }}
              </dd>
            </div>
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.index.p95') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.p95_latency_ms, i).nodata }">
                {{ sparse(r.p95_latency_ms, i).nodata ? t('autoRoute.noData') : sparse(r.p95_latency_ms, i).text }}
              </dd>
            </div>
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.index.score') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.score_smart, n).nodata }">
                {{ sparse(r.score_smart, n).nodata ? t('autoRoute.noData') : sparse(r.score_smart, n).text }}
              </dd>
            </div>
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.index.pressure') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.pressure_ratio, n).nodata }">
                {{ sparse(r.pressure_ratio, n).nodata ? t('autoRoute.noData') : sparse(r.pressure_ratio, n).text }}
              </dd>
            </div>
          </dl>
        </li>
      </ul>
    </section>

    <!-- ══ 3. 客户成本 ══════════════════════════════════════════════ -->
    <section class="ar__section">
      <header class="ar__head">
        <h2 class="ar__title">{{ t('autoRoute.customerCost.title') }}</h2>
        <button
          type="button"
          class="ar__load"
          :disabled="loading === 'customerCost'"
          @click="load('customerCost')"
        >
          {{ loaded.customerCost && !error.customerCost ? t('autoRoute.reload') : t('autoRoute.load') }}
        </button>
      </header>

      <p v-if="error.customerCost" class="ar__msg ar__msg--err">{{ error.customerCost }}</p>
      <p v-if="loading === 'customerCost'" class="ar__msg">{{ t('common.loading') }}</p>
      <p v-else-if="isEmpty('customerCost')" class="ar__msg">{{ t('autoRoute.empty') }}</p>

      <ul v-if="customerRows.length" class="ar__list">
        <li v-for="r in customerRows" :key="r.api_key_id" class="ar__item">
          <div class="ar__item-head">
            <span class="ar__item-title">{{ r.key_alias ?? `#${r.api_key_id}` }}</span>
            <span class="badge badge--muted">#{{ r.api_key_id }}</span>
          </div>
          <!-- ★ 计数器自相矛盾必须单独报警，不能混在表格里 -->
          <p v-if="contradictionText(r)" class="ar__msg ar__msg--warn">{{ contradictionText(r) }}</p>
          <dl class="ar__kv">
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.customerCost.cost24h') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.cost_usd_24h, fmtUsd).nodata }">
                {{ sparse(r.cost_usd_24h, fmtUsd).nodata ? t('autoRoute.noData') : sparse(r.cost_usd_24h, fmtUsd).text }}
              </dd>
            </div>
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.customerCost.cost7d') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.cost_usd_7d, fmtUsd).nodata }">
                {{ sparse(r.cost_usd_7d, fmtUsd).nodata ? t('autoRoute.noData') : sparse(r.cost_usd_7d, fmtUsd).text }}
              </dd>
            </div>
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.customerCost.requests') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.total_auto_requests, i).nodata }">
                {{ sparse(r.total_auto_requests, i).nodata ? t('autoRoute.noData') : sparse(r.total_auto_requests, i).text }}
              </dd>
            </div>
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.customerCost.lastRequest') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.last_request_at, s).nodata }">
                {{ sparse(r.last_request_at, s).nodata ? t('autoRoute.noData') : sparse(r.last_request_at, s).text }}
              </dd>
            </div>
          </dl>
        </li>
      </ul>
    </section>

    <!-- ══ 4. 模型成本 ══════════════════════════════════════════════ -->
    <section class="ar__section">
      <header class="ar__head">
        <h2 class="ar__title">{{ t('autoRoute.modelCost.title') }}</h2>
        <button
          type="button"
          class="ar__load"
          :disabled="loading === 'modelCost'"
          @click="load('modelCost')"
        >
          {{ loaded.modelCost && !error.modelCost ? t('autoRoute.reload') : t('autoRoute.load') }}
        </button>
      </header>

      <p v-if="error.modelCost" class="ar__msg ar__msg--err">{{ error.modelCost }}</p>
      <p v-if="loading === 'modelCost'" class="ar__msg">{{ t('common.loading') }}</p>
      <p v-else-if="isEmpty('modelCost')" class="ar__msg">{{ t('autoRoute.empty') }}</p>

      <ul v-if="modelRows.length" class="ar__list">
        <li v-for="(r, idx) in modelRows" :key="r.raw_model + idx" class="ar__item">
          <div class="ar__item-head">
            <span class="ar__item-title">{{ r.raw_model }}</span>
          </div>
          <dl class="ar__kv">
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.modelCost.totalCost') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.total_cost_usd, fmtUsd).nodata }">
                {{ sparse(r.total_cost_usd, fmtUsd).nodata ? t('autoRoute.noData') : sparse(r.total_cost_usd, fmtUsd).text }}
              </dd>
            </div>
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.modelCost.perRequest') }}</dt>
              <!-- ★ 分母缺失/为 0 ⇒ 不显示，绝不让 NaN 渲染成空白 -->
              <dd :class="{ 'ar__cell--nodata': perRequestText(r).nodata }">
                {{ perRequestText(r).nodata ? t('autoRoute.noData') : perRequestText(r).text }}
              </dd>
            </div>
            <div class="ar__kv-row">
              <dt>{{ t('autoRoute.modelCost.successRate') }}</dt>
              <dd :class="{ 'ar__cell--nodata': sparse(r.success_rate, n).nodata }">
                {{ sparse(r.success_rate, n).nodata ? t('autoRoute.noData') : sparse(r.success_rate, n).text }}
              </dd>
            </div>
          </dl>
        </li>
      </ul>
    </section>

    <!-- ══ 5. 调优准确率 ════════════════════════════════════════════ -->
    <section class="ar__section">
      <header class="ar__head">
        <h2 class="ar__title">{{ t('autoRoute.accuracy.title') }}</h2>
        <button
          type="button"
          class="ar__load"
          :disabled="loading === 'accuracy'"
          @click="load('accuracy')"
        >
          {{ loaded.accuracy && !error.accuracy ? t('autoRoute.reload') : t('autoRoute.load') }}
        </button>
      </header>

      <label class="ar__days">
        <span>{{ t('autoRoute.accuracy.days', { n: days }) }}</span>
        <input v-model.number="days" type="number" min="1" max="90" step="1" class="ar__days-input" />
      </label>

      <p v-if="error.accuracy" class="ar__msg ar__msg--err">{{ error.accuracy }}</p>
      <p v-if="loading === 'accuracy'" class="ar__msg">{{ t('common.loading') }}</p>
      <p v-else-if="isEmpty('accuracy')" class="ar__msg">{{ t('autoRoute.empty') }}</p>

      <template v-if="accuracy && accuracy.breakdown.length">
        <!-- ★ 跨窗口不可直接比 -->
        <p class="ar__echo ar__granularity">{{ t('autoRoute.accuracy.granularityNote', { gran: accuracyGranularity }) }}</p>
        <!-- ★ 编造的 0 免责 -->
        <p v-if="accuracyAnyPlaceholder" class="ar__msg ar__msg--warn">
          {{ t('autoRoute.accuracy.zeroPlaceholder') }}
        </p>
        <ul class="ar__list">
          <li v-for="r in accuracy.breakdown" :key="r.task_type + '|' + r.classifier" class="ar__item">
            <div class="ar__item-head">
              <span class="ar__item-title">{{ r.task_type }}</span>
              <span class="badge badge--muted">{{ r.classifier }}</span>
              <span class="ar__item-time">{{ fmtInt(r.total) }}</span>
            </div>
            <p
              v-if="autoRouteAccuracyAveragesMayBeZeroPlaceholders(r)"
              class="ar__msg ar__msg--warn"
            >{{ t('autoRoute.accuracy.zeroPlaceholder') }}</p>
            <dl class="ar__kv">
              <div class="ar__kv-row">
                <dt>{{ t('autoRoute.accuracy.quality') }}</dt>
                <dd>{{ fmtNum(r.avg_quality, 3) }}</dd>
              </div>
              <div class="ar__kv-row">
                <dt>{{ t('autoRoute.accuracy.success') }}</dt>
                <dd>{{ fmtNum(r.avg_success, 3) }}</dd>
              </div>
              <div class="ar__kv-row">
                <dt>{{ t('autoRoute.accuracy.drift') }}</dt>
                <dd>{{ fmtNum(r.drift_rate, 3) }}</dd>
              </div>
            </dl>
          </li>
        </ul>
      </template>
    </section>
  </div>
</template>

<style scoped>
.ar {
  padding: var(--app-space-3);
}
.ar__section {
  margin-bottom: var(--app-space-4);
}
.ar__head {
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
/* ★ R1：新增触控控件 ≥48 CSS px */
.ar__load {
  margin-left: auto;
  min-height: 48px;
  padding: 0 var(--app-space-3);
  border-radius: var(--app-radius-pill);
  border: 1px solid var(--app-primary);
  background: var(--app-surface);
  color: var(--app-primary);
  font-size: 13px;
}
.ar__load:disabled {
  opacity: 0.6;
}
.ar__title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--app-text);
}
.ar__msg {
  margin: 0 0 var(--app-space-2);
  padding: 8px 12px;
  border-radius: var(--app-radius-sm);
  font-size: 12px;
}
.ar__msg--err {
  background: var(--app-danger-soft);
  color: var(--app-danger);
}
.ar__msg--warn {
  background: var(--app-warning-soft);
  color: var(--app-warning);
}
.ar__stale {
  font-weight: 600;
}
.ar__echo {
  margin: 0 0 var(--app-space-2);
  color: var(--app-text-muted);
  font-size: 11px;
}
.ar__kpis {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
.ar__kpi {
  padding: var(--app-space-2);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
}
.ar__kpi-label {
  display: block;
  font-size: 11px;
  color: var(--app-text-muted);
}
/* ★ 「没有流量」与「0% 成功率」必须视觉可分 */
.ar__kpi-value--notraffic {
  color: var(--app-text-muted);
  font-style: italic;
}
.ar__kpi-value {
  display: block;
  font-size: 16px;
  font-weight: 600;
  color: var(--app-text);
}
.ar__list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.ar__item {
  padding: var(--app-space-3);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  margin-bottom: var(--app-space-2);
}
.ar__item-head {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.ar__item-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--app-text);
}
.ar__item-time {
  margin-left: auto;
  font-size: 11px;
  color: var(--app-text-muted);
}
.ar__kv {
  margin: var(--app-space-2) 0 0;
  display: grid;
  gap: 2px;
}
.ar__kv-row {
  display: flex;
  gap: var(--app-space-2);
  min-width: 0;
}
.ar__kv-row dt {
  font-size: 11px;
  color: var(--app-text-muted);
  min-width: 96px;
  flex-shrink: 0;
}
.ar__kv-row dd {
  margin: 0;
  font-size: 12px;
  color: var(--app-text-secondary);
  min-width: 0;
  word-break: break-word;
}
/* ★ 无数据态：判据锚在这个 class 上，不只靠字形 */
.ar__cell--nodata {
  color: var(--app-text-muted);
  font-style: italic;
}
.ar__block-failed {
  color: var(--app-danger);
}
.ar__block-ok {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.ar__chip {
  padding: 2px 8px;
  border-radius: var(--app-radius-pill);
  background: var(--app-surface-2);
  font-size: 11px;
  color: var(--app-text-secondary);
}
.ar__days {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--app-text-secondary);
  margin-bottom: var(--app-space-2);
}
.ar__days-input {
  width: 88px;
  min-height: 48px;
  padding: 0 8px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
  background: var(--app-surface);
  color: var(--app-text);
  font-size: 14px;
}
</style>