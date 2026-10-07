<script setup lang="ts">
// CompressionView — 压缩可观测（/compression，**admin 档**）。
//
// 数据源（两条，均 admin 档，api/compression.ts 文件头）：
//   GET /api/admin/compression/stats     窗口内的压缩率 / 策略分布 / token 估算 / 序列
//   GET /api/admin/compression/sessions  被压过的会话（分页）
//
// ## 与既有页面的分工（按「答什么」划界）
//   /data-flow       答「记录怎么分布」（逐日冷热、增长趋势）
//   本页             答「压缩策略实际压了多少、被压成什么样」
//   ⚠️ 两处的压缩率**单位相反**（本页 0-1 比例 / data-flow 0-100 百分数）
//     ⇒ 不可并列成同一组对比。
//
// ## ⚠️ 权限档位
// admin/handler.go:954/955 两条注册都是 `admin(...)`
// ⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
//
// ## ★★★★★ 本页必须钉住的九件事（全部来自 §11.104 的后端取证）
//
// (1) ★★★★ **`count` 是真实总数**（`COUNT(DISTINCT gw_session_id)`）
//      ⇒ 可以据此翻页。
//      ★ 但空 `gw_session_id` 的行被 `if item.GwSessionID != ""` 静默丢弃
//      而 count 仍算它 ⇒ `count > items.length` 是**可预期**的，不是 bug。
//
// (2) ★★★★ **`count: 0` 是二义的**：count 查询失败时后端返的是
//      **200 + `{items:[], count:0}`**（compression_sessions.go:108-112），
//      与「真的没有会话」完全一样 ⇒ 措辞只能说「没有可展示的记录」。
//
// (3) ★★★ **`compression_rate` 是 0-1 比例**（:187 除法无 ×100）
//      ⇒ 展示时乘 100 并标「比例」。分母 `total_requests === 0` 时那个 0 **无意义**。
//
// (4) ★★★ **`hours` 只在 from/to 都缺省时生效**（:89-109）
//      ⇒ 本页用「近 N 小时 / 自定义区间」二选一，
//      **选了自定义区间就不再显示 hours 控件**，并在界面上说明。
//
// (5) ★★★ **`hourly_series` 的粒度随时间窗变化**（:257-266）：
//      ≤48h 每小时 / ≤168h 每 6 小时 / 更长每天 ⇒ 不能写「按小时」。
//      ★ 桶查询失败时也是空数组 ⇒ 空序列**不能**说成「没有流量」。
//
// (6) ★★★ **三个 token 估算字段缺键时统一说「未给出估算」**：
//      `estimated_tokens_saved` 只在 `orig > outbound` 时才设指针（:204-206）
//      ⇒ 「节省为 0 / 为负 / 估算查询静默失败」**三种情况都是键缺失**
//      ⇒ **绝不能**把缺失说成「没节省」或「为 0」。
//
// (7) ★★ `msg_reduction` 为 null ⇒ 显示「未知」；
//      `= 0` 且 `outbound > orig` ⇒ 差值为负被后端夹到 0（:185-187）
//      ⇒ 要提示「压缩后消息数没减少（后端按 0 记）」。
//
// (8) ★★ `compression_strategy` 与 `sample_request_id` 都是
//      `MAX(...)` 的**字典序最大值**（:129/:135）
//      ⇒ 不是「这个会话的策略」也不是「最近一次请求」，必须标口径。
//
// (9) ★★★ **tenant_admin 只看成功请求**（`$3 OR rl.success`，$3 = !tenantFilter）
//      ⇒ 页面上标明本账号口径，否则会与超管的数字对不上。
//
// 另：`compressed_total` 是**组级口径**（组内有一行有 outbound_body 就整组算），
// 分子不是「被压缩的行数」；`default` 租户的 tenant_admin **不被隔离**。

