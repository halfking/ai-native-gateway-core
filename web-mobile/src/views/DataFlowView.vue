<script setup lang="ts">
// DataFlowView — 数据流与大字段（/data-flow，**admin 档**）。
//
// 数据源（四条，均 admin 档，api/dataLifecycleStats.ts 文件头）：
//   GET /api/admin/data-lifecycle/stats      冷热分段 + 租户分布 + 增长趋势
//   GET /api/admin/data-lifecycle/metrics    轻量指标（Prometheus 用）
//   GET /api/admin/data-lifecycle/jobs       通用异步任务（running + history）
//   GET /api/admin/data-lifecycle/blobs/top  大字段 Top-N
//
// ## ★ 与 /data-lifecycle 的分工（按「答什么」划界，不按名字划界）
//   /data-lifecycle  答「数据库这一层什么状态」——分区清单 + 表体积榜（**表级**）
//   本页             答「记录怎么分布、有没有在清理、大字段占多少」——**记录级**
// 两者粒度不同，且本页的 `metrics` 是**全表口径**（SQL 无 WHERE），
// 与 /data-lifecycle 的租户/榜内口径并排会给出错误对比 ⇒ **不合并成一张页面**。
//
// ## ⚠️ 权限档位
// admin/handler.go:960/962/979/994 四条注册全部是 `admin(...)`
// ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//
// ## ★★★★★ 本页必须钉住的九件事（全部来自 §11.102 的后端取证）
//
// (1) ★★★★ **`metrics` 是全表口径**：`data_lifecycle_metrics.go:63` 的
//      `FROM request_logs` 没有 WHERE，注册却是 `admin(...)`
//      ⇒ tenant_admin 看到的也是**整表**数字。
//      页面上 metrics 区必须显式挂「全表口径」横幅，
//      且**不得**与 stats 的租户口径数字并排成同一组对比。
//
// (2) ★★★★ **`stats.total_rows` 是租户口径、`total_size_bytes` 是全表口径**
//      （前者带 tenantFilter，后者是 `pg_total_relation_size('request_logs')`
//      不过滤，data_lifecycle.go:75-76）
//      ⇒ **不能**并排写成「本租户 X 行 / Y 字节」。两行各自挂口径标签。
//
// (3) ★★★★ **四段可为 `null`**：逐行 Scan 出错走 `warnRowSkip` 后 continue
//      （data_lifecycle.go:146-149）⇒ **显示「查不出来」，不是「0 行」**。
//
// (4) ★★★★ **`by_tenant` / `growth_trend` 的空数组是二义的**：
//      这两段查询失败是**非致命**的（:197-201 / :262-266，注释 "non-fatal, continue"），
//      失败时后端仍把预置空切片编进响应 ⇒ `[]` 与「真没数据」**不可分**。
//      ⇒ 措辞只能是「没有可展示的记录」，不能断言「无数据」。
//
// (5) ★★★ **`growth_trend` 是新的一天在前**（`ORDER BY day DESC`，:245/:258）
//      ⇒ 折线/列表要**反转**成时间正序，否则趋势是倒着走的。
//
// (6) ★★★ **`jobs` 的 limit 是后端硬编码 50**（`h.listJobs(50)`，:322）
//      ⇒ 本页**刻意不给「条数」选择器**，给了会让人以为生效了。
//
// (7) ★★★ **`blobs/top` 的 `total_bytes` 是这 N 行的合计**（:135 逐行累加）
//      ⇒ 必须标「本次 N 行合计」，不能说「这些大字段共占 X」。
//
// (8) ★★★★ **`last_cleanup_at` / `last_archive_at` 恒不存在**
//      （后端从不赋值，见 api 文件头）⇒ 显示「本端点不提供」，
//      **绝不能**说「从未清理过」。
//
// (9) ★★★ **段/租户的 `size_bytes` 是按行数摊派的估算值**
//      （`pg_total_relation_size × rows / total_count`，:105-106），
//      含索引，不是实测占用 ⇒ 一律标「摊派估算」。
//
// 另有：`percent_of_total` 是 0-100（不是 0-1），`total_rows===0` 时无意义；
// `days` 是后端写死的标注值 7/23/60/999，不是区间上界；
// `compression_rate` 被后端夹到 100（:280-282）；
// 30 天边界双侧闭区间 ⇒ 段行数之和可能 > total_rows（重复计数，不是数据错）。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtInt, fmtNum } from '@/utils/format'
import {
  fetchDataLifecycleStats,
  fetchDataLifecycleMetrics,
  fetchLifecycleJobs,
  fetchBlobTop,
  segmentUnavailable,
  anySegmentUnavailable,
  segmentPercentTotal,
  percentMeaningless,
  segmentRowsDisagree,
  byTenantTruncated,
  growthTrendTruncated,
  listEmptyIsAmbiguous,
  compressionRateSaturated,
  metricsCleanupTimeAbsent,
  metricsArchiveTimeAbsent,
  metricsRowsDisagree,
  jobRunning,
  jobFailed,
  jobProgressMeaningless,
  blobModelUnknown,
  blobRowHasNoBody,
  blobTopLikelyTruncated,
  blobsTopLimitEffective,
  SEGMENT_DAYS,
  LIFECYCLE_JOBS_HISTORY_LIMIT,
  BLOB_TOP_LIMIT_DEFAULT,
  BLOB_TOP_LIMIT_MAX,
  type SegmentKey,
  type DailyGrowth,
  type DataLifecycleStats,
  type DataSegment,
  type DataLifecycleMetrics,
  type LifecycleJobsResponse,
  type JobRun,
  type BlobTopResponse,
} from '@/api/dataLifecycleStats'

