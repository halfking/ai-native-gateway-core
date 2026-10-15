<script setup lang="ts">
// ReportRollupView — 对账报表：区间汇总 + 维度候选（2026-10-14，第一百一十一批）。
//
// GET /api/admin/report-rollup/summary     → { report: GrainReport }
// GET /api/admin/report-rollup/dimensions → { dimensions: {…} }
//
// 权限：★★ **superAdmin 档**（`admin/handler.go:1086`
//   `mux.HandleFunc("/api/admin/report-rollup/", h.superAdmin(h.handleReportRollup))`）
//   且**整个前缀带尾斜杠**都是这一档 ⇒ 抽屉席须设 `requiresRole: 'super_admin'`，
//   并同步 `src/components/shell/AppDrawer.spec.ts` 的排序白名单。
//
// ★★★ 与既有 `reconciliation` 席**是两个不同功能**（命名相近，容易接错）：
//   · `reconciliation` → `CostReconciliationView.vue` → `@/api/costReconciliation`（费用对账）
//   · 本席 → `@/api/reportRollup`（对账报表：区间多维汇总 + 筛选栏候选）
//   ⇒ 标题与 i18n 前缀都必须能区分开，否则抽屉里两席看上去一样。
//
// ★★ 租户口径：本族**不设**租户条件（`grainreport.go` 按 `report_date` + 维度聚合，
//   `GrainFilter` 的 tenant 只是个**过滤入参**）。但它是 **superAdmin 档**
//   ⇒ **档位本身就是那道门** ⇒ 视图内**不需要**任何角色分支（本批刻意如此）。
//
// ★★★★★ 本族最要紧的六件事（详见 api/reportRollup.ts 文件头 15 条）：
//   1. ★★★★★ **「降级」是 200 + 一个两键信封**，主键整个消失：
//      `{"degraded": true, "error_code": "REPORT_SNAPSHOTS_NOT_MIGRATED"}`
//      ⇒ 页面必须**分形状**渲染，不能当成「加载失败」。
//   2. ★★★★ `source` **不是独立字段，而是从 `coverage` 两个数组派生的**
//      （`grainreport.go:945-951`）⇒ 这是本族最锐利的可自验不变式。
//      ⇒ ★★ 推论：`source === "grain"` **不蕴含** `grain_dates` 非空（两个都空也走 default）。
//   3. ★★★★ 四条**可自算的派生列**（`report.go:210-231` 的 `finalize()`）：
//      · `error_count === request_count − success_count`（无条件）
//      · `total_tokens === input + output + cache_read + cache_write`
//        （★ 把 cache 也算进去 ⇒「total = input + output」在本族**恒不成立**）
//      · ★★ `request_count === 0` ⇒ `error_rate` 是 **0**，不是 `null`、不是 `NaN`
//        （除法被 `if RequestCount > 0` 包住，而值类型字段保持零值并照常出现）
//      · `cache_hit_ratio` 分母为 0 ⇒ **裸 `null`**（指针无 omitempty，键恒在）
//   4. ★★★ 区间是 **T+1** 语义，缺省「**昨日往前 7 天**」，**不含今日**
//      （注释自陈「今日快照尚不存在」）⇒ 页面**不该**提供「今天」。
//   5. ★★★ `start` / `end` 回显的是 **RFC3339Nano**，不是发进去的 `YYYY-MM-DD`
//      ⇒ 一进一出**两种形状**，不能把回显当「后端认了这个值」的凭证。
//   6. ★★★★ `filter` 标着 `json:"-"` ⇒ **六个维度过滤一个都不回显**
//      ⇒ 页面只能显式说明「服务端实际用了什么过滤，响应里查不到」。
//
// ★ 本页**不提供** xlsx 导出：那个端点返回 `…spreadsheetml.sheet` 二进制，
//   不是 JSON（`api/reportRollup.ts` 明确声明「不提供 export 的解包器」），
//   而在移动端 WebView 里落盘/分享 xlsx 需要原生能力 ⇒ 做了就是半成品。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchReportRollupSummary,
  fetchReportRollupDimensions,
  reportRollupIsDegraded,
  reportRollupDegradedCode,
  reportTotalsErrorCountMatches,
  reportTotalsTotalTokensMatches,
  reportTotalsErrorRateIsZeroWhenNoRequests,
  reportTotalsCacheHitRatioIsNull,
  reportRollupSourceMatchesCoverage,
  reportRollupFilterIsNeverEchoed,
  reportCoverageDatesAreAscending,
  reportCoverageGrainDatesIsNull,
  reportCoverageLegacyDatesIsNull,
  reportStartIsRfc3339NotDateOnly,
  reportDimensionHasName,
  reportDimensionKeyLooksNumeric,
  REPORT_ROLLUP_VIEWS,
  REPORT_ROLLUP_DEFAULT_VIEW,
  type ReportRollupSummaryBody,
  type ReportRollupDimensionsBody,
  type ReportGrainReport,
  type ReportTotals,
  type ReportDimensionOption,
} from '@/api/reportRollup'

