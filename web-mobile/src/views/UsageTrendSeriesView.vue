<script setup lang="ts">
// UsageTrendSeriesView — 用量趋势线 + 可选模型清单（2026-10-10，第一百零八批）。
//
// GET /api/admin/usage/trend-series   → 折线数据（series）
// GET /api/admin/usage/trend-models   → 模型清单（下拉选项）
//
// 权限：**admin 档**（`admin/handler.go:1267` 的 `h.admin(h.HandleUsageAdmin)` 前缀子路由，
//   分派见 `admin/usage.go:60-64`）⇒ tenant_admin 可用 ⇒ 抽屉席**不设** `requiresRole`。
// ⚠️ 桌面把这条路由标成 `super: true, hideForTenant: true`
//   （`web/src/config/appNav.ts:151`）—— **前端比后端严**，按纪律以可执行注册为准。
//
// ★★★ 本族的租户口径是**本仓第四种，而且是唯一安全的那种**：
//   `statsTenantScope`（`admin/stats.go:87-97`）—— 角色不是 `super_admin` / `admin_key`
//   就强制用 `auth.TenantID` 并**忽略 `tenant_id` 查询参数**
//   ⇒ **tenant_admin 天然隔离**。
//   ★ 与批 104（dashboardBoard 三条子查询硬编码 tenant）、批 106（selfCheck 有列不接线）
//     恰好相反 —— 本族**不需要**段落级门控，抽屉席不设 requiresRole 是对的。
//   ⇒ 所以本页**不做**任何角色分支，那是多余的复杂度。
//
// ★★★★ 本页最要紧的八件事（详见 api/usageTrendSeries.ts 文件头）：
//   1. ★★★★★ **降级时 `series` 也是 `[]`** ⇒ 空数组**分不清**「零用量」与「没算出来」
//      ⇒ 唯一区分是 `degraded` 那一个布尔。**把空数组渲染成「这段时间没有用量」就是撒谎。**
//      且降级时 `bucket_minutes` / `top` 仍是解析后的值（`:204-205`）
//      ⇒ **也不能靠这两个键判降级**。
//   2. ★★★★★ **`start`/`end` 只给一个 ⇒ 后端 400**（`usage.go:1451-1453`）
//      ⇒ 页面**只发配对值**，不发半对。
//   3. ★★★★★ **`end` 有两种形态**：预设路径是 `now`（带时分秒），
//      自定义路径是**次日 UTC 零点**（左闭右开，`usage.go:1469`）
//      ⇒ 显示「用户选的结束日」必须减一天（`trendSelectedEndDay`），否则多显示一天。
//   4. ★★★★ **`days` clamp 是 [1,90]**（不是别族的 366），且 **days=7 起点是六天前零点**
//      （`todayStart.Add(-(days-1)*24h)`）⇒ 不是「往前 7×24 小时」。
//   5. ★★★★ **非法/负数 ID 静默换数据档**（`queryInt` 回落 0 ⇒ 落到 default 档）
//      ⇒ 页面**不发送**非法 ID，并显式告知用户「这个过滤不会生效」。
//   6. ★★★★ **`model` 多选要发重复参数**（`model=a&model=b`），不是逗号串；
//      超 20 个**静默截断** ⇒ 必须告知丢了几个。
//   7. ★★★★ **`trend-models` 硬编码 `LIMIT 100` 静默截断**，响应无字段可证
//      ⇒ 满 100 行只能说「可能还有更多」。
//   8. ★★★ **传了 `model` 多选 ⇒ `trend-models` 被 `= ANY` 收敛**
//      ⇒ 它是「已选模型的量」而不是「可选模型清单」，用它回填下拉会只剩已选项。
//
// ★ 与桌面的两处**刻意不同**（不是漏抄）：
//   · 桌面用 ECharts 画折线；移动端无图表依赖，且 90 天 × 60 分钟桶 = 上千个点
//     ⇒ 本页呈现**每条线的合计 + 桶数 + 首末桶时间**，不逐桶渲染（逐桶会拖垮移动端）。
//   · 桌面对 `bucket_minutes` 写 `resp.bucket_minutes || 60`（兜底恒不生效，
//     且 60 不是唯一合法档位）⇒ 本页直接显示回显值。
import { computed, onBeforeUnmount, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import { t } from '@/i18n'
import {
  fetchTrendSeries,
  fetchTrendModels,
  trendDegraded,
  trendDegradedReason,
  trendMissingView,
  trendHintLocal,
  trendHintDisagrees,
  trendSeriesIsGenuinelyEmpty,
  trendTopEchoed,
  trendWasFolded,
  trendFoldSuppressedByModelSelection,
  trendOthersLine,
  trendOthersIsLast,
  trendSeriesNonIncreasing,
  trendModelsPossiblyTruncated,
  trendModelsListIsConstrained,
  trendEndIsNextMidnight,
  trendSelectedEndDay,
  trendModelsNormalize,
  trendModelsDroppedCount,
  trendDaysEffective,
  trendCustomRange,
  TREND_DAYS_DEFAULT,
  TREND_DAYS_MAX,
  TREND_TOP_DEFAULT,
  TREND_TOP_MAX,
  TREND_MAX_MODELS,
  TREND_MODELS_SQL_LIMIT,
  TREND_OTHERS_KEY,
  type TrendSeriesResponse,
  type TrendModelsResponse,
} from '@/api/usageTrendSeries'

useHyperPage({ title: () => t('uts.title') })

// ══ 时间窗 ══════════════════════════════════════════════════════════════

type WindowMode = 'preset' | 'custom'

const mode = ref<WindowMode>('preset')
const days = ref<number>(TREND_DAYS_DEFAULT)
const startDate = ref('')
const endDate = ref('')

/** ★★★ 只给一个 ⇒ 后端 400（`usage.go:1451-1453`）⇒ 没配对就不发。 */
const customPairReady = computed(() => startDate.value !== '' && endDate.value !== '')
const customPairBroken = computed(
  () => mode.value === 'custom' && !customPairReady.value && (startDate.value !== '' || endDate.value !== ''),
)

const DAY_CHOICES = [1, 7, 30, TREND_DAYS_MAX] as const
const TOP_CHOICES = [TREND_TOP_DEFAULT, 10, TREND_TOP_MAX] as const

// ══ 过滤 ════════════════════════════════════════════════════════════════

const top = ref<number>(TREND_TOP_DEFAULT)
const providerId = ref<string>('')
const apiKeyId = ref<string>('')
const selectedModels = ref<string[]>([])

/**
 * 把输入框的字符串转成查询参数里的 ID。
 *
 * ★★★ 非正整数 / 非数字一律返回 `undefined` —— **不进查询对象**。
 *   理由与 `idWarn` 同源（`queryInt` 解析失败回落 0 ⇒ `f.providerID > 0`
 *   不成立 ⇒ 后端**静默换数据档**，响应里看不出异常）。
 *   `trendQuery` 内部也有一道防护，但那是**第二道**；
 *   本页自己先挡，是让「用户填的非法值」在视图这一层就停止，
 *   也让判据能对着查询对象本身断言（而不是只能对着 URL 断言）。
 *
 * ⚠⚠ 与 `idWarn` 是**两个不同的东西**，不能合并：
 *   本函数回答「这个值能不能进查询」（要的是过滤掉的**结果**），
 *   `idWarn` 回答「这个值为什么无效」（要的是**告诉用户为什么被过滤掉**）。
 *   ★ 第一版让告警也读 `idOf` 的结果 ⇒ **告警随之消失** ——
 *     因为「被过滤掉」和「需要提醒」是两件事，合起来两个都做不了。
 */
function idOf(v: string): number | undefined {
  const raw = v.trim()
  if (raw === '') return undefined
  const n = Number(raw)
  if (!Number.isFinite(n) || Math.trunc(n) <= 0) return undefined
  return Math.trunc(n)
}

/**
 * 原始输入 → 告警文案（空串 ⇒ 不告警）。
 *
 * 口径与 `trendFilterIdInvalid` 一致，但它收的是**数值**，
 * 而这里读的是**输入框字符串** ⇒ 字符串 `'7'` 传给它会被判成「不是数字」。
 * ⇒ 所以这里自己做一份，接受字符串，并且**空串算「没填」而不是「无效」**。
 */
function idWarn(field: 'provider_id' | 'api_key_id', raw: string): string | null {
  const s = raw.trim()
  if (s === '') return null
  const n = Number(s)
  if (!Number.isFinite(n)) return `${field} 不是数字，会被后端静默当成 0`
  if (Math.trunc(n) <= 0) return `${field} <= 0 不构成过滤条件，会被后端静默忽略`
  return null
}

/**
 * ★★★★ 非正整数 / 非数字 ID 会被后端**静默当成 0**（`queryInt` 回落），
 *   落到 default 档 —— 用户以为加了过滤，实际拿到的是另一张表。
 *   ⇒ 本页**不发**这类值（`trendQuery` 也做了同样的防护），并显式告知。
 */
const idFilterIgnored = computed(
  () => idWarn('provider_id', providerId.value) ?? idWarn('api_key_id', apiKeyId.value),
)

/** ★ 超 20 个模型被截断（后端 `:133-143` 同口径）⇒ 告知丢了几个。 */
const droppedModels = computed(() => trendModelsDroppedCount(selectedModels.value))
const effectiveModels = computed(() => trendModelsNormalize(selectedModels.value))

// ══ 取数 ════════════════════════════════════════════════════════════════

const series = ref<TrendSeriesResponse | null>(null)
const models = ref<TrendModelsResponse | null>(null)
const seriesError = ref<string | null>(null)
const modelsError = ref<string | null>(null)
const loadingSeries = ref(false)
const loadingModels = ref(false)

/** 当前这次请求用的查询参数 —— 判据要能对着它断言。 */
function utcDay(s: string): Date {
  return new Date(`${s}T00:00:00Z`)
}

/**
 * ★★ 自定义区间**直接发用户选的日期**，不发「加一天」的那个 ——
 *   `+1 天` 是**后端**解析时做的事（`usage.go:1469` 的 `endDay.Add(24*time.Hour)`），
 *   客户端补齐会让响应 `end` 与请求对不上号。
 *
 * ★ 这里用 `trendCustomRange`（后端解析的本地镜像）算的是**预期的响应 start**，
 *   用来与 `series.start` 对拍 —— 能抓到「后端把窗口换掉了」这一类漂移。
 */
function currentQuery() {
  // ★★ ID 过滤必须真的进查询串。`trendQuery` 内部对非正整数/NaN 不发
  //   （发了会被后端静默当成 0 并换数据档）⇒ 这里原样传即可，不重复做过滤。
  const ids = {
    ...(idOf(providerId.value) !== undefined ? { provider_id: idOf(providerId.value) } : {}),
    ...(idOf(apiKeyId.value) !== undefined ? { api_key_id: idOf(apiKeyId.value) } : {}),
  }
  if (mode.value === 'custom' && customPairReady.value) {
    return {
      start: startDate.value,
      end: endDate.value,
      top: top.value,
      model: selectedModels.value,
      ...ids,
    }
  }
  return {
    days: trendDaysEffective(days.value),
    top: top.value,
    model: selectedModels.value,
    ...ids,
  }
}

async function loadSeries(): Promise<void> {
  // ★★★ 自定义模式但起止日没配齐 ⇒ **直接不发**。
  //   第一版让 `currentQuery()` 自己 fallback 到 days 预设，于是：
  //   页面上一行写着「只给一个后端会 400，本页因此不发出请求」，
  //   实际却**静默换成了另一个窗口出图** —— 文案与行为直接矛盾，
  //   而且用户以为看的是自己填的区间。
  //   ⇒ fallback 只允许发生在 **preset 模式**里（那里本来就该走 days）。
  if (mode.value === 'custom' && !customPairReady.value) {
    series.value = null
    seriesError.value = null
    return
  }
  loadingSeries.value = true
  seriesError.value = null
  try {
    series.value = await fetchTrendSeries(currentQuery())
  } catch (err) {
    // ★ 失败 ⇒ 清空，绝不保留上一次的成功结果冒充本次。
    series.value = null
    seriesError.value = (err as Error)?.message || String(err)
  } finally {
    loadingSeries.value = false
  }
}

async function loadModels(): Promise<void> {
  loadingModels.value = true
  modelsError.value = null
  try {
    // ★ 注意：模型清单**不传** model 多选，否则会被 = ANY 收敛成「已选项」，
    //   拿到一个只剩已选项的列表（新选就没得选了）。
    models.value = await fetchTrendModels({ days: trendDaysEffective(days.value) })
  } catch (err) {
    models.value = null
    modelsError.value = (err as Error)?.message || String(err)
  } finally {
    loadingModels.value = false
  }
}

// ══ 降级披露 ════════════════════════════════════════════════════════════

const degraded = computed(() => (series.value ? trendDegraded(series.value) : false))
const degradedReason = computed(() => (series.value ? trendDegradedReason(series.value) : null))
const missingView = computed(() => (series.value ? trendMissingView(series.value) : null))

/** ★ 本地拼的 hint（可本地化），后端那份只当契约校验对象。 */
const hintLocal = computed(() => (degraded.value ? trendHintLocal(missingView.value) : ''))
const hintDrifted = computed(() =>
  series.value && degraded.value ? trendHintDisagrees(series.value, missingView.value) : false,
)

/** ★★★★ 空序列 + 降级 ⇒ **不能说「这段时间没有用量」**。 */
const genuinelyEmpty = computed(() =>
  series.value ? trendSeriesIsGenuinelyEmpty(series.value) : false,
)

// ══ 时间窗呈现 ══════════════════════════════════════════════════════════

/** ★★ `end` 是次日零点 ⇒ 走的是自定义路径；否则是预设路径的 `now`。 */
const endIsNextMidnight = computed(() => (series.value ? trendEndIsNextMidnight(series.value.end) : false))
/** ★★ 显示「用户选的结束日」必须减一天，否则多显示一天空桶。 */
const shownEndDay = computed(() => {
  if (!series.value) return ''
  if (!endIsNextMidnight.value) return series.value.end
  const d = trendSelectedEndDay(series.value.end)
  return d ? d.toISOString().slice(0, 10) : series.value.end
})
const pathLabel = computed(() =>
  endIsNextMidnight.value ? t('uts.window.pathCustom') : t('uts.window.pathPreset'),
)

/**
 * ★ 自定义区间下，响应 `start` 必须**逐字等于**本地复算值
 *   （`usage.go:1468` 的 `Start = startDay.UTC()`，`trendCustomRange` 是它的镜像）。
 * ⇒ 不等就说明后端把窗口换掉了 —— 这一格本地可算，所以要判，不要只写注释。
 */
const expectedStart = computed(() => {
  if (mode.value !== 'custom' || !customPairReady.value) return ''
  // ★ 后端是 `time.RFC3339`（**秒级**），而 `toISOString()` 带毫秒 ⇒
  //   不去掉 `.000` 的话这条对拍**永远不等**，变成一条恒红的判据。
  return trendCustomRange(utcDay(startDate.value), utcDay(endDate.value))
    .start.toISOString()
    .replace(/\.\d{3}Z$/, 'Z')
})
const startDrifted = computed(() =>
  series.value !== null && expectedStart.value !== '' && series.value.start !== expectedStart.value,
)

// ══ 折叠与排序 ══════════════════════════════════════════════════════════

const folded = computed(() => (series.value ? trendWasFolded(series.value, selectedModels.value) : false))
const foldSuppressed = computed(() => trendFoldSuppressedByModelSelection(selectedModels.value))
const others = computed(() => (series.value ? trendOthersLine(series.value) : null))
const othersLast = computed(() => (series.value ? trendOthersIsLast(series.value) : true))
const nonIncreasing = computed(() =>
  series.value ? trendSeriesNonIncreasing(series.value) : true,
)
const topEcho = computed(() => (series.value ? trendTopEchoed(series.value) : TREND_TOP_DEFAULT))

/** ★ 清单达到硬编码 LIMIT 100 ⇒ 静默截断，只能说「可能还有更多」。 */
const modelsTruncated = computed(() => (models.value ? trendModelsPossiblyTruncated(models.value) : false))
/** ★★ 选了 model 时 trend-models 不再是「可选清单」。 */
const modelsListConstrained = computed(() =>
  models.value ? trendModelsListIsConstrained(models.value, selectedModels.value) : false,
)

function modelLabel(m: string): string {
  if (m === TREND_OTHERS_KEY) return t('uts.others')
  return m
}

function pointsSummary(pts: { bucket: string }[]): string {
  if (pts.length === 0) return t('uts.points.zero')
  const first = pts[0]?.bucket ?? ''
  const last = pts[pts.length - 1]?.bucket ?? ''
  return `${pts.length} × ${t('uts.points.unit')} · ${first} → ${last}`
}

function toggleModel(m: string): void {
  selectedModels.value = selectedModels.value.includes(m)
    ? selectedModels.value.filter((x) => x !== m)
    : [...selectedModels.value, m]
}

onBeforeUnmount(() => {
  series.value = null
  models.value = null
  seriesError.value = null
  modelsError.value = null
})
</script>

<template>
  <div class="uts">
    <!-- ══ 时间窗 ══ -->
    <section class="uts__sec">
      <header class="uts__head">
        <h2 class="uts__h">{{ t('uts.window.title') }}</h2>
      </header>
      <div class="uts__chips">
        <button
          type="button"
          class="uts__chip-btn"
          :class="{ 'uts__chip-btn--on': mode === 'preset' }"
          data-testid="uts-mode-preset"
          @click="mode = 'preset'"
        >
          {{ t('uts.window.preset') }}
        </button>
        <button
          type="button"
          class="uts__chip-btn"
          :class="{ 'uts__chip-btn--on': mode === 'custom' }"
          data-testid="uts-mode-custom"
          @click="mode = 'custom'"
        >
          {{ t('uts.window.custom') }}
        </button>
      </div>

      <template v-if="mode === 'preset'">
        <div class="uts__chips">
          <button
            v-for="d in DAY_CHOICES"
            :key="d"
            type="button"
            class="uts__chip-btn"
            :class="{ 'uts__chip-btn--on': d === days }"
            :data-testid="'uts-days-' + d"
            @click="days = d"
          >
            {{ d }} {{ t('uts.window.days') }}
          </button>
        </div>
        <!-- ★★ days=7 的起点是**六天前**零点（todayStart.Add(-(days-1)*24h)） -->
        <p class="uts__msg" data-testid="uts-days-hint">
          {{ t('uts.window.daysHint', { n: trendDaysEffective(days) - 1 }) }}
        </p>
      </template>

      <template v-else>
        <div class="uts__row">
          <label class="uts__label">
            {{ t('uts.window.start') }}
            <input v-model="startDate" type="date" class="uts__input" data-testid="uts-start" />
          </label>
          <label class="uts__label">
            {{ t('uts.window.end') }}
            <input v-model="endDate" type="date" class="uts__input" data-testid="uts-end" />
          </label>
        </div>
        <!-- ★★★ 只给一个 ⇒ 后端 400 ⇒ 本页不发半对，且明说 -->
        <p v-if="customPairBroken" class="uts__msg uts__msg--warn" data-testid="uts-pair-broken">
          {{ t('uts.window.pairBroken') }}
        </p>
      </template>
    </section>

    <!-- ══ 过滤 ══ -->
    <section class="uts__sec">
      <header class="uts__head">
        <h2 class="uts__h">{{ t('uts.filter.title') }}</h2>
      </header>
      <div class="uts__chips">
        <span class="uts__chip-label">{{ t('uts.filter.top') }}</span>
        <button
          v-for="tp in TOP_CHOICES"
          :key="tp"
          type="button"
          class="uts__chip-btn"
          :class="{ 'uts__chip-btn--on': tp === top }"
          :data-testid="'uts-top-' + tp"
          @click="top = tp"
        >
          {{ tp }}
        </button>
      </div>
      <div class="uts__row">
        <label class="uts__label">
          {{ t('uts.filter.providerId') }}
          <input
            v-model="providerId"
            type="text"
            inputmode="numeric"
            class="uts__input"
            data-testid="uts-provider"
          />
        </label>
        <label class="uts__label">
          {{ t('uts.filter.apiKeyId') }}
          <input
            v-model="apiKeyId"
            type="text"
            inputmode="numeric"
            class="uts__input"
            data-testid="uts-apikey"
          />
        </label>
      </div>
      <!-- ★★★★ 非法/负数 ID 会被后端静默当成 0 ⇒ 换数据档而响应看不出异常 -->
      <p v-if="idFilterIgnored" class="uts__msg uts__msg--warn" data-testid="uts-id-ignored">
        {{ idFilterIgnored }}
      </p>

      <!-- 模型多选 -->
      <header class="uts__head">
        <h3 class="uts__h3">{{ t('uts.filter.models') }}</h3>
        <button type="button" class="uts__btn" :disabled="loadingModels" data-testid="uts-load-models" @click="loadModels">
          {{ models ? t('uts.reload') : t('uts.loadModels') }}
        </button>
      </header>
      <p v-if="modelsError" class="uts__msg uts__msg--err">{{ modelsError }}</p>
      <p v-if="loadingModels" class="uts__msg">{{ t('common.loading') }}</p>
      <template v-if="models">
        <!-- ★★ 满 100 ⇒ 静默截断，无字段可证 -->
        <p v-if="modelsTruncated" class="uts__msg uts__msg--warn" data-testid="uts-models-truncated">
          {{ t('uts.models.truncated', { limit: TREND_MODELS_SQL_LIMIT }) }}
        </p>
        <!-- ★★ 选了 model ⇒ 这份清单已被收敛成「已选项」 -->
        <p v-if="modelsListConstrained" class="uts__msg uts__msg--warn" data-testid="uts-models-constrained">
          {{ t('uts.models.constrained') }}
        </p>
        <div v-if="models.models.length" class="uts__chips">
          <button
            v-for="e in models.models"
            :key="e.model"
            type="button"
            class="uts__chip-btn"
            :class="{ 'uts__chip-btn--on': selectedModels.includes(e.model) }"
            :data-testid="'uts-model-' + e.model"
            @click="toggleModel(e.model)"
          >
            {{ modelLabel(e.model) }} · {{ e.requests }}
          </button>
        </div>
        <p v-else class="uts__msg">{{ t('uts.models.empty') }}</p>
      </template>
      <!-- ★ 超 20 个被截断（`:133-143`） -->
      <p v-if="droppedModels > 0" class="uts__msg uts__msg--warn" data-testid="uts-models-dropped">
        {{ t('uts.models.dropped', { n: droppedModels, max: TREND_MAX_MODELS }) }}
      </p>
      <p v-if="selectedModels.length" class="uts__msg" data-testid="uts-models-selected">
        {{ t('uts.models.selected', { n: effectiveModels.length }) }}
      </p>
    </section>

    <!-- ══ 序列 ══ -->
    <section class="uts__sec">
      <header class="uts__head">
        <h2 class="uts__h">{{ t('uts.series.title') }}</h2>
        <button type="button" class="uts__btn" :disabled="loadingSeries" data-testid="uts-load-series" @click="loadSeries">
          {{ series ? t('uts.reload') : t('uts.load') }}
        </button>
      </header>
      <p v-if="customPairBroken" class="uts__msg uts__msg--warn">{{ t('uts.window.pairBlocked') }}</p>
      <p v-if="seriesError" class="uts__msg uts__msg--err" data-testid="uts-series-error">
        {{ seriesError }}
      </p>
      <p v-if="loadingSeries" class="uts__msg">{{ t('common.loading') }}</p>

      <!-- ★★★★ 降级披露：常驻，不随数据段消失 -->
      <p v-if="degraded" class="uts__msg uts__msg--warn" data-testid="uts-degraded">
        {{ t('uts.degraded.badge') }}
        <template v-if="degradedReason"> · {{ degradedReason }}</template>
        <template v-if="missingView"> · {{ missingView }}</template>
      </p>
      <p v-if="degraded" class="uts__msg uts__msg--warn" data-testid="uts-degraded-hint">
        {{ hintLocal }}
      </p>
      <p v-if="hintDrifted" class="uts__msg uts__msg--err" data-testid="uts-hint-drift">
        {{ t('uts.degraded.hintDrift') }}
      </p>

      <template v-if="series">
        <p v-if="startDrifted" class="uts__msg uts__msg--err" data-testid="uts-start-drift">
          {{ t('uts.window.startDrift', { want: expectedStart, got: series.start }) }}
        </p>
        <p class="uts__msg" data-testid="uts-window-echo">
          {{ t('uts.window.echo', { s: series.start, e: shownEndDay, path: pathLabel }) }}
        </p>
        <p class="uts__msg" data-testid="uts-meta">
          {{ t('uts.series.meta', {
            bucket: series.bucket_minutes,
            top: topEcho,
            source: series.source,
          }) }}
        </p>

        <!-- ★ 空数组 + 降级 ⇒ 绝不能说「没有用量」 -->
        <template v-if="genuinelyEmpty">
          <p class="uts__msg" data-testid="uts-truly-empty">{{ t('uts.series.trulyEmpty') }}</p>
        </template>
        <template v-else-if="series.series.length === 0">
          <p class="uts__msg uts__msg--warn" data-testid="uts-empty-degraded">
            {{ t('uts.series.emptyDegraded') }}
          </p>
        </template>
        <template v-else>
          <p v-if="folded" class="uts__msg uts__msg--warn" data-testid="uts-folded">
            {{ t('uts.series.folded', { top: topEcho }) }}
          </p>
          <p v-if="foldSuppressed" class="uts__msg" data-testid="uts-fold-suppressed">
            {{ t('uts.series.foldSuppressed') }}
          </p>
          <p v-if="!nonIncreasing" class="uts__msg uts__msg--err" data-testid="uts-order-bad">
            {{ t('uts.series.orderBad') }}
          </p>
          <p v-if="!othersLast" class="uts__msg uts__msg--err" data-testid="uts-others-not-last">
            {{ t('uts.series.othersNotLast') }}
          </p>
          <article
            v-for="s in series.series"
            :key="s.model"
            class="uts__card"
            :class="{ 'uts__card--others': s.model === TREND_OTHERS_KEY }"
            :data-testid="'uts-line-' + s.model"
          >
            <div class="uts__row">
              <span class="uts__model">{{ modelLabel(s.model) }}</span>
              <span class="uts__badge">{{ s.total_requests }} {{ t('uts.unit.requests') }}</span>
            </div>
            <dl class="uts__kv">
              <div class="uts__kv-row">
                <dt>{{ t('uts.col.tokens') }}</dt><dd>{{ s.total_tokens }}</dd>
              </div>
              <div class="uts__kv-row">
                <dt>{{ t('uts.col.credits') }}</dt><dd>{{ s.total_credits }}</dd>
              </div>
              <div class="uts__kv-row">
                <dt>{{ t('uts.col.cost') }}</dt><dd>{{ s.total_cost_usd }}</dd>
              </div>
              <div class="uts__kv-row">
                <dt>{{ t('uts.col.points') }}</dt><dd>{{ pointsSummary(s.points) }}</dd>
              </div>
            </dl>
          </article>
          <p v-if="others" class="uts__msg" data-testid="uts-others-note">
            {{ t('uts.series.othersNote') }}
          </p>
        </template>
      </template>
    </section>
  </div>
</template>

<style scoped>
.uts {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-4);
}