useHyperPage({ title: () => t('dataFlow.title') })

/** 「查不出来」占位。字形与 0 不同，且与数字类分开 class。 */
const UNAVAILABLE = '—'

type SectionKey = 'stats' | 'metrics' | 'jobs' | 'blobs'

const stats = ref<DataLifecycleStats | null>(null)
const metrics = ref<DataLifecycleMetrics | null>(null)
const jobs = ref<LifecycleJobsResponse | null>(null)
const blobs = ref<BlobTopResponse | null>(null)

const loading = ref<SectionKey | null>(null)
const loaded = ref<Record<SectionKey, boolean>>({ stats: false, metrics: false, jobs: false, blobs: false })
const error = ref<Record<SectionKey, string | null>>({ stats: null, metrics: null, jobs: null, blobs: null })

/* ── 大字段条数（后端 (0,200] 之外静默回落 20，所以只给合法值） ────── */

const blobLimit = ref(BLOB_TOP_LIMIT_DEFAULT)
const blobLimitEcho = computed(() => blobsTopLimitEffective(blobLimit.value))

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('dataFlow.errForbidden')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

async function load(section: SectionKey): Promise<void> {
  loading.value = section
  error.value[section] = null
  try {
    if (section === 'stats') stats.value = await fetchDataLifecycleStats()
    if (section === 'metrics') metrics.value = await fetchDataLifecycleMetrics()
    if (section === 'jobs') jobs.value = await fetchLifecycleJobs()
    if (section === 'blobs') blobs.value = await fetchBlobTop({ limit: blobLimit.value })
  } catch (err) {
    if (section === 'stats') stats.value = null
    if (section === 'metrics') metrics.value = null
    if (section === 'jobs') jobs.value = null
    if (section === 'blobs') blobs.value = null
    error.value[section] = describeError(err)
  } finally {
    loading.value = null
    loaded.value[section] = true
  }
}

function reloadBlobs(): void {
  void load('blobs')
}

/* ── (2) stats：两行口径不同，各挂标签 ────────────────────────────── */

