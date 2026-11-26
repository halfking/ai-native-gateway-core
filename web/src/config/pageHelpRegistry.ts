/**
 * pageHelpRegistry.ts — 「页面右上角帮助」的路由→内容映射（2026-11-20）。
 *
 * 设计：
 * · 帮助图标由 AppTopbar 按当前 route.path 渲染；点击打开 PageHelpDrawer。
 * · 本文件是**唯一**的路由→帮助内容映射；PathHelpRegistry.test.ts 会拿
 *   router.ts 的真实路由表对拍，保证「有页面的路由都有帮助」不漂移。
 * · 内容本体是 web/content/page-help/<id>.zh-CN.md，编译期经 Vite
 *   `import.meta.glob(..., { eager: true, query: '?raw' })` 全量打包进
 *   首屏 bundle（正文是纯文本，单文件几 KB，总体 <100KB，无运行时网络请求）。
 * · 新增页面帮助的步骤：① 建 md 文件 ② 在此登记 { match, id, titleKey }。
 *   匹配用「路由 pattern」（vue-router 风格 :param / 通配尾段），注册表按
 *   声明顺序取第一个命中的条目（具体页在前、列表页在后）。
 *
 * 豁免清单（2026-11-20 L2 轮登记；未登记 ⇒ 不渲染帮助图标，属有意豁免）：
 * · 登录/维护等 public 页 —— AppTopbar 本就不挂载，天然无图标；
 * · redirect 型路由（如 /system-monitor、/maas/*、/routing）—— 落地后才
 *   有「页面」，帮助登记在目标路由上；
 * · 纯嵌入/弹层型视图（无独立菜单入口的抽屉、iframe 宿主页）—— 帮助入口
 *   在宿主页面；
 * · 后续新增页面若需要帮助：按上面「新增步骤」登记一条即可，钉测会同步
 *   校验 match 在 router.ts 里真实存在。
 */

export interface PageHelpEntry {
  /** vue-router 路径 pattern；支持 :param 段与通配尾段（a/b/*） */
  match: string
  /** 附加跨报表的统计与对账定义。 */
  statistics?: boolean
  /** 内容文件 id（= web/content/page-help/<id>.<locale>.md；8 语种，缺译文回退 zh-CN） */
  id: string
  /** 抽屉标题的语言键（i18n；zh-CN 提供中文，其余语种 parity 同步） */
  titleKey: string
}