.uts__sec {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-2);
}

.uts__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--app-space-2);
}

.uts__h {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
  color: var(--app-text);
}

.uts__h3 {
  margin: 0;
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--app-text-secondary);
}

.uts__btn {
  min-height: 48px;
  padding: 0 16px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.uts__chip-btn {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.uts__chip-btn--on {
  color: var(--app-text);
  border-color: var(--app-text-secondary);
}

.uts__chip-label {
  align-self: center;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.uts__input {
  min-height: 48px;
  padding: 0 12px;
  font-size: 0.8125rem;
  color: var(--app-text);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.uts__label {
  display: flex;
  flex-direction: column;
  gap: 2px;
  flex: 1 1 140px;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.uts__row {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-2);
  align-items: flex-end;
}

.uts__chips {
  display: flex;
  flex-wrap: wrap;
  gap: var(--app-space-1);
  align-items: center;
}

.uts__msg {
  margin: 0;
  font-size: 0.75rem;
  color: var(--app-text-muted);
}

.uts__msg--err {
  color: var(--app-danger);
}

.uts__msg--warn {
  color: var(--app-warning, var(--app-danger));
}

.uts__card {
  display: flex;
  flex-direction: column;
  gap: var(--app-space-1);
  padding: var(--app-space-3);
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-sm);
}

.uts__card--others {
  background: var(--app-surface-muted);
}

.uts__model {
  font-size: 0.875rem;
  font-weight: 600;
  color: var(--app-text);
  word-break: break-all;
}

.uts__badge {
  font-size: 0.6875rem;
  padding: 1px 6px;
  border-radius: var(--app-radius-sm);
  background: var(--app-surface-muted);
  color: var(--app-text-secondary);
}

.uts__kv {
  margin: 0;
}

.uts__kv-row {
  display: flex;
  justify-content: space-between;
  gap: var(--app-space-2);
  padding: 2px 0;
  font-size: 0.8125rem;
}

.uts__kv-row dt {
  color: var(--app-text-muted);
}

.uts__kv-row dd {
  margin: 0;
  color: var(--app-text);
  word-break: break-all;
  text-align: right;
}
</style>