const statsPercentMeaningless = computed(() => (stats.value ? percentMeaningless(stats.value) : false))
const statsRowsDisagree = computed(() => (stats.value ? segmentRowsDisagree(stats.value) : false))
const statsAnyMissing = computed(() => (stats.value ? anySegmentUnavailable(stats.value) : false))
const statsPercentTotal = computed(() => (stats.value ? segmentPercentTotal(stats.value) : null))

/** ★★ 四段里 `null` 的那些。UI 据此渲染「查不出来」而不是 0。 */
function segment(s: DataLifecycleStats, key: SegmentKey): DataSegment | null {
  // ★ 这里**不能**再写 `segmentUnavailable(s, key) ? null : s[key]`：
  //   `segmentUnavailable` 为真时 `s[key]` 本来就是 null，两个分支返同一个值
  //   ⇒ 那个三元是恒真的无用复杂度（第六十七批变异 #2 实测：改掉它仍全绿）。
  // `DataLifecycleStats[key]` 的类型本身就是 `DataSegment | null`，
  // 模板里 `v-if="segment(...)"` 靠 null 的 falsy 直接分流。
  return s[key]
}

function segmentLabel(key: SegmentKey): string {
  return t(`dataFlow.segment.${key}`)
}

/** ★★ `days` 是后端写死的标注值（7/23/60/999），不是区间上界。 */
function segmentDaysText(key: SegmentKey): string {
  return t('dataFlow.segmentDays', { days: SEGMENT_DAYS[key] })
}

/**
 * ★★ `percent_of_total` 是 0-100，**不是** 0-1；
 * 且 `total_rows === 0` 时四个都留 0，与「真的是 0%」不可分。
 */
function segmentPercentText(s: DataLifecycleStats, key: SegmentKey): string {
  if (segmentUnavailable(s, key)) return UNAVAILABLE
  if (percentMeaningless(s)) return t('dataFlow.percentMeaningless')
  return fmtNum((s[key] as DataSegment).percent_of_total, 1) + '%'
}

/** ★★ `size_bytes` 是按行数摊派的估算（含索引），不是实测。 */
function segmentSizeText(s: DataLifecycleStats, key: SegmentKey): string {
  if (segmentUnavailable(s, key)) return UNAVAILABLE
  return s[key]!.size_human
}

/* ── (5) growth_trend：新的一天在前 ⇒ 列表要反转 ──────────────────── */

const trendAscending = computed(() =>
  [...(stats.value?.growth_trend ?? [])].reverse(),
)
const statsTruncated = computed(() => (stats.value ? growthTrendTruncated(stats.value) : false))

/** ★ `compression_rate` 被后端夹到 100，命中时说明当日 bodies 与主表口径错位。 */
function rateText(d: DailyGrowth): string {
  if (compressionRateSaturated(d)) return t('dataFlow.stats.rateSaturated')
  if (d.requests === 0) return t('dataFlow.stats.rateNoSamples')
  return fmtNum(d.compression_rate, 1) + '%'
}

const tenantsTruncated = computed(() => (stats.value ? byTenantTruncated(stats.value) : false))
const statsListAmbiguous = computed(() => (stats.value ? listEmptyIsAmbiguous(stats.value) : false))

/* ── (1)(8) metrics：全表口径 + 清理时间不提供 ─────────────────────── */

const metricsDisagree = computed(() => (metrics.value ? metricsRowsDisagree(metrics.value) : false))
/** ★★ 恒为真（后端从不对这两个字段赋值），UI 据此说「本端点不提供」。 */
const cleanupNotReported = computed(() => (metrics.value ? metricsCleanupTimeAbsent(metrics.value) : false))
const archiveNotReported = computed(() => (metrics.value ? metricsArchiveTimeAbsent(metrics.value) : false))

/* ── (6) jobs：不给条数选择器 ─────────────────────────────────────── */

