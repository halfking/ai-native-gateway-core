<script setup lang="ts">
// SimpleOverviewView.vue — 简易模式总览页（2026-11-17；2026-11-18「总监+看板」化扩展）
//
// 设计稿：docs/03-design/02-feature-design/simple-mode-concept-v2.html §四
// 借鉴：docs/03-design/02-feature-design/simple-mode-v2-magpie-comparison.md
//
// ★ 这一页要回答初级用户的三个问题，顺序不能乱：
//   ① 现在能不能用？      ⇒ 状态条（一句话结论 + 状态点 + **去处理动作**）
//                           ＋ 运维 chips（后台任务 / 系统自检，复用专家面 BoardOpsBar）
//   ② 今天跑了多少？      ⇒ 统计卡行（今日口径 + 失败笔数语境）
//     消耗走势怎么样？    ⇒ 近 7 日消耗卡（2026-11-18 新增：owner 报障
//                           「关心的是整体状况、消耗情况」—— 只有今日一个口径
//                           看不出走向；与专家面看板 days=7 同一端点同口径）
//   ③ 我现在该做什么？    ⇒ 常用操作 + 接入进度 + issue 行内动作
//   ④ 我的客户端在用什么？ ⇒ 接入清单 + 上游健康 + 最常用模型（带今日花费）
//
// ★ 为什么把 v2 的「接入清单化」保留、又在上面加一层：
//   v2 设计稿的首页是「一张接入清单」（借鉴 magpie 的 B1），
//   但它自己也在风险表 G1 里登记过：「首页换成清单后，今天花了多少变弱」。
//   ⇒ 这一页是 v2 稿的**完整兑现**：清单仍在（主体），
//     统计 / 状态 / 快捷操作补在上面（G1 的处置）。
//
// ★ 数据口径**与专家面完全一致**（同一批 API、同一张表）：
//   /api/admin/dashboard/v2/board?days=1 ⇒ summary / pies / operational（今日口径）
//   /api/admin/dashboard/v2/board?days=7 ⇒ trends（15min 桶）+ 7 日 summary（2026-11-18 新增；
//                                        专家面看板 7d 预设同款；桶在前端按 UTC 日聚合）
//   /api/providers                     ⇒ 上游健康
//   /api/keys                          ⇒ 密钥清单
//   简易模式不新建任何表、不新造任何统计口径。
//
// ★ ★ 性能纪律（2026-11-17 修；2026-11-18 扩）：
//   旧实现对**每一个密钥**发一次 /api/usage/{id}/models（线上 208 个密钥
//   ⇒ 208 个请求）。这一页改为**四个请求**：board(days=1)、board(days=7)、
//   providers、keys —— days=7 只为趋势卡服务，与专家面切到 7d 预设是
//   同一次调用；「当前模型」仍用 board 自带的 pies ⇒ 208 → 4。
//
// ★ 诚实边界（降级必须可见）：
//   · 看板 summary 降级 ⇒ 统计卡显示占位符，**不显示 0**
//   · 趋势降级（days=7 失败 / degraded_trends / summary 降级）⇒ 趋势卡显示
//     「读不到」占位符 —— 零线会被读成「这七天一点消耗都没有」
//   · 上游健康读不到（无权限 / 接口失败）⇒ 面板写「读不到」，
//     状态条也**不宣称健康**（magpie 的绿点语义：不知道 ≠ 正常）
//   · 四个数据源互相独立降级 ⇒ 一个失败不拖垮整页
import { computed, onMounted, ref, watch, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ElRadioButton, ElRadioGroup } from 'element-plus'
import { getKeys, getProviders, type ApiKey, type Provider } from '../api'
import { ApiError } from '../api/_core'
import { fetchDashboardBoard, type BoardPayload } from '../api/board'
import BoardOpsBar from '../components/board/BoardOpsBar.vue'
import PageHeader from '../components/ui/PageHeader.vue'
import PaginationBar from '../components/ui/PaginationBar.vue'
import StatCard from '../components/ui/StatCard.vue'
import StatsRow from '../components/ui/StatsRow.vue'
import { useWorkMode } from '../composables/useWorkMode'
import { surfaceError } from '../i18n/errorSurface'
import { fmtDateTimeShort } from '../i18n/useFormat'
import {
  aggregateTrendByDay,
  deriveOnboardingSteps,
  deriveOverviewStats,
  deriveOverviewStatus,
  deriveProviderHealth,
  fillMissingDays,
  formatCompact,
  formatCount,
  formatUsd,
  lastDailyPoints,
  onboardingComplete,
  paginateClientRows,
  sortClientRows,
  sparkPolylinePoints,
  type ClientPage,
  type ClientRow,
  type DailyTrendPoint,
  type OverviewIssue,
  type OverviewStat,
  type ProviderHealthRow,
} from '../composables/simpleOverviewModel'

const { t } = useI18n()
const router = useRouter()
const { isSimple } = useWorkMode()

// ─── 状态 ────────────────────────────────────────────────────────────────────

const board = ref<BoardPayload | null>(null)
/** 近 7 日看板（趋势卡专用；与专家面 7d 预设同一端点） */
const board7 = ref<BoardPayload | null>(null)
const providers = ref<Provider[] | null>(null)
const providersDenied = ref(false)
const keys = ref<ApiKey[]>([])
const loading = ref(false)
const error = ref('')

const boardFailed = ref(false)
const board7Failed = ref(false)
const providersFailed = ref(false)
const keysFailed = ref(false)

// ─── 取数 ────────────────────────────────────────────────────────────────────

/**
 * 四个数据源**各自独立**加载：任一失败只降级自己那一块，
 * 不让一个接口把整页拖垮（Promise.all 会让最小的一个失败带走全部）。
 * days=7 那一路失败只降级趋势卡，不影响今日口径的任何数字。
 */
