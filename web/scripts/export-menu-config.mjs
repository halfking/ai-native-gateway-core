#!/usr/bin/env node
// scripts/export-menu-config.mjs — 构建时导出 Gateway 菜单配置
// 2026-07-23 v3: 增加 tenantScope 字段，明确标注每个菜单的租户可见性
//                安全原则：菜单信息也是敏感数据，必须明确标注
//
// tenantScope 值：
//   - 'default'  : 仅 default 租户 + super_admin 可见
//   - 'tenant'   : 仅租户门户（非 default 租户）可见
//   - '*'        : 所有租户可见
//
// 2026-09-10 (审计 R9 候选 19): 不再维护 appNav.ts 的内嵌同步副本——
// 那份副本已经漂移（多 plugin 会话项、缺 /admin/turns 等 3 项）。改为
// 用 esbuild 把 src/config/appNav.ts 打包成临时 ESM 模块在 Node 里执行，
// 直接 import NAV_PRIMARY_ITEMS / NAV_GROUPS 单一事实源。appNav.ts 的
// 模块级副作用（usePersistedValue / edition 的 window、localStorage、
// import.meta.env 访问）均带守卫，Node 下安全；import.meta.env 通过
// define 注入空对象。
//
// 注意：本脚本是构建链一环（"build": "node scripts/export-menu-config.mjs
// && vite build"）。appNav.ts 变更后重跑本脚本即可再生成 menu-config.json。

import fs from 'fs'
import path from 'path'
import { fileURLToPath, pathToFileURL } from 'url'
import { build } from 'esbuild'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

/**
 * 根据菜单项的权限标记计算 tenantScope
 * - super: true → 'default' (仅超级管理员)
 * - platformOps: true → 'default' (仅平台运维)
 * - providerConsole: true → 'default' (default 租户 + super_admin，2026-09-04)
 * - hideForTenant: true → 'default' (对租户隐藏)
 * - tenantOnly: true → 'tenant' (仅租户门户)
 * - 其余 → '*'
 */
function computeTenantScope(item) {
  if (item.super || item.platformOps || item.providerConsole || item.hideForTenant) return 'default'
  if (item.tenantOnly) return 'tenant'
  return '*'
}

/**
 * 根据菜单项的 plugin 标记计算 pluginScope
 * 2026-07-23: 软注入探测机制
 * - 有 plugin 字段 → 'plugin' (依赖运行时探测)
 * - 无 plugin 字段 → '*' (始终可用)
 */
function computePluginScope(item) {
  if (item.plugin) return 'plugin'
  return '*'
}

// 把 src/config/appNav.ts（单一事实源）打包成临时 ESM 模块并加载，
// 取出 NAV_PRIMARY_ITEMS / NAV_GROUPS。不再内嵌同步副本。
async function loadNavData() {
  const bundlePath = path.join(__dirname, '.export-menu-config.bundle.mjs')
  const appNavPath = path.resolve(__dirname, '../src/config/appNav')
  await build({
    stdin: {
      contents: `import { NAV_PRIMARY_ITEMS, NAV_GROUPS } from ${JSON.stringify(appNavPath)}\nexport const navData = { primary: NAV_PRIMARY_ITEMS, groups: NAV_GROUPS }\n`,
      resolveDir: path.resolve(__dirname, '..'),
      loader: 'ts',
    },
    bundle: true,
    platform: 'node',
    format: 'esm',
    outfile: bundlePath,
    write: true,
    logLevel: 'silent',
    define: {
      // edition.ts 顶层读 import.meta.env.VITE_*；Node 侧无 Vite 注入，
      // 给空对象（esbuild define 要求 JSON 字面量）让 `|| 默认值` 分支接管。
      'import.meta.env': '{}',
    },
  })
  try {
    const mod = await import(pathToFileURL(bundlePath).href)
    return mod.navData
  } finally {
    fs.rmSync(bundlePath, { force: true })
  }
}

const { primary: NAV_PRIMARY_ITEMS, groups: NAV_GROUPS } = await loadNavData()
if (!Array.isArray(NAV_PRIMARY_ITEMS) || !Array.isArray(NAV_GROUPS) || NAV_GROUPS.length === 0) {
  console.error('❌ appNav.ts 导出为空，拒绝生成空菜单配置')
  process.exit(1)
}

// 为每个分组计算 tenantScope
function annotateGroup(group) {
  const items = group.items.map((item) => ({
    ...item,
    tenantScope: computeTenantScope(item),
    pluginScope: computePluginScope(item),
  }))

  // 计算分组的整体可见性
  // - 如果所有 item 都是 'default'，分组也是 'default'
  // - 如果所有 item 都是 'tenant'，分组也是 'tenant'
  // - 如果混合，分组是 'mixed'
  const scopes = new Set(items.map((i) => i.tenantScope))
  let groupScope
  if (scopes.size === 1) {
    groupScope = scopes.values().next().value
  } else {
    groupScope = 'mixed'
  }

  // pluginScope 类似
  const pluginScopes = new Set(items.map((i) => i.pluginScope))
  let groupPluginScope
  if (pluginScopes.size === 1) {
    groupPluginScope = pluginScopes.values().next().value
  } else {
    groupPluginScope = 'mixed'
  }

  return {
    ...group,
    tenantScope: groupScope,
    pluginScope: groupPluginScope,
    items,
  }
}

const menuConfig = {
  version: '1.0.0',
  exported_at: new Date().toISOString(),
  source: 'gateway-appNav',
  // 安全标注：tenantScope 字段说明
  tenantScopeLegend: {
    default: '仅默认租户 + super_admin 可见',
    tenant: '仅租户门户（非 default 租户）可见',
    '*': '所有租户可见',
    mixed: '分组内包含多种可见性，需逐项检查',
  },
  // 软注入标注（2026-07-23）
  pluginScopeLegend: {
    plugin: '依赖 plugin 运行时探测（按 plugin 字段名匹配）',
    '*': '始终可用，无 plugin 依赖',
  },
  primary: NAV_PRIMARY_ITEMS.map((item) => ({
    ...item,
    tenantScope: computeTenantScope(item),
    pluginScope: computePluginScope(item),
  })),
  groups: NAV_GROUPS.map(annotateGroup),
}

// 输出路径
const outputPath = path.resolve(__dirname, '../public/menu-config.json')

// 写入文件
fs.writeFileSync(outputPath, JSON.stringify(menuConfig, null, 2), 'utf-8')

// 统计
const primaryCount = menuConfig.primary.length
const groupCount = menuConfig.groups.length
const itemCount = menuConfig.groups.reduce((sum, g) => sum + g.items.length, 0)
const defaultOnlyCount = menuConfig.groups.reduce(
  (sum, g) => sum + g.items.filter((i) => i.tenantScope === 'default').length,
  0,
)
const tenantOnlyCount = menuConfig.groups.reduce(
  (sum, g) => sum + g.items.filter((i) => i.tenantScope === 'tenant').length,
  0,
)

console.log(`✅ 菜单配置已导出: ${outputPath}`)
console.log(`   版本: ${menuConfig.version}`)
console.log(`   来源: ${menuConfig.source} (import 单一事实源，非内嵌副本)`)
console.log(`   主菜单项: ${primaryCount}`)
console.log(`   分组数: ${groupCount}`)
console.log(`   分组总项: ${itemCount}`)
console.log(`   ─ 仅 default 租户: ${defaultOnlyCount}`)
console.log(`   ─ 仅 tenant 门户: ${tenantOnlyCount}`)