const runningJobs = computed<JobRun[]>(() => jobs.value?.running ?? [])
const historyJobs = computed<JobRun[]>(() => jobs.value?.history ?? [])
const failedCount = computed(() => historyJobs.value.filter(jobFailed).length)

function jobState(item: JobRun): 'running' | 'failed' | 'ok' {
  if (jobRunning(item)) return 'running'
  if (jobFailed(item)) return 'failed'
  return 'ok'
}

function jobStateText(item: JobRun): string {
  const s = jobState(item)
  if (s === 'running') return t('dataFlow.jobs.jobRunning')
  if (s === 'failed') return item.error || t('dataFlow.jobs.jobFailed')
  return t('dataFlow.jobs.jobDone')
}

/** ★ 进度分母 <=0 时 percent 无意义（不是「完成 0%」）。 */
function jobProgressText(item: JobRun): string {
  if (jobProgressMeaningless(item)) return t('dataFlow.jobs.progressUnknown')
  const p = item.progress!
  return t('dataFlow.jobs.progressText', { done: fmtInt(p.done), total: fmtInt(p.total) })
}

function jobFinishedText(item: JobRun): string {
  return item.finished_at ?? t('dataFlow.jobs.jobRunning')
}

/* ── (7) blobs：total 是 N 行合计 ─────────────────────────────────── */

const blobRows = computed(() => blobs.value?.rows ?? [])
const blobsTruncated = computed(() => (blobs.value ? blobTopLikelyTruncated(blobs.value, blobLimit.value) : false))

/** ★★ 后端合计 = 本次返回这几行之和，措辞必须带上 N。 */
const blobTotalText = computed(() => {
  const b = blobs.value
  if (!b) return UNAVAILABLE
  return t('dataFlow.blobs.blobTotalPartial', { n: b.rows.length, size: b.total_human })
})

function blobTimeText(row: { occurred_at: string }): string {
  return row.occurred_at
}

/** ★ 无会话/无租户时后端给的是**空串**（COALESCE 兜底），不是 null。 */
function blobSessionText(row: { session_key: string }): string {
  return row.session_key === '' ? t('dataFlow.blobs.noSession') : row.session_key
}

function blobTenantText(row: { tenant_id: string }): string {
  return row.tenant_id === '' ? t('dataFlow.blobs.noTenant') : row.tenant_id
}

/** ★ `model` 带 omitempty ⇒ 键不存在即「未知模型」。 */
function blobModelText(row: Parameters<typeof blobModelUnknown>[0]): string {
  return blobModelUnknown(row) ? t('dataFlow.blobs.modelUnknown') : row.model!
}

/** ★ 两个分量都为 0 ⇒ 这一行其实不是大字段（LEFT JOIN 没匹配到 bodies）。 */
function blobBodyText(row: Parameters<typeof blobRowHasNoBody>[0]): string {
  if (blobRowHasNoBody(row)) return t('dataFlow.blobs.bodyNone')
  return row.total_human
}

onBeforeUnmount(() => {
  stats.value = null
  metrics.value = null
  jobs.value = null
  blobs.value = null
})
</script>

