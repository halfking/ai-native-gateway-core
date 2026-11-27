/** Sidebar navigation — grouped menus with role / tenant visibility flags. */

import { showOpsPlatform } from './edition'
import { isHiddenNavPath, HIDDEN_NAV_PATHS } from './navHidden'

// 2026-11-27：re-export 给构建链（export-menu-config.mjs）——menu-config.json
// 与渲染层共用同一份隐藏清单，下游（maintain-web 内嵌兜底）不再宣传已隐藏入口。
export { HIDDEN_NAV_PATHS }

export type NavItem = {
  path: string
  label?: string
  labelKey?: string  // i18n key, if present will be used via t(labelKey)
  icon: string
  /** super_admin only */
  super?: boolean
  /** super_admin + default tenant (platform ops) */
  platformOps?: boolean
  /**
   * 2026-09-04: 供应商控制台 —— super_admin 或 default 租户 tenant_admin。
   * 后端 ProviderConsoleMiddleware 对 tenant_admin 放行只读 + 凭据
   * API Key 轮换，其余写操作仍 super_admin 专属。
   */
  providerConsole?: boolean
  /** Non-default tenant portal only */
  tenantOnly?: boolean
  /** Hidden when logged in as non-default tenant (tenant_admin) */
  hideForTenant?: boolean
  /**
   * Ops-center entries owned by ai-native-maintain.
   * Hidden unless Maintain is detected at runtime (or forced via env).
   */
  opsPlatform?: boolean
  /**
   * ★ 2026-11-18 审计轮 P2：依赖运行时可用性探测的菜单项
   * （当前只有 /admin/pi-analysis）。
   *
   * 该页背后的 `/api/admin/pi-analysis/*` 只在「有 DB」且
   * 「admin 鉴权材料齐备」时才注册（后端 fail-closed）⇒
   * 无 DB / 无 secret 的部署上点了菜单项就是一个 404 空页。
   * ⇒ 探测到不可用时**隐藏该菜单项**（不再宣传一个不存在的功能）。
   * 口径见 config/piAnalysis.ts。
   */
  piAnalysis?: boolean
  /**
   * 2026-07-23: Plugin 模式标记
   * 例如 'ai-session-manager' — 由对应 plugin 的软注入探测控制显示
   * 类似 opsPlatform，但用于非运维类的 plugin（会话管理、聊天等）
   */
  plugin?: string
  /**
   * Same-origin path owned by ai-native-maintain SPA (`/maintain/*`).
   * Render as a full-page `<a href>` instead of Vue RouterLink.
   */
  external?: boolean
  /** Only highlight when the path matches exactly (no prefix matching).
   *  Use for items whose path is a prefix of another item's path, e.g.
   *  '/routing-v2' (路由全景) vs '/routing-v2/credentials' (凭据监控) —
   *  without this both highlight at once on the credentials page. */
  exact?: boolean
  /** Only show when system is not activated (for activation-related pages) */
  notActivatedOnly?: boolean
  /**
   * 2026-09-02: 当系统未激活时,该菜单项会被渲染成「前往激活」的按钮,
   * 目标路径替换为 /customer/update-activate;激活后恢复为正常菜单项。
   * 用于: 自动升级、License 管理等需要 License 的运维入口,
   * 避免未激活用户点进去看不到 license / 升级通道。
   */
  activateWhenNotActivated?: boolean
  /**
   * ★ 2026-11-27 非管理员菜单重构轮：管理面标记。
   *
   * 带此标记的条目 = 「管理面菜单」—— 运营管理工具、运维中心、系统设置
   * 等**管理职责**入口。只有管理视角可见：
   *   - super_admin（平台管理员）
   *   - default 租户的 tenant_admin（平台侧协管，沿用 isPlatformOps 视角）
   * 非 default 租户门户与普通 role=user 一律隐藏。
   *
   * 为什么是「标记」而不是把条目挪进某个组：管理员现有工作流不动
   * （操作习惯零回归），非管理员看到的是**按角色裁剪后**的形态；
   * 同一条目不需要在两份真源里各写一遍（那是漂移温床）。
   */
  management?: boolean
  /**
   * ★ 2026-11-27 非管理员菜单重构轮：用户面标记 —— 所有已登录用户可见，
   * 数据范围由后端按角色收窄（super→平台 / tenant_admin→本租户 / user→本人）。
   * 与 management 互斥使用：不带任何标记的条目维持原有角色语义不变。
   */
  userFacing?: boolean
}

export type NavGroup = {
  id: string
  label?: string
  labelKey?: string  // i18n key for group title
  items: NavItem[]
}

