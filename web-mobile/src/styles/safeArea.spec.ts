import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'

// safe-area 通道门禁（UI规范 10 §4.6.32）。
//
// 为什么需要门：Android 上壳注入的是 `--safe-area-inset-*` 自定义属性，
// 而 `env(safe-area-inset-*)` 在无 cutout mode 的 WebView 上恒为 0。
// 只读 env() 是一种「看起来在处理刘海、实际全程 0」的写法，且**不会报错**
// —— 顶栏/底导航/抽屉/弹层共 11 处消费点会一起静默失效。
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