useHyperPage({ title: () => t('rr.title') })

// ══ 查询条件 ════════════════════════════════════════════════════════════

const view = ref<string>(REPORT_ROLLUP_DEFAULT_VIEW)
const detailOn = ref(false)

// ★★ 窗口档位：缺省 7 天，上界 366 天（`report.go:143-145` 的 `> 366*24h`）。
//   ★ 不提供「今天」：聚合是 T+1 的，`start === end`（单日）是合法的。
// ★ 含 **1 天**（单日）：`report_rollup.go:143-145` 判的是 `end.Sub(start) > 366*24h`
//   ⇒ `start === end` 是**合法**的单日区间，而单日对账是很常用的一种看法。
const WINDOW_CHOICES = [1, 7, 30, 90, 366] as const
const windowDays = ref<number>(7)

/**
 * ★★★ T+1 语义：区间是**闭区间**且**不含今日** ——
 *   后端 `reportRange`（`report_rollup.go:122-147`）：`end = 昨日`、`start = 昨日 − (n−1)`。
 *   注释自陈原因：「聚合语义是 **T+1** 凌晨出昨日报表」⇒ **今日快照尚不存在**。
 *   ⇒ 客户端**不要**自己拼「今天到今天」——那会得到一个恒空的结果。
 */
function isoDay(d: Date): string {
  return d.toISOString().slice(0, 10)
}

function rangeFor(days: number): { start: string; end: string } {
  // ★ 用 UTC：后端是 `time.ParseInLocation("2006-01-02", raw, time.UTC)`，
  //   本地时区算出来的日期可能整体偏一天。
  const now = new Date()
  const end = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - 1))
  const start = new Date(end.getTime() - (days - 1) * 86400000)
  return { start: isoDay(start), end: isoDay(end) }
}

const range = computed(() => rangeFor(windowDays.value))

// ══ summary ═════════════════════════════════════════════════════════════

const summary = ref<ReportRollupSummaryBody | null>(null)
const summaryError = ref<string | null>(null)
const loadingSummary = ref(false)

async function loadSummary(): Promise<void> {
  loadingSummary.value = true
  summaryError.value = null
  try {
    summary.value = await fetchReportRollupSummary({
      start: range.value.start,
      end: range.value.end,
      view: view.value,
      // ★ 真值集合是 `1`/`true`/`yes`/`daily`（`:194-200`）⇒ 恒发 `1` 或 `0`。
      //   ⚠️ export 用的是**正好相反**的一组（`:207-213`），本页不碰 export。
      detail: detailOn.value ? '1' : '0',
    })
  } catch (err) {
    summary.value = null
    summaryError.value = (err as Error)?.message || String(err)
  } finally {
    loadingSummary.value = false
  }
}

const summaryDegraded = computed(() => (summary.value ? reportRollupIsDegraded(summary.value) : false))
const report = computed<ReportGrainReport | null>(() =>
  summary.value && !reportRollupIsDegraded(summary.value) ? summary.value.report : null,
)
const totals = computed<ReportTotals | null>(() => report.value?.totals ?? null)

// ── 四条派生列的自验（告警类判据的**双向**见 .spec.ts）────────────────────

/** ★★ 等式不成立 = 载荷被换过，不是「后端口径变了」。 */
const errorCountBad = computed(() => (totals.value ? !reportTotalsErrorCountMatches(totals.value) : false))
const totalTokensBad = computed(() => (totals.value ? !reportTotalsTotalTokensMatches(totals.value) : false))
/**
 * ★★★★ `request_count === 0` 时 `error_rate` **必须是 0**。
 *   服务端把除法包在 `if RequestCount > 0` 里、而字段是值类型 ⇒ 零请求时给的是 `0`。
 *   ⇒ 客户端自己拿 `error_count / request_count` 重算会得到 `NaN`，**与服务端给的不一样**。
 */