/** Top-level sidebar links (no accordion group). Used for default-tenant platform ops. */
export const NAV_PRIMARY_ITEMS: NavItem[] = [
  // 2026-11-19: 简易模式顶栏顺序调整 —— 总览是首页，放最左；实时拓扑次之。
  // 按使用频率排序：总览（首页） → 实时拓扑 → 阶段树。
  // ★ 2026-11-27 非管理员菜单重构轮：总览与实时拓扑改为 userFacing ——
  //   所有已登录用户可见（owner 指令「将原来的总览放出来…并且保留实时拓扑」）。
  //   数据面后端已收窄：board/usage v2 走 requestStatsV2Scope 三档
  //   （super→平台 / tenant_admin→本租户 / user→本租户+本人）；
  //   live SSE user 档已收进本租户键（本轮 P0 修复）。
  { path: '/dashboard', labelKey: 'nav.item.overview', icon: '📊', platformOps: true, userFacing: true, exact: true },
  // 2026-10-16: live-topology home. Placed before "Overview" — it is a NEW entry;
  // `/` and `/dashboard` stay untouched (the old home page is fully preserved, ADR §7 G7).
  { path: '/live', labelKey: 'nav.item.liveTopology', icon: '🛰', platformOps: true, userFacing: true, exact: true },
  // 2026-11-16: 请求阶段树（live2）—— 与 /live 并存一个版本周期。
  // 入口放在 /live 旁边（同一分组），并由 /live 页顶栏的「阶段树」按钮直达。
  // 2026-11-17: owner 要求暂时隐藏菜单（路由/页面保留；直达 /live2 仍可用）。
  // 2026-11-17（owner「打开功能」轮）：恢复可见 —— hidden 闸门整体退役。
  //   ★ 为什么**整条退役**而不是留一个恒 false 的 `hidden: false`：
  //     「开关本体」只有这一个消费者，留着一个没人用的闸门，下一轮就会有人
  //     拿它当「以后还能临时藏一下」的正式开关 —— 而 2026-11-17 那次「暂时隐藏」
  //     正是这么来的（藏了一天 ⇒ 就要再藏 ⇒ 又要恢复）。⇒ 没有第二消费者的
  //     布尔闸门不是资产，是负债：它把「一次决定」伪装成「一个机制」。
  { path: '/live2', labelKey: 'nav.item.liveStageTree', icon: '🌲', platformOps: true, exact: true },
]

/**
 * 2026-09-02: 当一个菜单项被 `activateWhenNotActivated` 标记且系统未激活时,
 * 点击该菜单项会被路由到本路径。它是激活向导页面的统一入口。
 */
export const ACTIVATE_REDIRECT_PATH = '/customer/update-activate'

/**
 * 2026-09-02: 根据当前激活状态决定一个菜单项最终渲染的路径与样式。
 * 返回值供 AppTopbar 等渲染层直接使用:
 *   - `path`: 跳转目标(未激活时替换为 ACTIVATE_REDIRECT_PATH)
 *   - `activateAction`: 是否以「激活」CTA 形式渲染(粗体 / 强调色 / 右上角角标)
 *   - `originalPath`: 原始路径(只在 activateAction=true 时有意义,用于 tooltip 提示)
 */
export function resolveNavItemActivation(
  item: NavItem,
  opts: { isActivated?: boolean },
): {
  path: string
  labelKey?: string
  label?: string
  icon: string
  external?: boolean
  exact?: boolean
  activateAction: boolean
  originalPath: string
} {
  const base = {
    labelKey: item.labelKey,
    label: item.label,
    icon: item.icon,
    external: item.external,
    exact: item.exact,
    originalPath: item.path,
  }
  if (item.activateWhenNotActivated && opts.isActivated === false) {
    return {
      ...base,
      path: ACTIVATE_REDIRECT_PATH,
      activateAction: true,
    }
  }
  return {
    ...base,
    path: item.path,
    activateAction: false,
  }
}