async function load(): Promise<void> {
  loading.value = true
  error.value = ''

  const [boardRes, board7Res, providersRes, keysRes] = await Promise.allSettled([
    fetchDashboardBoard({ days: 1 }),
    fetchDashboardBoard({ days: 7 }),
    getProviders(),
    getKeys(),
  ])

  if (boardRes.status === 'fulfilled') {
    board.value = boardRes.value
    boardFailed.value = false
  } else {
    board.value = null
    boardFailed.value = true
  }

  if (board7Res.status === 'fulfilled') {
    board7.value = board7Res.value
    board7Failed.value = false
  } else {
    board7.value = null
    board7Failed.value = true
  }

  if (providersRes.status === 'fulfilled') {
    providers.value = providersRes.value
    providersFailed.value = false
    // 403 = 权限不足（ProviderConsoleMiddleware：非 default 租户的 tenant_admin）
    // ⇒ 与「服务异常」分开说：前者是权限问题，后者是故障。
    providersDenied.value = false
  } else {
    providers.value = null
    providersFailed.value = true
    providersDenied.value =
      providersRes.reason instanceof ApiError &&
      (providersRes.reason.status === 403 || providersRes.reason.status === 401)
  }

  if (keysRes.status === 'fulfilled') {
    keys.value = keysRes.value
    keysFailed.value = false
  } else {
    keys.value = []
    keysFailed.value = true
  }

  // 主干三源全失败 ⇒ 这是真故障，给一条带恢复动作的错误（errorSurface 出口）；
  // board7（趋势）不在其中 —— 它失败只降级趋势卡自己。
  if (boardFailed.value && providersFailed.value && keysFailed.value) {
    error.value = surfaceError(boardRes.status === 'rejected' ? boardRes.reason : new Error('load failed'), {
      key: 'simpleMode.overview.loadFailed',
    })
  }

  loading.value = false
}

onMounted(load)

// ─── 派生（全部走纯函数，便于单测钉住判据）────────────────────────────────────

const summaryDegraded = computed(() => {
  const b = board.value
  if (!b) return true
  // ★ 降级标记在 summary **内部**（BoardSummary.degraded_summary /
  //   summary_missing_view），不是顶层 —— 读错一层就会把「整屏不可信」
  //   当成「一切正常」，页面上就会出现 0 与真实数字混排。
  return b.summary?.degraded_summary === true || b.summary?.summary_missing_view != null
})

const providerHealth = computed(() =>
  deriveProviderHealth(providers.value, {
    unavailable: providersFailed.value,
  }),
)

const selfcheck = computed(() => board.value?.operational?.selfcheck ?? null)

const status = computed(() =>
  deriveOverviewStatus({
    providerHealth: providerHealth.value,
    selfcheck: selfcheck.value,
    statsDegraded: summaryDegraded.value,
    statsMissing: boardFailed.value,
  }),
)

const stats = computed<OverviewStat[]>(() =>
  deriveOverviewStats({
    summary: board.value?.summary ?? null,
    degraded: summaryDegraded.value,
    keyTotal: keys.value.length,
    keyEnabled: keys.value.filter((k) => k.enabled).length,
    // 失败笔数的明细来源（专家面英雄区同口径：pies 优先、summary 兜底）
    errorsPie: board.value?.pies?.errors ?? null,
  }),
)

// ─── 近 7 日消耗（趋势卡；2026-11-18）────────────────────────────────────────

const TREND_METRICS = ['requests', 'tokens', 'cost'] as const
type TrendMetric = (typeof TREND_METRICS)[number]
const trendMetric = ref<TrendMetric>('requests')

/** 趋势是否读不到（请求失败 / 服务端自报降级 / summary 整屏降级） */
const trendDegraded = computed(() => {
  if (board7Failed.value) return true
  const b = board7.value
  if (!b) return true
  // degraded_trends：服务端没算出趋势（回退切片等）—— 与「零流量」必须分得开
  return b.degraded_trends === true || b.summary?.degraded_summary === true
})

/** 每日点（UTC 日聚合 + 窗口内补零 + 只留最近 7 天） */
const trendDays = computed<DailyTrendPoint[]>(() => {
  if (trendDegraded.value) return []
  return fillMissingDays(lastDailyPoints(aggregateTrendByDay(board7.value?.trends), 7))
})

/** 7 天合计（来自 days=7 的 summary 块 —— 与专家面 7d 数字同源） */
const trendTotalsText = computed(() => {
  const s = board7.value?.summary ?? null
  return {
    requests: formatCount(s?.total_requests) ?? '-',
    tokens: formatCompact(s?.total_tokens) ?? '-',
    cost: formatUsd(s?.total_cost_usd) ?? '-',
  }
})

function trendValue(d: DailyTrendPoint): number {
  switch (trendMetric.value) {
    case 'tokens':
      return d.tokens
    case 'cost':
      return d.costUsd
    default:
      return d.requests
  }
}

function trendMetricLabel(m: TrendMetric): string {
  // 复用专家面看板的指标文案键（8 语种齐备），不新增一份同义词
  switch (m) {
    case 'tokens':
      return t('dashboard.board.trendTokens')
    case 'cost':
      return t('dashboard.board.trendCost')
    default:
      return t('dashboard.board.trendRequests')
  }
}

function trendValueText(d: DailyTrendPoint): string {
  switch (trendMetric.value) {
    case 'tokens':
      return formatCompact(d.tokens) ?? '0'
    case 'cost':
      return formatUsd(d.costUsd) ?? '$0.00'
    default:
      return formatCount(d.requests) ?? '0'
  }
}

function trendBarPct(d: DailyTrendPoint): string {
  const max = Math.max(...trendDays.value.map(trendValue), 1)
  const v = trendValue(d)
  if (v <= 0) return '0%'
  // 非零值至少 2% 高度：否则小消耗日在爆量日旁边「看不见」（用户以为缺数据）
  return `${Math.max(2, Math.round((v / max) * 100))}%`
}

