/** Sidebar navigation — grouped menus with role / tenant visibility flags. */

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
  /** Only highlight when the path matches exactly (no prefix matching).
   *  Use for items whose path is a prefix of another item's path, e.g.
   *  '/routing-v2' (路由全景) vs '/routing-v2/credentials' (凭据监控) —
   *  without this both highlight at once on the credentials page. */
  exact?: boolean
}

export type NavGroup = {
  id: string
  label: string
  labelKey?: string  // i18n key for group title
  items: NavItem[]
}

/** Top-level sidebar links (no accordion group). Used for default-tenant platform ops. */
export const NAV_PRIMARY_ITEMS: NavItem[] = [
  { path: '/', label: '总览', labelKey: 'nav.item.overview', icon: '📊', platformOps: true },
]

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
      { path: '/sessions', label: '会话列表', labelKey: 'nav.item.sessions', icon: '💬' },
      { path: '/session-compare', label: '会话对比', labelKey: 'nav.item.sessionCompare', icon: '🔍' },
      { path: '/session-context', label: '会话上下文', labelKey: 'nav.item.sessionContext', icon: '💭' },
      { path: '/admin/session-analytics', label: '会话分析中心', labelKey: 'nav.item.sessionAnalytics', icon: '📊', super: true, hideForTenant: true },
      { path: '/admin/session-clusters', label: '会话聚类', labelKey: 'nav.item.sessionClusters', icon: '🗂️', super: true, hideForTenant: true },
      { path: '/admin/session-audit', label: '会话审计', labelKey: 'nav.item.sessionAudit', icon: '🔍', super: true, hideForTenant: true },
    ],
  },
  {
    id: 'data-ops',
    label: '数据运维',
    labelKey: 'nav.group.dataOps',
    items: [
      { path: '/admin/settings', label: '系统设置', labelKey: 'nav.item.settings', icon: '⚙️', super: true, hideForTenant: true },
      { path: '/admin/data-lifecycle', label: '数据生命周期', labelKey: 'nav.item.dataLifecycle', icon: '💾', platformOps: true, hideForTenant: true },
      { path: '/format-anomalies', label: '格式异常监控', labelKey: 'nav.item.formatAnomalies', icon: '⚠️', super: true, hideForTenant: true },
      { path: '/admin/modules', label: '模块管理', labelKey: 'nav.item.modules', icon: '🧩', super: true, hideForTenant: true },
      { path: '/admin/compression', label: '压缩管理', labelKey: 'nav.item.compression', icon: '📦', platformOps: true, hideForTenant: true },
      { path: '/admin/agents', label: 'Agent Registry', labelKey: 'nav.item.agents', icon: '🤖', super: true, hideForTenant: true },
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
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean },
): boolean {
  if (item.super && !opts.isSuperAdmin) return false
  if (item.platformOps && !opts.isPlatformOps) return false
  if (item.tenantOnly && !opts.isTenantPortal) return false
  if (item.hideForTenant && opts.isTenantPortal) return false
  return true
}

export function visibleNavItems(
  items: NavItem[],
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean },
): NavItem[] {
  return items.filter((item) => canShowNavItem(item, opts))
}

export function visibleNavGroups(
  groups: NavGroup[],
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean },
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

export function readSidebarCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === '1'
  } catch {
    return false
  }
}

export function writeSidebarCollapsed(collapsed: boolean) {
  try {
    localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed ? '1' : '0')
  } catch {
    // ignore
  }
}
