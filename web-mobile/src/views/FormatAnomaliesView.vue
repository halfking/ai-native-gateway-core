<script setup lang="ts">
// FormatAnomaliesView — 响应格式异常：明细列表 + 按小时汇总（2026-10-14，第一百零九批）。
//
// GET /api/admin/format-anomalies        → 明细（分页，limit/offset 回显）
// GET /api/admin/format-anomaly-summary  → 按 (hour, type, severity) 汇总
//
// 权限：★★ **superAdmin 档**（`admin/handler.go:913-914` 两条都是 `h.superAdmin`）
//   ⇒ 抽屉席**须设** `requiresRole: 'super_admin'`，
//     并同步 `src/components/shell/AppDrawer.spec.ts` 的排序白名单。
//
// ★★★ 本族是本仓**第六次「不隔离」**（`withAllTenantReadOnlyTx`，
//   `admin/format_anomalies.go:106`/`:143`/`:213`）：两张表都有 `tenant_id` 列
//   且 list 会 SELECT 出来，但 WHERE **从不过滤**。
//   ★★ 但与批 104 / 批 106 的处置**完全相反**：
//     那两族是 **admin 档** + 不隔离 ⇒ tenant_admin 能读到跨租户数据
//       ⇒ 必须在**视图内部按段落**门控；
//     本族是 **superAdmin 档** ⇒ **档位本身就挡住了**跨租户访问
//       ⇒ 抽屉席设 `requiresRole` 即是完整处置，**不需要**任何段落级门控。
//   ⇒ 所以本页**没有**角色分支 —— 这是刻意的，不是漏写。
//
// ★★★★ 本族最要紧的（详见 api/formatAnomalies.ts 文件头 15 条）：
//   1. ★★★★★★★ **`count` 在两个端点里语义相反**：
//      · list（`:100-111` + `:186-191`）的 `count` 是**全表命中数**
//        ⇒ 恒成立的是 `count >= anomalies.length`（与批 78/81 的 `count == 本页长度` **相反**）；
//      · summary（`:231` 后）的 `count` 是 **`len(summaries)`**，恒等于本页长度。
//      ⇒ ★★★ 同名字段、两种语义 ⇒ 页面上**必须分开措辞**，不能共用一句「共 N 条」。
//   2. ★★★★ `limit` / `offset` **都回显**（`:61-71`）⇒ 分页能精确判定；
//      ⚠️ 与批 79/80/81（只回显 days、不回显 limit）**相反**，判据不能跨族照抄。
//      `offset` **没有上界**（`offset=999999` 照发）⇒ 只判「到末页」，不判「越界」。
//   3. ★★★★ `provider_code` 走 `COALESCE(rfa.provider_code, p.code)`，
//      **展示与过滤用同一个表达式**（`:119` / `:81`）
//      ⇒ 键缺失的行**不可能**是 provider 过滤的结果（`NULL = 'x'` 求值为 NULL）。
//   4. ★★★★ summary 的三个 `AVG(...)` 是 `*float64` + omitempty
//      ⇒ **`AVG` 返回 NULL 时键被省略**，不是 0、不是 null
//      ⇒ ★★★ 客户端算平均时**不能**把键缺当 0 参与运算。
//   5. ★★★★ summary 的排序是 `ORDER BY hour DESC, anomaly_count DESC`（`:230`）
//      ⇒ **整张表不按计数降序**，只能断言「**同一 hour 内**按 `anomaly_count` 非增」。
//   6. ★★★ summary 的 `LIMIT 200` **硬编码在 SQL 里**（`:231`），不可调、不回显
//      ⇒ 只能靠 `count === 200` 反推可能被截断。
//   7. ★★★ `anomaly_type` / `severity` 是**开放域**（写入入口接受任意字符串、建表无 CHECK）
//      ⇒ 未知取值**不是错误**，页面要标出来但不能当异常。
//   8. ★★★ `request_id` 恒在但**可以是空串**（`text NOT NULL` ≠ 非空串）。
//   9. ★★★ 三条 500 的文案**各不相同**（`count query failed` / `list query failed` /
//      `summary query failed`）⇒ 失败发生在哪一步是可区分的。
//  10. ★★ `unresolved_only` 用 `queryBool`（`handler.go:1546-1549`）：
//      只有 `true`/`TRUE`/`True`/`1` 为真，**其它任何值（含 `yes`/`on`/`t`）都为假**
//      ⇒ 这是「不报错、不回落成缺省」的布尔解析，**无法区分「没传」与「传了假值」**。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchFormatAnomalies,
  fetchFormatAnomalySummary,
  formatCountCoversWholeResult,
  formatHasMorePages,
  formatIsLastPage,
  formatProviderCodeIsUnmatchable,
  formatAnomalyTypeIsKnown,
  formatSummaryAffectedWithinCount,
  formatSummaryResolvedWithinCount,
  formatSummaryIsHourDesc,
  formatSummaryIsCountDescWithinHour,
  formatSummaryAvgIsAbsent,
  formatSummaryAtSqlCap,
  formatHoursWasRewritten,
  formatRequestIdIsEmpty,
  formatNumericValueIsZero,
  FORMAT_ANOMALY_LIMIT_DEFAULT,
  FORMAT_ANOMALY_LIMIT_MAX,
  FORMAT_SUMMARY_HOURS_DEFAULT,
  FORMAT_SUMMARY_HOURS_MAX,
  FORMAT_SUMMARY_SQL_LIMIT,
  FORMAT_SEVERITY_TABLE_DEFAULT,
  type FormatAnomaliesResponse,
  type FormatAnomalySummaryResponse,
  type FormatAnomalyRecord,
  type FormatAnomalySummaryRow,
} from '@/api/formatAnomalies'