import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import { fmtInt, fmtNum } from '@/utils/format'
import {
  fetchCompressionStats,
  fetchCompressionSessions,
  hoursEffective,
  seriesGranularityOf,
  compressionRateMeaningless,
  strategyCountsDisagree,
  tokenBandCount,
  outboundTokensMissing,
  estimatedOrigMissing,
  tokensSavedMissing,
  summaryModeRowsMissing,
  hourlySeriesMayBeFailed,
  bucketCountsDisagree,
  sessionsEmptyIsAmbiguous,
  countExceedsListable,
  sessionsHasNextPage,
  sessionsPageEffective,
  sessionsPageSizeEffective,
  sessionReductionUnknown,
  sessionOutboundMsgUnknown,
  sessionReductionClamped,
  HOURS_DEFAULT,
  HOURS_MIN,
  HOURS_MAX,
  SESSIONS_PAGE_SIZE_DEFAULT,
  SESSIONS_PAGE_SIZE_MAX,
  type CompressionStatsResponse,
  type CompressionSessionsResponse,
  type CompressionSessionItem,
  type TokenBand,
} from '@/api/compression'

useHyperPage({ title: () => t('compressionView.title') })

const UNAVAILABLE = '—'

type SectionKey = 'stats' | 'sessions'

const stats = ref<CompressionStatsResponse | null>(null)
const sessions = ref<CompressionSessionsResponse | null>(null)

const loading = ref<SectionKey | null>(null)
const loaded = ref<Record<SectionKey, boolean>>({ stats: false, sessions: false })
const error = ref<Record<SectionKey, string | null>>({ stats: null, sessions: null })

/* ── 时间窗：二选一，与后端的两种口径一一对应 ────────────────────── */

/** ★ 选「自定义区间」时 hours 被后端完全忽略（:89-109），所以控件要消失。 */
const windowMode = ref<'hours' | 'explicit'>('hours')
const hours = ref(HOURS_DEFAULT)
const fromLocal = ref('')
const toLocal = ref('')

const mode = computed(() => (windowMode.value === 'explicit' ? 'explicit' : 'hours'))
const hoursEcho = computed(() => hoursEffective(hours.value))
/** 序列真实粒度：由 hours 口径下的时间跨度推出。 */
const granularity = computed(() =>
  seriesGranularityOf(mode.value === 'hours' ? hoursEcho.value : 168),
)

/** `datetime-local` 的本地值 ⇒ 后端要的 RFC3339（带 Z）。 */
function toRfc3339(local: string): string {
  const d = new Date(local)
  return Number.isNaN(d.getTime()) ? '' : d.toISOString()
}

function windowQuery() {
  if (mode.value === 'explicit') {
    const from = toRfc3339(fromLocal.value)
    const to = toRfc3339(toLocal.value)
    return { from: from || undefined, to: to || undefined }
  }
  return { hours: hoursEcho.value }
}

function describeError(err: unknown): string {
  const statusCode = (err as { status?: number })?.status
  if (statusCode === 403) return t('compressionView.errForbidden')
  // 后端对 from/to 格式错返 400
  if (statusCode === 400) return t('compressionView.errBadTime')
  return (err instanceof Error ? err.message : String(err)) || t('common.error')
}

async function load(section: SectionKey): Promise<void> {
  loading.value = section
  error.value[section] = null
  try {
    if (section === 'stats') {
      stats.value = await fetchCompressionStats(windowQuery())
    } else {
      sessions.value = await fetchCompressionSessions({ ...windowQuery(), page: page.value })
    }
  } catch (err) {
    if (section === 'stats') stats.value = null
    if (section === 'sessions') sessions.value = null
    error.value[section] = describeError(err)
  } finally {
    loading.value = null
    loaded.value[section] = true
  }
}

function reloadAll(): void {
  void load('stats')
  void load('sessions')
}

function switchWindow(next: 'hours' | 'explicit'): void {
  windowMode.value = next
  // ★ 口径变了就重新拉，否则界面上挂的是上一套时间窗的数字
  if (loaded.value.stats || loaded.value.sessions) reloadAll()
}

/* ── ①③⑤⑥⑨ stats ─────────────────────────────────────────────────── */

const rateMeaningless = computed(() => (stats.value ? compressionRateMeaningless(stats.value) : false))
const distDisagree = computed(() => (stats.value ? strategyCountsDisagree(stats.value) : false))
const bucketsDisagree = computed(() => (stats.value ? bucketCountsDisagree(stats.value) : false))
const seriesMayBeFailed = computed(() => (stats.value ? hourlySeriesMayBeFailed(stats.value) : false))

/** ★ 0-1 比例 ⇒ 展示乘 100，并标「比例」而非「百分数字段」。 */
function rateText(ratio: number): string {
  return fmtNum(ratio * 100, 1) + '%'
}

