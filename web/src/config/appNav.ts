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
  /** Maintain service only: only visible to default tenant (platform operators) */
  maintainOnly?: boolean
  /** Only highlight when the path matches exactly (no prefix matching).
   *  Use for items whose path is a prefix of another item's path, e.g.
   *  '/routing-v2' (路由全景) vs '/routing-v2/credentials' (凭据监控) —
   *  without this both highlight at once on the credentials page. */
  exact?: boolean
  /**
   * Same-origin path owned by ai-native-maintain SPA (`/maintain/*`).
   * Render as a full-page `<a href>` instead of Vue RouterLink.
   */
  external?: boolean
}

export type NavGroup = {
  id: string
  label: string
  labelKey?: string  // i18n key for group title
  items: NavItem[]
}

/** Top-level sidebar links (no accordion group). Used for default-tenant platform ops. */
export const NAV_PRIMARY_ITEMS: NavItem[] = [
  { path: '/', label: '总览', labelKey: 'nav.item.overview', icon: '', platformOps: true },
]

export const NAV_GROUPS: NavGroup[] = [
  {
    id: 'tenant-portal',
    label: '我的服务',
    labelKey: 'nav.group.tenantPortal',
    items: [
      { path: '/tenant/models', label: '标准模型', labelKey: 'nav.item.tenantModels', icon: '', tenantOnly: true },
      { path: '/tenant/account', label: '我的账户', labelKey: 'nav.item.tenantAccount', icon: '', tenantOnly: true },
      { path: '/tenant/pricing', label: '套餐与充值', labelKey: 'nav.item.tenantPricing', icon: '', tenantOnly: true },
      { path: '/tenant/usage', label: '我的消耗', labelKey: 'nav.item.tenantUsage', icon: '', tenantOnly: true },
    ],
  },
  {
    id: 'models-routing',
    label: '模型与路由',
    labelKey: 'nav.group.modelsRouting',
    items: [
      { path: '/models', label: '模型与目录', labelKey: 'nav.item.models', icon: '', platformOps: true, hideForTenant: true },
      { path: '/routing-v2', label: '路由全景', labelKey: 'nav.item.routingOverview', icon: '', super: true, hideForTenant: true, exact: true },
      { path: '/routing-v2/credentials', label: '凭据监控', labelKey: 'nav.item.credentialMonitor', icon: '' },
      { path: '/probe-health', label: '探测健康度', labelKey: 'nav.item.probeHealth', icon: '', super: true, hideForTenant: true },
      { path: '/providers', label: '供应商', labelKey: 'nav.item.providers', icon: '', super: true, hideForTenant: true },
      // 2026-07-20: temporarily hide standalone quality page — fields merged into /providers + detail Quality tab.
      // { path: '/provider-quality', label: '供应商质量画像', labelKey: 'nav.item.providerQuality', icon: '', super: true, hideForTenant: true },
      { path: '/pricing', label: '成本价格', labelKey: 'nav.item.pricing', icon: '', platformOps: true, hideForTenant: true },
      { path: '/model-pricing', label: '定价管理', labelKey: 'nav.item.modelPricing', icon: '', platformOps: true, hideForTenant: true },
      { path: '/free-pool', label: '免费资源', labelKey: 'nav.item.freePool', icon: '', super: true, hideForTenant: true },
      // M2（22 章 §22.6）— 显式默认路由：菜单入口已隐藏，配置仍保留在
      // Smart 路由标签页（/routing-v2?tab=smart）内通过 SmartRoutingConfigPanel
      // 直接管理，避免重复入口。路由本身仍可用（router.ts 仍保留 /routing/defaults）。
    ],
  },
  {
    id: 'tenant-users',
    label: '租户用户',
    labelKey: 'nav.group.tenantUsers',
    items: [
      { path: '/tenants', label: '租户管理', labelKey: 'nav.item.tenants', icon: '', super: true, hideForTenant: true },
      { path: '/users', label: '用户管理', labelKey: 'nav.item.users', icon: '' },
      { path: '/keys', label: 'API 密钥', labelKey: 'nav.item.keys', icon: '' },
      { path: '/key-applications', label: '密钥申请', labelKey: 'nav.item.keyApplications', icon: '', super: true, hideForTenant: true },
      { path: '/audit-logs', label: '审计日志', labelKey: 'nav.item.auditLogs', icon: '', super: true, hideForTenant: true },
    ],
  },
  {
    id: 'requests-sessions',
    label: '请求与会话',
    labelKey: 'nav.group.requestsSessions',
    items: [
      { path: '/request-logs', label: '请求日志', labelKey: 'nav.item.requestLogs', icon: '' },
      { path: '/sessions', label: '会话列表', labelKey: 'nav.item.sessions', icon: '' },
      { path: '/admin/sessions', label: '会话管理', labelKey: 'nav.item.sessionManagement', icon: '', super: true, hideForTenant: true },
      { path: '/session-compare', label: '会话对比', labelKey: 'nav.item.sessionCompare', icon: '' },
      { path: '/session-context', label: '会话上下文', labelKey: 'nav.item.sessionContext', icon: '' },
      { path: '/admin/session-analytics', label: '会话分析中心', labelKey: 'nav.item.sessionAnalytics', icon: '', super: true, hideForTenant: true },
      { path: '/admin/session-clusters', label: '会话聚类', labelKey: 'nav.item.sessionClusters', icon: '', super: true, hideForTenant: true },
      { path: '/admin/session-audit', label: '会话审计', labelKey: 'nav.item.sessionAudit', icon: '', super: true, hideForTenant: true },
    ],
  },
  {
    id: 'data-ops',
    label: '数据运维',
    labelKey: 'nav.group.dataOps',
    items: [
      { path: '/admin/settings', label: '系统设置', labelKey: 'nav.item.settings', icon: '', super: true, hideForTenant: true },
      { path: '/admin/session-config', label: '会话配置', labelKey: 'nav.item.sessionConfig', icon: '', super: true, hideForTenant: true },
      { path: '/admin/data-lifecycle', label: '数据生命周期', labelKey: 'nav.item.dataLifecycle', icon: '', platformOps: true, hideForTenant: true },
      { path: '/format-anomalies', label: '格式异常监控', labelKey: 'nav.item.formatAnomalies', icon: '', super: true, hideForTenant: true },
      { path: '/admin/modules', label: '模块管理', labelKey: 'nav.item.modules', icon: '', super: true, hideForTenant: true },
      { path: '/admin/prompt-injection', label: '提示词注入检测', labelKey: 'nav.item.promptInjection', icon: '', super: true, hideForTenant: true },
      { path: '/admin/compression', label: '压缩管理', labelKey: 'nav.item.compression', icon: '', platformOps: true, hideForTenant: true },
      { path: '/admin/modules?module=wechat_bot', label: '微信机器人', labelKey: 'nav.item.wechatBot', icon: '', super: true, hideForTenant: true },
      { path: '/admin/agents', label: 'Agent Registry', labelKey: 'nav.item.agents', icon: '', super: true, hideForTenant: true },
    ],
  },
  {
    id: 'opsplatform',
    label: '运维平台',
    labelKey: 'nav.group.opsplatform',
    items: [
      // Migrated to ai-native-maintain SPA (same-origin /maintain/*).
      { path: '/maintain/ops/overview', label: '运维总览', labelKey: 'nav.item.opsOverview', icon: '', super: true, maintainOnly: true, external: true },
      { path: '/maintain/ops/center', label: '中心运维', labelKey: 'nav.item.opsCenter', icon: '', super: true, maintainOnly: true, external: true },
      { path: '/maintain/ops/downloads', label: '发布与下载', labelKey: 'nav.item.opsDownloads', icon: '', super: true, maintainOnly: true, external: true },
      { path: '/maintain/ops/licenses', label: 'License管理', labelKey: 'nav.item.opsLicenses', icon: '', super: true, maintainOnly: true, external: true },
      { path: '/maintain/ops/faults', label: '故障管理', labelKey: 'nav.item.opsFaults', icon: '', super: true, maintainOnly: true, external: true },
      { path: '/maintain/ops/autoupdate', label: '自动更新', labelKey: 'nav.item.opsAutoUpdate', icon: '', super: true, maintainOnly: true, external: true },
      { path: '/maintain/download', label: '产品入口', labelKey: 'nav.item.opsProductEntry', icon: '', super: true, maintainOnly: true, external: true },
      // Not yet migrated — keep inside Gateway SPA.
      { path: '/ops/vibecoding', label: 'VibeCoding', labelKey: 'nav.item.opsVibeCoding', icon: '', super: true, maintainOnly: true },
      { path: '/maintain/tenant/license', label: '我的授权', labelKey: 'nav.item.tenantLicense', icon: '', tenantOnly: true, external: true },
      { path: '/maintain/tenant/autoupdate', label: '我的更新', labelKey: 'nav.item.tenantAutoUpdate', icon: '', tenantOnly: true, external: true },
    ],
  },
  {
    id: 'guide',
    label: '接入指南',
    labelKey: 'nav.group.guide',
    items: [{ path: '/examples', label: '接入示例', labelKey: 'nav.item.examples', icon: '' }],
  },
  {
    id: 'chat',
    label: '对话',
    labelKey: 'nav.group.chat',
    items: [{ path: '/chat', label: '对话', labelKey: 'nav.item.chat', icon: '' }],
  },
]