useHyperPage({ title: () => t('fa.title') })

// ══ 明细段的过滤与分页 ═══════════════════════════════════════════════════

const provider = ref('')
const model = ref('')
const anomalyType = ref('')
const unresolvedOnly = ref(false)

const LIMIT_CHOICES = [FORMAT_ANOMALY_LIMIT_DEFAULT, 100, FORMAT_ANOMALY_LIMIT_MAX] as const
const limit = ref<number>(FORMAT_ANOMALY_LIMIT_DEFAULT)
const offset = ref(0)

const list = ref<FormatAnomaliesResponse | null>(null)
const listError = ref<string | null>(null)
const loadingList = ref(false)

async function loadList(): Promise<void> {
  loadingList.value = true
  listError.value = null
  try {
    list.value = await fetchFormatAnomalies({
      limit: limit.value,
      offset: offset.value,
      // ★ 三个字符串过滤走 `strings.TrimSpace` ⇒ 空串等价「不过滤」，仍照发。
      //   这里只在非空时传，避免制造一堆无意义的空串参数。
      ...(provider.value.trim() ? { provider: provider.value.trim() } : {}),
      ...(model.value.trim() ? { model: model.value.trim() } : {}),
      ...(anomalyType.value.trim() ? { anomalyType: anomalyType.value.trim() } : {}),
      // ★ `unresolvedOnly` 恒传（true/false 都发）⇒ 与「没传」区分得开。
      unresolvedOnly: unresolvedOnly.value,
    })
  } catch (err) {
    list.value = null
    listError.value = (err as Error)?.message || String(err)
  } finally {
    loadingList.value = false
  }
}

function nextPage(): void {
  offset.value = offset.value + limit.value
  void loadList()
}

function prevPage(): void {
  offset.value = Math.max(0, offset.value - limit.value)
  void loadList()
}

/**
 * ★ 换每页档位时必须把 `offset` 归零：否则 offset 还是上一档累计出来的值，
 *   换档后会直接从中间开始读，**跨过一整段记录**（而 count 是全表命中数，
 *   用户完全看不出中间少了什么）。
 */
function applyLimit(lp: number): void {
  limit.value = lp
  offset.value = 0
  void loadList()
}

function resetFilter(): void {
  provider.value = ''
  model.value = ''
  anomalyType.value = ''
  unresolvedOnly.value = false
  offset.value = 0
  void loadList()
}

// ══ 汇总段 ══════════════════════════════════════════════════════════════

const HOUR_CHOICES = [FORMAT_SUMMARY_HOURS_DEFAULT, 168, FORMAT_SUMMARY_HOURS_MAX] as const
const hours = ref<number>(FORMAT_SUMMARY_HOURS_DEFAULT)

const summary = ref<FormatAnomalySummaryResponse | null>(null)
const summaryError = ref<string | null>(null)
const loadingSummary = ref(false)

async function loadSummary(): Promise<void> {
  loadingSummary.value = true
  summaryError.value = null
  try {
    // ★ 没有 limit 参数：上限 200 硬编码在 SQL 里（`:231`）
    summary.value = await fetchFormatAnomalySummary({ hours: hours.value })
  } catch (err) {
    summary.value = null
    summaryError.value = (err as Error)?.message || String(err)
  } finally {
    loadingSummary.value = false
  }
}

