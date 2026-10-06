import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

// 移动端行点击契约门禁（docs/UI规范/03 §3.3 条款 1/2/5）。
//
// ## 为什么要有门
//
// 03 §3.3 规定「**行 = 详情入口**」，但本仓的实现分**两套**：
//   · 卡片形态：`ResponsiveDataView` → `CardList`，靠 `clickable` + `@row-click`；
//   · 表格形态：桌面 `<tr class="row-click" @click>` —— **compact 下不存在**。
// 而 `ResponsiveDataView` 的 `#table` 插槽**只在桌面渲染**。
// ⇒ 于是「把入口写在 `#table` 里」这个写法，会让 compact 端**静默丢掉**进入详情的路径：
//   不报错、测试全绿、页面照常渲染，只是手机上点不动。
//
// ## 判据：登记表与实测集合**双向相等**
//
// 任何「compact 无入口」的调用点，都必须显式登记，并写清**为什么**可以没有入口：
//   · `NON_NAVIGABLE` —— 本就没有详情目的地（登记即「已确认合格」）；
//   · `KNOWN_GAP`    —— 有详情目的地却没有 compact 入口（**登记即「不合格但已知」**）。
// 两条登记都必须是**具体理由**，不接受空字符串 ——
// 「注册表」若无理由字段，就退化成一张「已知问题清单」的摆设。
//
// ⚠️ 03 §3.3 的门禁描述（「扫 `CardList` 调用点 + `#cards` 插槽两层」）是照参考仓写的，
// 与本仓实际形状**不符**：本仓 `web/src` 里 `#cards` 插槽**零消费者**，
// 卡片全部由 `ResponsiveDataView` 默认渲染。门禁按**本仓实际形状**写。

const ROOT = resolve(process.cwd(), 'src')

/** compact 下确实有进入详情/动作的入口。 */
function hasCompactEntry(tag: string, slots: Set<string>): boolean {
  const clickable = /:clickable\b|clickable=/.test(tag)
  return (clickable && /@row-click\b/.test(tag)) || slots.has('actions') || slots.has('cards')
}

/**
 * 注释剥离口径：块注释 + HTML 注释 + trim 后以 `//` / `*` / `/*` 开头的整行。
 *
 * ★ **必须保持行结构与字符位置不变**（把注释内容替换成等长空格）。
 * 第一版直接 `replace(/\/\*[\s\S]*?\*\//g, '')`，整块注释被删 ⇒
 * **后续所有行号整体前移**，登记表按原文行号写的，于是 13 条断言里 4 条红，
 * 且红的原因是「找不到」而不是「不合格」—— 判量具坏了，不是产品坏了。
 */
function stripComments(src: string): string {
  const blank = (m: string) => m.replace(/[^\n]/g, ' ')
  return src
    .replace(/\/\*[\s\S]*?\*\//g, blank)
    .replace(/<!--[\s\S]*?-->/g, blank)
    .split('\n')
    .map((l) => {
      const t = l.trim()
      return t.startsWith('//') || t.startsWith('*') || t.startsWith('/*') ? blank(l) : l
    })
    .join('\n')
}

function walk(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    const full = join(dir, e)
    if (statSync(full).isDirectory()) walk(full, out)
    else if (e.endsWith('.vue')) out.push(full)
  }
  return out
}

interface Site {
  file: string
  line: number
  hasCompactEntry: boolean
  slots: string[]
  /** 该文件是否存在详情目的地（router.push / router.replace）。 */
  fileHasNav: boolean
}

/** 取 `<ResponsiveDataView …>` 起始标签的完整文本（含 `>`），正确处理属性里的花括号。 */
function readTag(src: string, from: number): { tag: string; end: number } {
  let i = from
  let depth = 0
  while (i < src.length) {
    const c = src[i]
    if (c === '{') depth++
    else if (c === '}') depth--
    else if (c === '>' && depth === 0) return { tag: src.slice(from, i + 1), end: i + 1 }
    i++
  }
  return { tag: src.slice(from, i), end: i }
}