const errorRateZeroBad = computed(() =>
  totals.value && totals.value.request_count === 0 ? !reportTotalsErrorRateIsZeroWhenNoRequests(totals.value) : false,
)
const cacheRatioNull = computed(() => (totals.value ? reportTotalsCacheHitRatioIsNull(totals.value) : false))

// ── source / coverage ───────────────────────────────────────────────────

/** ★★★ `source` 与 `coverage` 派生值不一致 ⇒ 报违例（派生逻辑对不上）。 */
const sourceBad = computed(() => (report.value ? !reportRollupSourceMatchesCoverage(report.value) : false))
const coverageAscending = computed(() =>
  report.value ? reportCoverageDatesAreAscending(report.value) : true,
)
/**
 * ★★ `coverage` 的两个日期数组有**三种**状态，必须分开说：
 *   **键缺失**（键根本没序列化）/ **裸 `null`**（`[]string` 无 omitempty 且是 nil slice）/
 *   **有值**（长度）。★ 把三者合并成「0」就是撒谎。
 */
const coverageText = computed(() => {
  const r = report.value
  if (!r) return null
  const grain = r.coverage.grain_dates
  const legacy = r.coverage.legacy_dates
  return {
    // ★ 两个条件都在：`reportCoverageGrainDatesIsNull(r)` 是**语义权威**
    //   （判据与文档都引它），而 `grain === null` 是**收窄用**的 ——
    //   该谓词收的是 report 而不是数组，TS 无法据此把 `grain` 从 `string[] | null`
    //   收窄成 `string[]`（vue-tsc TS18047）。两者对每一个后端可达取值**恒等**
    //   （`grain` 就是 `r.coverage.grain_dates`），所以这不是冗余分支，
    //   而是「语义判据 + 类型收窄」各司其职。
    grain:
      grain === undefined
        ? t('rr.summary.keyAbsent')
        : grain === null || reportCoverageGrainDatesIsNull(r)
          ? t('rr.summary.nullArray')
          : String(grain.length),
    legacy:
      legacy === undefined
        ? t('rr.summary.keyAbsent')
        : legacy === null || reportCoverageLegacyDatesIsNull(r)
          ? t('rr.summary.nullArray')
          : String(legacy.length),
  }
})

/**
 * 降级码在降级信封里恒在（解包器已校验 `error_code === REPORT_ROLLUP_DEGRADED_CODE`），
 * 而 `reportRollupDegradedCode` 的返回类型是 `string | undefined`（非降级时为 `undefined`）。
 * ⇒ 这里只做**类型上的**兜底，不新增语义分支。
 */
function degradedCodeOf(
  body: ReportRollupSummaryBody | ReportRollupDimensionsBody | null,
): string {
  if (!body) return ''
  return reportRollupDegradedCode(body) ?? t('rr.summary.unknownCode')
}

// ── 回显 ────────────────────────────────────────────────────────────────

/** ★★ 回显是 RFC3339Nano，**不是**发进去的 `YYYY-MM-DD` ⇒ 两种形状并排显示。 */
const startIsEchoNotInput = computed(() =>
  report.value ? reportStartIsRfc3339NotDateOnly(report.value.start) : false,
)
/** ★ `filter` 标着 `json:"-"` ⇒ 恒不回显；页面据实说明，不假装能显示。 */
const filterNotEchoed = computed(() => (summary.value ? reportRollupFilterIsNeverEchoed(summary.value) : false))

// ══ dimensions ══════════════════════════════════════════════════════════

const dims = ref<ReportRollupDimensionsBody | null>(null)
const dimsError = ref<string | null>(null)
const loadingDims = ref(false)

async function loadDims(): Promise<void> {
  loadingDims.value = true
  dimsError.value = null
  try {
    dims.value = await fetchReportRollupDimensions({
      start: range.value.start,
      end: range.value.end,
      view: view.value,
    })
  } catch (err) {
    dims.value = null
    dimsError.value = (err as Error)?.message || String(err)
  } finally {
    loadingDims.value = false
  }
}

const dimsDegraded = computed(() => (dims.value ? reportRollupIsDegraded(dims.value) : false))
const dimGroups = computed(() => {
  if (!dims.value || reportRollupIsDegraded(dims.value)) return []
  const d = dims.value.dimensions
  return [
    { key: 'providers', label: t('rr.dims.providers'), options: d.providers },
    { key: 'credentials', label: t('rr.dims.credentials'), options: d.credentials },
    { key: 'api_keys', label: t('rr.dims.apiKeys'), options: d.api_keys },
    { key: 'models', label: t('rr.dims.models'), options: d.models },
    { key: 'tenants', label: t('rr.dims.tenants'), options: d.tenants },
    { key: 'persons', label: t('rr.dims.persons'), options: d.persons },
  ]
})