/** UTC 日期（YYYY-MM-DD）→ M/D（不引入时区换算，标签与聚合口径同源） */
function trendDayLabel(date: string): string {
  const month = Number(date.slice(5, 7))
  const day = Number(date.slice(8, 10))
  if (!Number.isFinite(month) || !Number.isFinite(day)) return date
  return `${month}/${day}`
}

/** 今日请求卡的迷你趋势线（5 分钟桶，最近 16 桶 ≈ 80 分钟；专家面英雄区同款） */
const requestSpark = computed(() => sparkPolylinePoints(board.value?.trends))

const enabledKeys = computed(() => keys.value.filter((k) => k.enabled).length)
const keysInUse = computed(() => keys.value.filter((k) => !!k.last_used_at).length)

const onboardingSteps = computed(() =>
  deriveOnboardingSteps({
    providerHealth: providerHealth.value,
    keysEnabled: enabledKeys.value,
    keysInUse: keysInUse.value,
  }),
)

/** 三步全完成 ⇒ 引导卡自动消失（magpie app.js:1740 同款） */
const showOnboarding = computed(() => !onboardingComplete(onboardingSteps.value))

// ─── 状态条文案 ──────────────────────────────────────────────────────────────

const statusHeadline = computed(() => {
  switch (status.value.tone) {
    case 'blocked':
      return t('simpleMode.overview.statusBlocked')
    case 'warn':
      return t('simpleMode.overview.statusWarn')
    default:
      return t('simpleMode.overview.statusOk')
  }
})

/** 状态条右侧的一行关键数字（读不到就不显示，绝不用 0 顶替） */
const statusDetail = computed(() => {
  const b = board.value
  if (!b || summaryDegraded.value) return t('simpleMode.overview.statusDetailUnknown')
  const requests = b.summary?.total_requests
  const rate = b.summary?.success_rate
  const usable = providerHealth.value.unavailable ? null : providerHealth.value.usable
  return t('simpleMode.overview.statusDetail', {
    requests: typeof requests === 'number' ? requests.toLocaleString('en-US') : '-',
    rate: typeof rate === 'number' ? `${(rate * 100).toFixed(1)}%` : '-',
    providers: usable == null ? '-' : String(usable),
  })
})

const statusIssues = computed<OverviewIssue[]>(() => status.value.issues)

// ─── issue 的「去处理」动作（2026-11-18）─────────────────────────────────────
//
// ★ owner 报障：「有上游不可达，请到供应商页处理」只是一句话，初级用户
//   还得自己去菜单里找供应商页。⇒ 每条 issue 直接给出**行内动作按钮**。
//
// ★ 路由必须是 router.ts 里注册过的字面量（本文件底部的死链契约测试会扫
//   这张表 —— 里面写一个不存在的路径，测试立刻红）。
//   provider_status_unavailable（权限不足）与 stats_unavailable（稍后重试）
//   没有自助动作 ⇒ 不在表里 ⇒ 不渲染按钮（只留文案）。
const ISSUE_ACTION_ROUTES: Partial<Record<OverviewIssue, string>> = {
  no_usable_provider: '/providers',
  provider_unreachable: '/providers',
  provider_warning: '/providers',
  selfcheck_failed: '/dashboard?tab=selfcheck',
  selfcheck_stale: '/dashboard?tab=selfcheck',
}

function issueRoute(issue: OverviewIssue): string | null {
  return ISSUE_ACTION_ROUTES[issue] ?? null
}

function openIssue(issue: OverviewIssue): void {
  const route = ISSUE_ACTION_ROUTES[issue]
  if (route) void router.push(route)
}

/** BoardOpsBar 的自检 chip 点击（复用专家面同一交互） */
function openSelfCheck(): void {
  void router.push('/dashboard?tab=selfcheck')
}

/** 统计卡的语境行：成功率卡带失败笔数，其余卡只带口径 */
function statSub(stat: OverviewStat): string {
  const scopeText = stat.scope ? t(`simpleMode.overview.statScope.${stat.scope}`) : ''
  if (stat.key === 'successRate' && stat.failures) {
    const failuresText = t('simpleMode.overview.statFailures', { n: stat.failures })
    return scopeText ? `${failuresText} · ${scopeText}` : failuresText
  }
  return scopeText
}

// ─── 接入清单 ────────────────────────────────────────────────────────────────

/**
 * 接入清单的一行。
 *
 * ★ 原型与展示文本分开：排序/分页要用**原始数值**
 *   （积累请求数、时间戳），绘制要用文本（带千分位/币种）。
 *   两者混在一起 ⇒ 排序会按「字符串」比（「90」优先于「9」）。
 */
interface AccessRow extends ClientRow {
  key: ApiKey
  /** 展示用文本（已格式化） */
  spendText: string
  budgetText: string
  lastUsedText: string
  requestsText: string
}

/** 在用的密钥（已禁用/未使用的收进折叠，magpie 的 offCount 同款） */
const activeRows = computed<AccessRow[]>(() =>
  keys.value
    .filter((k) => k.enabled && (k.status === 'active' || k.status === 'pending'))
    .map(toAccessRow),
)

const inactiveRows = computed<AccessRow[]>(() =>
  keys.value
    .filter((k) => !(k.enabled && (k.status === 'active' || k.status === 'pending')))
    .map(toAccessRow),
)

function toAccessRow(k: ApiKey): AccessRow {
  return {
    key: k,
    id: k.id,
    name: k.key_alias || k.application_code || k.key_prefix,
    stateKey: stateOf(k),
    requests: k.total_requests,
    spend: k.total_cost_usd,
    lastUsedAt: k.last_used_at,
    createdAt: k.created_at ?? null,
    requestsText: k.total_requests.toLocaleString('en-US'),
    spendText: fmtUsd(k.total_cost_usd),
    budgetText: k.budget_usd == null ? '-' : fmtUsd(k.budget_usd),
    lastUsedText: k.last_used_at ? fmtDateTimeShort(k.last_used_at) : t('simpleMode.accessList.neverUsed'),
  }
}

