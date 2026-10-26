/**
 * style.token-resolution.test.ts — 全仓 CSS 令牌解析门禁
 *
 * 存在理由（真实踩过的坑，2026-10-25）：
 *   组件层大量用的是**第二代命名**（--space-* / --text-* / --font-semibold /
 *   --bg-surface / --border-muted / --card-bg / --kx-text-secondary …），
 *   而这些名字**在全仓从未被定义过**（`git log -S'--space-3:'` 查证：
 *   这道定义在仓库整个历史上都不存在；全仓 grep 也搜不到任何 --space-3:）。
 *
 * ★ 为什么这不是「少个变量」的小事：CSS 规范里 var() 引用未定义的自定义
 *   属性 ⇒ 整条声明 **invalid at computed-value time** ⇒ 不是退回默认值，
 *   而是**这条声明整个消失**。实测（浏览器计算样式）：
 *     .st-card        padding = 0px      background = transparent
 *     .oo-card        padding = 0px      border-width = 0px
 *     .st-card-price  font-size = 14px / font-weight = 400（应为 24px / 600）
 *   ⇒ 卡片没有内边距、没有边框、底色透明、文字层级塌成一片 —— 这正是
 *   「界面不像人做的东西」的根因之一。
 *
 * 为什么这道门值得存在（两道门都看不见它）：
 *   ① color:check **结构上看不见**：没有字面兜底就没有违规，压根不报。
 *   ② src/lib/shell/hyper/gates.spec.ts 里已有同类的「令牌存在性」门，
 *      但它**只扫 10 个自有文件**，并在注释里明确把「把门开到整个 src/」
 *      记为**另一件事**（「全仓实测有 74 处跨文件未定义令牌……把门开在
 *      整个 src/ 上会立刻撞上 74 处存量欠账，那属于另一件事」）。
 *      ⇒ 本文件就是那件事。
 *
 * 判据口径（与既有门禁互补，不是重复）：
 *   既有门禁：只判「var(--x) 的**第一个**参数是否被定义」。
 *   本门禁：**解析整条兜底链** —— `var(--a, var(--b, 字面量))` 在 --a 未
 *   定义时会退到 --b，--b 定义了就没事 ⇒ 只判第一个参数会误报。
 *   兜底的**形态**（字面色 / 字面长度）不归本门禁管：那归 color:check。
 */
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = resolve(process.cwd(), 'src')

/** 递归收集 .vue/.ts/.css 源文件。 */
function collectFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'node_modules' || entry === 'dist') continue
    const p = join(dir, entry)
    if (statSync(p).isDirectory()) collectFiles(p, out)
    else if (/\.(vue|ts|css)$/.test(entry)) out.push(p)
  }
  return out
}

/** 剥掉注释：块注释 + 行注释（行注释要避开 `://`，否则 URL 被吃掉）。 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:])\/\/.*$/gm, '$1')
}

/** 从 `var(` 起点解析出整条调用的参数（按顶层逗号切分）与结束位置。 */
function readVarCall(text: string, start: number): { args: string[]; end: number } | null {
  if (!text.startsWith('var(', start)) return null
  let depth = 0
  let cur = ''
  const args: string[] = []
  for (let i = start; i < text.length; i++) {
    const ch = text[i]
    if (ch === '(') {
      depth++
      // ★ depth 变 1 的那一刻要把 'var' 这个前缀从 cur 里清掉 ——
      //   否则第一个参数会变成 'var--x'，`^--…$` 匹配不上 ⇒ 全部判绿。
      //   （反向对照那条断言就是钉这一处的。）
      if (depth === 1) {
        cur = ''
        continue
      }
    } else if (ch === ')') {
      depth--
      if (depth === 0) {
        args.push(cur)
        return { args, end: i + 1 }
      }
    } else if (ch === ',' && depth === 1) {
      args.push(cur)
      cur = ''
      continue
    }
    cur += ch
  }
  return null
}

/** 一条 var() 链最终能否取到值。 */
function resolves(args: string[], globalDefs: Set<string>, localDefs: Set<string>): boolean {
  const first = args[0].trim()
  const name = first.match(/^--[a-zA-Z0-9_-]+$/)?.[0]
  if (!name) return true // 非 var(--x) 形态（如 var(0)）：CSS 合法，不归本门禁
  if (globalDefs.has(name) || localDefs.has(name)) return true
  if (args.length < 2) return false // 无兜底 ⇒ 解析为空 ⇒ 整条声明作废
  const fb = args.slice(1).join(',').trim()
  if (!fb.includes('var(')) return true // 字面量兜底 ⇒ 有效
  const at = fb.indexOf('var(')
  const inner = readVarCall(fb, at)
  if (!inner) return true
  return resolves(inner.args, globalDefs, localDefs)
}

const ALL_FILES = collectFiles(SRC).filter((f) => !/\.(test|spec)\.ts$/.test(f))

/**
 * 「定义」的判定：令牌名后面紧跟冒号，且**前面**必须是分隔符。
 * ★ 为什么不能只用 `/(--[\w-]+)\s*:/`：BEM 类名 `.btn--primary:hover`
 *   会被读成「定义了 --primary」⇒ 门禁把真正未定义的令牌判成已定义。
 *   （`RouteIncidentDrawer.vue` 的 `.route-incident-drawer__btn--primary:hover`
 *   就是这么把 13 处真断链藏住的。）
 * 允许的前置字符：空白 / 分号 / 花括号 / 圆括号 / 引号（行内 `:style="{ '--x': v }"`）。
 */