// ══ 明细段的判读 ════════════════════════════════════════════════════════

/** ★★★ `count >= 本页长度`（count 是全表命中数）。这**不是**「count == 本页长度」。 */
const countCovers = computed(() => (list.value ? formatCountCoversWholeResult(list.value) : true))
const countViolation = computed(() => list.value !== null && !countCovers.value)

const hasMore = computed(() => (list.value ? formatHasMorePages(list.value) : false))
const isLastPage = computed(() => (list.value ? formatIsLastPage(list.value) : true))

/** ★★ 「键缺失」不是「值为空」——provider 行被删时是键缺。 */
function providerCodeText(r: FormatAnomalyRecord): string {
  if (r.provider_code !== undefined && r.provider_code !== '') return r.provider_code
  return formatProviderCodeIsUnmatchable(r) ? t('fa.row.providerUnmatchable') : t('fa.row.providerAbsent')
}

/** ★★ 恒在键但**可能是空串** ⇒ 必须显式说「空」，不能渲染成空白。 */
function requestIdText(r: FormatAnomalyRecord): string {
  return formatRequestIdIsEmpty(r) ? t('fa.row.requestIdEmpty') : r.request_id
}

/** ★ `anomaly_type` 是**开放域** ⇒ 未知取值要标出来，但它**不是错误**。 */
function typeText(r: FormatAnomalyRecord | FormatAnomalySummaryRow): string {
  return formatAnomalyTypeIsKnown(r) ? r.anomaly_type : `${r.anomaly_type} ★`
}

/** ★ `severity` 缺省值是表级默认 `medium`；另一张表默认是 `low`。 */
function severityText(r: FormatAnomalyRecord | FormatAnomalySummaryRow): string {
  return r.severity === FORMAT_SEVERITY_TABLE_DEFAULT ? `${r.severity} ${t('fa.sev.default')}` : r.severity
}

/**
 * ★★ 三个可选数值：**键缺 = NULL**，而 0 是**真的 0**（指针指向 0，键仍在）。
 * ⇒ 必须用模块的具名判据 `formatNumericValueIsZero(row, key)`，它收的是**行 + 键名**；
 *   自己写 `v === 0` 会把「键缺」与「值为 0」混在一起。
 *
 * ★ 另注：JSON 键是 `content_size_bytes`，与 Go 字段名 `ContentSize` **不一致**（契约头 (6)）——
 *   但 TypeScript 接口里那个字段名是对的，所以直接按字段名读即可，**不需要任何索引技巧**
 *   （先写过一版 `r as Record<string, unknown>` 的索引取值，vue-tsc 报 TS2352：
 *   `FormatAnomalyRecord` 没有索引签名，转 `Record` 不安全 ⇒ 白绕一圈）。
 */
function numericText(
  row: FormatAnomalyRecord,
  key: 'expected_tokens' | 'actual_tokens' | 'content_size_bytes',
  unit: string,
): string {
  // ★ 这里用 `=== undefined` 而不是 `!(key in row)`：对 TS 的可选字段
  //   （`expected_tokens?: number`）而言，JSON 里键缺解包后**就是** undefined
  //   ⇒ 两个判断在每一个后端可达的取值上完全一致（变异实测：换过去一条没红）
  //   ⇒ 按「可证等价 ⇒ 简化而不是加判据」处置，保留直白的那一个。
  //   ⚠️ 0 仍然是**真的 0**（指针指向 0，键在）—— 下面这一行才是把两者分开的地方。
  const v = row[key]
  if (v === undefined) return t('fa.row.nullValue')
  if (formatNumericValueIsZero(row, key)) return `0 ${unit}`
  return `${v} ${unit}`
}

// ══ 汇总段的判读 ════════════════════════════════════════════════════════

const summaryCountIsLength = computed(
  () => summary.value !== null && summary.value.count === summary.value.summaries.length,
)
const hourDesc = computed(() => (summary.value ? formatSummaryIsHourDesc(summary.value) : true))
const countDescWithinHour = computed(() =>
  summary.value ? formatSummaryIsCountDescWithinHour(summary.value) : true,
)

/** ★★★ `count === 200` ⇒ 可能被 SQL 硬上限截断（响应无字段能证）。 */
const atSqlCap = computed(() => (summary.value ? formatSummaryAtSqlCap(summary.value) : false))
const hoursRewritten = computed(() =>
  summary.value ? formatHoursWasRewritten(summary.value, hours.value) : false,
)