<template>
  <div class="view-root df">
    <!-- ══ 1. 分段统计 ═══════════════════════════════════════════════ -->
    <section class="df__section">
      <header class="df__head">
        <h2 class="df__title">{{ t('dataFlow.stats.title') }}</h2>
        <button
          type="button"
          class="df__load"
          :disabled="loading === 'stats'"
          @click="load('stats')"
        >
          {{ loaded.stats && !error.stats ? t('dataFlow.reload') : t('dataFlow.load') }}
        </button>
      </header>

      <p v-if="error.stats" class="df__msg df__msg--err">{{ error.stats }}</p>
      <p v-if="loading === 'stats'" class="df__msg">{{ t('common.loading') }}</p>

      <template v-if="stats">
        <!-- ★★ total_rows 是租户口径，total_size_bytes 是全表口径 —— 两行各挂标签，不能并排 -->
        <dl class="df__kv">
          <dt>{{ t('dataFlow.stats.totalRows') }}</dt>
          <dd>
            {{ fmtInt(stats.total_rows) }}
            <span class="df__tag">{{ t('dataFlow.scope.tenant') }}</span>
          </dd>
          <dt>{{ t('dataFlow.stats.totalSize') }}</dt>
          <dd>
            {{ stats.total_size_human }}
            <!-- ★★ pg_total_relation_size 不过滤租户 -->
            <span class="df__tag df__tag--warn">{{ t('dataFlow.scope.platform') }}</span>
          </dd>
        </dl>

        <p class="df__note">
          {{ t('dataFlow.stats.mixedScope') }}
        </p>

        <!-- ★★★ 四段：null ⇒ 查不出来，不是 0 行 -->
        <h3 class="df__sub">{{ t('dataFlow.stats.segments') }}</h3>
        <p v-if="statsAnyMissing" class="df__msg df__msg--warn">
          {{ t('dataFlow.stats.segmentMissing') }}
        </p>
        <p v-if="statsPercentMeaningless" class="df__msg df__msg--warn">
          {{ t('dataFlow.percentMeaningless') }}
        </p>
        <p v-if="statsRowsDisagree" class="df__msg df__msg--warn">
          {{ t('dataFlow.stats.rowsDisagree') }}
        </p>

        <ul class="df__list">
          <li v-for="key in (['hot_data', 'warm_data', 'cold_data', 'expired_data'] as SegmentKey[])" :key="key" class="df__row">
            <span class="df__rowLabel">{{ segmentLabel(key) }}</span>
            <!-- ★ null 段走 UNAVAILABLE 分支 -->
            <template v-if="segment(stats, key)">
              <span class="df__rowMain">{{ fmtInt(segment(stats, key)!.rows) }} {{ t('dataFlow.rowsUnit') }}</span>
              <span class="df__rowMeta">{{ segmentDaysText(key) }}</span>
              <span class="df__rowMeta">{{ segmentPercentText(stats, key) }}</span>
              <!-- ★★ 摊派估算，不是实测 -->
              <span class="df__rowMeta">{{ t('dataFlow.apportioned', { size: segmentSizeText(stats, key) }) }}</span>
            </template>
            <template v-else>
              <span class="df__rowMain df__rowMain--unknown">{{ t('dataFlow.segmentUnavailable') }}</span>
            </template>
          </li>
        </ul>

        <p v-if="statsPercentTotal === null" class="df__note">
          {{ t('dataFlow.stats.percentTotalUnknown') }}
        </p>
        <p v-else class="df__note">
          {{ t('dataFlow.stats.percentTotal', { pct: fmtNum(statsPercentTotal, 1) }) }}
        </p>

        <!-- ★★ 租户分布：LIMIT 10，且 size 是摊派估算 -->
        <h3 class="df__sub">{{ t('dataFlow.stats.byTenant') }}</h3>
        <p v-if="tenantsTruncated" class="df__msg df__msg--warn">
          {{ t('dataFlow.stats.tenantTruncated') }}
        </p>
        <!-- ★★★ [] 可能是查询失败，措辞不能说「无数据」 -->
        <p v-if="stats.by_tenant.length === 0" class="df__msg">
          {{ statsListAmbiguous ? t('dataFlow.emptyAmbiguous') : t('dataFlow.emptyNoRecords') }}
        </p>
        <ul v-else class="df__list">
          <li v-for="(row, i) in stats.by_tenant" :key="i" class="df__row">
            <span class="df__rowLabel">{{ row.tenant_id }}</span>
            <span class="df__rowMain">{{ fmtInt(row.rows) }} {{ t('dataFlow.rowsUnit') }}</span>
            <span class="df__rowMeta">{{ t('dataFlow.apportioned', { size: row.size_human }) }}</span>
          </li>
        </ul>

        <!-- ★★★ 趋势：后端新的一天在前，这里反转成时间正序 -->
        <h3 class="df__sub">{{ t('dataFlow.stats.growth') }}</h3>
        <p v-if="statsTruncated" class="df__msg df__msg--warn">
          {{ t('dataFlow.stats.growthTruncated') }}
        </p>
        <p v-if="stats.growth_trend.length === 0" class="df__msg">
          {{ statsListAmbiguous ? t('dataFlow.emptyAmbiguous') : t('dataFlow.emptyNoRecords') }}
        </p>
        <ul v-else class="df__list">
          <li v-for="d in trendAscending" :key="d.date" class="df__row">
            <span class="df__rowLabel">{{ d.date }}</span>
            <span class="df__rowMain">{{ fmtInt(d.requests) }} {{ t('dataFlow.requestsUnit') }}</span>
            <span class="df__rowMeta">{{ rateText(d) }}</span>
          </li>
        </ul>
        <p class="df__note">{{ t('dataFlow.stats.growthOrderNote') }}</p>
      </template>
    </section>

    <!-- ══ 2. 轻量指标（全表口径） ════════════════════════════════════ -->
    <section class="df__section">
      <header class="df__head">
        <h2 class="df__title">{{ t('dataFlow.metrics.title') }}</h2>
        <button
          type="button"
          class="df__load"
          :disabled="loading === 'metrics'"
          @click="load('metrics')"
        >
          {{ loaded.metrics && !error.metrics ? t('dataFlow.reload') : t('dataFlow.load') }}
        </button>
      </header>

      <p v-if="error.metrics" class="df__msg df__msg--err">{{ error.metrics }}</p>
      <p v-if="loading === 'metrics'" class="df__msg">{{ t('common.loading') }}</p>

      <template v-if="metrics">
        <!-- ★★★★ 必须挂横幅：这条端点 SQL 无 WHERE，任何角色拿到的都是整表 -->
        <p class="df__banner">{{ t('dataFlow.metrics.platformScope') }}</p>

        <dl class="df__kv">
          <dt>{{ t('dataFlow.metrics.totalRows') }}</dt>
          <dd>{{ fmtInt(metrics.total_rows) }}</dd>
          <dt>{{ t('dataFlow.stats.totalSize') }}</dt>
          <dd>{{ fmtInt(metrics.total_size_bytes) }} {{ t('dataFlow.bytesUnit') }}</dd>
        </dl>

        <p v-if="metricsDisagree" class="df__msg df__msg--warn">
          {{ t('dataFlow.metrics.rowsDisagree') }}
        </p>

        <h3 class="df__sub">{{ t('dataFlow.metrics.segments') }}</h3>
        <ul class="df__list">
          <li v-for="key in (['hot_data', 'warm_data', 'cold_data', 'expired_data'] as SegmentKey[])" :key="key" class="df__row">
            <span class="df__rowLabel">{{ segmentLabel(key) }}</span>
            <span class="df__rowMain">{{ fmtInt(metrics[`${key}_rows` as keyof DataLifecycleMetrics] as number) }} {{ t('dataFlow.rowsUnit') }}</span>
            <span class="df__rowMeta">{{ fmtInt(metrics[`${key}_size_bytes` as keyof DataLifecycleMetrics] as number) }} {{ t('dataFlow.bytesUnit') }}</span>
          </li>
        </ul>

        <!-- ★★★★ 清理/归档时间：后端从不赋值 ⇒ 只能说「本端点不提供」 -->
        <h3 class="df__sub">{{ t('dataFlow.metrics.maintenance') }}</h3>
        <dl class="df__kv">
          <dt>{{ t('dataFlow.metrics.lastCleanup') }}</dt>
          <dd v-if="cleanupNotReported">{{ t('dataFlow.metrics.notReported') }}</dd>
          <dd v-else>{{ metrics.last_cleanup_at }}</dd>
          <dt>{{ t('dataFlow.metrics.lastArchive') }}</dt>
          <dd v-if="archiveNotReported">{{ t('dataFlow.metrics.notReported') }}</dd>
          <dd v-else>{{ metrics.last_archive_at }}</dd>
        </dl>
        <p class="df__note">{{ t('dataFlow.metrics.notReportedNote') }}</p>
      </template>
    </section>

    <!-- ══ 3. 异步任务 ═══════════════════════════════════════════════ -->
    <section class="df__section">
      <header class="df__head">
        <h2 class="df__title">{{ t('dataFlow.jobs.title') }}</h2>
        <button
          type="button"
          class="df__load"
          :disabled="loading === 'jobs'"
          @click="load('jobs')"
        >
          {{ loaded.jobs && !error.jobs ? t('dataFlow.reload') : t('dataFlow.load') }}
        </button>
      </header>

      <p v-if="error.jobs" class="df__msg df__msg--err">{{ error.jobs }}</p>
      <p v-if="loading === 'jobs'" class="df__msg">{{ t('common.loading') }}</p>

      <!-- ★★ 后端硬编码 50，所以本页不给条数选择器 -->
      <p class="df__note">{{ t('dataFlow.jobs.limitNote', { limit: LIFECYCLE_JOBS_HISTORY_LIMIT }) }}</p>

      <template v-if="jobs">
        <h3 class="df__sub">{{ t('dataFlow.jobs.running') }} ({{ runningJobs.length }})</h3>
        <p v-if="runningJobs.length === 0" class="df__msg">{{ t('dataFlow.jobs.noneRunning') }}</p>
        <ul v-else class="df__list">
          <li v-for="item in runningJobs" :key="item.run_id" class="df__row df__row--running">
            <span class="df__rowLabel">{{ item.op }}</span>
            <span class="df__rowMain">{{ jobStateText(item) }}</span>
            <span class="df__rowMeta">{{ jobProgressText(item) }}</span>
          </li>
        </ul>

        <h3 class="df__sub">
          {{ t('dataFlow.jobs.history') }} ({{ historyJobs.length }})
          <span v-if="failedCount > 0" class="df__tag df__tag--warn">{{ t('dataFlow.jobs.failedCount', { n: failedCount }) }}</span>
        </h3>
        <p v-if="historyJobs.length === 0" class="df__msg">{{ t('dataFlow.jobs.noneHistory') }}</p>
        <ul v-else class="df__list">
          <li v-for="item in historyJobs" :key="item.run_id" class="df__row">
            <span class="df__rowLabel">{{ item.op }}</span>
            <span class="df__rowMain">{{ jobStateText(item) }}</span>
            <span class="df__rowMeta">{{ jobFinishedText(item) }}</span>
            <span class="df__rowMeta">{{ fmtInt(item.duration_ms) }} {{ t('dataFlow.msUnit') }}</span>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 4. 大字段 Top-N ══════════════════════════════════════════ -->
    <section class="df__section">
      <header class="df__head">
        <h2 class="df__title">{{ t('dataFlow.blobs.title') }}</h2>
        <button
          type="button"
          class="df__load"
          :disabled="loading === 'blobs'"
          @click="reloadBlobs"
        >
          {{ loaded.blobs && !error.blobs ? t('dataFlow.reload') : t('dataFlow.load') }}
        </button>
      </header>

      <p v-if="error.blobs" class="df__msg df__msg--err">{{ error.blobs }}</p>
      <p v-if="loading === 'blobs'" class="df__msg">{{ t('common.loading') }}</p>

      <!-- 后端 limit 只在 (0,200] 生效，否则静默回落 20 ⇒ 这里只给合法值 -->
      <label class="df__field">
        <span class="df__fieldLabel">{{ t('dataFlow.blobs.limitLabel') }}</span>
        <select v-model.number="blobLimit" class="df__select" @change="reloadBlobs">
          <option v-for="n in [20, 50, 100, BLOB_TOP_LIMIT_MAX]" :key="n" :value="n">{{ n }}</option>
        </select>
      </label>
      <p class="df__note">
        {{ t('dataFlow.blobs.limitNote', { n: blobLimitEcho, max: BLOB_TOP_LIMIT_MAX }) }}
      </p>

      <template v-if="blobs">
        <!-- ★★★ total_bytes 是这 N 行之和，不是全表 -->
        <p class="df__banner">{{ blobTotalText }}</p>
        <p v-if="blobsTruncated" class="df__msg df__msg--warn">
          {{ t('dataFlow.blobs.truncated') }}
        </p>

        <p v-if="blobRows.length === 0" class="df__msg">{{ t('dataFlow.blobs.none') }}</p>
        <ul v-else class="df__list">
          <li v-for="row in blobRows" :key="row.request_id" class="df__row">
            <span class="df__rowLabel">{{ row.request_id }}</span>
            <span class="df__rowMain">{{ blobBodyText(row) }}</span>
            <span class="df__rowMeta">{{ blobModelText(row) }}</span>
            <span class="df__rowMeta">{{ blobSessionText(row) }}</span>
            <span class="df__rowMeta">{{ blobTenantText(row) }}</span>
            <span class="df__rowMeta">{{ blobTimeText(row) }}</span>
          </li>
        </ul>
      </template>
    </section>
  </div>
