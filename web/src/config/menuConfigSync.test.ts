/**
 * menuConfigSync — `web/public/menu-config.json` ≡ `src/config/appNav.ts` 反向同步门（F-8）。
 *
 * ## 为什么要有这道门
 *
 * menu-config.json 是**构建产物**：`package.json` 的 build 第一步跑
 * `node scripts/export-menu-config.mjs`，它用 esbuild 把 appNav.ts 打包成临时
 * ESM 模块、取 `NAV_PRIMARY_ITEMS` / `NAV_GROUPS` 单一事实源、逐项加
 * `tenantScope` / `pluginScope` 两个派生标注后写盘。388fe0bd6 修过一次
 * 「源改了、导出件没跟上」的漂移，但当时**没有加防回归门**——此后 appNav.ts
 * 再漂，唯一的发现途径仍是下一次构建时人工看 diff。
 *
 * 既有门只有单向 i18n parity（`src/i18n/parity.test.ts`：校验导出件里的
 * labelKey 在 zh-CN 有定义），**没有**「导出件 ≡ appNav.ts 源」的结构对比门。
 * 本门补上这个缺口。
 *
 * ## 判据（先读 export-menu-config.mjs 确定它导出什么，门就钉什么）
 *
 * 导出脚本对每个 item 做的是 `{...item, tenantScope, pluginScope}`——即
 * **源 item 的全部自有字段** + 两个派生字段；对 group 是 `{...group,
 * tenantScope, pluginScope, items(逐项同样标注)}`。因此本门按同样形状构造
 * 「期望值」并与 JSON 逐字段对比：
 *   - primary 数组：长度 + 逐项全字段；
 *   - groups 数组：长度 + 逐组 id / 自有字段 / items 逐项全字段；
 *   - 派生字段 tenantScope / pluginScope 按 export-menu-config.mjs 的
 *     computeTenantScope / computePluginScope 逻辑**在本文件内复算**
 *     （改脚本逻辑而不重导出 ⇒ 这两个字段先红）。
 *
 * ## 刻意不比的东西（诚实边界）
 *
 *   - `exported_at`：时间戳，每次导出必变，按任务口径忽略；
 *   - `version` / `source` / `tenantScopeLegend` / `pluginScopeLegend`：
 *     这些是**导出脚本自己的常量**，不派生自 appNav.ts——它们若被改，
 *     变的是脚本而不是「源↔产物」的同步关系。若未来脚本改这些常量的形状，
 *     应有意识地更新本门，而不是让本门对常量做常量对比装样子。
 *
 * ## 失败输出的可操作性
 *
 * 每处不一致单独一行，定位到「哪个组（id）、哪一项（path）、哪个字段」，
 * 并给出源值与导出值；整体消息的第一行提示修复动作
 * （跑 `node scripts/export-menu-config.mjs`）。
 *
 * ## 读取手法
 *
 * 跟随同目录 navCoverage.test.ts / src/i18n/parity.test.ts 的既有手法：
 * `import.meta.url` 推出本文件位置，`node:fs` 同步读取
 * `../../public/menu-config.json`。menu-config.json 是 **git 跟踪的产物**
 * （`git ls-files` 可证），所以它缺失时本门**红而不是跳过**——与 parity.test.ts
 * 的「fresh checkout 未构建则跳过」不同：那份文件 tracked 缺失本身就是回归。
 */

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { NAV_PRIMARY_ITEMS, NAV_GROUPS, type NavItem } from './appNav'

const here = dirname(fileURLToPath(import.meta.url))
const menuPath = join(here, '..', '..', 'public', 'menu-config.json')

type JsonItem = Record<string, unknown>

/** 载入导出产物（缺失即红：该文件 git 跟踪，缺失=回归，见文件头）。 */
function loadExportedMenu(): { primary: JsonItem[]; groups: JsonItem[] } {
  let raw: string
  try {
    raw = readFileSync(menuPath, 'utf8')
  } catch {
    throw new Error(
      `menu-config.json 不存在（${menuPath}）。它是 git 跟踪的构建产物；` +
        `缺失本身即回归。请跑 \`node scripts/export-menu-config.mjs\` 重新生成并提交。`,
    )
  }
  const parsed = JSON.parse(raw) as Partial<{ primary: JsonItem[]; groups: JsonItem[] }>
  expect(Array.isArray(parsed.primary), 'menu-config.json 缺 primary 数组——导出脚本形状变了？').toBe(true)
  expect(Array.isArray(parsed.groups), 'menu-config.json 缺 groups 数组——导出脚本形状变了？').toBe(true)
  return { primary: parsed.primary as JsonItem[], groups: parsed.groups as JsonItem[] }
}

