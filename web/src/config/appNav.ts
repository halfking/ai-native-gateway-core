/** Sidebar navigation — grouped menus with role / tenant visibility flags. */

import { showOpsPlatform } from './edition'
import { usePersistedValue } from '../composables/usePersistedValue'

export type NavItem = {
  path: string
  label: string
  labelKey?: string  // i18n key, if present will be used via t(labelKey)
  icon: string
  /** super_admin only */
  super?: boolean
  /** super_admin + default tenant (platform ops) */
  platformOps?: boolean
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
}

export type NavGroup = {
  id: string
  label: string
  labelKey?: string  // i18n key for group title
  items: NavItem[]
}

/** Top-level sidebar links (no accordion group). Used for default-tenant platform ops. */
export const NAV_PRIMARY_ITEMS: NavItem[] = [
  { path: '/dashboard', label: '总览', labelKey: 'nav.item.overview', icon: '📊', platformOps: true, exact: true },
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
  label: string
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
  {
    id: 'tenant-portal',
    label: '我的服务',
    labelKey: 'nav.group.tenantPortal',
    items: [
      { path: '/tenant/models', label: '标准模型', labelKey: 'nav.item.tenantModels', icon: '🤖', tenantOnly: true },
      { path: '/tenant/account', label: '我的账户', labelKey: 'nav.item.tenantAccount', icon: '💰', tenantOnly: true },
      { path: '/tenant/pricing', label: '套餐与充值', labelKey: 'nav.item.tenantPricing', icon: '💳', tenantOnly: true },
      { path: '/tenant/usage', label: '我的消耗', labelKey: 'nav.item.tenantUsage', icon: '📉', tenantOnly: true },
    ],
  },
  {
    id: 'models-routing',
    label: '模型与路由',
    labelKey: 'nav.group.modelsRouting',
    items: [
      { path: '/models', label: '模型与目录', labelKey: 'nav.item.models', icon: '🏷️', platformOps: true, hideForTenant: true },
      { path: '/routing-v2', label: '路由全景', labelKey: 'nav.item.routingOverview', icon: '🗺️', super: true, hideForTenant: true, exact: true },
      { path: '/routing-v2/credentials', label: '凭据监控', labelKey: 'nav.item.credentialMonitor', icon: '📊' },
      { path: '/probe-health', label: '探测健康度', labelKey: 'nav.item.probeHealth', icon: '🔍', super: true, hideForTenant: true },
      { path: '/dashboard?tab=selfcheck', label: '系统自检', labelKey: 'nav.item.systemMonitor', icon: '📈', super: true, hideForTenant: true },
      { path: '/providers', label: '供应商', labelKey: 'nav.item.providers', icon: '🔌', super: true, hideForTenant: true },
      { path: '/pricing', label: '成本价格', labelKey: 'nav.item.pricing', icon: '📉', platformOps: true, hideForTenant: true },
      { path: '/model-pricing', label: '定价管理', labelKey: 'nav.item.modelPricing', icon: '💰', platformOps: true, hideForTenant: true },
      { path: '/free-pool', label: '免费资源', labelKey: 'nav.item.freePool', icon: '🎁', super: true, hideForTenant: true },
    ],
  },
  {
    id: 'tenant-users',
    label: '租户用户',
    labelKey: 'nav.group.tenantUsers',
    items: [
      { path: '/tenants', label: '租户管理', labelKey: 'nav.item.tenants', icon: '🏢', super: true, hideForTenant: true },
      { path: '/users', label: '用户管理', labelKey: 'nav.item.users', icon: '👤' },
      { path: '/keys', label: 'API 密钥', labelKey: 'nav.item.keys', icon: '🔑' },
      { path: '/key-applications', label: '密钥申请', labelKey: 'nav.item.keyApplications', icon: '📬', super: true, hideForTenant: true },
      { path: '/audit-logs', label: '审计日志', labelKey: 'nav.item.auditLogs', icon: '📋', super: true, hideForTenant: true },
    ],
  },
  {
    id: 'requests-sessions',
    label: '请求与会话',
    labelKey: 'nav.group.requestsSessions',
    items: [
      { path: '/request-logs', label: '请求日志', labelKey: 'nav.item.requestLogs', icon: '📋' },
      { path: '/dispatch/waterfall', label: '队列瀑布图', labelKey: 'nav.item.dispatchWaterfall', icon: '📊', platformOps: true, hideForTenant: true },
      { path: '/admin/turns', label: '轮次列表', labelKey: 'nav.item.turns', icon: '🔄', super: true, hideForTenant: true },
      // T9 — 请求注册表 + 连接注册台（mock stage）
      { path: '/admin/request-registry', label: '请求注册表', labelKey: 'nav.item.requestRegistry', icon: '📑', super: true, hideForTenant: true },
      { path: '/admin/connection-registry', label: '连接注册台', labelKey: 'nav.item.connectionRegistry', icon: '🔗', super: true, hideForTenant: true },
      // 2026-07-23: ai-session-manager plugin 入口
      // Plugin 模式：完整页面跳转（同 opsPlatform 的 external 机制）
      { path: '/plugins/ai-session-manager/sessions', label: '会话列表', labelKey: 'nav.item.pluginSessions', icon: '💬', super: true, hideForTenant: true, external: true, plugin: 'ai-session-manager' },
    ],
  },
  {
    id: 'data-ops',
    label: '数据运维',
    labelKey: 'nav.group.dataOps',
    items: [
      { path: '/admin/settings', label: '系统设置', labelKey: 'nav.item.settings', icon: '⚙️', super: true, hideForTenant: true },
      { path: '/admin/proxy', label: '代理管理', labelKey: 'nav.item.proxy', icon: '🌐', super: true, hideForTenant: true },
      { path: '/admin/data-lifecycle', label: '数据生命周期', labelKey: 'nav.item.dataLifecycle', icon: '💾', platformOps: true, hideForTenant: true },
      { path: '/format-anomalies', label: '格式异常监控', labelKey: 'nav.item.formatAnomalies', icon: '⚠️', super: true, hideForTenant: true },
      { path: '/model-integrity', label: '模型完整性监控', labelKey: 'nav.item.modelIntegrity', icon: '🛰️', super: true, hideForTenant: true },
      { path: '/admin/modules', label: '模块管理', labelKey: 'nav.item.modules', icon: '🧩', super: true, hideForTenant: true },
      { path: '/admin/prompt-injection', label: '提示词注入检测', labelKey: 'nav.item.promptInjection', icon: '🛡️', super: true, hideForTenant: true },
      { path: '/admin/compression', label: '压缩管理', labelKey: 'nav.item.compression', icon: '📦', platformOps: true, hideForTenant: true },
      { path: '/admin/modules?module=wechat_bot', label: '微信机器人', labelKey: 'nav.item.wechatBot', icon: '💬', super: true, hideForTenant: true },
      { path: '/admin/agents', label: 'Agent Registry', labelKey: 'nav.item.agents', icon: '🤖', super: true, hideForTenant: true },
      // 非核心节点：合并站点/激活/许可/协议 →「更新与激活」
      { path: '/customer/update-activate', label: '更新与激活', labelKey: 'nav.item.updateActivate', icon: '🔄' },
      { path: '/customer/offline-activation', label: '离线激活', labelKey: 'nav.item.licenseOffline', icon: '🔌', notActivatedOnly: true },
      { path: '/maintain/tenant/telemetry', label: '数据采集范围', labelKey: 'nav.item.telemetryScope', icon: '📡', external: true },
    ],
  },
  {
    id: 'opsplatform',
    label: '运维中心',
    labelKey: 'nav.group.opsplatform',
    items: [
      // Dynamically shown only when ai-native-maintain is reachable.
      { path: '/maintain/ops/overview', label: '运维总览', labelKey: 'nav.item.opsOverview', icon: '🧭', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/center', label: '中心运维', labelKey: 'nav.item.opsCenter', icon: '🖥️', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/downloads', label: '发布与下载', labelKey: 'nav.item.opsDownloads', icon: '📦', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/licenses', label: 'License管理', labelKey: 'nav.item.opsLicenses', icon: '🔑', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/faults', label: '故障管理', labelKey: 'nav.item.opsFaults', icon: '⚠️', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/autoupdate', label: '自动更新', labelKey: 'nav.item.opsAutoUpdate', icon: '🚀', super: true, hideForTenant: true, opsPlatform: true, external: true, activateWhenNotActivated: true },
      { path: '/ops/vibecoding', label: 'VibeCoding', labelKey: 'nav.item.opsVibeCoding', icon: '💻', super: true, hideForTenant: true, opsPlatform: true },
    ],
  },
  {
    id: 'guide',
    label: '接入指南',
    labelKey: 'nav.group.guide',
    items: [{ path: '/examples', label: '接入示例', labelKey: 'nav.item.examples', icon: '📝' }],
  },
  {
    id: 'chat',
    label: '对话',
    labelKey: 'nav.group.chat',
    items: [{ path: '/chat', label: '对话', labelKey: 'nav.item.chat', icon: '💬' }],
  },
]

export function canShowNavItem(
  item: NavItem,
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean; isActivated?: boolean },
): boolean {
  if (item.opsPlatform && !showOpsPlatform()) return false
  if (item.super && !opts.isSuperAdmin) return false
  if (item.platformOps && !opts.isPlatformOps) return false
  if (item.tenantOnly && !opts.isTenantPortal) return false
  if (item.hideForTenant && opts.isTenantPortal) return false
  if (item.notActivatedOnly && opts.isActivated) return false
  return true
}

