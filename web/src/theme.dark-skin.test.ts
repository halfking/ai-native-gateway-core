import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import { applyTheme } from './theme'

// 暗色皮肤适配回归（2026-09-13 用户报告：暗色下 EP 组件大块亮色）。
// 根因：EP 组件消费 --el-*（theme-chalk/dark/css-vars.css 以 html.dark 门控），
// 但 applyTheme 只切 data-theme，EP 组件始终亮色。
const mainTs = readFileSync(resolve(process.cwd(), 'src/main.ts'), 'utf8')
const elementDarkCss = readFileSync(
  resolve(process.cwd(), 'src/styles/element-dark.css'),
  'utf8',
)
// 启动路径：index.html <head> 内同步执行的 theme-init.js（public/，不参与打包），
// 是「刷新/直链（无 ?theme= 参数）」场景下唯一设置主题的代码——漏掉 dark 类
// 会让 EP 组件在每次刷新后回退亮色（2026-09-13 审计发现的真因）。
const themeInitJs = readFileSync(resolve(process.cwd(), 'public/theme-init.js'), 'utf8')
// FOUC 前提：theme-init.js 必须在 <head> 内同步执行（无 defer/async）——
// 同步脚本是解析阻塞点，先于 Vite 注入的样式表与模块脚本，类在任何渲染前就位。
// 若被移出 head 或加了 defer，暗色直链/刷新将出现亮色首帧闪烁。
const indexHtml = readFileSync(resolve(process.cwd(), 'index.html'), 'utf8')