// ── 派生字段复算（与 export-menu-config.mjs 的 computeTenantScope /
//    computePluginScope 逐行同义；脚本逻辑变更而未重导出时，此处先红）──

function computeTenantScope(item: NavItem): string {
  if (item.super || item.platformOps || item.providerConsole || item.hideForTenant) return 'default'
  if (item.tenantOnly) return 'tenant'
  return '*'
}

function computePluginScope(item: NavItem): string {
  if (item.plugin) return 'plugin'
  return '*'
}

/** 源 item → 期望的导出形状：剥掉 undefined 值（JSON.stringify 会丢掉它们），再加两个派生字段。 */
function expectedItemShape(item: NavItem): JsonItem {
  const out: JsonItem = {}
  for (const [k, v] of Object.entries(item)) {
    if (v !== undefined) out[k] = v
  }
  out.tenantScope = computeTenantScope(item)
  out.pluginScope = computePluginScope(item)
  return out
}

/** 源 group → 期望的导出形状（组级派生字段取「items 全体一致则同值，否则 mixed」）。 */
function expectedGroupShape(group: (typeof NAV_GROUPS)[number]): JsonItem {
  const out: JsonItem = {}
  for (const [k, v] of Object.entries(group)) {
    if (k === 'items' || v === undefined) continue
    out[k] = v
  }
  // 注意：group 的 items 数组由调用方单独逐项对比（见 groups 判据），
  // 组级 diffItem 会显式忽略两侧的 'items' 键。
  const scopes = new Set(group.items.map(computeTenantScope))
  out.tenantScope = scopes.size === 1 ? [...scopes][0] : 'mixed'
  const pluginScopes = new Set(group.items.map(computePluginScope))
  out.pluginScope = pluginScopes.size === 1 ? [...pluginScopes][0] : 'mixed'
  return out
}

/**
 * 逐字段对比，返回差异行（空数组 = 一致）。
 *
 * 口径：键集合必须完全一致（多/少都算漂移——导出脚本加新字段时本门会红，
 * 写门的人有意识地跟进即可）；值按 Object.is 严格比较（菜单 item 的值域是
 * string/boolean 标量；未来若出现嵌套结构，Object.is 会红 ⇒ 有意识地扩展本门）。
 */
function diffItem(expected: JsonItem, actual: JsonItem, where: string, ignoreKeys: string[] = []): string[] {
  const lines: string[] = []
  const ignore = new Set(ignoreKeys)
  const expectedKeys = new Set(Object.keys(expected).filter((k) => !ignore.has(k)))
  const actualKeys = new Set(Object.keys(actual).filter((k) => !ignore.has(k)))
  for (const k of expectedKeys) {
    if (!actualKeys.has(k)) {
      lines.push(`${where}: 字段 '${k}' 源有（${JSON.stringify(expected[k])}）导出件缺失`)
      continue
    }
    if (!Object.is(expected[k], actual[k])) {
      lines.push(`${where}: 字段 '${k}' 源=${JSON.stringify(expected[k])} 导出=${JSON.stringify(actual[k])}`)
    }
  }
  for (const k of actualKeys) {
    if (!expectedKeys.has(k)) {
      lines.push(`${where}: 导出件多出字段 '${k}'（=${JSON.stringify(actual[k])}，源里没有——导出脚本形状变了？）`)
    }
  }
  return lines
}

/** 数组长度不一致的定位行。 */
function lengthMismatch(where: string, what: string, srcLen: number, expLen: number): string {
  return `${where}: ${what}数量不一致 源=${srcLen} 导出=${expLen}`
}

function labelOf(item: JsonItem): string {
  return typeof item.path === 'string' ? item.path : '(无 path)'
}