function rateCell(m: CompressionStatsResponse): string {
  if (rateMeaningless.value) return t('compressionView.rateMeaningless')
  return rateText(m.compression_rate)
}

function strategyEntries(m: CompressionStatsResponse): Array<[string, number]> {
  return Object.entries(m.strategy_distribution)
}

/** ★ `'none'` 是 `COALESCE(NULLIF(compression_strategy,''),'none')` 归并出来的。 */
function strategyName(key: string): string {
  if (key === 'none') return t('compressionView.strategy.none')
  return key
}

const BANDS: readonly TokenBand[] = ['below', 'preliminary', 'forced']

/** ★★ 缺键 ⇒ null（缺键 ≠ 0 条），UI 只能说「这一档没有出现」。 */
function bandText(m: CompressionStatsResponse, band: TokenBand): string {
  const n = tokenBandCount(m, band)
  if (n === null) return t('compressionView.bandMissing')
  return fmtInt(n)
}

/** ★★ 三个估算字段统一措辞：缺键一律「未给出估算」。 */
const estimatedOrigText = computed(() => {
  const m = stats.value
  if (!m) return UNAVAILABLE
  return estimatedOrigMissing(m) ? t('compressionView.notEstimated') : fmtInt(m.estimated_original_tokens!)
})

const outboundTokensText = computed(() => {
  const m = stats.value
  if (!m) return UNAVAILABLE
  return outboundTokensMissing(m) ? t('compressionView.notEstimated') : fmtInt(m.total_outbound_tokens!)
})

const savedText = computed(() => {
  const m = stats.value
  if (!m) return UNAVAILABLE
  // ★ 缺键绝不能写成「没节省」：负值与查询失败都落到这里
  return tokensSavedMissing(m) ? t('compressionView.savedNotGiven') : fmtInt(m.estimated_tokens_saved!)
})

const summaryModeText = computed(() => {
  const m = stats.value
  if (!m) return UNAVAILABLE
  return summaryModeRowsMissing(m) ? t('compressionView.notEstimated') : fmtInt(m.summary_mode_rows!)
})

/* ── ①②⑦⑧ sessions ──────────────────────────────────────────────── */

const page = ref(1)
const pageSize = ref(SESSIONS_PAGE_SIZE_DEFAULT)
const pageEcho = computed(() => sessionsPageEffective(page.value))
const pageSizeEcho = computed(() => sessionsPageSizeEffective(pageSize.value))

const sessionRows = computed<CompressionSessionItem[]>(() => sessions.value?.items ?? [])
const hasNext = computed(() =>
  sessions.value ? sessionsHasNextPage(sessions.value, pageSize.value) : false,
)
const emptyAmbiguous = computed(() => (sessions.value ? sessionsEmptyIsAmbiguous(sessions.value) : false))
const countGap = computed(() => (sessions.value ? countExceedsListable(sessions.value) : false))

function gotoPage(delta: number): void {
  page.value = Math.max(1, page.value + delta)
  void load('sessions')
}

/** ★ null ⇒ 未知（缺原始估算或缺 outbound 计数），不是 0。 */
function reductionText(item: CompressionSessionItem): string {
  if (sessionReductionUnknown(item)) return t('compressionView.sessions.reductionUnknown')
  return fmtInt(item.msg_reduction!)
}

/** ★★ 差值为负被夹到 0 ⇒ 必须说出来，否则「0」会被读成「刚好持平」。 */
function reductionHint(item: CompressionSessionItem): string {
  return sessionReductionClamped(item) ? t('compressionView.sessions.reductionClamped') : ''
}

function outboundMsgText(item: CompressionSessionItem): string {
  return sessionOutboundMsgUnknown(item) ? UNAVAILABLE : fmtInt(item.outbound_msg_count!)
}

onBeforeUnmount(() => {
  stats.value = null
  sessions.value = null
})
</script>

