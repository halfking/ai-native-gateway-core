// simpleNav.test.ts —— 简易模式导航派生的钉测（2026-11-17）
//
// 钉住三件事：
//  1. ★ 结构不变式：专家面每一条入口，在简易模式里**必须**落到三个桶之一
//     （直链 / 组内 / 高级折叠）—— 一条都不许凭空消失。
//     这条是「专家面 63 条一条不删」的机器可验版本。
//  2. 派生口径与台账生成器（scripts/audit/gen-simple-mode-ledger.mjs）**对拍**：
//     两处都从 appNav.ts 派生，数字必须一致；不一致说明有一处漂了。
//  3. 兜底：任何路径都不匹配时，进「高级」组而不是被丢弃。

import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import {
  NAV_GROUPS,
  NAV_PRIMARY_ITEMS,
  visibleNavGroups,
  visibleNavItems,
  type NavItem,
} from './appNav'
import {
  buildSimpleNav,
  SIMPLE_ADVANCED_GROUP_ID,
  SIMPLE_HREF_REWRITE,
} from './simpleNav'

/** 把简易模式渲染 path 还原成专家面真源 path（台账对拍用）。 */
function canonicalExpertPath(path: string): string {
  for (const [expert, href] of Object.entries(SIMPLE_HREF_REWRITE)) {
    if (href === path) return expert
  }
  return path
}

const SUPER_OPTS = {
  isSuperAdmin: true,
  isPlatformOps: true,
  isTenantPortal: false,
  isProviderConsole: true,
  isActivated: true,
}

function expertNav() {
  const primary = visibleNavItems(NAV_PRIMARY_ITEMS, SUPER_OPTS)
  const groups = visibleNavGroups(NAV_GROUPS, SUPER_OPTS)
  return { primary, groups }
}

describe('简易模式导航派生', () => {
  it('★ 结构不变式：专家面每条入口都落到 直链/组内/高级 之一（一条不删）', () => {
    const { primary, groups } = expertNav()
    const result = buildSimpleNav(primary, groups)

    // 对拍用专家面真源 path（L3 href 改写还原后再比）
    const landed = new Set<string>()
    for (const item of result.direct) landed.add(canonicalExpertPath(item.path))
    for (const g of result.groups) {
      for (const item of g.items) landed.add(canonicalExpertPath(item.path))
    }

    const expected: string[] = []
    for (const item of primary) expected.push(item.path)
    for (const g of groups) for (const item of g.items) expected.push(item.path)

    for (const path of expected) {
      expect(landed.has(path), `入口 ${path} 在简易模式里消失了`).toBe(true)
    }
    // 反向：简易模式不得**多出**专家面没有的入口（按真源 path 计）。
    expect(landed.size).toBe(new Set(expected).size)
    expect(result.stats.total).toBe(expected.length)
    expect(result.stats.direct + result.stats.grouped + result.stats.advanced).toBe(expected.length)
  })

  it('★ 与台账生成器对拍：同一个 path 在两边必须落到同一个桶（两份独立实现）', () => {
    // 说明：台账生成器（scripts/audit/gen-simple-mode-ledger.mjs）与本文件
    // （web/src/config/simpleNav.ts）是**两份独立实现**的落点映射，
    // 都从 appNav.ts 派生 ⇒ 对拍能抓到「改了一处忘了另一处」。
    //
    // ★ 对拍用**未做角色过滤**的全量导航（台账口径），因为角色过滤是
    //   另一层语义（谁能看见），不属于落点映射。
    const result = buildSimpleNav(NAV_PRIMARY_ITEMS, NAV_GROUPS)

    const ledger = JSON.parse(
      execFileSync('node', [resolve(process.cwd(), '../scripts/audit/gen-simple-mode-ledger.mjs'), '--json'], {
        encoding: 'utf8',
      }),
    ) as {
      total: number
      distinctPaths: number
      buckets: Record<string, number>
      rows: { path: string; pos: { bucket: string } }[]
    }

    expect(result.stats.total).toBe(ledger.total)
    expect(result.stats.distinctPaths).toBe(ledger.distinctPaths)

    // 前端实际落点 → 专家面真源 path → 桶
    // （L3 会把 /keys 渲染成 /simple/keys，对拍必须还原后再比）
    const frontendBucket = new Map<string, string>()
    for (const item of result.direct) {
      frontendBucket.set(canonicalExpertPath(item.path), 'direct')
    }
    for (const g of result.groups) {
      for (const item of g.items) {
        frontendBucket.set(
          canonicalExpertPath(item.path),
          g.id === SIMPLE_ADVANCED_GROUP_ID ? 'advanced' : g.id,
        )
      }
    }

    const mismatches: string[] = []
    for (const row of ledger.rows) {
      const mine = frontendBucket.get(row.path)
      if (mine === undefined) {
        mismatches.push(`${row.path}: 前端没落点（台账=${row.pos.bucket}）`)
        continue
      }
      if (mine !== row.pos.bucket) {
        mismatches.push(`${row.path}: 前端=${mine} 台账=${row.pos.bucket}`)
      }
    }
    expect(mismatches, `落点映射两处不一致：\n${mismatches.join('\n')}`).toEqual([])
  })

  it('L3：专家面 /keys 落在「密钥与用户」组，渲染 href 改写为 /simple/keys', () => {
    const result = buildSimpleNav(NAV_PRIMARY_ITEMS, NAV_GROUPS)
    const tenantUsers = result.groups.find((g) => g.id === 'tenant-users')
    expect(tenantUsers).toBeDefined()
    const keys = tenantUsers!.items.find((i) => i.path === '/simple/keys')
    expect(keys, '简易模式应渲染 /simple/keys').toBeTruthy()
    expect(tenantUsers!.items.some((i) => i.path === '/keys')).toBe(false)
  })

  it('「高级」组收集所有未点名落点的条目，且不与专家面分组重名', () => {
    const { primary, groups } = expertNav()
    const result = buildSimpleNav(primary, groups)

    const advanced = result.groups.find((g) => g.id === SIMPLE_ADVANCED_GROUP_ID)
    expect(advanced).toBeDefined()
    expect(advanced!.items.length).toBe(result.stats.advanced)
    for (const g of groups) {
      expect(advanced!.id).not.toBe(g.id)
    }
  })

  it('兜底：未知路径不会被丢弃，而是进「高级」组', () => {
    const fake: NavItem[] = [{ path: '/brand-new-page', icon: '🧪' }]
    const result = buildSimpleNav(fake, [])
    expect(result.stats.advanced).toBe(1)
    expect(result.stats.total).toBe(1)
    const advanced = result.groups.find((g) => g.id === SIMPLE_ADVANCED_GROUP_ID)
    expect(advanced!.items[0].path).toBe('/brand-new-page')
  })
})
