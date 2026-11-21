/**
 * navCounts — 导航口径守卫（简易模式三份方案文档的数不许漂）
 *
 * ## 为什么要有这道门
 *
 * 2026-11-16 出了一次真实漂移：概念稿写「专家面 61 个入口 / 60 个落地路径 /
 * 47 项折叠」，而当天 `appNav.ts` 已经是 63 条目 / 62 路径（`/live2` 请求阶段树
 * 加入 + 「自动更新」跨运维中心与接入指南两组重复挂载）。
 *
 * 漂移的杀伤力不在数字本身，而在它**同时藏起了两条入口**：
 * 旧稿把「用量趋势分析」`/admin/usage-trends` 换成了新建的简化页 `/usage`，
 * 于是这条真源入口从「高级」折叠面板里**整个消失** —— 而稿子自己写着
 * 「没有任何一个入口被删掉」。表格看着完整、渲染出来也好看，
 * 只有拿真源逐条对拍才知道少了两条。
 *
 * ⇒ 这道门分四层，每层都从**真源**派生、期望值**硬编码**：
 *   A 真源层   appNav.ts 的规模（条目 / 落地路径 / 分组 / 顶层）
 *   B 落点层   落点规格（概念稿 §2 的四个直链 + 四个下拉）对照真源
 *              ⇒ 露出 14 / 折叠 50 闭合（2026-11-17 F-07 后口径；PI 轮 /admin/pi-analysis 落地后再 49→50）
 *   C 生成器层 台账生成器与本门同口径（不各说各话）
 *   D 文档层   三份 HTML 里出现的每个菜单口径数字都必须等于锁定值，
 *              且概念稿归属表的**每一行**都要与规格逐条对拍
 *
 * 「期望值硬编码、实际值从真源派生」是刻意的：期望值要是从被断言对象派生的，
 * 真源漂了门也跟着漂，门就成了摆设。真源合法变更（加一条菜单）时，
 * 本门会红 —— 那正是要人**显式**更新锁定值与三份文档的时机。
 *
 * 这道门只守**文档与真源**这一侧；实现侧（`web/src/config/simpleNav.ts`）
 * 由 `simpleNav.test.ts` 守（「一条不删」+ 与台账生成器对拍），两边不重叠。
 *
 * @see docs/03-design/02-feature-design/simple-mode-concept.html（条目归属）
 * @see scripts/audit/gen-simple-mode-ledger.mjs（台账生成器）
 */