const entries: PageHelpEntry[] = [
  // ── 总览与实时面 ──
  { match: '/dashboard', id: 'dashboard', titleKey: 'pageHelp.title.dashboard', statistics: true },
  // 看板 / 会话与统计两个页签的**统计标准**（2026-11-23 一致性取证轮）：
  // 两页的请求数与费用口径不同（请求级 vs 会话级）、窗口不同、探测剔除面
  // 不同 ⇒ 数字本来就不该相等。这篇正文把两页逐项口径与差异写成对照表，
  // 由两个页签**页面内嵌**的帮助按钮打开（match 是虚拟键，不是真实路由）。
  { match: '/dashboard-stats-standards', id: 'dashboard-stats-standards', titleKey: 'pageHelp.title.dashboardStatsStandards', statistics: true },
  // 简易模式总览：与 /dashboard 同一路由（HomeView 内嵌 SimpleOverviewView 按
  // useWorkMode 切换渲染）。match 用虚拟键 /dashboard-simple，AppTopbar 在简易
  // 模式下把路径改写成它 ⇒ 同一 URL 两份帮助正文互不覆盖。
  { match: '/dashboard-simple', id: 'dashboard-simple', titleKey: 'pageHelp.title.dashboardSimple', statistics: true },
  { match: '/live', id: 'live-topology', titleKey: 'pageHelp.title.liveTopology' },
  { match: '/live2', id: 'live-stage-tree', titleKey: 'pageHelp.title.liveStageTree' },
  { match: '/live/request/:requestId', id: 'request-detail', titleKey: 'pageHelp.title.requestDetail', statistics: true },

  // ── 请求与会话 ──
  { match: '/request-logs', id: 'request-logs', titleKey: 'pageHelp.title.requestLogs', statistics: true },
  { match: '/request-detail/:requestId', id: 'request-detail', titleKey: 'pageHelp.title.requestDetail', statistics: true },
  { match: '/admin/sessions/:id', id: 'session-detail', titleKey: 'pageHelp.title.sessionDetail', statistics: true },
  { match: '/admin/turns', id: 'turns-list', titleKey: 'pageHelp.title.turns', statistics: true },
  { match: '/admin/turns/filter-options', id: 'turns-filter-options', titleKey: 'pageHelp.title.turnsFilter' },
  { match: '/admin/request-actions/:id', id: 'request-actions', titleKey: 'pageHelp.title.requestActions' },
  { match: '/admin/request-registry', id: 'request-registry', titleKey: 'pageHelp.title.requestRegistry' },
  { match: '/admin/request-registry/journey/:requestId', id: 'request-journey', titleKey: 'pageHelp.title.requestJourney' },
  { match: '/admin/connection-registry', id: 'connection-registry', titleKey: 'pageHelp.title.connectionRegistry' },
  { match: '/admin/connection-registry/:credentialId/recovery', id: 'node-health-timeline', titleKey: 'pageHelp.title.nodeHealthTimeline' },
  { match: '/dispatch/waterfall', id: 'dispatch-waterfall', titleKey: 'pageHelp.title.dispatchWaterfall' },

  // ── 会话分析 ──
  // ★ 2026-11-23 L14 落地轮：★ 整合页（★ 上游 95e44dc15 新增的「Dashboard 会话统计
  // ＋ Top10 用户排行」）另立 URL 后，★ 它的帮助正文**沿用既有的
  // `session-analytics-overview`**（★ 那份正文描述的正是「核心指标卡 ＋ 趋势 ＋ 排名」
  // 这套内容 ⇒ ★ 与整合页的实际内容同型 ⇒ ★ **不需要新写 8 语种正文**）。
  // ★ ★ 顺序要紧：★ 本表「按声明顺序取第一个命中的条目」⇒ ★ 静态段 `/users/overview`
  // ★ **必须排在**参数段 `/users/:owner` **之前** ⇒ ★ 否则 `overview` 会被
  // ★ `:owner` 抢先匹配 ⇒ ★ 整合页会拿到「用户详情页」的帮助正文。
  // ★ ★ statistics 标记取上游 aac6602ab 轮（★ 上游给本族 9 条统一加了
  // ★ `statistics: true`）★ ⇒ ★ 本分支保留自己的**静态段前置**、★ 同时采上游
  // ★ 的**统计标记** ⇒ ★ 两边语义都保留、★ 互不覆盖。
  { match: '/admin/session-analytics/users/overview', id: 'session-analytics-overview', titleKey: 'pageHelp.title.sessionAnalyticsOverview', statistics: true },
  { match: '/admin/session-analytics/users/:owner', id: 'session-user-detail', titleKey: 'pageHelp.title.sessionUserDetail', statistics: true },
  { match: '/admin/session-analytics/users', id: 'session-users', titleKey: 'pageHelp.title.sessionUsers', statistics: true },
  { match: '/admin/session-analytics/user-profiles', id: 'session-users', titleKey: 'pageHelp.title.sessionUsers', statistics: true },
  { match: '/admin/session-analytics/clients/:id', id: 'session-client-detail', titleKey: 'pageHelp.title.sessionClientDetail', statistics: true },
  { match: '/admin/session-analytics/tasks/:id', id: 'session-task-detail', titleKey: 'pageHelp.title.sessionTaskDetail', statistics: true },
  { match: '/admin/session-analytics/user-ranking/:owner', id: 'session-user-detail', titleKey: 'pageHelp.title.sessionUserDetail', statistics: true },
  { match: '/admin/session-analytics/user-ranking', id: 'session-ranking', titleKey: 'pageHelp.title.sessionRanking', statistics: true },
  { match: '/admin/session-analytics/agent-ranking', id: 'session-ranking', titleKey: 'pageHelp.title.sessionAgentRanking', statistics: true },
  { match: '/admin/session-analytics/model-ranking', id: 'session-ranking', titleKey: 'pageHelp.title.sessionModelRanking', statistics: true },
  { match: '/admin/session-analytics', id: 'session-analytics-overview', titleKey: 'pageHelp.title.sessionAnalyticsOverview', statistics: true },
  { match: '/admin/session-analytics/filter-options', id: 'filter-options', titleKey: 'pageHelp.title.filterOptions' },

  // ── 供应商与模型 ──
  { match: '/providers/:id', id: 'provider-detail', titleKey: 'pageHelp.title.providerDetail', statistics: true },
  { match: '/providers', id: 'providers', titleKey: 'pageHelp.title.providers' },
  { match: '/models', id: 'models', titleKey: 'pageHelp.title.models' },
  { match: '/routing-v2', id: 'routing-dashboard', titleKey: 'pageHelp.title.routingOverview' },
  { match: '/routing-v2/credentials', id: 'credential-monitor', titleKey: 'pageHelp.title.credentialMonitor' },
  { match: '/probe-health/detail', id: 'probe-health-detail', titleKey: 'pageHelp.title.probeHealthDetail' },
  { match: '/routing-v2/work-types/:key', id: 'work-types', titleKey: 'pageHelp.title.workTypes' },
  { match: '/routing-v2/work-types', id: 'work-types', titleKey: 'pageHelp.title.workTypes' },
  { match: '/routing-v2/auto-ops', id: 'auto-routing-ops', titleKey: 'pageHelp.title.autoOps' },
  { match: '/routing-policy', id: 'routing-policy', titleKey: 'pageHelp.title.routingPolicy' },
  { match: '/free-pool', id: 'free-pool', titleKey: 'pageHelp.title.freePool' },
  { match: '/free-discovery', id: 'free-discovery', titleKey: 'pageHelp.title.freeDiscovery' },
  { match: '/correlations', id: 'correlations', titleKey: 'pageHelp.title.correlations' },
  { match: '/quality-correlations', id: 'quality-correlations', titleKey: 'pageHelp.title.qualityCorrelations' },
  { match: '/routing/overrides/audit', id: 'routing-audit', titleKey: 'pageHelp.title.routingAudit' },
  { match: '/routing/overrides', id: 'routing-override', titleKey: 'pageHelp.title.routingOverride' },
  { match: '/routing-overview', id: 'routing-overview', titleKey: 'pageHelp.title.routingOverview' },
  { match: '/routing-decisions', id: 'routing-decisions', titleKey: 'pageHelp.title.routingDecisions' },

  // ── 密钥与用户（含简易模式） ──
  { match: '/simple/keys', id: 'simple-keys', titleKey: 'pageHelp.title.simpleKeys' },
  { match: '/keys/:id', id: 'key-detail', titleKey: 'pageHelp.title.keyDetail' },
  { match: '/keys', id: 'keys', titleKey: 'pageHelp.title.keys' },
  { match: '/users', id: 'users', titleKey: 'pageHelp.title.users' },
  { match: '/tenants/:tenantId', id: 'tenant-detail', titleKey: 'pageHelp.title.tenantDetail' },
  { match: '/tenants', id: 'tenants', titleKey: 'pageHelp.title.tenants' },
  { match: '/key-applications', id: 'key-applications', titleKey: 'pageHelp.title.keyApplications' },
  { match: '/audit-logs', id: 'audit-logs', titleKey: 'pageHelp.title.auditLogs' },

  // ── 租户门户（我的服务） ──
  { match: '/tenant/models', id: 'tenant-models', titleKey: 'pageHelp.title.tenantModels' },
  { match: '/tenant/account', id: 'tenant-account', titleKey: 'pageHelp.title.tenantAccount', statistics: true },
  { match: '/tenant/bills', id: 'tenant-bills', titleKey: 'pageHelp.title.tenantBills', statistics: true },
  { match: '/tenant/pricing', id: 'tenant-pricing', titleKey: 'pageHelp.title.tenantPricing' },
  { match: '/tenant/orders/:id', id: 'tenant-order', titleKey: 'pageHelp.title.tenantOrder' },
  { match: '/tenant/usage', id: 'tenant-usage', titleKey: 'pageHelp.title.tenantUsage', statistics: true },

  // ── 运营管理 ──
  { match: '/admin/board-operational', id: 'board-operational', titleKey: 'pageHelp.title.boardOperational', statistics: true },
  { match: '/admin/board-operational/:dimension', id: 'usage-breakdown', titleKey: 'pageHelp.title.usageBreakdown', statistics: true },
  { match: '/admin/analytics/:dimension/:id', id: 'usage-breakdown', titleKey: 'pageHelp.title.usageBreakdown', statistics: true },
  { match: '/admin/ops-overview', id: 'ops-overview', titleKey: 'pageHelp.title.opsOverview', statistics: true },
  { match: '/pricing', id: 'pricing-management', titleKey: 'pageHelp.title.pricing' },
  { match: '/model-pricing', id: 'model-pricing', titleKey: 'pageHelp.title.modelPricing' },
  { match: '/admin/reconciliation', id: 'reconciliation', titleKey: 'pageHelp.title.reconciliation', statistics: true },
  { match: '/operations/sales', id: 'sales-workbench', titleKey: 'pageHelp.title.salesWorkbench' },
  { match: '/operations/marketing', id: 'operations-marketing', titleKey: 'pageHelp.title.operationsMarketing' },
  { match: '/operations/market-prices', id: 'market-prices', titleKey: 'pageHelp.title.marketPrices' },
  { match: '/admin/usage', id: 'usage-cost', titleKey: 'pageHelp.title.usageCost', statistics: true },
  { match: '/admin/usage-trends', id: 'usage-trends', titleKey: 'pageHelp.title.usageTrends', statistics: true },
  { match: '/admin/usage-enhanced', id: 'usage-enhanced', titleKey: 'pageHelp.title.usageEnhanced', statistics: true },
  { match: '/admin/subscription-tiers', id: 'subscription-tiers', titleKey: 'pageHelp.title.subscriptionTiers' },
  { match: '/admin/approvals/:id', id: 'approval-detail', titleKey: 'pageHelp.title.approvalDetail' },
  { match: '/admin/approvals', id: 'approval-list', titleKey: 'pageHelp.title.approvals' },
  { match: '/admin/approval-config', id: 'approval-config', titleKey: 'pageHelp.title.approvalConfig' },

  // ── 数据运维 ──
  { match: '/admin/data-lifecycle/migration-state', id: 'storage-migration-state', titleKey: 'pageHelp.title.storageMigrationState' },
  { match: '/admin/data-lifecycle', id: 'data-lifecycle', titleKey: 'pageHelp.title.dataLifecycle' },
  { match: '/admin/storage-and-log-config', id: 'storage-and-log-config', titleKey: 'pageHelp.title.storageAndLogConfig' },
  { match: '/admin/compression', id: 'compression', titleKey: 'pageHelp.title.compression' },
  { match: '/admin/sanitize-rules', id: 'sanitize-rules', titleKey: 'pageHelp.title.sanitizeRules' },
  { match: '/admin/output-compliance', id: 'output-compliance', titleKey: 'pageHelp.title.outputCompliance' },
  { match: '/format-anomalies', id: 'format-anomalies', titleKey: 'pageHelp.title.formatAnomalies' },
  { match: '/model-integrity', id: 'model-integrity', titleKey: 'pageHelp.title.modelIntegrity' },
  { match: '/model-credibility', id: 'model-credibility', titleKey: 'pageHelp.title.modelCredibility' },
  { match: '/admin/settings', id: 'settings', titleKey: 'pageHelp.title.settings' },
  { match: '/admin/agents', id: 'agent-registry', titleKey: 'pageHelp.title.agents' },
  { match: '/admin/modules', id: 'modules', titleKey: 'pageHelp.title.modules' },
  { match: '/admin/prompt-injection', id: 'prompt-injection', titleKey: 'pageHelp.title.promptInjection' },
  { match: '/admin/pi-analysis', id: 'pi-analysis', titleKey: 'pageHelp.title.piAnalysis' },
  { match: '/admin/plugin-settings', id: 'plugin-settings', titleKey: 'pageHelp.title.pluginSettings' },
  { match: '/admin/self-update', id: 'self-update', titleKey: 'pageHelp.title.selfUpdate' },
  { match: '/admin/proxy', id: 'proxy', titleKey: 'pageHelp.title.proxy' },

  // ── 接入指南 ──
  { match: '/examples', id: 'examples', titleKey: 'pageHelp.title.examples' },
  { match: '/help', id: 'help-manual', titleKey: 'pageHelp.title.helpManual' },
  { match: '/chat', id: 'chat', titleKey: 'pageHelp.title.chat' },
]