</template>

<style scoped>
.df__section {
  margin-bottom: 20px;
  padding: 12px;
  border: 1px solid var(--df-line, #e3e6ea);
  border-radius: 10px;
}

.df__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.df__title {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
}

.df__load {
  /* R1：新增控件 ≥48px */
  min-height: 48px;
  min-width: 48px;
  padding: 0 14px;
  font-size: 14px;
}

.df__sub {
  font-size: 13px;
  font-weight: 600;
  margin: 12px 0 4px;
}

.df__kv {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 4px 12px;
  margin: 8px 0;
  font-size: 13px;
}

.df__kv dt {
  color: var(--app-text-secondary);
}

.df__kv dd {
  margin: 0;
  text-align: right;
}

.df__list {
  list-style: none;
  margin: 4px 0 0;
  padding: 0;
}

.df__row {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 10px;
  padding: 8px 0;
  border-top: 1px solid var(--df-line, #e3e6ea);
  font-size: 13px;
}

.df__rowLabel {
  font-weight: 600;
  min-width: 88px;
}

.df__rowMain {
  font-variant-numeric: tabular-nums;
}

.df__rowMain--unknown {
  color: #9aa0a6;
  font-style: italic;
}

.df__rowMeta {
  color: var(--app-text-secondary);
  font-size: 12px;
}

.df__tag {
  display: inline-block;
  margin-left: 6px;
  padding: 0 6px;
  border: 1px solid var(--df-line, #e3e6ea);
  border-radius: 4px;
  font-size: 11px;
  color: var(--app-text-secondary);
}

.df__tag--warn {
  border-color: #d97706;
  color: #b45309;
}

.df__msg {
  margin: 6px 0;
  font-size: 13px;
  color: var(--app-text-secondary);
}

.df__msg--warn {
  color: #b45309;
}

.df__msg--err {
  color: #b91c1c;
}

.df__note {
  margin: 6px 0;
  font-size: 12px;
  color: var(--app-text-secondary);
}

.df__banner {
  margin: 8px 0;
  padding: 8px 10px;
  border-left: 3px solid #d97706;
  background: #fff7ed;
  font-size: 12px;
  color: #9a3412;
}

.df__field {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 8px 0;
  font-size: 13px;
}

.df__fieldLabel {
  color: var(--app-text-secondary);
}

/* R1：新增控件 ≥48px */
.df__select {
  min-height: 48px;
  min-width: 96px;
  font-size: 14px;
}
</style>