import { describe, it, expect } from 'vitest'
import { readFileSync, existsSync } from 'node:fs'
import { join, resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
import { NAV_GROUPS, NAV_PRIMARY_ITEMS } from './appNav'
import navLocale from '../locales/zh-CN/nav'

const here = dirname(fileURLToPath(import.meta.url))
const REPO = resolve(here, '../../..')
const DOC_DIR = join(REPO, 'docs/03-design/02-feature-design')

// ─────────────────────────────────────────────────────────────
// 锁定的口径（硬编码常量 —— 不从任何被断言对象派生）
// ─────────────────────────────────────────────────────────────

/** 专家面菜单条目总数。 */
const EXPECTED_ITEMS =68
/** 落地路径数。比条目少 1：「自动更新」跨两组重复挂载，算两条目一条路径。 */
const EXPECTED_PATHS =67
/** 顶栏分组数。 */
const EXPECTED_GROUPS = 8
/** 顶层主链接数（不进分组，直接挂顶栏）。 */
const EXPECTED_PRIMARY = 3
/** 简易模式直接露出的既有入口数（4 直链 + 10 分组下拉；2026-11-17 F-07 后「请求」组含 /tenant/usage）。 */
const EXPECTED_EXPOSED = 14
/** 收进「高级」折叠组的条数（2026-11-17 F-07 把 /tenant/usage 提升进「请求」组后 50→49；
 *  2026-11-17 PI 轮 /admin/pi-analysis 落地后 49→50；
 *  2026-11-20 会话分析轮 4 项（总览/人员/智能体/模型排行）落地后 50→53；
 *  2026-11-21 错误处理手册轮 /admin/error-playbook 落地后 53→54）。
 *  ★ 口径：FOLDED = ITEMS − EXPOSED（落在「高级折叠」桶的条目数）。 */
const EXPECTED_FOLDED =54
/** 简易模式可见页面数（13 个既有入口 + 1 个新建简化页 `/usage`）。 */
const EXPECTED_VISIBLE_PAGES = 14

/**
 * 专家面分组 id → 简易模式下的分组名。
 *
 * 简易模式把四个分组重新命名（概念稿 §2 的四个下拉面板）；
 * 专家面分组 id 是实现侧 `simpleNav.ts` 用的键，这张表把两边对上。
 */
const SIMPLE_GROUP_CN: Record<string, string> = {
  'models-routing': '供应商与模型',
  'tenant-users': '密钥与用户',
  'requests-sessions': '请求',
  guide: '接入指南',
}

// ─────────────────────────────────────────────────────────────
// 真源派生
// ─────────────────────────────────────────────────────────────

type SourceItem = { path: string; group: string }

const sourceItems: SourceItem[] = [
  ...NAV_PRIMARY_ITEMS.map((i) => ({ path: i.path, group: 'primary' })),
  ...NAV_GROUPS.flatMap((g) => g.items.map((i) => ({ path: i.path, group: g.id }))),
]

const sourcePaths = sourceItems.map((i) => i.path)
const distinctPaths = new Set(sourcePaths).size

/** 专家面分组 id → 该组在 zh-CN 词条里的中文名。 */
function expertGroupCn(groupId: string): string {
  if (groupId === 'primary') return '顶层主链接'
  const g = NAV_GROUPS.find((x) => x.id === groupId)
  const key = (g?.labelKey ?? '').replace(/^nav\.group\./, '')
  return (navLocale.group as Record<string, string>)[key] ?? groupId
}

/**
 * 简易模式的落点**规格**（= 概念稿 §2 的四个下拉面板 + 四个直链）。
 *
 * 为什么规格写在门里，而不是只依赖实现（`web/src/config/simpleNav.ts`）：
 *   ① 门必须**只依赖已入库的文件**才能单独提交 —— 静态 import 一个还在
 *      在途、未必入库的实现，会让「只提交这道门」的分支直接编译不过；
 *   ② 规格本来就是**文档侧的东西**：概念稿 §2 的四个面板就是这么画的，
 *      门要验的正是「文档画的规则」在真源上成立；
 *   ③ 实现侧另有 `simpleNav.test.ts` 守着（「一条不删」+ 与台账生成器对拍），
 *      两边各守一头，不重叠。
 *
 * 规格与实现若不一致 ⇒ 文档与实现对不上，这条差异由人裁决，不由门猜。
 */
const DIRECT_LINKS = ['/dashboard', '/live', '/chat', '/admin/settings']
const GROUP_ITEMS: Record<string, string[]> = {
  供应商与模型: ['/models', '/providers'],
  密钥与用户: ['/users', '/keys', '/tenants'],
  // ★ 2026-11-17（12h 审计 N1）：F-07 死路径修复后，简易模式「请求」组
  //   下拉含「租户用量」/tenant/usage（simpleNav.ts:38 与台账生成器已同口径）；
  //   本门的规格表也必须跟齐，否则两套分桶法各说各话（生成器 49 vs 本门 50）。
  请求: ['/request-logs', '/tenant/usage'],
  接入指南: ['/examples', '/examples#auto-config', '/help'],
}

/** 每条真源路径按规格落到哪个桶。 */
const bucketOf = new Map<string, string>()
for (const p of DIRECT_LINKS) bucketOf.set(p, '顶栏直链')
for (const [name, paths] of Object.entries(GROUP_ITEMS)) {
  for (const p of paths) bucketOf.set(p, name)
}
for (const item of sourceItems) {
  if (!bucketOf.has(item.path)) bucketOf.set(item.path, '高级折叠')
}

const exposedCount = DIRECT_LINKS.length + Object.values(GROUP_ITEMS).flat().length
const foldedCount = sourceItems.length - exposedCount

/**
 * 文档里该行该显示的位置 chip。
 *
 * 绝对 URL（http 开头）显示「站外链接」—— 它落在「模型与路由」组里，
 * 随组折叠、但点开走新窗口。同源路径即使带 `external: true`（/maintain/*）
 * 也照常显示「高级折叠」。
 */
function expectedChip(path: string): string {
  if (path.startsWith('http')) return '站外链接'
  return bucketOf.get(path) ?? '（实现里没有落点）'
}

// ─────────────────────────────────────────────────────────────
// A · 真源层
// ─────────────────────────────────────────────────────────────

describe('A 真源：appNav.ts 的菜单规模', () => {
  it(`条目 ${EXPECTED_ITEMS} / 落地路径 ${EXPECTED_PATHS} / ${EXPECTED_GROUPS} 组 / 顶层 ${EXPECTED_PRIMARY} 项`, () => {
    expect(sourceItems.length).toBe(EXPECTED_ITEMS)
    expect(distinctPaths).toBe(EXPECTED_PATHS)
    expect(NAV_GROUPS.length).toBe(EXPECTED_GROUPS)
    expect(NAV_PRIMARY_ITEMS.length).toBe(EXPECTED_PRIMARY)
  })

  it('条目数与落地路径数差的 1 条，是「自动更新」跨两组重复挂载', () => {
    // 为什么单独钉这一条：路径数少于条目数只有一个合法原因 ——
    // 同一条路径在两个分组里各登记一次。少了这条断言，将来某个分组
    // 不小心多挂一次、或漏挂一次，门都只看到「差 1」而不会指出是谁。
    const dup = sourcePaths.filter((p, i) => sourcePaths.indexOf(p) !== i)
    expect(dup).toEqual(['/maintain/ops/autoupdate'])
    expect(sourceItems.filter((i) => i.path === '/maintain/ops/autoupdate').map((i) => i.group)).toEqual([
      'opsplatform',
      'guide',
    ])
  })
})

// ─────────────────────────────────────────────────────────────
// B · 落点层（规格 vs 真源）
// ─────────────────────────────────────────────────────────────

/** 简易模式分组名 → 它对应的专家面分组 id。 */
const EXPERT_GROUP_OF_SIMPLE: Record<string, string> = Object.fromEntries(
  Object.entries(SIMPLE_GROUP_CN).map(([id, name]) => [name, id]),
)

describe('B 落点：简易模式露出 14 / 折叠 50 闭合', () => {
  it('直链 4 条 / 组内 10 条 / 高级 50 条', () => {
    expect(DIRECT_LINKS).toHaveLength(4)
    expect(Object.values(GROUP_ITEMS).flat()).toHaveLength(10)
    expect(exposedCount).toBe(EXPECTED_EXPOSED)
    expect(foldedCount).toBe(EXPECTED_FOLDED)
  })

  it(`露出 ${EXPECTED_EXPOSED} 个既有入口，其余 ${EXPECTED_FOLDED} 条收进「高级」折叠组`, () => {
    expect(exposedCount).toBe(EXPECTED_EXPOSED)
    expect(foldedCount).toBe(EXPECTED_FOLDED)
  })

  it('折叠 = 条目 − 露出（不留差额，否则有条入口没落点）', () => {
    expect(EXPECTED_EXPOSED + EXPECTED_FOLDED).toBe(EXPECTED_ITEMS)
    expect(exposedCount + foldedCount).toBe(EXPECTED_ITEMS)
    expect(sourceItems.length).toBe(EXPECTED_ITEMS)
    expect(distinctPaths).toBe(EXPECTED_PATHS)
  })

  it('每一条真源入口都有落点，没有第四条去处', () => {
    for (const item of sourceItems) {
      expect(bucketOf.has(item.path), `入口没有落点：${item.path}`).toBe(true)
    }
    expect(bucketOf.size).toBe(new Set(sourcePaths).size)
  })

  it('规格里每个分组的路径，在真源里确实属于它对应的专家面分组', () => {
    // 为什么要有这条：规格把 /models /providers 叫「供应商与模型」，
    // 而真源里它们挂在「模型与路由」组。规格把路径放错分组时，
    // 数量可能照样对得上（13/50 不变）⇒ 只有逐条查它属于哪个专家面分组才抓得到。
    // ★ 跨组提升白名单（2026-11-17 12h 审计 N1）：简易模式允许把个别条目
    //   从它的专家面组**提升**到另一简易组（concept-v2「重新归组」语义）。
    //   首例：/tenant/usage 真源在 operations（运营管理），F-07 死路径修复后
    //   提升进「请求」组（simpleNav.ts + 台账生成器已同口径）。这里的键是
    //   「路径 → 它被提升进的简易分组名」，命中即放行，不再要求同组。
    const CROSS_GROUP_PROMOTIONS: Record<string, string> = {
      '/tenant/usage': '请求',
    }
    for (const [name, paths] of Object.entries(GROUP_ITEMS)) {
      const expertId = EXPERT_GROUP_OF_SIMPLE[name]
      expect(expertId, `分组「${name}」没有对应的专家面分组`).toBeTruthy()
      for (const p of paths) {
        const item = sourceItems.find((i) => i.path === p)
        expect(item, `规格里的路径不在真源中：${p}`).toBeTruthy()
        if (CROSS_GROUP_PROMOTIONS[p] === name) continue
        expect(item!.group, `${p} 在真源里属于「${item!.group}」，规格却把它放在「${name}」`).toBe(
          expertId,
        )
      }
    }
  })

  it('直链与下拉的每条路径都在真源里', () => {
    for (const p of DIRECT_LINKS) expect(sourcePaths).toContain(p)
    for (const p of Object.values(GROUP_ITEMS).flat()) expect(sourcePaths).toContain(p)
  })
})

// ─────────────────────────────────────────────────────────────
// C · 生成器层
// ─────────────────────────────────────────────────────────────

describe('C 台账生成器与本门同口径', () => {
  const tool = join(REPO, 'scripts/audit/gen-simple-mode-ledger.mjs')

  it('生成器报出的条目数与落地路径数等于锁定值', () => {
    expect(existsSync(tool)).toBe(true)
    const out = execFileSync(process.execPath, [tool, '--json'], {
      cwd: REPO,
      encoding: 'utf8',
    })
    const data = JSON.parse(out) as {
      total: number
      distinctPaths: number
      counts: Record<string, number>
    }
    expect(data.total).toBe(EXPECTED_ITEMS)
    expect(data.distinctPaths).toBe(EXPECTED_PATHS)
  })

  it('生成器的「高级折叠 + 站外链接」= 折叠 50（两套分桶法的换算钉在这里）', () => {
    // 生成器按**打开方式**分桶：高级折叠 49 + 站外链接 1。
    // 本门按**物理位置**分桶：折叠区 50 条（49 页内 + 1 站外随组收起）。
    // 同一个 50 的两种切法 ⇒ 换算必须写死，否则两套口径会各说各话。
    // （2026-11-17 F-07 把 /tenant/usage 提升进「请求」组后 50→49；
    //  2026-11-17 PI 轮 /admin/pi-analysis 落地后 49→50。）
    const out = execFileSync(process.execPath, [tool, '--json'], {
      cwd: REPO,
      encoding: 'utf8',
    })
    const data = JSON.parse(out) as { counts: Record<string, number> }
    const folded = (data.counts['高级折叠'] ?? 0) + (data.counts['站外链接'] ?? 0)
    expect(folded).toBe(EXPECTED_FOLDED)
  })
})

// ─────────────────────────────────────────────────────────────
// D · 文档层
// ─────────────────────────────────────────────────────────────

const DOCS = [
  { file: 'simple-mode-concept.html', label: '整页与菜单概念稿' },
  { file: 'simple-mode-ui-mockup.html', label: '控制台效果稿' },
  { file: 'simple-expert-mode-design.html', label: '设计方案' },
]

/** 去掉样式、脚本与图形，只留正文文字 —— 数字口径只看正文。 */
function docText(file: string): string {
  const html = readFileSync(join(DOC_DIR, file), 'utf8')
  return html
    .replace(/<style[^>]*>[\s\S]*?<\/style>/g, ' ')
    .replace(/<script[^>]*>[\s\S]*?<\/script>/g, ' ')
    .replace(/<svg[^>]*>[\s\S]*?<\/svg>/g, ' ')
    .replace(/<[^>]+>/g, ' ')
    .replace(/[ \t]+/g, ' ')
}

const strip = (s: string) => s.replace(/<[^>]+>/g, '').trim()

/**
 * 文档里「菜单口径数字」的措辞清单。
 *
 * 每条规则只认**带上下文**的数字（数字旁边必须有「入口」「落地路径」
 * 「折叠/收进」这类词），不认裸数字 —— 稿子里还有 463 个设置项、
 * 93 个后台 worker、31 条孤儿白名单、47 项 supervisor 等与菜单无关的数。
 */
const COUNT_RULES: { label: string; re: RegExp; expected: number }[] = [
  { label: '专家面条目数', re: /(\d+)\s*个(?:导航入口|专家面入口|入口)/g, expected: EXPECTED_ITEMS },
  { label: '专家面条目数（数在后）', re: /(?:导航入口|专家面入口|入口)\s*(\d+)\s*个/g, expected: EXPECTED_ITEMS },
  { label: '专家面条目数（项形态）', re: /专家面\s*(\d+)\s*项/g, expected: EXPECTED_ITEMS },
  { label: '专家面条目数（导航 N 项）', re: /导航\s*(\d+)\s*项/g, expected: EXPECTED_ITEMS },
  { label: '专家面条目数（各算一个入口，共 N 项）', re: /各算一个入口，共\s*(\d+)\s*项/g, expected: EXPECTED_ITEMS },
  { label: '专家面条目数（收到 N 项）', re: /(\d+)\s*项\s*收到/g, expected: EXPECTED_ITEMS },
  { label: '专家面条目数（· N 项）', re: /·\s*(\d+)\s*项(?!\s*设置)/g, expected: EXPECTED_ITEMS },
  { label: '落地路径数', re: /(\d+)\s*个落地路径/g, expected: EXPECTED_PATHS },
  { label: '落地路径数（数在后）', re: /落地路径\s*(\d+)\s*个/g, expected: EXPECTED_PATHS },
  {
    label: '折叠条数',
    re: /(\d+)\s*条\s*(?:收进|收在|按专家面|专家面收进|折叠|进「高级」|收进高级折叠|·\s*展开后按专家面)/g,
    expected: EXPECTED_FOLDED,
  },
  { label: '可见页面数', re: /(\d+)\s*个(?:可见)?页面/g, expected: EXPECTED_VISIBLE_PAGES },
]

/**
 * 「8 组 N 项 + 顶层 M 项」这种**分解式**写法：把各部分数一起锁住。
 *
 * 为什么单独一条：只锁总数 63 的话，「8 组 59 项 + 顶层 4 项」也能凑到 63 ——
 * 分组规模变了而总数没变，正是上次漂移的形态之一。
 */
const DECOMPOSITION_RULE = {
  // 「8 个分组 64 项 + 顶层 3 项」与「8 组 64 项 + 顶层 3 项 = 67」两种写法都要认，
  // 「= 总数」可有可无（文档头不带、页脚带）。
  re: /(\d+)\s*(?:个分组|组)\s*(\d+)\s*项\s*\+\s*顶层\s*(\d+)\s*项(?:\s*=\s*(\d+))?/g,
  groups: EXPECTED_GROUPS,
 groupItems:65,
  primaryItems: EXPECTED_PRIMARY,
  total: EXPECTED_ITEMS,
}

/** 旧口径的措辞 —— 出现即漂移，逐条钉死。 */
const STALE_PATTERNS: { label: string; re: RegExp }[] = [
  { label: '旧条目数 61', re: /61\s*个(?:导航入口|专家面入口|入口)/g },
  { label: '旧条目数 61（数在后）', re: /(?:导航入口|专家面入口|入口)\s*61\s*个/g },
  { label: '旧落地路径数 60', re: /60\s*个落地路径|落地路径\s*60\s*个/g },
  {
    label: '旧折叠数 47',
    re: /47\s*(?:条|项)\s*(?:收进|收在|按专家面|专家面收进|折叠|进「高级」|收进高级折叠)/g,
  },
  { label: '旧折叠数 47（面板头）', re: /全部\s*47\s*项/g },
  { label: '旧分组规模 8 组 59 项', re: /8\s*组\s*59\s*项/g },
  { label: '旧可见入口数 18', re: /18\s*个左右/g },
  // ★ 2026-11-17（PI 轮）：重锁到 64 条后，**分组项数本身变成了 61**
  //   （8 组 61 项 + 顶层 3 项 = 64）⇒ 原来那条「61 项 ⇒ 旧口径」的钝规则
  //   会把**合法的分解式**判成漂移 ⇒ 收窄成「只认总数形态的 61 项」，
  //   放过「N 组 61 项」这种合法的分组规模写法。
  { label: '旧口径 61 项（总数形态）', re: /(?<!组 )61\s*项/g },
]

describe('D 文档：三份稿的菜单口径等于锁定值', () => {
  it('三份稿都在', () => {
    for (const d of DOCS) {
      expect(existsSync(join(DOC_DIR, d.file)), `${d.label} 缺失：${d.file}`).toBe(true)
    }
  })

  for (const d of DOCS) {
    describe(d.label, () => {
      const text = docText(d.file)

      for (const rule of COUNT_RULES) {
        it(`${rule.label} = ${rule.expected}`, () => {
          const hits = [...text.matchAll(rule.re)].map((m) => Number(m[1]))
          // 每处出现都必须是锁定值：措辞可以改，数字不能改
          for (const v of hits) expect(v).toBe(rule.expected)
        })
      }

      it(`分组规模分解 = ${DECOMPOSITION_RULE.groups} 组 ${DECOMPOSITION_RULE.groupItems} 项 + 顶层 ${DECOMPOSITION_RULE.primaryItems} 项 = ${DECOMPOSITION_RULE.total}`, () => {
        const hits = [...text.matchAll(DECOMPOSITION_RULE.re)]
        for (const m of hits) {
          expect(Number(m[1]), '分组数变了').toBe(DECOMPOSITION_RULE.groups)
          expect(Number(m[2]), '分组项数变了').toBe(DECOMPOSITION_RULE.groupItems)
          expect(Number(m[3]), '顶层项数变了').toBe(DECOMPOSITION_RULE.primaryItems)
          if (m[4] !== undefined) expect(Number(m[4]), '总数变了').toBe(DECOMPOSITION_RULE.total)
        }
      })

      for (const stale of STALE_PATTERNS) {
        it(`没有旧口径：${stale.label}`, () => {
          const hits = [...text.matchAll(stale.re)].map((m) => m[0].trim())
          expect(hits, `文档还在用旧口径：${hits.join(' / ')}`).toEqual([])
        })
      }
    })
  }
})

describe('D 文档：概念稿的归属表逐条对拍真源与实现', () => {
  const html = readFileSync(join(DOC_DIR, 'simple-mode-concept.html'), 'utf8')

  type LedgerRow = { name: string; path: string; group: string; chips: string[] }
  const rows: LedgerRow[] = [...html.matchAll(/<tr data-ledger-row>([\s\S]*?)<\/tr>/g)].map((m) => {
    const tds = [...m[1].matchAll(/<td[^>]*>([\s\S]*?)<\/td>/g)].map((x) => x[1])
    return {
      name: strip(tds[0] ?? ''),
      path: strip(tds[1] ?? ''),
      group: strip(tds[2] ?? ''),
      chips: [...(tds[3] ?? '').matchAll(/<span class="chip[^"]*">([^<]*)<\/span>/g)].map((x) => x[1]),
    }
  })

  it(`台账 ${EXPECTED_ITEMS} 行`, () => {
    expect(rows).toHaveLength(EXPECTED_ITEMS)
  })

  it('每一条真源入口都在表里，且不多不少', () => {
    // 逐条对拍，不只比行数 —— 少一条和多一条都可能。
    for (const item of sourceItems) {
      expect(rows.map((r) => r.path), `台账漏了：${item.path}`).toContain(item.path)
    }
    const extra = rows.map((r) => r.path).filter((p) => !sourcePaths.includes(p))
    expect(extra, `台账里有真源没有的路径：${extra.join(' / ')}`).toEqual([])
  })

  it('每行的「专家分组」与真源分组一致（含重复挂载的条目）', () => {
    // 为什么比对「路径 + 分组」的**配对全量**而不是逐行 find()：
    // 「自动更新」跨运维中心与接入指南两组各挂一次 ⇒ 台账里两行同路径不同分组。
    // 逐行 find() 只会拿到第一条，于是第二行必然误判；反过来，
    // 两行的分组写反了，逐行 find() 也发现不了。配对全量两种错都抓。
    const srcPairs = sourceItems.map((i) => `${i.path}\t${expertGroupCn(i.group)}`).sort()
    const docPairs = rows.map((r) => `${r.path}\t${r.group}`).sort()
    const diff = docPairs.filter((p, i) => p !== srcPairs[i])
    expect(diff, `台账的「路径 + 专家分组」与真源不一致：\n${diff.join('\n')}`).toEqual([])
    expect(docPairs).toHaveLength(srcPairs.length)
  })

  it('每行的「简易模式位置」与落点规格一致', () => {
    // 这一条是整套门里最硬的一条：文档说某条在「高级折叠」，
    // 规格却把它放在直链（或反过来）⇒ 文档漂了，肉眼看不出来。
    for (const row of rows) {
      const want = expectedChip(row.path)
      expect(row.chips, `${row.path}（${row.name}）的位置写成了 ${row.chips.join('/')}，应是 ${want}`).toContain(
        want,
      )
    }
  })

  it('台账第一列是显示名，不是 i18n key', () => {
    // key 形状（小驼峰开头 + 点分）才是问题；官方中文名里就有英文词
    // （Agent Registry、VibeCoding）⇒ 不能按「不含中文」判。
    const keys = rows
      .map((r) => r.name)
      .filter((n) => /^[a-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)+$/.test(n) || n.startsWith('nav.'))
    expect(keys, `台账里混进了 i18n key：${keys.join(' / ')}`).toEqual([])
  })
})
