import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

// safe-area 通道门禁（UI规范 10 §4.6.32）。
//
// 为什么需要门：Android 上壳注入的是 `--safe-area-inset-*` 自定义属性，
// 门禁钉的是**形态**，不是某条分支下的实测值 —— 形状对了，两条通道都有值；
// 形状错了，无论 WebView 落在哪个分支都会漏（10 §4.6.32 / §4.6.33）。
//
// ⚠️ 订正：本文件初版把理由写成「`env()` 在 Android 上恒为 0」——**说重了**。
// 实测 WebView 153 ≥ Capacitor 的 `WEBVIEW_VERSION_WITH_SAFE_AREA_FIX(140)`
// 且页面有 `viewport-fit=cover` ⇒ 落在 passthrough 分支，`env()` 也有真值。
// 仍然成立、且与版本无关的两条是：
//   ① `--app-safe-left/right` 零消费者（实测左侧手势 inset 29.7 CSS px）；
//   ② 读通道不应依赖 WebView 版本（B 分支下两条通道同时为 0）。
// 本组断言把三件事钉死：通道优先级、无消费者反模式、无恒零死项。

const ROOT = resolve(process.cwd(), 'src')
const SIDES = ['top', 'bottom', 'left', 'right'] as const

function walk(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry)
    if (statSync(full).isDirectory()) walk(full, out)
    else if (/\.(vue|ts|css)$/.test(entry)) out.push(full)
  }
  return out
}

const SOURCES = walk(ROOT)

/**
 * 注释剥离口径与本仓审计脚本一致：
 *   ① 块注释 `/* … *\/`   ② HTML 注释 `<!-- … -->`   ③ trim 后整行以 `//` / `*` / `/*` 开头
 * 刻意**不做**「`//` 到行尾」正则（会连代码一起吃掉）。
 * 门必须量的是**代码**：注释里提到 `var(--app-safe-*)` 既不是消费点也不是死项。
 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/<!--[\s\S]*?-->/g, '')
    .split('\n')
    .filter((line) => {
      const t = line.trim()
      return !(t.startsWith('//') || t.startsWith('*') || t.startsWith('/*'))
    })
    .join('\n')
}

const read = (f: string) => readFileSync(f, 'utf8')
/** 量「代码」而非散文：所有 safe-area 判据一律读剥离注释后的源码。 */
const readCode = (f: string) => stripComments(read(f))
const rel = (f: string) => relative(resolve(process.cwd()), f)

describe('safe-area token 声明', () => {
  it.each(SIDES)('--app-safe-%s 必须「壳注入值优先、env() 回落」', (side) => {
    const css = readCode(join(ROOT, 'styles/theme.css'))
    const decl = new RegExp(`--app-safe-${side}\\s*:\\s*([^;]+);`).exec(css)?.[1]?.trim() ?? ''
    expect(decl, `theme.css 缺少 --app-safe-${side} 声明`).not.toBe('')
    // 壳注入通道（Capacitor SystemBars injectSafeAreaCSS 写的就是这四个名字）
    expect(decl, `--app-safe-${side} 必须先读壳注入的 --safe-area-inset-${side}`).toContain(
      `var(--safe-area-inset-${side},`,
    )
    // iOS / 浏览器路径不能被顺手删掉
    expect(decl, `--app-safe-${side} 必须保留 env() 回落`).toContain(`env(safe-area-inset-${side},`)
  })
})

describe('safe-area 消费点', () => {
  it('不得出现「声明了但零消费者」的 safe-area token', () => {
    const offenders: string[] = []
    for (const side of SIDES) {
      const re = new RegExp(`var\\(--app-safe-${side}\\b`)
      const consumers = SOURCES.filter((f) => re.test(readCode(f)))
      if (consumers.length === 0) offenders.push(`--app-safe-${side}`)
    }
    // 每个 token 都必须有消费点：横屏刘海/曲面屏/折叠屏侧边靠 left/right，
    // 状态栏与手势条靠 top/bottom。零消费者 = 采了白采。
    expect(offenders, `这些 token 声明了却无人消费：${offenders.join(', ')}`).toEqual([])
  })

  it('壳根必须真的把横向 inset 用进布局（不是只声明）', () => {
    const shell = readCode(join(ROOT, 'components/shell/HyperApp.vue'))
    expect(shell).toMatch(/\.hyper-app\s*\{[^}]*padding-inline:\s*var\(--app-safe-left\)\s+var\(--app-safe-right\)/s)
  })
})

// ⚠️ 上一条断言**只覆盖流内**（10 §4.6.61 的实测缺陷）：
//   壳根 `.hyper-app` 没有 transform/filter/contain，而 `position: fixed` 的
//   包含块是**视口** ⇒ 壳根那点 padding-inline 对 fixed 浮层完全无效。
//   实测（注入 left/right=30px，740×360）：抽屉链接 `.drawer__link` 的命中区
//   从 x=12 起，**18px 落在 30px inset 带内**，20px 图标被刘海压住；
//   浮层里的可点目标共 65 个探边。改前 0 处、改后 0 处（判据同一次跑）。
describe('fixed 浮层必须自己消费横向 inset', () => {
  // 从源码里**现扫** fixed 元素，而不是手写文件清单 ——
  // 手写清单会随新浮层一起腐烂，而腐烂的方向正是「新浮层没人管 inset」。
  const fixedFiles = SOURCES.filter((f) => f.endsWith('.vue') && /position:\s*fixed/.test(readCode(f)))
    .map(rel)
    .sort()

  it('至少扫到 5 个 fixed 浮层（清单本身不许空转）', () => {
    expect(fixedFiles.length, `扫到的 fixed 浮层：${fixedFiles.join(', ')}`).toBeGreaterThanOrEqual(5)
  })

  it.each(fixedFiles)('%s 必须消费 --app-safe-left 与 --app-safe-right', (relPath) => {
    const full = resolve(process.cwd(), relPath)
    // 豁免标记读**原始**源码：stripComments 会把注释整段删掉，
    // 在剥离后的文本里找标记等于永远找不到（这坑踩过一次就不再犯）。
    if (/safe-area-opt-out/.test(read(full))) return
    const code = readCode(full)
    for (const side of ['left', 'right'] as const) {
      expect(
        code,
        `${relPath} 是 position:fixed 浮层，必须自己消费 --app-safe-${side}` +
        `（壳根 padding-inline 对 fixed 无效）；确实要满宽请加 /* safe-area-opt-out: 理由 */`,
      ).toMatch(new RegExp(`var\\(--app-safe-${side}\\b`))
    }
  })
})

describe('safe-area 反模式', () => {
  it('不得存在 `var(--app-safe-*) * 0` 恒零死项', () => {
    const hits: string[] = []
    const re = /var\(--app-safe-\w+\)\s*[*+]\s*0(?!\.)/
    for (const f of SOURCES) {
      if (re.test(readCode(f))) hits.push(rel(f))
    }
    // `* 0` 会让该项恒等于 0，却长得像在处理刘海 —— 曾出现在 AppSheet 的
    // padding-top 上（与上一行逐字冗余）。命中即说明又写了一个不会生效的避让。
    expect(hits, `发现恒零 safe-area 死项：${hits.join(', ')}`).toEqual([])
  })
})