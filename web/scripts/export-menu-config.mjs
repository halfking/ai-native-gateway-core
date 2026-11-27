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
import { createHash } from 'crypto'

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
      contents: `import { NAV_PRIMARY_ITEMS, NAV_GROUPS, HIDDEN_NAV_PATHS } from ${JSON.stringify(appNavPath)}\nexport const navData = { primary: NAV_PRIMARY_ITEMS, groups: NAV_GROUPS, hiddenPaths: HIDDEN_NAV_PATHS }\n`,
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

const { primary: NAV_PRIMARY_ITEMS, groups: NAV_GROUPS, hiddenPaths: HIDDEN_NAV_PATHS } = await loadNavData()
if (!Array.isArray(HIDDEN_NAV_PATHS)) {
  console.error('❌ navHidden.ts HIDDEN_NAV_PATHS 导出缺失，拒绝在未知隐藏面下生成菜单配置')
  process.exit(1)
}
if (!Array.isArray(NAV_PRIMARY_ITEMS) || !Array.isArray(NAV_GROUPS) || NAV_GROUPS.length === 0) {
  console.error('❌ appNav.ts 导出为空，拒绝生成空菜单配置')
  process.exit(1)
}

// 为每个分组计算 tenantScope
function annotateGroup(group) {
  // ★ 2026-11-27 非管理员菜单重构轮：menu-config 是**给下游（maintain-web
  //   内嵌兜底）的投递物**，与渲染层共用同一份隐藏清单（navHidden.ts）——
  //   藏了的路径不再进配置，否则下游会宣传一个网关侧已隐藏的入口。
  //   management/userFacing 透传给下游做角色裁剪（navLoader 2026-11-27 起消费）。
  const items = group.items
    .filter((item) => !HIDDEN_NAV_PATHS.includes(item.path))
    .map((item) => ({
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
  // ★ 2026-11-27 非管理员菜单重构轮：角色维度标注（与 tenantScope 正交）。
  roleScopeLegend: {
    management: '仅管理视角可见（super_admin ∧ default 租户；tenant_admin 不属管理视角）',
    userFacing: '所有已登录用户可见（数据范围由后端按角色收窄）',
    '*': '维持原有角色语义',
  },
  primary: NAV_PRIMARY_ITEMS.filter((item) => !HIDDEN_NAV_PATHS.includes(item.path)).map((item) => ({
    ...item,
    tenantScope: computeTenantScope(item),
    pluginScope: computePluginScope(item),
  })),
  groups: NAV_GROUPS.map(annotateGroup),
}

// ★★ 2026-11-23（12h 修订审计轮，落地 AGENTS.md 登记的 L14）：
//
//	本文件原先写 `exported_at: new Date().toISOString()` ——
//	**menu-config.json 是受版本控制的构建产物** ⇒ 每跑一次 build，
//	工作树就因为这一个时间戳变脏 ⇒
//	① 提交前必须人工甄别还原（AGENTS.md §⑭ L4 记载的正是这个动作）；
//	② 部署采集器把「工作树与发布面不一致」判成「并行会话泄漏」，
//	   而 menu-config.json 的 exported_at **每次 build 都变**
//	   ⇒ **恒定误报 2 处泄漏**（AGENTS.md §⑨ ② 记载）。
//
//	⇒ 改成**内容指纹**：对导出内容取 sha256 ⇒ 同样的内容导出同样的字节，
//	   内容变了指纹才变 ⇒ 「内容漂移」仍然可见，「仅时间戳变化」不再可见。
//	⇒ 字段名随之从 exported_at 改为 exported_fingerprint：
//	   一个叫 exported_at 的字段装哈希是**语义说谎**（下一个读它的人
//   会拿它当时间戳解析）⇒ 宁可改名，不留误导。
//	★ 指纹只覆盖**除自身以外**的载荷（下面算的是 menuConfig 的其余部分），
//	  否则会自引用。
const { exported_fingerprint: _omitFingerprint, ...fingerprintPayload } = menuConfig
const exportedFingerprint =
  'sha256:' +
  createHash('sha256')
    .update(JSON.stringify(fingerprintPayload))
    .digest('hex')
    .slice(0, 16)

// 最终写盘对象：指纹放在最后，作为载荷的「封印」
const output = { ...menuConfig, exported_fingerprint: exportedFingerprint }

// 输出路径
const outputPath = path.resolve(__dirname, '../public/menu-config.json')

// 写入文件
fs.writeFileSync(outputPath, JSON.stringify(output, null, 2), 'utf-8')

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