describe('menu-config.json ≡ appNav.ts 反向同步门（F-8）', () => {
  const { primary, groups } = loadExportedMenu()

  it('量具未腐化：导出件规模在合理量级（防空数组恒绿）', () => {
    expect(primary.length, 'primary 为空——export-menu-config.mjs 读到空源？').toBeGreaterThanOrEqual(1)
    expect(groups.length, 'groups 为空——export-menu-config.mjs 读到空源？').toBeGreaterThanOrEqual(3)
    const total = groups.reduce((s, g) => s + (Array.isArray(g.items) ? (g.items as JsonItem[]).length : 0), 0)
    expect(
      total,
      `分组总项数仅 ${total}（历史量级 ~54）——解析或导出疑似腐化`,
    ).toBeGreaterThanOrEqual(30)
    // 被钉的字段至少真实出现过：否则逐字段对比在空形状上恒绿。
    const seen = new Set<string>()
    for (const it of [...primary, ...groups.flatMap((g) => (Array.isArray(g.items) ? (g.items as JsonItem[]) : []))]) {
      for (const k of Object.keys(it)) seen.add(k)
    }
    for (const mustHave of ['path', 'icon', 'labelKey', 'tenantScope', 'pluginScope']) {
      expect(seen.has(mustHave), `导出件里没观测到字段 '${mustHave}'——被钉形状与实际产物脱节`).toBe(true)
    }
  })

  it('primary ≡ NAV_PRIMARY_ITEMS（逐项全字段）', () => {
    const diffs: string[] = []
    if (primary.length !== NAV_PRIMARY_ITEMS.length) {
      diffs.push(lengthMismatch('primary', '项', NAV_PRIMARY_ITEMS.length, primary.length))
    }
    const n = Math.min(primary.length, NAV_PRIMARY_ITEMS.length)
    for (let i = 0; i < n; i++) {
      diffs.push(...diffItem(expectedItemShape(NAV_PRIMARY_ITEMS[i]), primary[i], `primary[${i}] (${labelOf(primary[i])})`))
    }
    expect(
      diffs,
      `menu-config.json 的 primary 与 appNav.ts 不一致（${diffs.length} 处）——` +
        `先跑 \`node scripts/export-menu-config.mjs\` 再看剩余差异：\n` +
        diffs.map((d) => `  - ${d}`).join('\n'),
    ).toEqual([])
  })

  it('groups ≡ NAV_GROUPS（组清单 + 每组 items 逐项全字段）', () => {
    const diffs: string[] = []
    if (groups.length !== NAV_GROUPS.length) {
      diffs.push(lengthMismatch('groups', '组', NAV_GROUPS.length, groups.length))
    }
    const n = Math.min(groups.length, NAV_GROUPS.length)
    for (let i = 0; i < n; i++) {
      const src = NAV_GROUPS[i]
      const exp = groups[i]
      const gid = typeof exp.id === 'string' ? exp.id : `#${i}`
      const where = `groups[${i}] (id=${gid})`
      if (src.id !== gid) {
        diffs.push(`${where}: 组 id 源='${src.id}' 导出='${gid}'`)
      }
      diffs.push(...diffItem(expectedGroupShape(src), exp, where, ['items']))
      // items 数组
      const srcItems = src.items
      const expItems = Array.isArray(exp.items) ? (exp.items as JsonItem[]) : []
      if (expItems.length !== srcItems.length) {
        diffs.push(lengthMismatch(`${where}.items`, '项', srcItems.length, expItems.length))
      }
      const m = Math.min(expItems.length, srcItems.length)
      for (let j = 0; j < m; j++) {
        diffs.push(
          ...diffItem(expectedItemShape(srcItems[j]), expItems[j], `${where}.items[${j}] (${labelOf(expItems[j])})`),
        )
      }
    }
    expect(
      diffs,
      `menu-config.json 的 groups 与 appNav.ts 不一致（${diffs.length} 处）——` +
        `先跑 \`node scripts/export-menu-config.mjs\` 再看剩余差异：\n` +
        diffs.map((d) => `  - ${d}`).join('\n'),
    ).toEqual([])
  })
})