export const NAV_GROUPS: NavGroup[] = [
  // 2026-11-04 W1-T1（运营管理与价格计费体系，总体设计 §2.6 / AF-2/AF-5 修订）：
  // 「运营管理」组 —— 一节两态（租户看「我的服务」消费面，平台看「运营工具」
  // 设定面）。组 id `operations`（/ops/* 前缀已被 opsplatform 占用 ⇒ AF-2）；
  // tenant-portal 组退役（A-13），其 5 项原 path 迁入（收藏链接不断）；
  // /pricing、/model-pricing、/admin/reconciliation 自 models-routing 迁入
  // （path 一律不变）；/admin/board-operational 收编为运营总览并同步移出
  // navCoverage KNOWN_ORPHANS（AF-5；★ 后端 tenant 隔离遗留面**已在 2026-11-04
  // 12h 审计轮 D-1 降为 superAdmin 档**（/api/admin/dashboard/operational）⇒
  // 这条 super-only 门控**现在才成立**；载荷内部仍是平台级聚合，RLS 化改造登记待办）。
  {
    id: 'operations',
    labelKey: 'nav.group.operations',
    items: [
      // ── 我的服务（非 default 租户可见）──
      // ★ 2026-11-27 非管理员菜单重构轮：
      //   /tenant/pricing（套餐与充值）owner 指令隐藏 —— 走 navHidden 路径清单
      //   （隐藏 ≠ 删除，直链仍可用；菜单不宣传充值入口）。
      { path: '/tenant/models', labelKey: 'nav.item.tenantModels', icon: '🤖', tenantOnly: true },
      { path: '/tenant/pricing', labelKey: 'nav.item.tenantPricing', icon: '💳', tenantOnly: true },
      { path: '/tenant/usage', labelKey: 'nav.item.tenantUsage', icon: '📉', tenantOnly: true },
      { path: '/tenant/bills', labelKey: 'nav.item.tenantBills', icon: '🧾', tenantOnly: true },
      { path: '/tenant/account', labelKey: 'nav.item.tenantAccount', icon: '💰', tenantOnly: true },
      // ── 运营工具（平台可见）──
      // ★ 2026-11-27：以下平台运营工具全部加 management 标记 ——
      //   非 default 租户门户与普通 user 不再看到「运营管理」组（owner 指令：
      //   「非管理员不需要运营管理的功能」）；super/default-tenant 协管视角不变。
      { path: '/admin/board-operational', labelKey: 'nav.item.operationsOverview', icon: '🧭', super: true, hideForTenant: true, management: true },
      { path: '/pricing', labelKey: 'nav.item.pricing', icon: '📉', platformOps: true, hideForTenant: true, management: true },
      { path: '/model-pricing', labelKey: 'nav.item.modelPricing', icon: '💰', platformOps: true, hideForTenant: true, management: true },
      // 结算与对账：页面内含 provider/internal 双视角页签（ReconciliationReport.vue
      // view === 'internal'）⇒ tenant-users 组的 ?view=internal 独立菜单项删除
      // （防双入口，W1-T3）；此处直开默认 provider 视角。
      { path: '/admin/reconciliation', labelKey: 'nav.item.reconciliation', icon: '🧾', super: true, hideForTenant: true, management: true },
      // 2026-11-05 W1-T2 前端轮：报价系统 MVP 工作台（供给池/需求池/报价单；
      // 内部运营面 requiresAuth，供给登记写权限是 super-only（应用层闸），
      // 需求/报价动作带 owner 语义 ⇒ 菜单项不设 super 门，由服务端按角色收）。
      { path: '/operations/sales', labelKey: 'nav.item.salesWorkbench', icon: '💼', hideForTenant: true, management: true },
      // 2026-11-09 运营审计轮项4：营销管理（套餐/加油包/优惠码/活动；superAdmin）。
      { path: '/operations/marketing', labelKey: 'nav.item.operationsMarketing', icon: '🎯', platformOps: true, hideForTenant: true, management: true }, // 2026-11-12 A-8 权限对齐：super → platformOps（与组内 pricing 页同档）
      //2026-11-13 A-17 收口 M3-T1：市场价格采集面（registry 管理 + 同步台账）。2026-11-19 合并轮：价格对比面并入 /model-pricing（菜单更名「比较与定价」），price-compare菜单项删除。
      { path: '/operations/market-prices', labelKey: 'nav.item.marketPrices', icon: '🌐', platformOps: true, hideForTenant: true, management: true },
    ],
  },
  {
    id: 'models-routing',
    labelKey: 'nav.group.modelsRouting',
    items: [
      // ★ 2026-11-27 非管理员菜单重构轮（owner 指令：「供应商与模型」组保留
      //   「模型与目录」「路由全景」给非管理员，只读查询）：
      //   ① /models 与 /routing-v2 加 userFacing —— 所有已登录用户可见。
      //     页面内写操作仍由 isSuperAdmin 守卫（RoutingDashboardView 已有；
      //     ModelsView 本轮补只读守卫）；后端写闸本轮已收严为「非 super 拦写」。
      //     注意：/routing-v2 后端 analytics 五端点仍是 superAdmin 档 ⇒ 普通用户
      //     打开路由全景页时 KPI 卡会 403 降级（页面已按错误处理渲染其余面板），
      //     数据面放开是 owner 决策项，本轮先收菜单形态（只读面板可渲染部分）。
      //   ② 其余管理工具（凭据监控/自动路由闭环/供应商控制台/用量趋势/FreeLLM）
      //     加 management 或维持原有闸。
      { path: '/models', labelKey: 'nav.item.models', icon: '🏷️', platformOps: true, hideForTenant: true, userFacing: true },
      { path: '/routing-v2', labelKey: 'nav.item.routingOverview', icon: '🗺️', super: true, hideForTenant: true, exact: true, userFacing: true },
      { path: '/routing-v2/credentials', labelKey: 'nav.item.credentialMonitor', icon: '📊', management: true },
      // 2026-10-02 整合轮：原四项（人工标注/标注统计/任务档案/路由调参）收敛
      // 为 AUTO 路由闭环统一工作台。标注两页签全员可用；任务档案/调参审批
      // 页签 super_admin 专属（宿主 AutoRoutingOpsView normalizeTab 门控）。
      { path: '/routing-v2/auto-ops', labelKey: 'nav.item.autoOps', icon: '♻️', management: true },
      // 2026-09-07: /probe-health folded into 凭据监控「探测健康」tab
      // (/routing-v2/credentials?tab=probe-health); standalone menu entry removed.
      // 2026-10-02: 「系统自检」菜单入口去掉（看板横条仍可进 /dashboard?tab=selfcheck）。
      { path: '/providers', labelKey: 'nav.item.providers', icon: '🔌', providerConsole: true },
      // 2026-09-26 审计轮 F1：对账报表落地轮漏挂菜单入口（只能手敲 URL），
      // 本项 = 供应商结算对帐（goal #2）。页面内含 provider/internal 双视角
      // 切换；结算报表（internal 视角）入口见「租户用户」组同名项。
      // 2026-10-02 看板轮：全页用量趋势分析（按模型分线 + 供应商/租户/API Key/
      // 模型/指标过滤）。看板「用量趋势」卡时间范围右侧「更多」按钮跳同一页；
      // ★ 2026-11-27：用量趋势对非管理员放开（owner 指令）；核心数据链
      //   /api/usage/v2/* 走 requestStatsV2Scope 三档收窄（user=本人），
      //   零后端改动。侧栏供应商/密钥下拉 403 时前端容错隐藏（本轮补）。
      { path: '/admin/usage-trends', labelKey: 'nav.item.usageTrends', icon: '📈', super: true, hideForTenant: true, userFacing: true },
      // 2026-11-04 W1-T1：/pricing、/model-pricing、/admin/reconciliation 迁入
      // 「运营管理」组（operations，见上）—— path 不变，仅组归属变化。
      // 2026-11-02 S0：免费资源/免费发现归档 → 外链 freellm.itestu.cn（统一走网关会话 auth_request）
      // ★ 2026-11-27：FreeLLM 免费模型对非管理员放开（owner 指令）。
      { path: 'https://freellm.itestu.cn', labelKey: 'nav.item.freellm', icon: '🎁', super: true, hideForTenant: true, external: true, userFacing: true },
    ],
  },
  {
    id: 'tenant-users',
    labelKey: 'nav.group.tenantUsers',
    items: [
      { path: '/tenants', labelKey: 'nav.item.tenants', icon: '🏢', super: true, hideForTenant: true },
      // 三级配额制（2026-11-27）：组织管理入口（列表 /orgs；详情 /orgs/:id 不挂菜单）。
      { path: '/orgs', labelKey: 'nav.item.orgs', icon: '🏛️', super: true, hideForTenant: true },
      { path: '/users', labelKey: 'nav.item.users', icon: '👤' },
      { path: '/keys', labelKey: 'nav.item.keys', icon: '🔑' },
      { path: '/key-applications', labelKey: 'nav.item.keyApplications', icon: '📬', super: true, hideForTenant: true },
      { path: '/audit-logs', labelKey: 'nav.item.auditLogs', icon: '📋', super: true, hideForTenant: true },
      // 2026-11-04 W1-T3：结算报表独立菜单项删除 —— /admin/reconciliation
      // 页内已有 provider/internal 双视角页签，双入口防重（原入口并入
      // 「运营管理」组的结算与对账一项）。
    ],
  },
  {
    id: 'requests-sessions',
    labelKey: 'nav.group.requestsSessions',
    items: [
      // ★ 2026-11-27 非管理员菜单重构轮（owner 指令）：
      //   「请求与会话」不再暴露 我的会话(/my/sessions)、我的用量(/my/usage)、
      //   会话分析中心(/admin/session-analytics/users) 等子菜单；
      //   「用户会话分析」(/admin/session-analytics/users/overview) 更名
      //   「我的会话」（i18n 键 nav.item.sessionAnalyticsUsers 的值本轮更新）。
      //   数据面三档收窄已就绪（v2/users、overview 排行卡 user 档 403 是
      //   后端既有闸；本轮 overview 对 user 放开属 owner 决策项，先收菜单）。
      { path: '/my/sessions', labelKey: 'nav.item.mySessions', icon: '🗂️', management: true },
      { path: '/my/usage', labelKey: 'nav.item.myUsage', icon: '📈', management: true },
      { path: '/request-logs', labelKey: 'nav.item.requestLogs', icon: '📋' },
      { path: '/dispatch/waterfall', labelKey: 'nav.item.dispatchWaterfall', icon: '📊', platformOps: true, hideForTenant: true, management: true },
      { path: '/admin/turns', labelKey: 'nav.item.turns', icon: '🔄', super: true, hideForTenant: true, management: true },
      // 2026-10-01 R85-C P1：会话聚合观测「可交付未交付」——
      // cmd/gateway/main.go:6901-6906 的 /api/admin/session-analytics 三端点
      // 与真库 session_dim（121 万行）均已就绪，但本组原先没有任何入口挂载，
      // 导致 router.ts:257-258 的 /admin/session-analytics/users 与
      // /users/:owner 成为硬孤儿路由（只能手敲 URL）。
      // 挂载列表页即让整族可达：/clients/:id 与 /tasks/:id 是其子路由，
      // 由 UserProfileListView / UserProfileView 内部跳转。
      // ★ 2026-11-27：本项（会话分析中心）从非管理员菜单隐藏（management）。
      { path: '/admin/session-analytics/users', labelKey: 'nav.item.sessionAnalytics', icon: '📊', management: true },
      // ★ 2026-11-23 L14 轮：★ 整合页（★ 上游 95e44dc15 新增的「Dashboard 会话统计
      // ＋ Top10 用户排行」）另立菜单项 ⇒ ★ 它原先**没有**任何菜单入口
      // （★ 上游只把本 URL 从列表页改指过去 ⇒ ★ 导航能到、★ 但代价是列表页能力消失）。
      // ★ 标签键用上游已加好的 `nav.item.sessionAnalyticsUsers`
      // ⇒ ★ **不新增任何 i18n 键**。
      // ★ 2026-11-27（owner 指令）：本项对非管理员**保留**且更名「我的会话」
      //   （zh-CN 词条本轮更新；其余语种同键同步）。
      { path: '/admin/session-analytics/users/overview', labelKey: 'nav.item.sessionAnalyticsUsers', icon: '📈' },
      // T9 — 请求注册表 + 连接注册台（mock stage）
      { path: '/admin/request-registry', labelKey: 'nav.item.requestRegistry', icon: '📑', super: true, hideForTenant: true, management: true },
      { path: '/admin/connection-registry', labelKey: 'nav.item.connectionRegistry', icon: '🔗', super: true, hideForTenant: true, management: true },
      // V5.1: plugin-runtime nav (ai-session-manager etc.) is now injected
      // dynamically via /api/v1/plugin-nav. See mergeRemotePluginNav in
      // AppTopbar.vue — the request-sessions group above is the merge target.
    ],
  },
  {
    id: 'data-ops',
    labelKey: 'nav.group.dataOps',
    items: [
      // ★ 2026-11-27 非管理员菜单重构轮（owner 指令）：「数据运维」组对
      //   非管理员收敛为「系统设置」五项：敏感信息规则 / 提示词注入检测 /
      //   格式异常监控 / PI错误分析 / 错误处理手册。其余管理工具加 management
      //   （管理员视角不变）；组文案 zh-CN 同轮更新（数据运维 → 系统设置，
      //   仅对非管理员语义生效——管理员看到的是同一组条目，标签同步更新）。
      { path: '/admin/sanitize-rules', labelKey: 'sanitizeRules.title', icon: '🔒' },
      { path: '/admin/settings', labelKey: 'nav.item.settings', icon: '⚙️', super: true, hideForTenant: true, management: true },
      // 2026-11-16：自更新与升级（自动检索最新版本 → 提醒 → 点击安装升级）。
      // 后端是 /api/admin/releases 族（super admin 档）⇒ 菜单项同档 super。
      // 与「更新与激活」（/customer/update-activate）是两件事：后者是 license 激活，
      // 前者是本节点自身的二进制升级（会替换二进制并重启服务）。
      { path: '/admin/self-update', labelKey: 'nav.item.selfUpdate', icon: '⬆️', super: true, hideForTenant: true, management: true },
      { path: '/admin/proxy', labelKey: 'nav.item.proxy', icon: '🌐', super: true, hideForTenant: true, management: true },
      { path: '/admin/data-lifecycle', labelKey: 'nav.item.dataLifecycle', icon: '💾', platformOps: true, hideForTenant: true, management: true },
      { path: '/format-anomalies', labelKey: 'nav.item.formatAnomalies', icon: '⚠️', super: true, hideForTenant: true, userFacing: true },
      { path: '/model-integrity', labelKey: 'nav.item.modelIntegrity', icon: '🛰️', super: true, hideForTenant: true, management: true },
      { path: '/model-credibility', labelKey: 'nav.item.modelCredibility', icon: '🔎', super: true, hideForTenant: true, management: true },
      { path: '/admin/modules', labelKey: 'nav.item.modules', icon: '🧩', super: true, hideForTenant: true, management: true },
      { path: '/admin/prompt-injection', labelKey: 'nav.item.promptInjection', icon: '🛡️', super: true, hideForTenant: true, userFacing: true },
      { path: '/admin/pi-analysis', labelKey: 'nav.item.piAnalysis', icon: '🔍', super: true, hideForTenant: true, piAnalysis: true, userFacing: true },
      // 2026-11-21：错误处理手册（后端 admin/error_playbook_handler.go + domains/errorplaybook）。
      // 已知错误按预定方案标准执行、未知错误 PI 分析后落手册；本入口提供
      // 条目 CRUD、三形态方案编辑、执行留痕时间线与按快照回滚。
      // ★ 2026-11-27：对非管理员放开菜单入口（读面 admin 档可读；写面
      //   entries/execute/revert/analyze 本轮已加 super 闸）。页面内写按钮
      //   需按角色隐藏（本轮补只读守卫）。
      { path: '/admin/error-playbook', labelKey: 'nav.item.errorPlaybook', icon: '📕', super: true, hideForTenant: true, userFacing: true },
      // 2026-11-20：会话分析系统（后端 admin/session_analytics 的端点族）。
      // ★ 2026-11-23 审计轮：**移除「/admin/session-analytics」总览页菜单项**。
      //   理由：① 该页已按 owner 指令取消——路由改为重定向到
      //     /admin/session-analytics/users（内容已重组迁入该页）；
      //   ② 保留一个指向「已取消页面」的菜单项是技术债；
      //   ③ 它与 requests-sessions 组的 /admin/session-analytics/users
      //     共用同一文案（会话分析中心），顶栏会出现两个同名入口
      //     —— 正是 2026-11-20 L9 收口当初要避免的形态，重定向后反而复发。
      //   ⇒ 主入口是 requests-sessions 组的「会话分析中心」（users 页）；
      //     本组保留三个子榜（人员/智能体/模型排行）。
      // ★ 后端角色闸门放行 super_admin / tenant_admin / admin_key（且 tenant_admin
      //   有租户隔离）⇒ 菜单项**不设 super**，只 hideForTenant（租户门户隐藏），
      //   与 router 的 requiresAuth 对齐；真正的权限真源在后端。
      // ★ 详情页（/user-ranking/:owner）不单独挂菜单：由 navCovers 的前缀匹配
      //   被「人员排行」项覆盖，且入口在排行表里。
      { path: '/admin/session-analytics/user-ranking', labelKey: 'sessionAnalytics.nav.userRanking', icon: '👥', hideForTenant: true, management: true },
      { path: '/admin/session-analytics/agent-ranking', labelKey: 'sessionAnalytics.nav.agentRanking', icon: '🧑‍💼', hideForTenant: true, management: true },
      { path: '/admin/session-analytics/model-ranking', labelKey: 'sessionAnalytics.nav.modelRanking', icon: '🧠', hideForTenant: true, management: true },
      { path: '/admin/compression', labelKey: 'nav.item.compression', icon: '📦', platformOps: true, hideForTenant: true, management: true },
      { path: '/admin/modules?module=wechat_bot', labelKey: 'nav.item.wechatBot', icon: '💬', super: true, hideForTenant: true, management: true },
      { path: '/admin/agents', labelKey: 'nav.item.agents', icon: '🤖', super: true, hideForTenant: true, management: true },
      // 非核心节点：合并站点/激活/许可/协议 →「更新与激活」
      // ★ 2026-11-27 非管理员菜单重构轮：加 management —— license 激活是
      //   节点管理职责（未激活时的「前往激活」重定向走 ACTIVATE_REDIRECT_PATH，
      //   不依赖菜单可见性）⇒ owner 口径「系统设置五项」不含它。
      { path: '/customer/update-activate', labelKey: 'nav.item.updateActivate', icon: '🔄', management: true },
      { path: '/customer/offline-activation', labelKey: 'nav.item.licenseOffline', icon: '🔌', notActivatedOnly: true },
      // ★ 2026-11-27 非管理员菜单重构轮：加 management —— 数据采集范围是
      //   节点级管理配置（maintain 面租户遥测开关），非用户职责。
      { path: '/maintain/tenant/telemetry', labelKey: 'nav.item.telemetryScope', icon: '📡', external: true, management: true },
    ],
  },
  {
    id: 'opsplatform',
    labelKey: 'nav.group.opsplatform',
    items: [
      // ★ 2026-11-27 非管理员菜单重构轮（owner 指令）：「运维中心」对
      //   非系统管理员整体隐藏 —— 全组条目加 management（本地条目）；
      //   远端 ai-native-maintain 下发的运维菜单由 mergeRemoteOps 对
      //   非管理视角整组剔除（见 canShowNavItem/mergeRemoteOps 同轮改动）。
      // Dynamically shown only when ai-native-maintain is reachable.
      { path: '/maintain/ops/overview', labelKey: 'nav.item.opsOverview', icon: '🧭', super: true, hideForTenant: true, opsPlatform: true, external: true, management: true },
      { path: '/maintain/ops/center', labelKey: 'nav.item.opsCenter', icon: '🖥️', super: true, hideForTenant: true, opsPlatform: true, external: true, management: true },
      { path: '/maintain/ops/downloads', labelKey: 'nav.item.opsDownloads', icon: '📦', super: true, hideForTenant: true, opsPlatform: true, external: true, management: true },
      { path: '/maintain/ops/licenses', labelKey: 'nav.item.opsLicenses', icon: '🔑', super: true, hideForTenant: true, opsPlatform: true, external: true, management: true },
      { path: '/maintain/ops/faults', labelKey: 'nav.item.opsFaults', icon: '⚠️', super: true, hideForTenant: true, opsPlatform: true, external: true, management: true },
      { path: '/maintain/ops/autoupdate', labelKey: 'nav.item.opsAutoUpdate', icon: '🚀', super: true, hideForTenant: true, opsPlatform: true, external: true, activateWhenNotActivated: true, management: true },
      { path: '/ops/vibecoding', labelKey: 'nav.item.opsVibeCoding', icon: '💻', super: true, hideForTenant: true, opsPlatform: true, management: true },
    ],
  },
  {
    id: 'guide',
    labelKey: 'nav.group.guide',
    items: [
      { path: '/examples', labelKey: 'nav.item.examples', icon: '📝' },
      { path: '/examples#auto-config', labelKey: 'nav.item.autoConfig', icon: '🛠️' },
      // 2026-11-16：帮助手册（下载/安装/部署/供应商/凭据/租户/用户/API Key/
      // 客户端调用/常见故障）。正文是 web/content/help-manual.zh-CN.md，
      // 由 /help 路由渲染。不设角色门 —— 手册对所有登录用户开放。
      { path: '/help', labelKey: 'nav.item.helpManual', icon: '📖' },
      { path: '/maintain/ops/autoupdate', labelKey: 'nav.item.opsAutoUpdate', icon: '🚀', external: true, opsPlatform: true, activateWhenNotActivated: true },
      { path: '/maintain/donate', labelKey: 'nav.item.supportDonate', icon: '💖', external: true },
    ],
  },
  {
    id: 'chat',
    labelKey: 'nav.group.chat',
    items: [{ path: '/chat', labelKey: 'nav.item.chat', icon: '💬' }],
  },
]