// ── 分页：两个分页列表 + 已停用那一折（都用同一套派生逻辑）────────────────
//
// ★ 为什么不用 usePagination：那个 composable 的语义是「越界直接忽略」（供服务端分页用），
//   而我们的数据**全在前端**（一次 /api/keys 拿到全部）⇒ 列表可能缩短
//   ⇒ 页码越界时必须钳到最后一页（否则渲染出空页）。
//   钳制在模型层（paginateClientRows）做，视图只负责把钳制后的页码写回 ref。

/** 每页条数（固定：总览页要紧凑，不给初级用户改页尺寸的选项） */
const CLIENT_PAGE_SIZE = 10

const recentPage = ref(1)
const busiestPage = ref(1)
const inactivePage = ref(1)

/** ① 最近在用（按最近一次使用排序） */
const recentList = computed(() =>
  paginateClientRows(sortClientRows(activeRows.value, 'recent-used'), recentPage.value, CLIENT_PAGE_SIZE),
)

/** ② 用得最多（按累计请求数排序） */
const busiestList = computed(() =>
  paginateClientRows(sortClientRows(activeRows.value, 'busiest'), busiestPage.value, CLIENT_PAGE_SIZE),
)

/** ③ 已停用/未使用（折叠里也分页，否则展开就是 91 行） */
const inactiveList = computed(() =>
  paginateClientRows(sortClientRows(inactiveRows.value, 'recent-used'), inactivePage.value, CLIENT_PAGE_SIZE),
)

// 页码钳制回写：数据变短时不至于停在越界页（下一次点「上一页」会无反应）
watch([recentList, busiestList, inactiveList], ([a, b, c]) => {
  if (a.page !== recentPage.value) recentPage.value = a.page
  if (b.page !== busiestPage.value) busiestPage.value = b.page
  if (c.page !== inactivePage.value) inactivePage.value = c.page
})

/** 翻页（越界直接忽略，与 PaginationBar 的 disabled 状态一致） */
function turnPage(target: Ref<number>, page: ClientPage, delta: number): void {
  const next = page.page + delta
  if (next < 1 || next > page.pageCount) return
  target.value = next
}

/**
 * 三个列表各自的翻页入口。
 *
 * ★ 为什么不在模板里直接传 ref：**模板里的 ref 会自动解包**，
 *   把 ref 直接传进去的是**数字**而不是 Ref ⇒ `target.value = next`
 *   写到了一个数字上 ⇒ **翻页静默失效**（实测：点「下一页」后页码不变）。
 *   ⇒ 翻页入口收进脚本层，模板里只暴露无参的方法（delta）。
 */
function turnRecentPage(delta: number): void {
  turnPage(recentPage, recentList.value, delta)
}

function turnBusiestPage(delta: number): void {
  turnPage(busiestPage, busiestList.value, delta)
}

function turnInactivePage(delta: number): void {
  turnPage(inactivePage, inactiveList.value, delta)
}

function stateOf(k: ApiKey): AccessRow['stateKey'] {
  if (k.status === 'pending') return 'pending'
  if (!k.enabled) return 'off'
  if (!k.last_used_at) return 'idle'
  return 'ok'
}

function stateLabel(state: AccessRow['stateKey']): string {
  switch (state) {
    case 'ok':
      return t('simpleMode.accessList.healthOk')
    case 'idle':
      return t('simpleMode.accessList.healthIdle')
    case 'pending':
      return t('common.status.pending')
    default:
      return t('common.status.disabled')
  }
}

function stateClass(state: AccessRow['stateKey']): string {
  switch (state) {
    case 'ok':
      return 'badge-green'
    case 'idle':
      return 'badge-gray'
    case 'pending':
      return 'badge-yellow'
    default:
      return 'badge-red'
  }
}

function fmtUsd(v: number | null | undefined): string {
  if (v == null) return '-'
  return `$${v.toFixed(2)}`
}

// ─── 上游健康 ────────────────────────────────────────────────────────────────

function providerToneClass(row: ProviderHealthRow): string {
  switch (row.tone) {
    case 'ok':
      return 'badge-green'
    case 'warn':
      return 'badge-yellow'
    case 'danger':
      return 'badge-red'
    default:
      return 'badge-gray'
  }
}

function providerToneLabel(row: ProviderHealthRow): string {
  switch (row.tone) {
    case 'ok':
      return t('simpleMode.overview.providerUsable')
    case 'warn':
      return t('simpleMode.overview.providerWarning')
    case 'danger':
      return t('simpleMode.overview.providerUnreachable')
    default:
      return t('simpleMode.overview.providerUnknown')
  }
}

// ─── 最常用的模型（今日）─────────────────────────────────────────────────────

const topModels = computed(() => {
  const models = board.value?.pies?.models
  if (!Array.isArray(models)) return []
  return models.slice(0, 5)
})

// ─── 操作 ────────────────────────────────────────────────────────────────────

function go(path: string): void {
  void router.push(path)
}

function openSignKey(): void {
  // ★ 直接调 router.push（不经过 go 包装）：navCoverage 的孤儿路由扫描器
  //   按「行内出现 router.push + 路径字面量」判定可达性，包一层就看不见了。
  void router.push('/simple/keys')
}