<template>
  <div class="view-root cv">
    <!-- ══ 时间窗：与后端的两种口径一一对应 ══════════════════════════ -->
    <section class="cv__section">
      <h2 class="cv__title">{{ t('compressionView.window.title') }}</h2>

      <div class="cv__seg" role="group">
        <button
          type="button"
          class="cv__segBtn"
          :class="{ 'cv__segBtn--on': windowMode === 'hours' }"
          @click="switchWindow('hours')"
        >
          {{ t('compressionView.window.byHours') }}
        </button>
        <button
          type="button"
          class="cv__segBtn"
          :class="{ 'cv__segBtn--on': windowMode === 'explicit' }"
          @click="switchWindow('explicit')"
        >
          {{ t('compressionView.window.byRange') }}
        </button>
      </div>

      <!-- ★ hours 口径：控件只在后端真的用它时才出现 -->
      <template v-if="windowMode === 'hours'">
        <label class="cv__field">
          <span class="cv__fieldLabel">{{ t('compressionView.window.hours') }}</span>
          <select v-model.number="hours" class="cv__select" @change="reloadAll">
            <option v-for="n in [24, 48, 168, HOURS_MAX]" :key="n" :value="n">
              {{ t('compressionView.window.hoursN', { n }) }}
            </option>
          </select>
        </label>
        <p class="cv__note">{{ t('compressionView.window.clampNote', { min: HOURS_MIN, max: HOURS_MAX }) }}</p>
      </template>

      <!-- ★ explicit 口径：hours 控件整块消失，避免误导 -->
      <template v-else>
        <label class="cv__field">
          <span class="cv__fieldLabel">{{ t('compressionView.window.from') }}</span>
          <input v-model="fromLocal" type="datetime-local" class="cv__input" @change="reloadAll" />
        </label>
        <label class="cv__field">
          <span class="cv__fieldLabel">{{ t('compressionView.window.to') }}</span>
          <input v-model="toLocal" type="datetime-local" class="cv__input" @change="reloadAll" />
        </label>
        <p class="cv__note">{{ t('compressionView.window.ignoreHoursNote') }}</p>
      </template>

      <!-- ★★ tenant_admin 只看成功请求 -->
      <p class="cv__banner">{{ t('compressionView.successOnlyNote') }}</p>
    </section>

    <!-- ══ 1. 压缩统计 ═════════════════════════════════════════════ -->
    <section class="cv__section">
      <header class="cv__head">
        <h2 class="cv__title">{{ t('compressionView.stats.title') }}</h2>
        <button type="button" class="cv__load" :disabled="loading === 'stats'" @click="load('stats')">
          {{ loaded.stats && !error.stats ? t('compressionView.reload') : t('compressionView.load') }}
        </button>
      </header>

      <p v-if="error.stats" class="cv__msg cv__msg--err">{{ error.stats }}</p>
      <p v-if="loading === 'stats'" class="cv__msg">{{ t('common.loading') }}</p>

      <template v-if="stats">
        <dl class="cv__kv">
          <dt>{{ t('compressionView.stats.totalRequests') }}</dt>
          <dd>{{ fmtInt(stats.total_requests) }}</dd>
          <dt>{{ t('compressionView.stats.compressedTotal') }}</dt>
          <dd>
            {{ fmtInt(stats.compressed_total) }}
            <span class="cv__tag cv__tag--warn">{{ t('compressionView.stats.groupLevel') }}</span>
          </dd>
          <dt>{{ t('compressionView.stats.rate') }}</dt>
          <dd>{{ rateCell(stats) }}</dd>
        </dl>

        <p class="cv__note">{{ t('compressionView.ratioNote') }}</p>
        <p v-if="rateMeaningless" class="cv__msg cv__msg--warn">{{ t('compressionView.rateMeaninglessNote') }}</p>

        <h3 class="cv__sub">{{ t('compressionView.stats.strategies') }}</h3>
        <p v-if="distDisagree" class="cv__msg cv__msg--warn">{{ t('compressionView.stats.distDisagree') }}</p>
        <p v-if="strategyEntries(stats).length === 0" class="cv__msg">{{ t('compressionView.stats.noStrategy') }}</p>
        <ul v-else class="cv__list">
          <li v-for="[key, n] in strategyEntries(stats)" :key="key" class="cv__row">
            <span class="cv__rowLabel">{{ strategyName(key) }}</span>
            <span class="cv__rowMain">{{ fmtInt(n) }}</span>
          </li>
        </ul>

        <h3 class="cv__sub">{{ t('compressionView.stats.bands') }}</h3>
        <ul class="cv__list">
          <li v-for="band in BANDS" :key="band" class="cv__row">
            <span class="cv__rowLabel">{{ t(`compressionView.band.${band}`) }}</span>
            <span class="cv__rowMain">{{ bandText(stats, band) }}</span>
          </li>
        </ul>

        <h3 class="cv__sub">{{ t('compressionView.stats.tokens') }}</h3>
        <dl class="cv__kv">
          <dt>{{ t('compressionView.stats.outboundTokens') }}</dt>
          <dd>{{ outboundTokensText }}</dd>
          <dt>{{ t('compressionView.stats.estimatedOrig') }}</dt>
          <dd>{{ estimatedOrigText }}</dd>
          <dt>{{ t('compressionView.stats.saved') }}</dt>
          <dd>{{ savedText }}</dd>
          <dt>{{ t('compressionView.stats.summaryMode') }}</dt>
          <dd>{{ summaryModeText }}</dd>
        </dl>
        <p class="cv__note">{{ t('compressionView.stats.estimateNote') }}</p>

        <!-- ★⑤ 粒度随时间窗变，不能写「按小时」 -->
        <h3 class="cv__sub">{{ t('compressionView.stats.series', { g: t(`compressionView.granularity.${granularity}`) }) }}</h3>
        <p v-if="bucketsDisagree" class="cv__msg cv__msg--warn">{{ t('compressionView.stats.bucketDisagree') }}</p>
        <p v-if="stats.hourly_series.length === 0" class="cv__msg">
          {{ seriesMayBeFailed ? t('compressionView.seriesMayBeFailed') : t('compressionView.seriesEmpty') }}
        </p>
        <ul v-else class="cv__list">
          <li v-for="b in stats.hourly_series" :key="b.hour" class="cv__row">
            <span class="cv__rowLabel">{{ b.hour }}</span>
            <span class="cv__rowMain">{{ fmtInt(b.total) }}</span>
            <span class="cv__rowMeta">{{ t('compressionView.stats.seriesRate', { rate: rateText(b.rate) }) }}</span>
          </li>
        </ul>
      </template>
    </section>

    <!-- ══ 2. 压缩会话 ═════════════════════════════════════════════ -->
    <section class="cv__section">
      <header class="cv__head">
        <h2 class="cv__title">{{ t('compressionView.sessions.title') }}</h2>
        <button type="button" class="cv__load" :disabled="loading === 'sessions'" @click="load('sessions')">
          {{ loaded.sessions && !error.sessions ? t('compressionView.reload') : t('compressionView.load') }}
        </button>
      </header>

      <p v-if="error.sessions" class="cv__msg cv__msg--err">{{ error.sessions }}</p>
      <p v-if="loading === 'sessions'" class="cv__msg">{{ t('common.loading') }}</p>

      <template v-if="sessions">
        <!-- ★① count 是真实总数 -->
        <dl class="cv__kv">
          <dt>{{ t('compressionView.sessions.total') }}</dt>
          <dd>{{ fmtInt(sessions.count) }}</dd>
          <dt>{{ t('compressionView.sessions.page') }}</dt>
          <dd>{{ pageEcho }} · {{ t('compressionView.sessions.pageSizeN', { n: pageSizeEcho }) }}</dd>
        </dl>

        <!-- ★ count 与本页条数对不上是可预期的，不报成 bug -->
        <p v-if="countGap" class="cv__msg">{{ t('compressionView.sessions.countGapNote') }}</p>

        <label class="cv__field">
          <span class="cv__fieldLabel">{{ t('compressionView.sessions.pageSize') }}</span>
          <select v-model.number="pageSize" class="cv__select" @change="gotoPage(0)">
            <option v-for="n in [20, SESSIONS_PAGE_SIZE_DEFAULT, 100, SESSIONS_PAGE_SIZE_MAX]" :key="n" :value="n">{{ n }}</option>
          </select>
        </label>
        <p class="cv__note">{{ t('compressionView.sessions.pageSizeNote') }}</p>

        <!-- ★② count=0 是二义的 -->
        <p v-if="sessions.items.length === 0" class="cv__msg">
          {{ emptyAmbiguous ? t('compressionView.sessions.emptyAmbiguous') : t('compressionView.sessions.emptyNone') }}
        </p>

        <ul v-else class="cv__list">
          <li v-for="item in sessionRows" :key="item.gw_session_id" class="cv__row">
            <span class="cv__rowLabel">{{ item.gw_session_id }}</span>
            <!-- ★⑧ 策略是 MAX(字典序最大)，不是「这个会话的策略」 -->
            <span class="cv__rowMeta">{{ t('compressionView.sessions.strategyMax', { v: item.compression_strategy }) }}</span>
            <span class="cv__rowMain">{{ fmtInt(item.request_count) }} {{ t('compressionView.requestsUnit') }}</span>
            <!-- ★⑦ null ⇒ 未知；=0 且被夹 ⇒ 额外提示 -->
            <span class="cv__rowMeta">
              {{ t('compressionView.sessions.reduction', { n: reductionText(item) }) }}
              <span v-if="reductionHint(item)" class="cv__tag cv__tag--warn">{{ reductionHint(item) }}</span>
            </span>
            <span class="cv__rowMeta">
              {{ t('compressionView.sessions.msgOutbound', { n: outboundMsgText(item) }) }}
            </span>
            <!-- ★⑧ sample_request_id 也是 MAX，不是最近一次 -->
            <span class="cv__rowMeta">
              {{ t('compressionView.sessions.sampleMax', { v: item.sample_request_id }) }}
            </span>
          </li>
        </ul>

        <div class="cv__pager">
          <button type="button" class="cv__page" :disabled="pageEcho <= 1" @click="gotoPage(-1)">
            {{ t('compressionView.prev') }}
          </button>
          <button type="button" class="cv__page" :disabled="!hasNext" @click="gotoPage(1)">
            {{ t('compressionView.next') }}
          </button>
        </div>
      </template>
    </section>
  </div>