type NavVisibilityOpts = {
  isSuperAdmin: boolean
  isPlatformOps: boolean
  isTenantPortal: boolean
  /** super_admin 或 default 租户 tenant_admin（2026-09-04 供应商控制台） */
  isProviderConsole?: boolean
  isActivated?: boolean
  /**
   * ★ 2026-11-18 审计轮 P2：PI 分析（/admin/pi-analysis）运行时可用性。
   * undefined / true ⇒ 展示；false ⇒ 隐藏（探测到 API 不可用）。
   * 口径见 config/piAnalysis.ts。
   */
  piAnalysisAvailable?: boolean
  /**
   * ★ 2026-11-27 非管理员菜单重构轮：管理视角开关。
   *
   * true = 调用方是「管理视角」：super_admin ∧ default 租户（= isPlatformOps）。
   *   ★ owner 需求原文把 tenant_admin 明确列为「非系统管理员」被裁剪对象
   *     ⇒ tenant_admin **不**进管理视角（useAppNav 同轮已按此口径实现；
   *     maintain-web navLoader 同轮对齐）。
   * false = tenant_admin / 普通 role=user / 非 default 租户门户 ⇒ management
   *   条目隐藏。
   * undefined = 兼容旧调用（视同 true，保持既有测试与调用点零改动）。
   */
  isManagementView?: boolean
}