function scan(): Site[] {
  const sites: Site[] = []
  for (const file of walk(ROOT)) {
    const raw = readFileSync(file, 'utf8')
    if (file.endsWith('ResponsiveDataView.vue')) continue // 组件自身（文档注释里的示例）
    const src = stripComments(raw)
    if (!src.includes('<ResponsiveDataView')) continue
    const rel = relative(resolve(process.cwd()), file).replace(/\\/g, '/')
    const fileHasNav = /router\.push|router\.replace/.test(src)
    for (const m of src.matchAll(/<ResponsiveDataView\b/g)) {
      const from = m.index! + m[0].length - 1
      const { tag, end } = readTag(src, from)
      const close = src.indexOf('</ResponsiveDataView>', end)
      const body = src.slice(end, close === -1 ? src.length : close)
      const slots = [...body.matchAll(/<template #(\w+)/g)].map((x) => x[1])
      sites.push({
        file: rel,
        // stripComments 保持行结构，故 src 上的偏移与 raw 行号一致
        line: src.slice(0, m.index!).split('\n').length,
        hasCompactEntry: hasCompactEntry(tag, new Set(slots)),
        slots,
        fileHasNav,
      })
    }
  }
  return sites
}

/** 本仓确实没有详情目的地的调用点（登记即「已确认合格」）。 */
const NON_NAVIGABLE: Array<[string, number, string]> = [
  ['src/views/CorrelationsView.vue', 307, '按模型聚合的相关性（rows=resp.by_model，title-key=label）：一行是「模型」的统计，不是某次调用，无详情页'],
  ['src/views/CorrelationsView.vue', 351, '按策略聚合（resp.by_strategy，title-key=label）：策略是下拉筛选维度，不是可导航实体'],
  ['src/views/CorrelationsView.vue', 391, '按任务类型聚合（resp.by_task_type，title-key=label）：任务类型同为筛选维度，无落地页'],
  ['src/views/CorrelationsView.vue', 437, '模型×任务交叉表（modelTaskRows，title-key=card_key）：交叉统计单元，两侧都无详情页'],
  ['src/views/FreeDiscoveryView.vue', 1083, '发现流列表，本视图只做发现不做详情'],
  ['src/views/QualityCorrelationsView.vue', 186, '质量相关性聚合，无单条详情'],
  ['src/views/UserProfileView.vue', 285, '画像内嵌的 top_tasks（title-key=task_id）：任务名是分组标签，本视图无任务详情页'],
  ['src/views/UserProfileView.vue', 308, '画像内嵌的 top_end_users（title-key=end_user_id）：画像页本身就是终点，无二级详情'],
  ['src/views/tenant/MaaSAccountView.vue', 282, '账号内嵌表，无账号详情页'],
  ['src/views/tenant/MaaSUsageView.vue', 354, '用量内嵌表，无用量详情页'],
  ['src/views/tenant/TenantModelsView.vue', 207, '租户模型内嵌表，无详情页'],
]

/** 有详情目的地、但 compact 没有入口 —— **不合格但已知**，等产品裁决。 */
const KNOWN_GAP: Array<[string, number, string]> = [
  [
    'src/views/ApprovalListView.vue',
    451,
    '详情目的地 /admin/approvals/:request_id 存在，但 viewDetail 两个调用点都写在 #table 插槽里；'
      + '该视图只传 #table ⇒ compact 卡片无任何入口。修法涉及桌面行是否整行可点（行内有 ✕ 拒绝等写操作），属产品裁决。',
  ],
  [
    'src/views/provider-detail/LogsTab.vue',
    289,
    'goCanonical（跳 /models?q=）写在 #table 内，compact 丢失该跨实体跳转。'
      + '按 03 §3.3 条款 2 跨实体跳转属于「另一个意图」，本就该放 actions 区 —— 是位置放错，不是契约破损。',
  ],
]

const key = (f: string, l: number) => `${f}:${l}`
const sites = scan()

describe('03 §3.3 移动端行点击契约', () => {
  it('登记表与实测集合双向相等', () => {
    const measured = new Set(sites.filter((s) => !s.hasCompactEntry).map((s) => key(s.file, s.line)))
    const registered = new Set([...NON_NAVIGABLE, ...KNOWN_GAP].map(([f, l]) => key(f, l)))

    const unregistered = [...measured].filter((k) => !registered.has(k)).sort()
    const stale = [...registered].filter((k) => !measured.has(k)).sort()

    expect(
      unregistered,
      `这些调用点 compact 无入口但**未登记**（要么补入口，要么登记理由）：\n  ${unregistered.join('\n  ')}`,
    ).toEqual([])
    expect(
      stale,
      `登记表里有**已不成立**的调用点 —— 要么该处已具备 compact 入口（缺陷已修，未销账），要么行号漂移/文件已改名：\n  ${stale.join('\n  ')}`,
    ).toEqual([])
    expect(measured.size, '实测集合大小变化时务必复核上面两条的输出').toBe(registered.size)
  })

  it('每条登记都必须写明理由（拒绝空理由的走过场）', () => {
    for (const [f, l, why] of [...NON_NAVIGABLE, ...KNOWN_GAP]) {
      expect(typeof why, `${key(f, l)} 缺少理由`).toBe('string')
      expect(why.trim().length, `${key(f, l)} 的理由是空的`).toBeGreaterThan(8)
    }
  })

  it('KNOWN_GAP 的每一项，其文件必须真的有详情目的地（缺口已修则本门转红）', () => {
    for (const [f, l] of KNOWN_GAP) {
      const site = sites.find((s) => s.file === f && s.line === l)
      expect(site, `${key(f, l)} 在源码里找不到了`).toBeTruthy()
      expect(site!.fileHasNav, `${key(f, l)} 文件里已无 router.push ⇒ 缺口可能已修，请销账`).toBe(true)
    }
  })

  it('NON_NAVIGABLE 的每一项，其文件不得有详情目的地（有了就该给入口）', () => {
    for (const [f, l] of NON_NAVIGABLE) {
      const site = sites.find((s) => s.file === f && s.line === l)
      expect(site, `${key(f, l)} 在源码里找不到了`).toBeTruthy()
      expect(
        site!.fileHasNav,
        `${key(f, l)} 文件里已有 router.push ⇒ 不再是「无详情目的地」，请改挂入口或移入 KNOWN_GAP`,
      ).toBe(false)
    }
  })
})