export function canShowNavItem(
  item: NavItem,
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean; canAccessMaintain: boolean },
): boolean {
  if (item.super && !opts.isSuperAdmin) return false
  if (item.platformOps && !opts.isPlatformOps) return false
  if (item.tenantOnly && !opts.isTenantPortal) return false
  if (item.hideForTenant && opts.isTenantPortal) return false
  if (item.maintainOnly && !opts.canAccessMaintain) return false
  return true
}

// 整个分组是否对当前用户隐藏（例如「模型与路由」分组对租户用户隐藏）。
// 分组本身的隐藏规则由调用方传入的 hideForTenant 决定。
export function canShowNavGroup(
  group: NavGroup,
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean; canAccessMaintain: boolean },
): boolean {
  // 「模型与路由」分组对租户（tenant portal）整体隐藏：内部的 items 大多
  // 已带 hideForTenant:true，但显式分组隐藏更明确，避免任何 item 漏写导致
  // 暴露给非普通租户管理员。
  if (group.id === 'models-routing' && opts.isTenantPortal) return false
  return true
}

export function visibleNavItems(
  items: NavItem[],
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean; canAccessMaintain: boolean },
): NavItem[] {
  return items.filter((item) => canShowNavItem(item, opts))
}

export function visibleNavGroups(
  groups: NavGroup[],
  opts: { isSuperAdmin: boolean; isPlatformOps: boolean; isTenantPortal: boolean; canAccessMaintain: boolean },
): NavGroup[] {
  return groups
    .filter((g) => canShowNavGroup(g, opts))
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

// ── plugin nav merging ──────────────────────────────────────────────
import type { NavEntry } from '@/api/plugins'

/** 把插件菜单条目合并进静态分组。返回新数组，不修改入参。 */
export function mergeNav(
  staticGroups: NavGroup[],
  pluginItems: NavEntry[],
): NavGroup[] {
  const groups = staticGroups.map((g) => ({ ...g, items: [...g.items] }))
  if (!groups.some((g) => g.id === 'plugins')) {
    groups.push({ id: 'plugins', label: '插件', labelKey: 'nav.group.plugins', items: [] })
  }
  for (const item of pluginItems) {
    // 只把条目合并进已存在的静态分组；未知 nav_group 一律落到 plugins 兜底分组，
    // 不凭 nav_group 原值新建任意分组（否则兜底分组永远为空、被末尾 filter 剔除）。
    const gid = staticGroups.some((g) => g.id === item.nav_group) ? item.nav_group : 'plugins'
    const group = groups.find((g) => g.id === gid)
    group!.items.push({
      path: item.route_url,
      label: item.label_key,
      labelKey: item.label_key,
      icon: item.icon || '',
      super: item.super,
      platformOps: item.platform_ops,
      tenantOnly: item.tenant_only,
    })
  }
  return groups.filter((g) => g.items.length > 0)
}