/**
 * 「查看请求」→ 请求日志页（/request-logs）。
 *
 * ★ 2026-11-17 实测修正：本按钮原先跳 `/usage`，而 **`/usage` 从来没有路由定义**
 *   （router.ts 里没有；appNav 的 requests-sessions 组里也没有）⇒ 点进去是
 *   vue-router 的 no-match 空白页。同一处幻觉还出现在 simpleNav.ts 与
 *   scripts/audit/gen-simple-mode-ledger.mjs 的分组清单里（两边同源 ⇒ 互相掩盖）。
 *
 * ★ 为什么**不**跳 `/tenant/usage`（并行列车把幻觉改成了这个真实存在的路径）：
 *   取证（super_admin 实开，2026-11-17）：
 *     · /tenant/usage ⇒ 「我的消耗 / 积分流水」，积分消耗 **0 积分**、
 *       「最近 50 条 consume 记录：**0 笔**」、空态「暂无流水记录。去购买积分」
 *       —— 它是**租户积分账单**视角，super_admin 没有积分账户 ⇒ 落到空态。
 *     · /request-logs ⇒ 「请求日志」，17,674 条真实流水 + 模型统计，
 *       **全角色可用**（router.ts:300 无 meta 限定），与统计卡「今日请求」呼应。
 *   ⇒ 按钮走 /request-logs：信息量足、没有空态陷阱，也不与本轮
 *     「读不到 ≠ 0、不给用户看假零」的诚实口径冲突。
 *   ★ 两条口径别混：simpleNav 的分组清单仍保留 '/tenant/usage'
 *     （那是**导航投影**口径 —— 对租户用户它是对的入口）。
 *   文案用**已存在**的 `action.requests`（8 语种齐备，无需新增键）。
 */
function openRequestLogs(): void {
  void router.push('/request-logs')
}

function openDetail(k: ApiKey): void {
  go(`/keys/${k.id}`)
}
</script>