const DEF_RE = /(?:^|[\s;{,(])['"]?(--[a-zA-Z0-9_-]+)['"]?\s*:/g

/** 全局令牌：src 下所有 .css 都会被打进产物（style.css 由 main.ts 引入）。 */
const GLOBAL_DEFS = new Set<string>()
for (const f of ALL_FILES.filter((f) => f.endsWith('.css'))) {
  for (const m of stripComments(readFileSync(f, 'utf8')).matchAll(DEF_RE)) {
    GLOBAL_DEFS.add(m[1])
  }
}

interface Broken {
  token: string
  file: string
  line: number
}

function scanBroken(files: string[]): Broken[] {
  const out: Broken[] = []
  for (const f of files) {
    const text = stripComments(readFileSync(f, 'utf8'))
    // 同文件内定义的令牌算已定义（组件局部令牌是合法实践；
    // 也包含 `:style="{ '--x': v }"` 这类行内注入）。
    const local = new Set(
      [...text.matchAll(DEF_RE)].map((m) => m[1]),
    )
    let i = 0
    for (;;) {
      const idx = text.indexOf('var(', i)
      if (idx === -1) break
      const call = readVarCall(text, idx)
      if (!call) {
        i = idx + 4
        continue
      }
      if (!resolves(call.args, GLOBAL_DEFS, local)) {
        out.push({
          token: call.args[0].trim(),
          file: relative(SRC, f),
          line: text.slice(0, idx).split('\n').length,
        })
      }
      i = call.end
    }
  }
  return out
}

const BROKEN = scanBroken(ALL_FILES)

describe('全仓 CSS 令牌解析：var(--x) 必须能取到值', () => {
  it('style.css 至少定义了 150 个全局令牌（防止正则失效导致空集恒真）', () => {
    expect(GLOBAL_DEFS.size).toBeGreaterThan(150)
  })

  it('扫描范围够大（防止扫描失效导致断言恒真）', () => {
    expect(ALL_FILES.length).toBeGreaterThan(100)
    const totalRefs = ALL_FILES.reduce(
      (n, f) => n + (stripComments(readFileSync(f, 'utf8')).match(/var\(\s*--/g)?.length ?? 0),
      0,
    )
    expect(totalRefs).toBeGreaterThan(300)
  })

  it('反向对照：判据对「未定义令牌」确实有牙（不是恒绿门）', () => {
    // 夹具：一个只在同文件里定义的令牌（合法）+ 一个谁都没定义的（必须被抓）。
    const fixture = `
      .a { color: var(--local-only); }
      .b { color: var(--nobody-defines-this); }
    `
    const local = new Set(['--local-only'])
    const globalDefs = new Set<string>()
    const bad: string[] = []
    let i = 0
    for (;;) {
      const idx = fixture.indexOf('var(', i)
      if (idx === -1) break
      const call = readVarCall(fixture, idx)!
      if (!resolves(call.args, globalDefs, local)) bad.push(call.args[0].trim())
      i = call.end
    }
    expect(bad).toEqual(['--nobody-defines-this'])
  })

  it('兜底链解析：整条链一起判，不只看第一个参数', () => {
    // 若只判第一个参数，`var(--a, var(--b, #fff))` 在 --a 未定义时会误报 ——
    // 这里钉住「解析整条链」的口径，并分清「末级字面量」与「真的没有兜底」。
    const chain = readVarCall('var(--a, var(--b, #fff))', 0)!
    // ① --b 定义 ⇒ 有效
    expect(resolves(chain.args, new Set(['--b']), new Set())).toBe(true)
    // ② --b 也没定义，但末级是字面量 ⇒ 仍然有效（退到 #fff）
    expect(resolves(chain.args, new Set(), new Set())).toBe(true)
    // ③ 链上没有字面量兜底 ⇒ 真断链
    const noLiteral = readVarCall('var(--a, var(--b))', 0)!
    expect(resolves(noLiteral.args, new Set(), new Set())).toBe(false)
  })

  it('全仓没有「谁都没定义、也没有兜底」的令牌引用', () => {
    const detail = [...new Set(BROKEN.map((b) => `  ${b.token} ← ${b.file}:${b.line}`))].join('\n')
    expect(
      BROKEN.length,
      `以下 var() 引用的令牌在全仓都无定义，且没有兜底 ⇒ 该声明会整条失效：\n${detail}`,
    ).toBe(0)
  })

  it('第二代命名的关键令牌已被定义（钉住这次修复）', () => {
    for (const t of [
      '--space-1', '--space-2', '--space-3', '--space-4',
      '--text-sm', '--text-md', '--text-xl', '--text-2xl',
      '--font-semibold', '--font-mono',
      '--bg-surface', '--card-bg', '--border-muted', '--border-color',
      '--text-tertiary', '--kx-text-secondary', '--kx-accent',
    ]) {
      expect(GLOBAL_DEFS.has(t), `${t} 仍未定义`).toBe(true)
    }
  })

  it('已删除的混用令牌 --bg-elevated 不再被任何产品代码引用', () => {
    const users = ALL_FILES.filter((f) => !f.endsWith('.css')).filter((f) =>
      /var\(\s*--bg-elevated/.test(stripComments(readFileSync(f, 'utf8'))),
    )
    expect(users.map((f) => relative(SRC, f))).toEqual([])
  })
})