export function canShowNavItem(item: NavItem, opts: NavVisibilityOpts): boolean {
  // ★ 2026-11-23 owner 指令：隐藏清单（config/navHidden.ts）—— 路径级隐藏
  //   优先于一切角色/租户判据（藏了就是藏了，与谁登录无关）。
  if (isHiddenNavPath(item.path)) return false
  // ★ 2026-11-27 非管理员菜单重构轮：管理面条目只对管理视角可见。
  //   判据在 super/platformOps 之前：management 是「视角」判据，
  //   旧字段是「角色」判据；视角不满足时无论角色字段是什么都藏
  //   （普通 user 即使某条目恰好没带 super 也不该看到管理工具）。
  if (item.management && opts.isManagementView === false) return false
  // ★ 2026-11-27 非管理员菜单重构轮：userFacing 穿透 —— 带 userFacing 的条目
  //   语义是「所有已登录用户可见，数据范围由后端按角色收窄」⇒ 旧的角色/租户
  //   闸（super/platformOps/providerConsole/tenantOnly/hideForTenant）对它
  //   **不再生效**（否则 /dashboard 等条目加了标记仍被旧闸藏掉 —— 本轮真红
  //   过一次的教训：标记必须接到判据上才算数）。可用性闸（maintain 探测 /
  //   PI 分析探测 / 激活态）≠ 角色闸，照常生效：一个因后端 fail-closed 而
  //   不存在的页面对任何角色都不该出现在菜单里。
  if (item.userFacing) {
    if (item.opsPlatform && !showOpsPlatform()) return false
    if (item.piAnalysis && opts.piAnalysisAvailable === false) return false
    if (item.notActivatedOnly && opts.isActivated) return false
    return true
  }
  if (item.opsPlatform && !showOpsPlatform()) return false
  if (item.piAnalysis && opts.piAnalysisAvailable === false) return false
  if (item.super && !opts.isSuperAdmin) return false
  if (item.platformOps && !opts.isPlatformOps) return false
  if (item.providerConsole && !opts.isProviderConsole) return false
  if (item.tenantOnly && !opts.isTenantPortal) return false
  if (item.hideForTenant && opts.isTenantPortal) return false
  if (item.notActivatedOnly && opts.isActivated) return false
  return true
}

