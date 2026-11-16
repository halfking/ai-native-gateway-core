// simpleNav.ts — 简易模式的导航投影（2026-11-17）
//
// 设计稿：docs/03-design/02-feature-design/simple-mode-concept-v2.html §五
//
// ★ 本文件**不另造一份导航**：它从专家面 nav（appNav.ts）**派生**。
//   派生规则只有三条：
//     直链  = 指定路径清单
//     组内  = 分组 → 路径清单
//     其余  = 全部进「高级」折叠组
//   ⇒ 「专家面 63 条一条不删」不是一句承诺，而是**结构上成立**：
//     任何一条入口要么落进直链、要么落进分组、要么落进高级组，**没有第四种去处**。
//
// ★ 为什么用派生而不是手写一份简易导航：
//   手写清单会漂移（v1 稿就写着「61 个入口」，而真源已经是 63）。
//   派生让「新增一个专家面入口」自动出现在高级组里，不需要改第二处。
//
// ★ 落点口径与 scripts/audit/gen-simple-mode-ledger.mjs 的映射表同源：
//   两处必须一致，改一处要改另一处（simpleNav.test.ts 会拿台账生成器对拍）。

import type { NavGroup, NavItem } from './appNav'

/** 简易模式的顶栏直链（路径取自 appNav.ts 的真源） */
export const SIMPLE_DIRECT_PATHS: readonly string[] = [
  '/dashboard',
  '/live',
  '/chat',
  '/admin/settings',
]

/**
 * 简易模式的分组。
 * key = 专家面分组 id（沿用其 labelKey ⇒ 不新增分组文案）
 * value = 该分组在简易模式下保留的路径（必须是专家面真源 path，才能命中派生）
 */
export const SIMPLE_GROUP_PATHS: Record<string, readonly string[]> = {
  'models-routing': ['/models', '/providers'],
  'tenant-users': ['/users', '/keys', '/tenants'],
  'requests-sessions': ['/request-logs', '/usage'],
  guide: ['/examples', '/examples#auto-config', '/help'],
}

/**
 * 落点命中后的 href 改写（L3）。
 * 专家面真源仍是 `/keys`（台账 / navCounts / 「一条不删」都按真源 path 计）；
 * 简易模式点击时跳到两字段签发页 `/simple/keys`。
 * ★ 改写表的 key 必须是专家面 path；value 可以是专家面没有的简化页。
 */
export const SIMPLE_HREF_REWRITE: Readonly<Record<string, string>> = {
  '/keys': '/simple/keys',
}

/** 「高级」折叠组的 id（与专家面任何分组 id 都不冲突） */
export const SIMPLE_ADVANCED_GROUP_ID = 'simple-advanced'

export interface SimpleNavResult {
  /** 顶栏直链（保持 appNav 的原始顺序） */
  direct: NavItem[]
  /** 顶栏分组（专家面分组 id → 只保留指定路径；最后一项是「高级」组） */
  groups: NavGroup[]
  /** 计数：给设置页与钉测用 */
  stats: {
    direct: number
    grouped: number
    advanced: number
    /** 派生自专家面的条目总数（= 专家面条目数，一条不删） */
    total: number
    /** 落地路径去重后的数量 */
    distinctPaths: number
  }
}

/**
 * 从专家面导航派生简易模式导航。
 *
 * @param primaryItems 专家面顶层主链接（已按角色过滤）
 * @param groups       专家面分组（已按角色过滤）
 */
export function buildSimpleNav(
  primaryItems: NavItem[],
  groups: NavGroup[],
): SimpleNavResult {
  // ① 展平成有序条目流：先主链接，后分组（保持专家面顺序）
  const ordered: { item: NavItem; groupId: string }[] = []
  for (const item of primaryItems) ordered.push({ item, groupId: 'primary' })
  for (const g of groups) {
    for (const item of g.items) ordered.push({ item, groupId: g.id })
  }

  // ② 分流：直链 / 组内 / 高级
  const direct: NavItem[] = []
  const byGroup = new Map<string, NavItem[]>()
  const advanced: NavItem[] = []

  for (const { item, groupId } of ordered) {
    const href = SIMPLE_HREF_REWRITE[item.path]
    const rendered = href && href !== item.path ? { ...item, path: href } : item
    if (SIMPLE_DIRECT_PATHS.includes(item.path)) {
      direct.push(rendered)
      continue
    }
    const target = Object.keys(SIMPLE_GROUP_PATHS).find((gid) =>
      SIMPLE_GROUP_PATHS[gid].includes(item.path),
    )
    if (target) {
      const bucket = byGroup.get(target) ?? []
      bucket.push(rendered)
      byGroup.set(target, bucket)
      continue
    }
    advanced.push(rendered)
  }

  // ③ 组装分组：沿用专家面分组的 labelKey（不新增分组文案）
  const outGroups: NavGroup[] = []
  for (const g of groups) {
    const bucket = byGroup.get(g.id)
    if (!bucket || bucket.length === 0) continue
    outGroups.push({ id: g.id, label: g.label, labelKey: g.labelKey, items: bucket })
  }
  // 没有被任何专家面分组命中的条目（理论上不会有），兜底进「高级」组。
  outGroups.push({
    id: SIMPLE_ADVANCED_GROUP_ID,
    labelKey: 'nav.group.advanced',
    items: advanced,
  })

  const distinctPaths = new Set(ordered.map((o) => o.item.path)).size
  const groupedCount = outGroups
    .filter((g) => g.id !== SIMPLE_ADVANCED_GROUP_ID)
    .reduce((acc, g) => acc + g.items.length, 0)

  return {
    direct,
    groups: outGroups,
    stats: {
      direct: direct.length,
      grouped: groupedCount,
      advanced: advanced.length,
      total: ordered.length,
      distinctPaths,
    },
  }
}