</template>

<style scoped>
.cv__section {
  margin-bottom: 20px;
  padding: 12px;
  border: 1px solid var(--cv-line, #e3e6ea);
  border-radius: 10px;
}

.cv__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.cv__title { font-size: 15px; font-weight: 600; margin: 0 0 6px; }
.cv__sub { font-size: 13px; font-weight: 600; margin: 12px 0 4px; }

/* R1：新增控件 ≥48px */
.cv__load, .cv__segBtn, .cv__page, .cv__select, .cv__input { min-height: 48px; }

.cv__load { min-width: 48px; padding: 0 14px; font-size: 14px; }

.cv__seg { display: flex; gap: 8px; margin: 8px 0; }
.cv__segBtn { padding: 0 14px; font-size: 14px; border: 1px solid var(--cv-line, #e3e6ea); border-radius: 8px; }
.cv__segBtn--on { border-color: var(--app-primary); color: var(--app-primary); font-weight: 600; }

.cv__field { display: flex; align-items: center; gap: 8px; margin: 8px 0; font-size: 13px; }
.cv__fieldLabel { color: var(--app-text-secondary); }
.cv__select { min-width: 110px; font-size: 14px; }
.cv__input { min-width: 190px; font-size: 14px; }

.cv__kv { display: grid; grid-template-columns: 1fr auto; gap: 4px 12px; margin: 8px 0; font-size: 13px; }
.cv__kv dt { color: var(--app-text-secondary); }
.cv__kv dd { margin: 0; text-align: right; }

.cv__list { list-style: none; margin: 4px 0 0; padding: 0; }
.cv__row { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; padding: 8px 0; border-top: 1px solid var(--cv-line, #e3e6ea); font-size: 13px; }
.cv__rowLabel { font-weight: 600; min-width: 96px; }
.cv__rowMain { font-variant-numeric: tabular-nums; }
.cv__rowMeta { color: var(--app-text-secondary); font-size: 12px; }

.cv__tag { display: inline-block; margin-left: 6px; padding: 0 6px; border: 1px solid var(--cv-line, #e3e6ea); border-radius: 4px; font-size: 11px; color: var(--app-text-secondary); }
.cv__tag--warn { border-color: #d97706; color: #b45309; }

.cv__msg { margin: 6px 0; font-size: 13px; color: var(--app-text-secondary); }
.cv__msg--warn { color: #b45309; }
.cv__msg--err { color: #b91c1c; }
.cv__note { margin: 6px 0; font-size: 12px; color: var(--app-text-secondary); }

.cv__banner { margin: 8px 0; padding: 8px 10px; border-left: 3px solid #d97706; background: #fff7ed; font-size: 12px; color: #9a3412; }

.cv__pager { display: flex; gap: 8px; margin-top: 10px; }
.cv__page { min-width: 72px; padding: 0 12px; font-size: 14px; }
</style>