describe('dark skin bridging (EP components)', () => {
  it('applyTheme toggles html.dark class together with data-theme', () => {
    const root = document.documentElement
    applyTheme('dark')
    expect(root.getAttribute('data-theme')).toBe('dark')
    expect(root.classList.contains('dark')).toBe(true)

    applyTheme('light')
    expect(root.getAttribute('data-theme')).toBe('light')
    expect(root.classList.contains('dark')).toBe(false)
  })

  it('main.ts imports EP dark css-vars and the element-dark bridge', () => {
    expect(mainTs).toContain("'element-plus/theme-chalk/dark/css-vars.css'")
    expect(mainTs).toContain("'./styles/element-dark.css'")
    // 桥接文件必须晚于 EP dark 文件引入（同特异性时后者胜出的兜底，
    // 桥接文件本身另用更高特异性 html.dark[data-theme='dark'] 双保险）。
    expect(mainTs.indexOf("'element-plus/theme-chalk/dark/css-vars.css'")).toBeLessThan(
      mainTs.indexOf("'./styles/element-dark.css'"),
    )
  })

  it('theme-init.js boot script toggles the dark class (refresh persistence)', () => {
    expect(themeInitJs).toContain("classList.toggle('dark'")
    // 与 theme.ts 同一存储键与同一双轨约定，防两处漂移
    expect(themeInitJs).toContain("llmgw_theme")
    expect(themeInitJs).toContain("setAttribute('data-theme'")
  })

  it('theme-init.js stays synchronous inside <head> (FOUC guard)', () => {
    const head = indexHtml.slice(0, indexHtml.indexOf('</head>'))
    const scriptTag = head.match(/<script\s+src="\/theme-init\.js"[^>]*>/)
    expect(scriptTag).not.toBeNull()
    const tag = scriptTag![0]
    // 同步执行是首帧前类就位的机制前提（双 rAF 行为探针 2026-09-13 三场景实测），
    // 任何 defer/async/移出 head 都会让暗色直链/刷新首帧回退亮色
    expect(tag).not.toContain('defer')
    expect(tag).not.toContain('async')
  })

  it('element-dark.css bridges EP surfaces to app tokens at higher specificity', () => {
    // 选择器只出现在规则处（行首），亮色不得被波及：桥接只存在于 dark 门控内
    expect(elementDarkCss.match(/(^|\n)html\.dark\[data-theme/g)).toHaveLength(1)
    for (const epVar of [
      '--el-bg-color:',
      '--el-bg-color-overlay:',
      '--el-bg-color-page:',
      '--el-fill-color-blank:',
      '--el-text-color-primary:',
      '--el-border-color:',
    ]) {
      expect(elementDarkCss).toContain(epVar)
      expect(elementDarkCss).toContain('var(--kx-')
    }
  })

  it('EP primary brand scale is re-derived from --kx-primary (EP dark mix ratios)', () => {
    // base 直接取应用令牌,派生阶按 EP dark 官方混合规则 color-mix 等价:
    // light-N = mix(primary, black, N*10%),dark-2 = mix(primary, white, 20%)
    expect(elementDarkCss).toContain('--el-color-primary: var(--kx-primary);')
    expect(elementDarkCss).toContain(
      '--el-color-primary-dark-2: color-mix(in srgb, var(--kx-primary) 80%, #ffffff);',
    )
    for (const [n, pct] of [
      ['3', 70],
      ['5', 50],
      ['7', 30],
      ['8', 20],
      ['9', 10],
    ] as const) {
      expect(elementDarkCss).toContain(
        `--el-color-primary-light-${n}: color-mix(in srgb, var(--kx-primary) ${pct}%, #000000);`,
      )
    }
  })
})

// 2026-09-29 用户报告：暗色下落地页「下载安装包」是一块看不清的亮色大块。
// 根因：它把「反白文字色」令牌 --on-primary 当成了表面色，而该令牌在明暗两套皮肤里
// 都是同一个纯白 #ffffff（style.css L28 亮 / L168 暗），对暗色皮肤完全不敏感。
// 门禁的作用是锁死「文字色令牌不得当背景」这条不变量，而不是记住某个具体色值。
describe('text tokens must not be used as surfaces', () => {
  const landing = readFileSync(
    resolve(process.cwd(), 'src/components/ServiceLandingPage.vue'),
    'utf8',
  )
  const styleCss = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8')

  // --on-primary / --kx-text-on-primary 都是「压在主色上的文字色」，语义是前景而非表面。
  const TEXT_TOKENS = ['--on-primary', '--kx-text-on-primary']

  function backgroundDeclarations(css: string): string[] {
    const out: string[] = []
    // 只取 background / background-color 的值，忽略注释，避免把修复说明本身当成违规。
    const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '')
    for (const m of stripped.matchAll(/background(?:-color)?\s*:\s*([^;]+);/g)) {
      out.push(m[1].trim())
    }
    return out
  }

  it('landing page never paints a background with a text-on-primary token', () => {
    const offenders = backgroundDeclarations(landing).filter((value) =>
      TEXT_TOKENS.some((token) => value.includes(`var(${token})`)),
    )
    expect(offenders).toEqual([])
  })

  it('landing secondary CTA paints with the theme-aware surface token', () => {
    // 显式钉住修复本身：次要 CTA 的任何背景都必须是随皮肤走的 --landing-surface
    // （→ --surface-elevated）。只断言「真正写了 background 的那些规则」，因为该选择器
    // 同时出现在共享规则 `.kx-landing__cta, .kx-landing__cta-secondary {…}` 里——
    // 那条只有排版属性、无 background，不该被要求带表面色。
    const bodies: string[] = []
    for (const m of landing.matchAll(/\n([^\n{}]*)\{([^{}]*)\}/g)) {
      if (m[1].trim() === '.kx-landing__cta-secondary') bodies.push(m[2])
    }
    const painted = bodies.filter((b) => /background(?:-color)?\s*:/.test(b))
    expect(painted.length).toBeGreaterThan(0)
    for (const body of painted) {
      expect(body).toMatch(/background(?:-color)?:\s*var\(--landing-surface\)/)
    }
  })

  it('--kx-text-on-primary is theme-invariant white (so it can never be a surface)', () => {
    // 这条把「为什么不能用它当背景」钉成可执行事实：两套皮肤都是 #ffffff。
    const decls = [...styleCss.matchAll(/--kx-text-on-primary:\s*(#[0-9a-f]{3,8})\s*;/gi)].map(
      (m) => m[1].toLowerCase(),
    )
    expect(decls.length).toBeGreaterThanOrEqual(2)
    expect(new Set(decls)).toEqual(new Set(['#ffffff']))
  })
})

// 2026-09-29 全站皮肤一致性巡检后追加：禁用「皮肤不变的黑底/白底当背景」
// 的两类形态——它们与 `--on-primary` 同源（文字/背景色令牌被错用），但形态不同。
describe('inline background should not be theme-invariant white or black', () => {
  const SRC = resolve(process.cwd(), 'src')
  const TEXT_TOKENS = ['--on-primary', '--kx-text-on-primary']

  // 1. inline `background: rgba(0,0,0,...)` 当面背景（不是遮罩层 drawback）
  function findFiles(): string[] {
    const out: string[] = []
    const fs = require('node:fs') as typeof import('node:fs')
    const path = require('node:path') as typeof import('node:path')
    const stack = [SRC]
    while (stack.length) {
      const dir = stack.pop()!
      for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, e.name)
        if (e.isDirectory()) stack.push(full)
        else if (/\.(vue|css|ts)$/.test(e.name)) out.push(full)
      }
    }
    return out
  }

  function inlineBlackBackgrounds(css: string): Array<{ line: number; val: string }> {
    // 找 background/background-color: rgba(0, 0, 0, ...) 形式，排除 .drawer-backdrop 类遮罩
    const out: Array<{ line: number; val: string }> = []
    const stripped = css.replace(/\/\*[\s\S]*?\*\//g, '')
    for (const m of stripped.matchAll(/background(?:-color)?\s*:\s*([^;]+);/g)) {
      const val = m[1].trim()
      if (/^rgba?\(\s*0\s*,\s*0\s*,\s*0\s*,/.test(val)) {
        // 排除遮罩层（命名以 .drawer-backdrop / .overlay / .modal-mask 开头或 inline style 上以 modal/mask/backdrop 出现）
        out.push({ line: -1, val })
      }
    }
    return out
  }

  it('no inline background uses rgba(0,0,0,...) outside drawer/overlay masks', () => {
    const exceptions = /(drawer-backdrop|overlay|mask-backdrop|modal-backdrop)/i
    const offenders: Array<{ file: string; val: string }> = []
    for (const f of findFiles()) {
      const src = require('node:fs').readFileSync(f, 'utf8')
      for (const { val } of inlineBlackBackgrounds(src)) {
        // 看上下 8 行有没有例外标签
        const idx = src.indexOf(val)
        const nearby = src.slice(Math.max(0, idx - 200), idx + val.length + 200)
        if (!exceptions.test(nearby)) {
          offenders.push({ file: f.replace(process.cwd() + '/', ''), val })
        }
      }
    }
    expect(offenders).toEqual([])
  })
})