export function visibleNavItems(
  items: NavItem[],
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean; isActivated?: boolean },
): NavItem[] {
  return items.filter((item) => canShowNavItem(item, opts))
}

export function visibleNavGroups(
  groups: NavGroup[],
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean; isActivated?: boolean },
): NavGroup[] {
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

const SIDEBAR_COLLAPSED_KEY = 'llmgw_sidebar_collapsed'

// LP8 (2026-08-24): sidebar collapse state persisted through usePersistedValue
// for shared lifecycle-flush / error-degrade. Stored as '0'/'1' (compact)
// instead of JSON to keep the legacy key format intact for any external
// tooling that reads localStorage directly.
const sidebarCollapsedPersisted = usePersistedValue<boolean>(
  SIDEBAR_COLLAPSED_KEY,
  () => false,
  {
    immediate: true,
    serialize: (v) => (v ? '1' : '0'),
    deserialize: (r) => (r === '1' ? true : r === '0' ? false : undefined),
  },
)

export function readSidebarCollapsed(): boolean {
  return sidebarCollapsedPersisted.value.value
}

export function writeSidebarCollapsed(collapsed: boolean) {
  sidebarCollapsedPersisted.value.value = collapsed
}

// 2026-07-21: 顶部 topbar 用的"扁平化 + 分组标签"导航数据。
// 把 NAV_PRIMARY_ITEMS（顶层无分组）+ NAV_GROUPS（分组）合并成一个 TopbarNavGroup[]，
// 每个 group 含 items；同时附 group.titleKey 给 i18n（nav.group.*）。
export type TopbarNavGroup = {
  id: string
  label: string
  labelKey?: string
  items: NavItem[]
  /** Topbar 上的"展开所有子项"按钮（用于 chat / guide 等只有 1 个 item 的 group 折叠显示） */
  primaryPath?: string
}

/**
 * 将 PRIMARY + GROUPS 合并成 topbar 用的扁平分组结构。
 * - PRIMARY（无分组）放到组 "primary"（总览）
 * - 每个 group 的 items 过滤掉 visible 后打包
 */
export function mergeNav(
  primary: NavItem[],
  groups: NavGroup[],
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean },
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