export function visibleNavItems(items: NavItem[], opts: NavVisibilityOpts): NavItem[] {
  return items.filter((item) => canShowNavItem(item, opts))
}

export function visibleNavGroups(groups: NavGroup[], opts: NavVisibilityOpts): NavGroup[] {
  return groups
    .map((g) => ({
      ...g,
      items: g.items.filter((item) => canShowNavItem(item, opts)),
    }))
    .filter((g) => g.items.length > 0)
}

export function isNavItemActive(path: string, currentPath: string, exact?: boolean): boolean {
  if (path === '/') return currentPath === '/'
  if (exact) return currentPath === path
  return currentPath === path || currentPath.startsWith(path + '/')
}

// 2026-09-12: 移除废弃的 sidebar 折叠持久化（SIDEBAR_COLLAPSED_KEY /
// readSidebarCollapsed / writeSidebarCollapsed）。topbar 迁移（2026-07-21）
// 后无任何调用方，localStorage key `llmgw_sidebar_collapsed` 随之废弃。

// 2026-07-21: 顶部 topbar 用的"扁平化 + 分组标签"导航数据。
// 把 NAV_PRIMARY_ITEMS（顶层无分组）+ NAV_GROUPS（分组）合并成一个 TopbarNavGroup[]，
// 每个 group 含 items；同时附 group.titleKey 给 i18n（nav.group.*）。
export type TopbarNavGroup = {
  id: string
  label?: string
  labelKey?: string
  items: NavItem[]
  /** Topbar 上的"展开所有子项"按钮（用于 chat / guide 等只有 1 个 item 的 group 折叠显示） */
  primaryPath?: string
}