<template>
  <div class="simple-overview" :data-loading="loading ? '1' : '0'">
    <PageHeader :title="t('simpleMode.overview.title')" :subtitle="t('simpleMode.overview.subtitle')">
      <template #actions>
        <span class="chip">{{ t('simpleMode.mode.label') }} · {{ isSimple ? t('simpleMode.mode.simple') : t('simpleMode.mode.expert') }}</span>
        <button class="btn btn-ghost" :disabled="loading" @click="load">
          {{ t('common.button.refresh') }}
        </button>
        <button class="btn btn-primary" @click="openSignKey">
          {{ t('simpleMode.accessList.signKey') }}
        </button>
      </template>
    </PageHeader>

    <div v-if="error" class="alert alert-danger" data-testid="overview-error">{{ error }}</div>

    <!-- ① 状态条：一句话结论 + 每条 issue 的行内「去处理」动作 -->
    <section
      class="status-bar"
      :class="`status-bar--${status.tone}`"
      data-testid="overview-status"
      :data-tone="status.tone"
    >
      <span class="status-dot" aria-hidden="true"></span>
      <div class="status-text">
        <strong class="status-headline">{{ statusHeadline }}</strong>
        <span class="status-detail">{{ statusDetail }}</span>
      </div>
      <ul v-if="statusIssues.length > 0" class="status-issues" data-testid="overview-status-issues">
        <li v-for="issue in statusIssues" :key="issue" class="status-issue">
          <span class="status-issue__text">{{ t(`simpleMode.overview.issue.${issue}`) }}</span>
          <button
            v-if="issueRoute(issue)"
            class="btn btn-ghost btn-sm status-issue__go"
            :data-testid="`overview-issue-action-${issue}`"
            @click="openIssue(issue)"
          >
            {{ t('simpleMode.overview.issueAction') }}
          </button>
        </li>
      </ul>
    </section>

    <!-- ①b 运维状态 chips：后台任务 + 系统自检（复用专家面 BoardOpsBar，同一口径同一交互） -->
    <div v-if="board?.operational" class="overview-ops" data-testid="overview-ops">
      <BoardOpsBar :operational="board.operational" @open-selfcheck="openSelfCheck" />
    </div>

    <!-- ② 统计卡：今日口径（成功率卡带失败笔数；请求卡带当日迷你趋势线） -->
    <section class="overview-stats" data-testid="overview-stats">
      <StatsRow :cols="{ mobile: 2, tablet: 3, desktop: 6 }">
        <StatCard
          v-for="stat in stats"
          :key="stat.key"
          :label="t(`simpleMode.overview.stat.${stat.key}`)"
          :value="stat.value ?? t('simpleMode.overview.statUnavailable')"
          :sub="statSub(stat)"
          :tone="stat.tone"
          compact
        >
          <template v-if="stat.key === 'requests' && requestSpark" #spark>
            <svg width="72" height="26" viewBox="0 0 72 26" aria-hidden="true">
              <polyline
                :points="requestSpark"
                fill="none"
                stroke="var(--accent)"
                stroke-width="2"
                stroke-linecap="round"
              />
            </svg>
          </template>
        </StatCard>
      </StatsRow>
    </section>

    <!-- ②b 近 7 日消耗：owner 报障「关心消耗情况」⇒ 与专家面看板 days=7 同端点同口径，
         15min 桶在前端按 UTC 日聚合成 7 根柱条；降级显示「读不到」，不画零线 -->
    <section class="card overview-panel" data-testid="overview-trend">
      <div class="panel-head">
        <div class="trend-headwrap">
          <span class="panel-title">{{ t('simpleMode.overview.trendTitle') }}</span>
          <span class="panel-hint">{{ t('simpleMode.overview.trendHint') }}</span>
        </div>
        <el-radio-group v-model="trendMetric" size="small" class="trend-metric">
          <el-radio-button v-for="m in TREND_METRICS" :key="m" :value="m">
            {{ trendMetricLabel(m) }}
          </el-radio-button>
        </el-radio-group>
      </div>
      <div v-if="trendDegraded" class="empty" role="status" data-testid="overview-trend-unavailable">
        {{ t('simpleMode.overview.trendUnavailable') }}
      </div>
      <div v-else-if="trendDays.length === 0" class="empty" data-testid="overview-trend-empty">
        {{ t('simpleMode.overview.trendEmpty') }}
      </div>
      <template v-else>
        <div class="trend-summary" data-testid="overview-trend-total">
          {{
            t('simpleMode.overview.trendTotal', {
              requests: trendTotalsText.requests,
              tokens: trendTotalsText.tokens,
              cost: trendTotalsText.cost,
            })
          }}
        </div>
        <div class="trend-chart" data-testid="overview-trend-chart">
          <div v-for="d in trendDays" :key="d.date" class="trend-col">
            <div class="trend-col__value">{{ trendValueText(d) }}</div>
            <div class="trend-col__track">
              <div class="trend-col__bar" :style="{ height: trendBarPct(d) }"></div>
            </div>
            <div class="trend-col__label">{{ trendDayLabel(d.date) }}</div>
          </div>
        </div>
      </template>
    </section>

    <!-- ③ 常用操作 -->
    <section class="quick-actions" data-testid="overview-quick-actions">
      <button class="btn btn-secondary" data-testid="quick-add-provider" @click="go('/providers')">
        {{ t('simpleMode.overview.action.addProvider') }}
      </button>
      <button class="btn btn-secondary" data-testid="quick-sign-key" @click="openSignKey">
        {{ t('simpleMode.overview.action.signKey') }}
      </button>
      <button class="btn btn-secondary" data-testid="quick-requests" @click="openRequestLogs">
        {{ t('simpleMode.overview.action.requests') }}
      </button>
      <button class="btn btn-secondary" data-testid="quick-chat" @click="go('/chat')">
        {{ t('simpleMode.overview.action.chat') }}
      </button>
    </section>

    <!-- ④ 接入进度：三步都完成就自动消失 -->
    <section v-if="showOnboarding" class="onboarding" data-testid="overview-onboarding">
      <div class="onboarding-head">{{ t('simpleMode.overview.onboardingTitle') }}</div>
      <ol class="onboarding-steps">
        <li
          v-for="step in onboardingSteps"
          :key="step.key"
          class="onboarding-step"
          :class="step.done ? 'is-done' : 'is-pending'"
          :data-step="step.key"
          :data-done="step.done ? '1' : '0'"
        >
          <span class="step-dot" aria-hidden="true"></span>
          <span class="step-label">
            {{ step.done ? t(`simpleMode.overview.onboarding.${step.key}Done`, { count: step.count }) : t(`simpleMode.overview.onboarding.${step.key}`) }}
          </span>
        </li>
      </ol>
    </section>

    <!-- ⑤ 主体：接入清单 + 上游健康 -->
    <div class="overview-cols">
      <section class="card overview-panel" data-testid="overview-access-list">
        <div class="panel-head">
          <span class="panel-title">{{ t('simpleMode.accessList.title') }}</span>
          <span class="panel-hint">{{ t('simpleMode.accessList.subtitle') }}</span>
        </div>

        <div v-if="loading" class="empty">{{ t('common.feedback.loading') }}</div>
        <div v-else-if="keysFailed" class="empty">{{ t('simpleMode.overview.accessListUnavailable') }}</div>
        <div v-else-if="activeRows.length === 0 && inactiveRows.length === 0" class="empty">
          <p>{{ t('simpleMode.accessList.empty') }}</p>
          <p class="section-hint">{{ t('simpleMode.accessList.emptyHint') }}</p>
        </div>
        <template v-else>
          <!-- ① 最近在用（按最近一次使用排序）──────────────────────────────── -->
          <section class="client-list" data-testid="overview-client-recent">
            <div class="panel-head panel-head--sub">
              <span class="panel-subtitle">{{ t('simpleMode.overview.clientRecentTitle') }}</span>
              <span class="section-hint">{{ t('simpleMode.overview.clientRecentHint') }}</span>
            </div>
            <div class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{{ t('simpleMode.accessList.client') }}</th>
                    <th style="width:110px">{{ t('common.table.status') }}</th>
                    <th style="width:150px">{{ t('simpleMode.accessList.lastUsed') }}</th>
                    <th style="width:110px">{{ t('simpleMode.accessList.requests') }}</th>
                    <th style="width:90px"></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="r in recentList.rows" :key="r.id">
                    <td>
                      <strong>{{ r.name }}</strong>
                      <span class="section-hint">{{ r.key.key_prefix }}</span>
                    </td>
                    <td><span class="badge" :class="stateClass(r.stateKey)">{{ stateLabel(r.stateKey) }}</span></td>
                    <td>{{ r.lastUsedText }}</td>
                    <td>{{ r.requestsText }}</td>
                    <td>
                      <button class="btn btn-ghost btn-sm" @click="openDetail(r.key)">
                        {{ t('common.button.detail') }}
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <PaginationBar
              v-if="recentList.total > 0"
              :page="recentList.page"
              :page-size="recentList.pageSize"
              :total="recentList.total"
              :page-sizes="[]"
              @prev="turnRecentPage(-1)"
              @next="turnRecentPage(1)"
            />
          </section>

          <!-- ② 用得最多（按累计请求数排序）-------------------------------- -->
          <section class="client-list" data-testid="overview-client-busiest">
            <div class="panel-head panel-head--sub">
              <span class="panel-subtitle">{{ t('simpleMode.overview.clientBusiestTitle') }}</span>
              <span class="section-hint">{{ t('simpleMode.overview.clientBusiestHint') }}</span>
            </div>
            <div class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{{ t('simpleMode.accessList.client') }}</th>
                    <th style="width:110px">{{ t('simpleMode.accessList.requests') }}</th>
                    <th style="width:170px">{{ t('simpleMode.accessList.spend') }}</th>
                    <th style="width:110px">{{ t('common.table.status') }}</th>
                    <th style="width:90px"></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="r in busiestList.rows" :key="r.id">
                    <td>
                      <strong>{{ r.name }}</strong>
                      <span class="section-hint">{{ r.key.key_prefix }}</span>
                    </td>
                    <td>{{ r.requestsText }}</td>
                    <td>{{ r.spendText }} / {{ r.budgetText }}</td>
                    <td><span class="badge" :class="stateClass(r.stateKey)">{{ stateLabel(r.stateKey) }}</span></td>
                    <td>
                      <button class="btn btn-ghost btn-sm" @click="openDetail(r.key)">
                        {{ t('common.button.detail') }}
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <PaginationBar
              v-if="busiestList.total > 0"
              :page="busiestList.page"
              :page-size="busiestList.pageSize"
              :total="busiestList.total"
              :page-sizes="[]"
              @prev="turnBusiestPage(-1)"
              @next="turnBusiestPage(1)"
            />
          </section>

          <!-- ③ 已停用 / 未使用（折叠 + 分页）-------------------------------- -->
          <details v-if="inactiveRows.length > 0" class="fold" data-testid="overview-access-fold">
            <summary>
              <span>{{ t('simpleMode.overview.inactiveLabel') }}</span>
              <span class="count-badge">{{ inactiveRows.length }}</span>
            </summary>
            <div class="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>{{ t('simpleMode.accessList.client') }}</th>
                    <th style="width:110px">{{ t('common.table.status') }}</th>
                    <th style="width:110px">{{ t('simpleMode.accessList.requests') }}</th>
                    <th style="width:170px">{{ t('simpleMode.accessList.spend') }}</th>
                    <th style="width:150px">{{ t('simpleMode.accessList.lastUsed') }}</th>
                    <th style="width:90px"></th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="r in inactiveList.rows" :key="r.id">
                    <td>
                      <strong>{{ r.name }}</strong>
                      <span class="section-hint">{{ r.key.key_prefix }}</span>
                    </td>
                    <td><span class="badge" :class="stateClass(r.stateKey)">{{ stateLabel(r.stateKey) }}</span></td>
                    <td>{{ r.requestsText }}</td>
                    <td>{{ r.spendText }} / {{ r.budgetText }}</td>
                    <td>{{ r.lastUsedText }}</td>
                    <td>
                      <button class="btn btn-ghost btn-sm" @click="openDetail(r.key)">
                        {{ t('common.button.detail') }}
                      </button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <PaginationBar
              v-if="inactiveList.total > 0"
              :page="inactiveList.page"
              :page-size="inactiveList.pageSize"
              :total="inactiveList.total"
              :page-sizes="[]"
              @prev="turnInactivePage(-1)"
              @next="turnInactivePage(1)"
            />
          </details>

          <div class="card-toolbar">
            <span class="section-hint">{{ t('simpleMode.accessList.usageHint') }}</span>
          </div>
        </template>
      </section>

      <div class="overview-side">
        <section
          class="card overview-panel"
          data-testid="overview-provider-health"
          :data-total="providerHealth.total"
          :data-shown="providerHealth.rows.length"
          :data-hidden="providerHealth.hidden"
        >
          <div class="panel-head">
            <span class="panel-title">{{ t('simpleMode.overview.providerTitle') }}</span>
            <span v-if="!providerHealth.unavailable" class="count-badge">{{ providerHealth.usable }}</span>
          </div>

          <div v-if="providerHealth.unavailable" class="empty">
            {{ providersDenied ? t('simpleMode.overview.providerDenied') : t('simpleMode.overview.providerUnavailable') }}
          </div>
          <div v-else-if="providerHealth.total === 0" class="empty">
            {{ t('simpleMode.overview.providerEmpty') }}
          </div>
          <template v-else>
            <ul class="provider-health-list">
              <li v-for="row in providerHealth.rows" :key="row.id" class="provider-health-row">
                <span class="provider-name">{{ row.name }}</span>
                <span class="badge" :class="providerToneClass(row)">{{ providerToneLabel(row) }}</span>
                <span class="provider-count">{{ row.credentialCount }}</span>
              </li>
            </ul>
            <!-- ★ 明细被截断时必须**披露**（2026-11-17 审计轮撞出）：
                 徽标写「19 个上游可用」而列表里只有 3 行「可用」——
                 因为明细按严重度排序、只留前 12 行，被切掉的几乎全是健康的。
                 不披露 ⇒ 用户以为数据对不上。 -->
            <div v-if="providerHealth.hidden > 0" class="card-toolbar">
              <span class="section-hint" data-testid="overview-provider-truncated">
                {{
                  t('simpleMode.overview.providerMore', {
                    shown: providerHealth.rows.length,
                    hidden: providerHealth.hidden,
                  })
                }}
              </span>
            </div>
            <div class="card-toolbar">
              <span class="section-hint">
                {{ t('simpleMode.overview.providerSummary', {
                  usable: providerHealth.usable,
                  warning: providerHealth.warning,
                  unreachable: providerHealth.unreachable,
                  unknown: providerHealth.unknown,
                }) }}
              </span>
            </div>
          </template>
        </section>

        <section class="card overview-panel" data-testid="overview-top-models">
          <div class="panel-head">
            <span class="panel-title">{{ t('simpleMode.overview.topModelsTitle') }}</span>
            <span class="panel-hint">{{ t('simpleMode.overview.topModelsHint') }}</span>
          </div>
          <div v-if="summaryDegraded" class="empty">{{ t('simpleMode.overview.statUnavailable') }}</div>
          <div v-else-if="topModels.length === 0" class="empty">{{ t('simpleMode.overview.topModelsEmpty') }}</div>
          <ul v-else class="model-list">
            <li v-for="m in topModels" :key="m.key" class="model-row">
              <span class="model-name mono">{{ m.key }}</span>
              <span class="model-count">{{ m.requests.toLocaleString('en-US') }}</span>
              <!-- 今日花费列：owner 报障「关心消耗情况」⇒ 模型排行不能只有请求数
                  （pies.models 的 cost_usd 与专家面同源同口径） -->
              <span class="model-cost">{{ fmtUsd(m.cost_usd) }}</span>
            </li>
          </ul>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
