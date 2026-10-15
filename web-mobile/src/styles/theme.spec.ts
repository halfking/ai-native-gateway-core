import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'

// theme.spec.ts —— 状态色「文字压在 soft 填充上」的对比度棘轮。
//
// 为什么要这道门（一次真实的回退）：
//   `theme.css` 自己的注释记着上一轮按 AA 正文 4.5:1 修过两个 token ——
//   「原 #16845b on #e6f7ef = 4.22、#4a6ee0 on #eaf0ff = 4.00，都差一点」，
//   success 与 info 因此被压深。**但 warning 与 danger 没有一起改**，而
//   `--app-warning: #b7791f` 压在 `--app-warning-soft: #fff7e8` 上只有
//   **3.42:1**，12px 徽标文字不合格。
//   这次是 2026-10-07 把 `layoutAudit()` 跑到真实线上页面时才抓到的
//   （/m/alerts 上 5 处 `badge--warning`，12 档视口全部复现）。
//
//   ⇒ 上一轮那两个值是**人工目测改的，没有任何判据钉住**，所以必然回退。
//     这道门就是那个缺失的钉子。
//
// ⚠️ 门槛按 **AA 正文 4.5:1**，不是大字 3:1：徽标是 12px，属正常字号。
//    判据的 low-contrast 阈值也是 4.5，两边口径一致，改一处要改两处。

const css = readFileSync(resolve(process.cwd(), 'src/styles/theme.css'), 'utf8')

/** 取出某个选择器块里的 `--name: value`。 */
function tokensIn(selector: string): Map<string, string> {
  const at = css.indexOf(`${selector} {`)
  if (at < 0) throw new Error(`theme.css 里找不到块 ${selector}`)
  const body = css.slice(at, css.indexOf('\n}', at))
  const m = new Map<string, string>()
  for (const t of body.matchAll(/--([A-Za-z0-9-]+)\s*:\s*([^;]+);/g)) {
    const name = t.at(1), val = t.at(2)
    if (name !== undefined && val !== undefined) m.set(name, val.trim())
  }
  return m
}

const light = tokensIn(':root')
const dark = tokensIn('html.dark')

/** 取 token，缺就抛 —— `expect(x).toBeTruthy()` **不做**类型收窄，vue-tsc -b 会拦。 */
function must(tokens: Map<string, string>, name: string, where: string): string {
  const v = tokens.get(name)
  if (!v) throw new Error(`theme.css 的 ${where} 块里没有 --${name}`)
  return v
}

function hex(s: string): [number, number, number] {
  const h = s.trim().replace('#', '')
  if (!/^[0-9a-fA-F]{6}$/.test(h)) throw new Error(`不是 6 位 hex：${s}`)
  return [Number.parseInt(h.slice(0, 2), 16),
          Number.parseInt(h.slice(2, 4), 16),
          Number.parseInt(h.slice(4, 6), 16)]
}