/**
 * ★★★★ `name` 缺失有**两种成因**，措辞必须分开：
 *   `report_rollup.go:378-380` 的 `fill()` **只回填** providers / credentials / api_keys；
 *   `models` / `tenants` / `persons` **连 names 表都没传进去**
 *   ⇒ 那三个维度的 `name` **恒缺**，不是「回查失败」。
 */
const NAME_NEVER_BACKFILLED = ['models', 'tenants', 'persons'] as const

function dimName(group: string, o: ReportDimensionOption): string {
  if (reportDimensionHasName(o)) return o.name as string
  return (NAME_NEVER_BACKFILLED as readonly string[]).includes(group)
    ? t('rr.dims.nameNeverFilled')
    : t('rr.dims.nameLookupFailed')
}

onBeforeUnmount(() => {
  summary.value = null
  dims.value = null
  summaryError.value = null
  dimsError.value = null
})
</script>

<template>
  <div class="rr">
    <!-- ══ 查询条件 ══ -->
    <section class="rr__sec">
      <header class="rr__head">
        <h2 class="rr__h">{{ t('rr.query.title') }}</h2>
        <button type="button" class="rr__btn" :disabled="loadingSummary" data-testid="rr-load" @click="loadSummary">
          {{ summary ? t('rr.reload') : t('rr.load') }}
        </button>
      </header>

      <div class="rr__chips">
        <span class="rr__chip-label">{{ t('rr.query.view') }}</span>
        <button
          v-for="v in REPORT_ROLLUP_VIEWS"
          :key="v"
          type="button"
          class="rr__chip-btn"
          :class="{ 'rr__chip-btn--on': v === view }"
          :data-testid="'rr-view-' + v"
          @click="view = v; loadSummary()"
        >
          {{ v }}
        </button>
      </div>

      <div class="rr__chips">
        <span class="rr__chip-label">{{ t('rr.query.window') }}</span>
        <button
          v-for="w in WINDOW_CHOICES"
          :key="w"
          type="button"
          class="rr__chip-btn"
          :class="{ 'rr__chip-btn--on': w === windowDays }"
          :data-testid="'rr-window-' + w"
          @click="windowDays = w; loadSummary()"
        >
          {{ t('rr.query.windowDays', { n: w }) }}
        </button>
      </div>

      <div class="rr__row">
        <label class="rr__label rr__label--inline">
          <input v-model="detailOn" type="checkbox" data-testid="rr-detail" @change="loadSummary()" />
          <span>{{ t('rr.query.detail') }}</span>
        </label>
      </div>

      <!-- ★★★ T+1：区间是闭区间且**不含今日**，页面据实说明而不是让用户以为漏了今天 -->
      <p class="rr__msg" data-testid="rr-range">
        {{ t('rr.query.range', { start: range.start, end: range.end }) }}
      </p>
      <p class="rr__msg" data-testid="rr-t1">{{ t('rr.query.t1') }}</p>

      <p v-if="summaryError" class="rr__msg rr__msg--err" data-testid="rr-summary-error">{{ summaryError }}</p>
      <p v-if="loadingSummary" class="rr__msg">{{ t('common.loading') }}</p>
    </section>

    <!-- ══ 汇总段 ══ -->
    <section class="rr__sec">
      <header class="rr__head">
        <h2 class="rr__h">{{ t('rr.summary.title') }}</h2>
        <button
          type="button"
          class="rr__btn rr__btn--ghost"
          :disabled="loadingDims"
          data-testid="rr-load-dims"
          @click="loadDims"
        >
          {{ dims ? t('rr.reload') : t('rr.loadCandidates') }}
        </button>
      </header>

      <!-- ★★★ 降级是 200 + 两键信封，主键整个消失 ⇒ 独立形状，不当失败 -->
      <template v-if="summaryDegraded">
        <p class="rr__msg rr__msg--warn" data-testid="rr-summary-degraded">
          {{ t('rr.summary.degraded', { code: degradedCodeOf(summary) }) }}
        </p>
      </template>

      <template v-else-if="report">
        <!-- ★★ 回显是 RFC3339Nano，不是发进去的 YYYY-MM-DD -->
        <p class="rr__msg" data-testid="rr-echo-range">
          {{ t('rr.summary.echoRange', { start: report.start, end: report.end }) }}
        </p>
        <p v-if="startIsEchoNotInput" class="rr__msg" data-testid="rr-echo-shape">
          {{ t('rr.summary.echoShape') }}
        </p>
        <p class="rr__msg" data-testid="rr-view-echo">{{ t('rr.summary.viewEcho', { v: report.view }) }}</p>

        <!-- ★★ filter 标着 json:"-" ⇒ 六个维度过滤一个都不回显 -->
        <p v-if="filterNotEchoed" class="rr__msg" data-testid="rr-filter-not-echoed">
          {{ t('rr.summary.filterNotEchoed') }}
        </p>

        <!-- ★★★ source 由 coverage 两个数组派生 ⇒ 不一致就是违例 -->
        <p class="rr__msg" data-testid="rr-source">{{ t('rr.summary.source', { s: report.source }) }}</p>
        <p v-if="sourceBad" class="rr__msg rr__msg--err" data-testid="rr-source-bad">
          {{ t('rr.summary.sourceBad', { s: report.source }) }}
        </p>
        <p v-if="!coverageAscending" class="rr__msg rr__msg--err" data-testid="rr-coverage-order-bad">
          {{ t('rr.summary.coverageOrderBad') }}
        </p>
        <!-- ★★ 两个日期数组可以是**裸 null**（不是 []、不是键缺） -->
        <p v-if="coverageText" class="rr__msg" data-testid="rr-coverage">
          {{ t('rr.summary.coverage', { grain: coverageText.grain, legacy: coverageText.legacy }) }}
        </p>

        <!-- ★ Totals：十六条恒在 + 一个 internal 视角条件键 -->
        <dl class="rr__kv">
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.requestCount') }}</dt>
            <dd :data-testid="'rr-t-request_count'">{{ totals?.request_count }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.successCount') }}</dt>
            <dd :data-testid="'rr-t-success_count'">{{ totals?.success_count }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.errorCount') }}</dt>
            <dd>
              <span :data-testid="'rr-t-error_count'">{{ totals?.error_count }}</span>
              <span v-if="errorCountBad" class="rr__danger" data-testid="rr-errorcount-bad">
                ★ {{ t('rr.summary.errorCountBad') }}
              </span>
            </dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.errorRate') }}</dt>
            <dd>
              <span :data-testid="'rr-t-error_rate'">{{ totals?.error_rate }}</span>
              <span v-if="errorRateZeroBad" class="rr__danger" data-testid="rr-errorrate-bad">
                ★ {{ t('rr.summary.errorRateBad') }}
              </span>
            </dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.inputTokens') }}</dt>
            <dd :data-testid="'rr-t-input_tokens'">{{ totals?.input_tokens }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.outputTokens') }}</dt>
            <dd :data-testid="'rr-t-output_tokens'">{{ totals?.output_tokens }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.cacheRead') }}</dt>
            <dd :data-testid="'rr-t-cache_read_tokens'">{{ totals?.cache_read_tokens }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.cacheWrite') }}</dt>
            <dd :data-testid="'rr-t-cache_write_tokens'">{{ totals?.cache_write_tokens }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.totalTokens') }}</dt>
            <dd>
              <span :data-testid="'rr-t-total_tokens'">{{ totals?.total_tokens }}</span>
              <span v-if="totalTokensBad" class="rr__danger" data-testid="rr-totaltokens-bad">
                ★ {{ t('rr.summary.totalTokensBad') }}
              </span>
            </dd>
          </div>
          <!-- ★★★ 命中率分母为 0 ⇒ 裸 null；「有缓存」与「命中率是 0」是两回事 -->
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.cacheHitRatio') }}</dt>
            <dd :data-testid="'rr-t-cache_hit_ratio'">
              {{ cacheRatioNull ? t('rr.summary.ratioNull') : totals?.cache_hit_ratio }}
            </dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.estimatedCost') }}</dt>
            <dd :data-testid="'rr-t-estimated_cost_cents'">{{ totals?.estimated_cost_cents }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.currency') }}</dt>
            <dd :data-testid="'rr-t-currency'">{{ totals?.currency }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.creditsCharged') }}</dt>
            <dd :data-testid="'rr-t-credits_charged'">{{ totals?.credits_charged }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.internalCost') }}</dt>
            <dd :data-testid="'rr-t-internal_cost_cents'">{{ totals?.internal_cost_cents }}</dd>
          </div>
          <!-- ★ 条件键：只有 internal 视角才有 -->
          <div v-if="totals?.internal_currency !== undefined" class="rr__kv-row">
            <dt>{{ t('rr.col.internalCurrency') }}</dt>
            <dd :data-testid="'rr-t-internal_currency'">{{ totals?.internal_currency }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.latencyP50') }}</dt>
            <dd :data-testid="'rr-t-latency_p50_ms'">{{ totals?.latency_p50_ms }}</dd>
          </div>
          <div class="rr__kv-row">
            <dt>{{ t('rr.col.latencyP95') }}</dt>
            <dd :data-testid="'rr-t-latency_p95_ms'">{{ totals?.latency_p95_ms }}</dd>
          </div>
        </dl>
      </template>
    </section>

    <!-- ══ 维度候选段 ══ -->
    <section class="rr__sec">
      <header class="rr__head">
        <h2 class="rr__h">{{ t('rr.dims.title') }}</h2>
      </header>

      <p v-if="dimsError" class="rr__msg rr__msg--err" data-testid="rr-dims-error">{{ dimsError }}</p>
      <p v-if="loadingDims" class="rr__msg">{{ t('common.loading') }}</p>

      <template v-if="dimsDegraded">
        <p class="rr__msg rr__msg--warn" data-testid="rr-dims-degraded">
          {{ t('rr.dims.degraded', { code: degradedCodeOf(dims) }) }}
        </p>
      </template>

      <template v-else-if="dims">
        <p v-if="dimGroups.every((g) => g.options.length === 0)" class="rr__msg" data-testid="rr-dims-empty">
          {{ t('rr.dims.empty') }}
        </p>
        <div
          v-for="g in dimGroups"
          :key="g.key"
          class="rr__group"
          :data-testid="'rr-group-' + g.key"
        >
          <p class="rr__group-title">
            {{ g.label }}
            <span class="rr__badge">{{ g.options.length }}</span>
          </p>
          <div v-if="g.options.length === 0" class="rr__msg" :data-testid="'rr-group-empty-' + g.key">
            {{ t('rr.dims.groupEmpty') }}
          </div>
          <div v-for="(o, i) in g.options" :key="i" class="rr__opt" :data-testid="'rr-opt-' + g.key + '-' + i">
            <span class="rr__opt-name" :data-testid="'rr-name-' + g.key + '-' + i">{{ dimName(g.key, o) }}</span>
            <span class="rr__opt-key" :data-testid="'rr-key-' + g.key + '-' + i">
              {{ o.key }}
              <span v-if="reportDimensionKeyLooksNumeric(o)" class="rr__hint">{{ t('rr.dims.keyLooksNumeric') }}</span>
            </span>
            <span class="rr__opt-n">{{ o.requests }}</span>
          </div>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.rr {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.rr__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.rr__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.rr__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.rr__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.rr__btn--ghost {
  align-self: flex-start;
  color: var(--app-text-secondary);
}

.rr__chip-btn {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.rr__chip-btn--on {
  color: var(--app-text);
  border-color: var(--app-text-secondary);
}

.rr__chip-label {
  align-self: center;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.rr__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: center;
}

.rr__label--inline {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: var(--app-space-2);
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.rr__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.rr__msg--err {
  color: var(--app-danger);
}

.rr__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.rr__danger {
  color: var(--app-danger);
}

.rr__kv {
  margin: 0;
}

.rr__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.rr__kv-row dt {
  color: var(--app-text-muted);
}

.rr__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}

.rr__group {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: var(--app-space-2);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.rr__group-title {
  margin: 0;
  font-size: 0.8125rem;
  font-weight: 600;
  color: var(--app-text);
  display: flex;
  align-items: center;
  gap: var(--app-space-2);
}

.rr__badge {
  font-size: 0.6875rem;
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
}

.rr__opt {
  display: flex;
  align-items: baseline;
  gap: var(--app-space-2);
  font-size: 0.8125rem;
  padding: 2px 0;
}

.rr__opt-name {
  flex: 1 1 auto;
  color: var(--app-text);
  word-break: break-all;
}

.rr__opt-key {
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  word-break: break-all;
}

.rr__opt-n {
  color: var(--app-text-secondary);
  font-variant-numeric: tabular-nums;
}

.rr__hint {
  color: var(--app-warning, var(--app-danger));
}
</style>