/**
 * V5.1: adapter from the plugin-runtime NavEntry[] (fetched via
 * /api/v1/plugin-nav) into our local NavItem shape.
 *
 * Notes:
 *  - Plugin entries are always rendered as full-page external `<a>` links
 *    (same as opsPlatform / maintain entries), because the plugin's web
 *    bundle lives at /plugins/<plugin_id>/<page_path> — outside the Gateway
 *    SPA's router. external:true triggers the <a href> branch in AppTopbar.
 *  - role/tenant flags come straight from the manifest (super, platform_ops,
 *    tenant_only). hideForTenant is the inverse of platform_ops for symmetry
 *    with the static items.
 *  - Unknown nav_group values fall back to 'plugins' so the entry still
 *    shows up under a discoverable group instead of being silently dropped.
 *  - i18n: label_key is forwarded; AppTopbar's `t()` resolves it against the
 *    active locale (zh-CN, en-US, ...). The hard-coded `label` is only the
 *    fallback when the key is missing.
 */
export interface RemoteNavEntry {
  plugin_id: string
  plugin_version: string
  page_path: string
  page_type: 'settings' | 'data'
  nav_group: string
  label_key: string
  icon?: string
  super: boolean
  platform_ops: boolean
  tenant_only: boolean
  order: number
  route_url: string
}