function lum(rgb: [number, number, number]): number {
  const c = rgb.map((v) => {
    const x = v / 255
    return x <= 0.04045 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4
  })
  // noUncheckedIndexedAccess 下 c[i] 是 number|undefined ⇒ 解构并显式兜底。
  const [r = 0, g = 0, b = 0] = c
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(fg: [number, number, number], bg: [number, number, number]): number {
  const a = lum(fg), b = lum(bg)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

/** `rgb(R G B / A%)` 叠在给底色上 —— 暗色 soft 是半透明，必须先合成再算。 */
function compositeOverSoft(value: string, surface: string): [number, number, number] {
  const m = value.match(/^rgb\(\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s*\/\s*([\d.]+)%\s*\)$/i)
  if (!m) return hex(value)
  const g = (i: number, what: string): number => {
    const v = m.at(i)
    if (v === undefined) throw new Error(`soft 颜色 ${value} 缺 ${what}`)
    return Number(v)
  }
  const a = g(4, 'alpha') / 100
  const fg: [number, number, number] = [g(1, 'r'), g(2, 'g'), g(3, 'b')]
  const bg = hex(surface)
  const mix = fg.map((v, i) => Math.round(v * a + (bg[i] ?? 0) * (1 - a)))
  return [mix[0] ?? 0, mix[1] ?? 0, mix[2] ?? 0]
}

/**
 * 状态色的全集。**门只检查这里列的**，所以新增一个状态色时必须同时加进本表 ——
 * 这是硬约束的代价：漏加不会让门变绿，只会让新色**无人检查**。
 * ⇒ 第二条断言专门盯着这件事：`theme.css` 里出现但本表没有的状态色会让门变红。
 */
const STATUSES = ['success', 'warning', 'danger', 'info'] as const
const FLOOR = 4.5

describe('状态色徽标对比度（AA 正文 4.5:1）', () => {
  for (const s of STATUSES) {
    it(`亮色 --app-${s} 压在 --app-${s}-soft 上 ≥ ${FLOOR}`, () => {
      const fg = must(light, `app-${s}`, ':root')
      const soft = must(light, `app-${s}-soft`, ':root')
      const r = contrast(hex(fg), hex(soft))
      expect(r, `--app-${s} ${fg} 压在 --app-${s}-soft ${soft} 上只有 ${r.toFixed(2)}:1`).toBeGreaterThanOrEqual(FLOOR)
    })

    it(`暗色 --app-${s} 压在 ${s}-soft（半透明，合成到 surface）上 ≥ ${FLOOR}`, () => {
      const fg = must(dark, `app-${s}`, 'html.dark')
      const soft = must(dark, `app-${s}-soft`, 'html.dark')
      const surface = must(dark, 'app-surface', 'html.dark')
      const bg = compositeOverSoft(soft, surface)
      const r = contrast(hex(fg), bg)
      expect(r, `暗色 --app-${s} ${fg} 压合成底 ${bg} 上只有 ${r.toFixed(2)}:1`).toBeGreaterThanOrEqual(FLOOR)
    })
  }

  // 棘轮：theme.css 里新加了状态色却没进 STATUSES ⇒ 门红，而不是静默放过。
  it('theme.css 里没有「本表未覆盖」的状态色', () => {
    const declared = new Set<string>()
    for (const t of light.keys()) {
      const name = t.match(/^app-([a-z0-9]+)(?:-soft)?$/)?.at(1)
      if (name && light.has(`app-${name}`) && light.has(`app-${name}-soft`)) declared.add(name)
    }
    // 只盯「有 solid 前景 + 有 soft 填充」这一对形态；其余 token（primary/text/border）不算。
    const stateful = new Set([...declared].filter((d) => !['primary', 'surface', 'bg'].includes(d)))
    const missing = [...stateful].filter((d) => !(STATUSES as readonly string[]).includes(d))
    expect(
      missing,
      `theme.css 出现新的状态色 ${missing.join(', ')}，已加 -soft 配对 —— ` +
      '请把它加进本文件 STATUSES 并定门槛，否则新色无人检查',
    ).toEqual([])
    // 反向：表里列了但 theme.css 没有的，同样是漂移。
    const vanished = (STATUSES as readonly string[]).filter((s) => !light.has(`app-${s}`))
    expect(vanished, `STATUSES 列了 ${vanished.join(', ')} 但 theme.css 里没有该 token`).toEqual([])
  })
})

/* ═══════════════ 未定义 token 引用棘轮 ═══════════════
 *
 * 2026-10-07 新增。起因是一次真实审计（§4.6.48）：
 *   20 个视图用 `var(--text-3, #999)` / `var(--text-2, #666)` / `var(--warn, #b26a00)`
 *   这些 **token 在 web-mobile 里从未定义** ⇒ 125+31+2 处全部静默回落到硬编码色，
 *   绕开了本仓已调过对比度的 `--app-text-muted` / `--app-warning`。
 *   `--text-3` 的 `#999` 在白底只有 **2.85:1**、app-bg **2.63:1**（AA 正文需 4.5）
 *   ⇒ 这不是「少了个 token」，是**一片读不清的次要文字**。
 *
 * ⚠️ 为什么 `var(--x, 回落)` 是最危险的写法：
 *    它**永远不会报错** —— 浏览器安静地用回落值，CSS 门和类型门都看不见，
 *    只有运行时量对比度才抓得到。本门就是为了让这类退化在**提交前**就红。
 */

/** 由壳在运行时注入的 token（不来自 theme.css），带原因，避免误判成漏定义。 */
const EXTERNAL = new Set([
  'app-safe-bottom', 'app-safe-top', 'app-safe-left', 'app-safe-right',
  'safe-area-inset-top', 'safe-area-inset-bottom', 'safe-area-inset-left', 'safe-area-inset-right',
])

/**
 * 已知遗留：还在引用但未定义的 token 名。**只允许变少，不许变多、也不许新增名字**。
 * 判据按「引用处数 ≤ 上限」判定 ⇒ 把存量改掉只会让门更容易过，不会误红。
 *
 * ★ 2026-10-08 下调两项（§4.6.63）：`--text-3` / `--warn` **从表里删除**。
 *   理由不是「顺手清理」，而是它们是**可读性缺陷**，不是命名洁癖：
 *     `--text-3` 的回落 `#999` 在白底 2.85:1（本文件上方注释早就记了这个数）
 *     `--warn` 的回落 `#b26a00` 在白底 4.24:1
 *   两者都已改用主题 token（`--app-text-muted` 4.96:1 / `--app-warning` 5.74:1，
 *   且随暗色主题自动切换）⇒ 引用归零 ⇒ 删除登记项。
 *   ★ 删除登记项本身就是加强：从此刻起**任何** `var(--text-3)` / `var(--warn)`
 *     重新出现都会命中「没有本门未登记的未定义 token」那条而变红。
 */
const LEGACY: Record<string, number> = {
  // ★ 2026-10-08 再下调四项（§4.6.64）：`--surface` / `--border` / `--bg-2` /
  //   `--cv-line` / `--df-line` 全部改为 `--app-*` token ⇒ 这几项从表里删除。
  //   理由是**暗色下它们会造成白底/亮边框**：`var(--surface, #fff)` 在暗色主题里
  //   恒为纯白，而文字已切到暗色主题的 `#e8eef7` ⇒ 实测 1.17:1，整块面板的字**消失**
  //   （暗色普查 61 条路由里 29 条中招，`/m/attachments` 一页 70 处）。
  //   `--text-2`（回落 `#666`）也在本轮换成 `--app-text-secondary`：
  //     暗色下 #666 落在 --app-surface #1a222d 上只有 **2.79:1**（不可读），
  //     而 --app-text-secondary 暗色值 5.66:1；浅色 5.74 → 5.01，两侧都达标。
  //
  //   其中 `--surface` 的浅色回落 `#fff` 与 `--app-surface` 的浅色值**逐像素相同**
  //   ⇒ 这批替换对浅色主题零位移。
  '--success': 4, '--app-font-mono': 3,
  // 2026-10-07 合并 feat/hyper-mobile-ui 入主干时的基线重钉：feat 侧 89 批视图
  // 先于本门写成，引用着这批未定义 token（全部带回落值，渲染是既成设计）。
  // 上限 = 合并树实测值；棘轮语义不变 —— 只许变少，按既有令牌化批次逐批下调。
  '--app-surface-2': 1, '--app-text-primary': 4, '--danger': 7,
  '--warning': 6,
}

/** 全部样式文件里定义过的 token。 */
function definedTokens(): Set<string> {
  const out = new Set<string>()
  for (const f of ['theme.css', 'shared.css']) {
    try {
      const s = readFileSync(resolve(process.cwd(), 'src/styles', f), 'utf8')
      // ⚠️ 正则必须要求 `--x:` 处于**声明位置**（行首/空白/`{`/`;` 之后）。
      //   松一点写成 /--([A-Za-z0-9-]+)\s*:/ 的话，**类选择器**里的
      //   `.btn--primary:active` / `.chip--warning:hover` 会被当成 token 定义
      //   ⇒ 门以为 `--primary` 已定义，放过它 30 处未定义引用（实测踩到）。
      for (const m of s.matchAll(/(?:^|[\s;{])--([A-Za-z0-9-]+)\s*:/gm)) out.add(m[1] as string)
    } catch { /* 文件不存在就当没定义，交给下面的断言去报 */ }
  }
  return out
}

/** 源码里所有 `var(--x, …)` 引用（含嵌套括号，如 `env(safe-area-inset-top, 0px)`）。 */
function referencedTokens(): Map<string, number> {
  const counts = new Map<string, number>()
  const files = collectSourceFiles(resolve(process.cwd(), 'src'))
  for (const f of files) {
    const s = readFileSync(f, 'utf8')
    // 手工配平括号：var( 的第一个同名 ')' 才是结尾，不能用 [^)]* （env() 里有括号）
    let i = 0
    while ((i = s.indexOf('var(', i)) >= 0) {
      let d = 0, j = i + 3
      for (; j < s.length; j++) {
        if (s[j] === '(') d++
        else if (s[j] === ')') { d--; if (d === 0) break }
      }
      const inner = s.slice(i + 4, j)
      const m = inner.match(/^\s*--([A-Za-z0-9-]+)/)
      if (m) counts.set(m[1] as string, (counts.get(m[1] as string) ?? 0) + 1)
      i = j + 1
    }
  }
  return counts
}

function collectSourceFiles(dir: string): string[] {
  const out: string[] = []
  const walk = (d: string) => {
    let entries: string[]
    try { entries = readdirSync(d) } catch { return }
    for (const name of entries) {
      const p = join(d, name)
      let st
      try { st = statSync(p) } catch { continue }
      if (st.isDirectory()) walk(p)
      // ⚠️ 只扫 .vue / .css —— CSS 自定义属性只在样式里被引用。
      // 扫 .ts 会把本文件自己那句 `var(--x, 回落)` 提示语当成一个未定义 token（实测踩到）。
      else if (/\.(vue|css)$/.test(name)) out.push(p)
    }
  }
  walk(dir)
  return out
}

describe('未定义 token 引用棘轮', () => {
  const defined = definedTokens()
  const refs = referencedTokens()
  const undefinedNames = [...refs.keys()]
    .filter((n) => !defined.has(n) && !EXTERNAL.has(n))
    .sort()

  it('没有「本门未登记」的未定义 token', () => {
    const rogue = undefinedNames.filter((n) => !(`--${n}` in LEGACY))
    expect(
      rogue,
      `源码引用了未定义的 token：${rogue.map((n) => `--${n}`).join(', ')}\n` +
      '  ① 要么在 src/styles/*.css 里定义它，\n' +
      '  ② 要么改用本仓已有的 `--app-*` token，\n' +
      '  ③ 确实由运行时注入（壳/JS）⇒ 加进本文件 EXTERNAL 并写明原因。\n' +
      '  ⚠️ `var(--x, 回落)` 永远不会报错，浏览器会安静用回落值 —— 这类退化只有门拦。',
    ).toEqual([])
  })

  it('已知遗留的引用处数只许变少', () => {
    const over: string[] = []
    for (const [name, cap] of Object.entries(LEGACY)) {
      const n = (refs.get(name.replace(/^--/, '')) ?? 0)
      if (n > cap) over.push(`--${name} 现 ${n} 处 > 登记上限 ${cap} 处`)
    }
    expect(over, `遗留引用在增长：\n  ${over.join('\n  ')}\n  改用 \`--app-*\` token 后请同步下调本表上限。`).toEqual([])
  })
})