.simple-overview {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-4);
}

/* ── 状态条 ────────────────────────────────────────────────────────────────
 * 状态点用「圆点 + 文字结论」两重信号：颜色不是唯一线索（色盲可读）。
 * 不用彩色侧边条（craft-floor 禁 ≥2px 彩色侧条）。 */
.status-bar {
  display: flex;
  align-items: flex-start;
  gap: var(--kx-space-3);
  padding: var(--kx-space-3) var(--kx-space-4);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-md);
  background: var(--kx-surface);
}

.status-dot {
  flex: none;
  width: 10px;
  height: 10px;
  margin-top: var(--kx-space-1);
  border-radius: 50%;
  background: var(--kx-muted);
}

.status-bar--ok .status-dot { background: var(--success); }
.status-bar--warn .status-dot { background: var(--warning); }
.status-bar--blocked .status-dot { background: var(--danger); }

.status-text {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  min-width: 0;
  flex: 1;
}

.status-headline {
  font-size: var(--kx-text-md);
  font-weight: 600;
  color: var(--kx-fg);
}

.status-detail {
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
}

.status-issues {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-1);
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
}

/* issue 行：文案 + 行内「去处理」动作（动作密度算在 issue 上，不占常用操作区配额） */
.status-issue {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  flex-wrap: wrap;
}