/**
 * ★★★ 三个 `AVG(...)` 是 `*float64` + omitempty ⇒ **`AVG` 返回 NULL 时键被省略**，
 *   不是 0、不是 null（契约头 (9)）。
 * ⇒ 必须走 `formatSummaryAvgIsAbsent(s, key)`（`!(key in s)`）——
 *   键缺与 0 是两件不同的事：把「键缺」当成 0 参与运算，就是凭空造出一个平均值。
 */
function avgText(
  s: FormatAnomalySummaryRow,
  key: 'avg_content_size' | 'avg_expected_tokens' | 'avg_actual_tokens',
  unit: string,
): string {
  if (formatSummaryAvgIsAbsent(s, key)) return t('fa.row.avgAbsent')
  const v = s[key]
  if (v === 0) return `0 ${unit}`
  return `${v ?? ''} ${unit}`
}

onBeforeUnmount(() => {
  list.value = null
  summary.value = null
  listError.value = null
  summaryError.value = null
})
</script>

<template>
  <div class="fa">
    <!-- ══ 明细段 ══ -->
    <section class="fa__sec">
      <header class="fa__head">
        <h2 class="fa__h">{{ t('fa.list.title') }}</h2>
        <button type="button" class="fa__btn" :disabled="loadingList" data-testid="fa-load-list" @click="loadList">
          {{ list ? t('fa.reload') : t('fa.load') }}
        </button>
      </header>

      <div class="fa__chips">
        <span class="fa__chip-label">{{ t('fa.filter.limit') }}</span>
        <button
          v-for="lp in LIMIT_CHOICES"
          :key="lp"
          type="button"
          class="fa__chip-btn"
          :class="{ 'fa__chip-btn--on': lp === limit }"
          :data-testid="'fa-limit-' + lp"
          @click="applyLimit(lp)"
        >
          {{ lp }}
        </button>
      </div>

      <div class="fa__row">
        <label class="fa__label">
          {{ t('fa.filter.provider') }}
          <input v-model="provider" type="text" class="fa__input" data-testid="fa-provider" />
        </label>
        <label class="fa__label">
          {{ t('fa.filter.model') }}
          <input v-model="model" type="text" class="fa__input" data-testid="fa-model" />
        </label>
      </div>
      <div class="fa__row">
        <label class="fa__label">
          {{ t('fa.filter.anomalyType') }}
          <input v-model="anomalyType" type="text" class="fa__input" data-testid="fa-type" />
        </label>
        <label class="fa__label fa__label--inline">
          <input v-model="unresolvedOnly" type="checkbox" data-testid="fa-unresolved" />
          <span>{{ t('fa.filter.unresolvedOnly') }}</span>
        </label>
      </div>
      <div class="fa__row">
        <button type="button" class="fa__btn fa__btn--ghost" data-testid="fa-reset" @click="resetFilter">
          {{ t('fa.filter.reset') }}
        </button>
      </div>

      <p v-if="listError" class="fa__msg fa__msg--err" data-testid="fa-list-error">{{ listError }}</p>
      <p v-if="loadingList" class="fa__msg">{{ t('common.loading') }}</p>

      <template v-if="list">
        <!-- ★★★ 两个端点的 count 语义相反，这里是 list 的：全表命中数 -->
        <p class="fa__msg" data-testid="fa-list-count">
          {{ t('fa.list.count', { total: list.count, page: list.anomalies.length }) }}
        </p>
        <!-- ★★★ 不等就是载荷被换过，不是「后端口径变了」 -->
        <p v-if="countViolation" class="fa__msg fa__msg--err" data-testid="fa-count-violation">
          {{ t('fa.list.countViolation', { total: list.count, page: list.anomalies.length }) }}
        </p>
        <!-- ★★ limit / offset 都回显 ⇒ 显式写出，让用户知道后端认的是哪一档 -->
        <p class="fa__msg" data-testid="fa-list-window">
          {{ t('fa.list.window', { limit: list.limit, offset: list.offset }) }}
        </p>

        <div class="fa__row">
          <button
            type="button"
            class="fa__btn fa__btn--ghost"
            :disabled="list.offset === 0"
            data-testid="fa-prev"
            @click="prevPage"
          >
            {{ t('fa.list.prev') }}
          </button>
          <button
            type="button"
            class="fa__btn fa__btn--ghost"
            :disabled="isLastPage"
            data-testid="fa-next"
            @click="nextPage"
          >
            {{ t('fa.list.next') }}
          </button>
          <span v-if="hasMore" class="fa__msg" data-testid="fa-has-more">{{ t('fa.list.hasMore') }}</span>
          <span v-else class="fa__msg" data-testid="fa-last-page">{{ t('fa.list.lastPage') }}</span>
        </div>

        <p v-if="list.anomalies.length === 0" class="fa__msg" data-testid="fa-list-empty">
          {{ t('fa.list.empty') }}
        </p>

        <article
          v-for="r in list.anomalies"
          :key="r.id"
          class="fa__card"
          :data-testid="'fa-row-' + r.id"
        >
          <div class="fa__row">
            <span class="fa__type">{{ typeText(r) }}</span>
            <span class="fa__badge" :class="{ 'fa__badge--warn': r.severity === 'high' || r.severity === 'critical' }">
              {{ severityText(r) }}
            </span>
            <span v-if="r.resolved" class="fa__badge fa__badge--ok">{{ t('fa.row.resolved') }}</span>
            <span v-else class="fa__badge">{{ t('fa.row.unresolved') }}</span>
          </div>
          <dl class="fa__kv">
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.detectedAt') }}</dt><dd>{{ r.detected_at }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.requestId') }}</dt><dd>{{ requestIdText(r) }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.provider') }}</dt><dd>{{ providerCodeText(r) }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.clientModel') }}</dt>
              <dd>{{ r.client_model ?? t('fa.row.absent') }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.outboundModel') }}</dt>
              <dd>{{ r.outbound_model ?? t('fa.row.absent') }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.contentSize') }}</dt>
              <dd :data-testid="'fa-f-' + r.id + '-content_size'">{{ numericText(r, 'content_size_bytes', t('fa.unit.bytes')) }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.expectedTokens') }}</dt>
              <dd :data-testid="'fa-f-' + r.id + '-expected_tokens'">{{ numericText(r, 'expected_tokens', t('fa.unit.tokens')) }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.actualTokens') }}</dt>
              <dd :data-testid="'fa-f-' + r.id + '-actual_tokens'">{{ numericText(r, 'actual_tokens', t('fa.unit.tokens')) }}</dd>
            </div>
          </dl>
        </article>
      </template>
    </section>

    <!-- ══ 汇总段 ══ -->
    <section class="fa__sec">
      <header class="fa__head">
        <h2 class="fa__h">{{ t('fa.summary.title') }}</h2>
        <button type="button" class="fa__btn" :disabled="loadingSummary" data-testid="fa-load-summary" @click="loadSummary">
          {{ summary ? t('fa.reload') : t('fa.loadSummary') }}
        </button>
      </header>

      <div class="fa__chips">
        <span class="fa__chip-label">{{ t('fa.summary.hours') }}</span>
        <button
          v-for="hp in HOUR_CHOICES"
          :key="hp"
          type="button"
          class="fa__chip-btn"
          :class="{ 'fa__chip-btn--on': hp === hours }"
          :data-testid="'fa-hours-' + hp"
          @click="hours = hp; loadSummary()"
        >
          {{ hp }}
        </button>
      </div>

      <p v-if="summaryError" class="fa__msg fa__msg--err" data-testid="fa-summary-error">
        {{ summaryError }}
      </p>
      <p v-if="loadingSummary" class="fa__msg">{{ t('common.loading') }}</p>

      <template v-if="summary">
        <!-- ★★ 同一个 count 字段，这里的语义是**本页行数** -->
        <p class="fa__msg" data-testid="fa-summary-count">
          {{ t('fa.summary.count', { n: summary.count }) }}
        </p>
        <p v-if="!summaryCountIsLength" class="fa__msg fa__msg--err" data-testid="fa-summary-count-bad">
          {{ t('fa.summary.countBad', { n: summary.count, len: summary.summaries.length }) }}
        </p>
        <!-- ★ 回显的是**生效值**（<=0 ⇒ 24，>720 ⇒ 720） -->
        <p class="fa__msg" data-testid="fa-summary-hours">
          {{ t('fa.summary.hoursEcho', { h: summary.hours }) }}
        </p>
        <p v-if="hoursRewritten" class="fa__msg fa__msg--warn" data-testid="fa-hours-rewritten">
          {{ t('fa.summary.hoursRewritten', { want: hours }) }}
        </p>
        <!-- ★★★ 硬编码 LIMIT 200，不可调不回显 ⇒ 只能靠 count===200 反推 -->
        <p v-if="atSqlCap" class="fa__msg fa__msg--warn" data-testid="fa-summary-cap">
          {{ t('fa.summary.atCap', { cap: FORMAT_SUMMARY_SQL_LIMIT }) }}
        </p>
        <!-- ★ 排序是 hour DESC ⇒ 整体不按计数降序，只能判 hour 内 -->
        <p v-if="!hourDesc" class="fa__msg fa__msg--err" data-testid="fa-hour-order-bad">
          {{ t('fa.summary.hourOrderBad') }}
        </p>
        <p v-if="!countDescWithinHour" class="fa__msg fa__msg--err" data-testid="fa-count-order-bad">
          {{ t('fa.summary.countOrderBad') }}
        </p>

        <p v-if="summary.summaries.length === 0" class="fa__msg" data-testid="fa-summary-empty">
          {{ t('fa.summary.empty') }}
        </p>

        <article
          v-for="(s, i) in summary.summaries"
          :key="'sm-' + i"
          class="fa__card"
          :data-testid="'fa-sm-' + i"
        >
          <div class="fa__row">
            <span class="fa__type">{{ typeText(s) }}</span>
            <span class="fa__badge">{{ severityText(s) }}</span>
            <span class="fa__hour">{{ s.hour }}</span>
          </div>
          <dl class="fa__kv">
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.anomalyCount') }}</dt><dd>{{ s.anomaly_count }}</dd>
            </div>
            <!-- ★★★ COUNT(DISTINCT request_id) ⇒ 恒 <= anomaly_count -->
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.affected') }}</dt>
              <dd>
                {{ s.affected_requests }}
                <span v-if="!formatSummaryAffectedWithinCount(s)" class="fa__danger">
                  ★ {{ t('fa.summary.affectedBad') }}
                </span>
              </dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.resolvedCount') }}</dt>
              <dd>
                {{ s.resolved_count }}
                <span v-if="!formatSummaryResolvedWithinCount(s)" class="fa__danger">
                  ★ {{ t('fa.summary.resolvedBad') }}
                </span>
              </dd>
            </div>
            <!-- ★★ AVG 为 NULL 时键被省略 ≠ 0 ⇒ 必须显式说「无法判定」 -->
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.avgContentSize') }}</dt>
              <dd :data-testid="'fa-s-' + i + '-avg_content_size'">{{ avgText(s, 'avg_content_size', t('fa.unit.bytes')) }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.avgExpectedTokens') }}</dt>
              <dd :data-testid="'fa-s-' + i + '-avg_expected_tokens'">{{ avgText(s, 'avg_expected_tokens', t('fa.unit.tokens')) }}</dd>
            </div>
            <div class="fa__kv-row">
              <dt>{{ t('fa.col.avgActualTokens') }}</dt>
              <dd :data-testid="'fa-s-' + i + '-avg_actual_tokens'">{{ avgText(s, 'avg_actual_tokens', t('fa.unit.tokens')) }}</dd>
            </div>
            <div v-if="s.provider_code" class="fa__kv-row">
              <dt>{{ t('fa.col.provider') }}</dt><dd>{{ s.provider_code }}</dd>
            </div>
          </dl>
        </article>
      </template>
    </section>
  </div>
</template>

<style scoped>
.fa {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.fa__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.fa__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.fa__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.fa__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.fa__btn--ghost {
  align-self: flex-start;
  color: var(--app-text-secondary);
}

.fa__chip-btn {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.fa__chip-btn--on {
  color: var(--app-text);
  border-color: var(--app-text-secondary);
}

.fa__chip-label {
  align-self: center;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.fa__input {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.fa__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 140px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.fa__label--inline {
  flex-direction: row;
  align-items: center;
  gap: var(--app-space-2);
}

.fa__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: flex-end;
}

.fa__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.fa__msg--err {
  color: var(--app-danger);
}

.fa__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.fa__danger {
  color: var(--app-danger);
}

.fa__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.fa__type {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}

.fa__badge {
  font-size: 0.6875rem;
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
}

.fa__badge--warn {
  color: var(--app-warning, var(--app-danger));
}

.fa__badge--ok {
  color: var(--app-success, var(--app-text));
}

.fa__hour {
  font-size: 0.6875rem;
  color: var(--app-text-muted);
  margin-left: auto;
}

.fa__kv {
  margin: 0;
}

.fa__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.fa__kv-row dt {
  color: var(--app-text-muted);
}

.fa__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}
</style>