/** 有序快照；匹配时按声明顺序取第一个命中（具体页在前、通用页在后）。 */
export const PAGE_HELP_ENTRIES: readonly PageHelpEntry[] = entries

/**
 * 解析当前路径对应的帮助条目。
 * @param routePath 当前 router 的 path（含 :param 形式的 pattern 或真实路径均可）
 */
export function resolvePageHelp(routePath: string): PageHelpEntry | null {
  if (!routePath || !routePath.startsWith('/')) return null
  const bare = routePath.split('?')[0].split('#')[0]
  for (const entry of PAGE_HELP_ENTRIES) {
    if (pathMatches(entry.match, bare)) return entry
    // 允许调用方直接传 pattern（测试便利），也允许传真实路径命中 pattern 条目
    if (entry.match === bare) return entry
  }
  return null
}

/**
 * 按内容 id 解析帮助条目（2026-11-23 统计标准轮）。
 *
 * 页面内嵌的帮助按钮不在路由上（例如「看板」页签里的按钮打开的是
 * 「看板与会话统计的统计标准」这篇正文），它们没有自己的路由 ⇒ 路由
 * 解析用不上。页面内嵌时按**内容 id** 取条目（标题键也由注册表给出，
 * 页面不自带文案 ⇒ 语种切换只有一个来源）。
 *
 * @param id 内容 id（= web/content/page-help/<id>.<locale>.md 的 <id>）
 */
export function resolvePageHelpById(id: string): PageHelpEntry | null {
  if (!id) return null
  for (const entry of PAGE_HELP_ENTRIES) {
    if (entry.id === id) return entry
  }
  return null
}

/**
 * vue-router 风格的路径匹配：
 * · 逐段比较；pattern 段 `:name` 匹配任意非空段
 * · 尾段 `*`（如 `/a/b/*`）匹配「任意剩余段数」（含 0 段）
 * · 末尾斜杠视为等价
 */
export function pathMatches(pattern: string, path: string): boolean {
  const norm = (p: string) => p.replace(/\/+$/, '') || '/'
  const pSegs = norm(pattern).split('/').filter(Boolean)
  const uSegs = norm(path).split('/').filter(Boolean)
  let i = 0
  for (; i < pSegs.length; i++) {
    const seg = pSegs[i]
    if (seg === '*') return true // 通配尾段：剩余全收
    if (i >= uSegs.length) return false
    if (seg.startsWith(':')) continue // 参数段：任意值
    if (seg !== uSegs[i]) return false
  }
  return i === uSegs.length
}