.status-issue__text {
  min-width: 0;
}

.status-issue__go {
  flex: none;
}

.overview-ops {
  display: flex;
  justify-content: flex-start;
}

/* ── 近 7 日消耗 ─────────────────────────────────────────────────────────── */
.trend-headwrap {
  display: flex;
  align-items: baseline;
  gap: var(--kx-space-3);
  flex-wrap: wrap;
  min-width: 0;
}

.trend-metric {
  flex-shrink: 0;
}

.trend-summary {
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
  margin-bottom: var(--kx-space-3);
  font-variant-numeric: tabular-nums;
}

.trend-chart {
  display: grid;
  grid-auto-flow: column;
  grid-auto-columns: 1fr;
  gap: var(--kx-space-3);
  align-items: end;
  max-width: 720px;
}

.trend-col {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--kx-space-1);
  min-width: 0;
}

.trend-col__value {
  font-size: var(--kx-text-xs);
  color: var(--kx-fg);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
}

.trend-col__track {
  width: 100%;
  max-width: 64px;
  height: 120px;
  display: flex;
  align-items: flex-end;
  justify-content: center;
  background: var(--kx-bg-accent);
  border-radius: var(--kx-radius-sm);
  overflow: hidden;
}

.trend-col__bar {
  width: 60%;
  min-height: 0;
  border-radius: var(--kx-radius-sm) var(--kx-radius-sm) 0 0;
  background: var(--accent);
  transition: height 0.2s var(--kx-motion-ease-out);
}

.trend-col__label {
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

/* ── 常用操作 ────────────────────────────────────────────────────────────── */
.quick-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--kx-space-2);
}

/* ── 接入进度 ────────────────────────────────────────────────────────────── */
.onboarding {
  padding: var(--kx-space-3) var(--kx-space-4);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-md);
  background: var(--kx-surface);
}

.onboarding-head {
  font-size: var(--kx-text-sm);
  font-weight: 600;
  color: var(--kx-muted);
  margin-bottom: var(--kx-space-2);
}

.onboarding-steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-wrap: wrap;
  gap: var(--kx-space-4);
}

.onboarding-step {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  font-size: var(--kx-text-sm);
}

.step-dot {
  flex: none;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--kx-border);
}

.onboarding-step.is-done .step-dot { background: var(--success); }
.onboarding-step.is-pending .step-dot { background: var(--warning); }

.onboarding-step.is-pending .step-label { color: var(--kx-fg); }
.onboarding-step.is-done .step-label { color: var(--kx-muted); }

/* ── 主体两栏 ────────────────────────────────────────────────────────────── */
.overview-cols {
  display: grid;
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
  gap: var(--kx-space-4);
  align-items: start;
}

.overview-side {
  display: flex;
  flex-direction: column;
  gap: var(--kx-space-4);
}

.overview-panel {
  padding: var(--kx-space-4);
}

.panel-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--kx-space-3);
  margin-bottom: var(--kx-space-3);
}

.panel-title {
  font-size: var(--kx-text-md);
  font-weight: 600;
  color: var(--kx-fg);
}

.panel-hint {
  font-size: var(--kx-text-xs);
  color: var(--kx-muted);
}

/* ── 客户端列表（两个分页列表）────────────────────────────────────────────
 * 2026-11-17：接入清单原来把在用密钥整表铺开（线上 118 行）⇒ 改成
 * 「最近在用」+「用得最多」两个分页列表，每页 10 条。 */
.client-list + .client-list {
  margin-top: var(--kx-space-4);
}

.panel-head--sub {
  margin-bottom: var(--kx-space-2);
}

.panel-subtitle {
  font-size: var(--kx-text-sm);
  font-weight: 600;
  color: var(--kx-fg);
}

/* ── 上游健康列表 ────────────────────────────────────────────────────────── */
.provider-health-list,
.model-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}

.provider-health-row,
.model-row {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  padding: var(--kx-space-2) 0;
  border-bottom: 1px solid var(--kx-border);
  font-size: var(--kx-text-sm);
}

.provider-name,
.model-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.provider-count,
.model-count {
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

/* 最常用模型行的今日花费列（固定宽，右对齐 —— 与请求数一起构成「量 × 钱」两列） */
.model-cost {
  flex: none;
  width: 88px;
  text-align: right;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

/* ── 折叠（已禁用 / 未使用）──────────────────────────────────────────────── */
.fold {
  margin-top: var(--kx-space-3);
}

.fold > summary {
  display: flex;
  align-items: center;
  gap: var(--kx-space-2);
  cursor: pointer;
  font-size: var(--kx-text-sm);
  color: var(--kx-muted);
  padding: var(--kx-space-2) 0;
}

.count-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 20px;
  height: 20px;
  padding: 0 var(--kx-space-1);
  border-radius: var(--kx-radius-sm);
  background: var(--kx-bg-accent);
  color: var(--kx-fg);
  font-size: var(--kx-text-xs);
  font-variant-numeric: tabular-nums;
}

/* ── 窄屏：两栏塌成一栏 ─────────────────────────────────────────────────── */
@media (max-width: 1024px) {
  .overview-cols {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 768px) {
  .status-bar {
    flex-direction: column;
    gap: var(--kx-space-2);
  }

  .onboarding-steps {
    flex-direction: column;
    gap: var(--kx-space-2);
  }
}
</style>
