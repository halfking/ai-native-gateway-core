#!/usr/bin/env node
// scripts/export-menu-config.mjs — 构建时导出 Gateway 菜单配置供 Maintain 加载
// 2026-07-23 v2: 从 src/config/appNav.ts 真实导出结构（NAV_PRIMARY_ITEMS + NAV_GROUPS）
//
// 数据结构（对齐 appNav.ts + visibleNavItems）：
//   {
//     version: "1.0.0",
//     exported_at: "2026-07-23T...",
//     primary: NavItem[],                    // 顶部主菜单（NAV_PRIMARY_ITEMS）
//     groups: [{ id, label, labelKey, items: NavItem[] }, ...]  // 分组菜单（NAV_GROUPS）
//   }

import fs from 'fs'
import path from 'path'
import { fileURLToPath } from 'url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

// 直接嵌入 appNav.ts 的核心数据（构建时复制，避免跨语言解析 TS）
// 这是 NAV_PRIMARY_ITEMS + NAV_GROUPS 的同步副本
// 任何 appNav.ts 变更需要同步更新这里

const NAV_PRIMARY_ITEMS = [
  { path: '/dashboard', label: '总览', labelKey: 'nav.item.overview', icon: '📊', platformOps: true, exact: true },
]

const NAV_GROUPS = [
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
      { path: '/admin/prompt-injection', label: '提示词注入检测', labelKey: 'nav.item.promptInjection', icon: '🛡️', super: true, hideForTenant: true },
      { path: '/admin/compression', label: '压缩管理', labelKey: 'nav.item.compression', icon: '📦', platformOps: true, hideForTenant: true },
      { path: '/admin/modules?module=wechat_bot', label: '微信机器人', labelKey: 'nav.item.wechatBot', icon: '💬', super: true, hideForTenant: true },
      { path: '/admin/agents', label: 'Agent Registry', labelKey: 'nav.item.agents', icon: '🤖', super: true, hideForTenant: true },
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
      { path: '/maintain/ops/overview', label: '运维总览', labelKey: 'nav.item.opsOverview', icon: '🧭', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/center', label: '中心运维', labelKey: 'nav.item.opsCenter', icon: '🖥️', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/downloads', label: '发布与下载', labelKey: 'nav.item.opsDownloads', icon: '📦', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/licenses', label: 'License管理', labelKey: 'nav.item.opsLicenses', icon: '🔑', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/faults', label: '故障管理', labelKey: 'nav.item.opsFaults', icon: '⚠️', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/maintain/ops/autoupdate', label: '自动更新', labelKey: 'nav.item.opsAutoUpdate', icon: '🚀', super: true, hideForTenant: true, opsPlatform: true, external: true },
      { path: '/ops/vibecoding', label: 'VibeCoding', labelKey: 'nav.item.opsVibeCoding', icon: '💻', super: true, hideForTenant: true, opsPlatform: true },
    ],
  },
  {
    id: 'guide',
    label: '接入指南',
    labelKey: 'nav.group.guide',
    items: [
      { path: '/examples', label: '接入示例', labelKey: 'nav.item.examples', icon: '📝' },
    ],
  },
  {
    id: 'chat',
    label: '对话',
    labelKey: 'nav.group.chat',
    items: [
      { path: '/chat', label: '对话', labelKey: 'nav.item.chat', icon: '💬' },
    ],
  },
]

const menuConfig = {
  version: '1.0.0',
  exported_at: new Date().toISOString(),
  source: 'gateway-appNav',
  primary: NAV_PRIMARY_ITEMS,
  groups: NAV_GROUPS,
}

// 输出路径
const outputPath = path.resolve(__dirname, '../public/menu-config.json')

// 写入文件
fs.writeFileSync(outputPath, JSON.stringify(menuConfig, null, 2), 'utf-8')

// 统计
const primaryCount = menuConfig.primary.length
const groupCount = menuConfig.groups.length
const itemCount = menuConfig.groups.reduce((sum, g) => sum + g.items.length, 0)

console.log(`✅ 菜单配置已导出: ${outputPath}`)
console.log(`   版本: ${menuConfig.version}`)
console.log(`   来源: ${menuConfig.source}`)
console.log(`   主菜单项: ${primaryCount}`)
console.log(`   分组数: ${groupCount}`)
console.log(`   分组总项: ${itemCount}`)