export function remoteNavToNavItems(entries: RemoteNavEntry[]): NavItem[] {
  if (!entries || entries.length === 0) return []
  return entries.map((e) => ({
    path: e.route_url,
    labelKey: e.label_key || undefined,
    icon: e.icon || '🧩',
    super: e.super,
    platformOps: e.platform_ops,
    tenantOnly: e.tenant_only,
    hideForTenant: e.platform_ops, // mirrors static convention
    external: true,
    plugin: e.plugin_id,
  }))
}

/**
 * V5.1: merge plugin nav entries into the static NAV_GROUPS.
 *
 * - Entries whose nav_group matches an existing group are appended to that
 *   group's items (and re-sorted by `order`).
 * - Entries with an unknown nav_group are gathered into a synthetic
 *   'plugins' group, kept at the end so it never displaces the curated
 *   layout.
 *
 * The result is consumed by visibleNavGroups → mergeNav, so all
 * role/tenant filters apply uniformly.
 */
export function mergeRemotePluginNav(groups: NavGroup[], entries: RemoteNavEntry[]): NavGroup[] {
  if (!entries || entries.length === 0) return groups
  const items = remoteNavToNavItems(entries)
  const knownIds = new Set(groups.map((g) => g.id))
  const buckets = new Map<string, NavItem[]>()
  for (let i = 0; i < entries.length; i++) {
    const e = entries[i]
    const it = items[i]
    const gid = e.nav_group && knownIds.has(e.nav_group) ? e.nav_group : 'plugins'
    if (!buckets.has(gid)) buckets.set(gid, [])
    buckets.get(gid)!.push(it)
  }
  const out: NavGroup[] = []
  for (const g of groups) {
    const add = buckets.get(g.id)
    if (add && add.length > 0) {
      out.push({ ...g, items: [...g.items, ...add] })
      buckets.delete(g.id)
    } else {
      out.push(g)
    }
  }
  const synthetic = buckets.get('plugins')
  if (synthetic && synthetic.length > 0) {
    out.push({
      id: 'plugins',
      labelKey: 'nav.group.plugins',
      items: synthetic,
    })
  }
  return out
}

/**
 * 将 PRIMARY + GROUPS 合并成 topbar 用的扁平分组结构。
 * - PRIMARY（无分组）放到组 "primary"（总览）
 * - 每个 group 的 items 过滤掉 visible 后打包
 */
export function mergeNav(
  primary: NavItem[],
  groups: NavGroup[],
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean; isProviderConsole?: boolean },
): TopbarNavGroup[] {
  const visiblePrimary = primary.filter((it) => canShowNavItem(it, opts))
  const visibleGroups = visibleNavGroups(groups, opts)
  const result: TopbarNavGroup[] = []
  if (visiblePrimary.length > 0) {
    result.push({
      id: 'primary',
      label: visiblePrimary.map((it) => (it.labelKey ? it.labelKey : it.label)).join(' / '),
      labelKey: visiblePrimary[0].labelKey,
      items: visiblePrimary,
      primaryPath: visiblePrimary[0].path,
    })
  }
  for (const g of visibleGroups) {
    result.push({
      id: g.id,
      label: g.label,
      labelKey: g.labelKey,
      items: g.items,
      primaryPath: g.items.length === 1 ? g.items[0].path : undefined,
    })
  }
  return result
}
