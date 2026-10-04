// V1DataFrozenBanner.test.ts — 横幅的**渲染**判据（审计 §9.73.7）。
//
// # 为什么必须有这道门（composable 的测试不够）
//
// `useV1DataHorizon.test.ts` 覆盖三态转换，但它**完全不需要组件存在**：
// 把 `V1DataFrozenBanner.vue` 整个删掉，那 7 条用例依然全绿。
// 那样就得到一个「有状态、有测试、但没有任何消费者」的 composable
// —— 正是 §9.37 记的「没有第二个消费方的字段在事实层面是装饰」。
//
// ⇒ 这里**真的挂载组件**，断言四种状态下渲染出什么；
// 另有一条读源文件的断言，把 App.vue 与横幅接起来（挂载整个 App.vue
// 需要 router + store + 一堆依赖，代价远大于收益，而这里要证明的
// 只是「App.vue 确实渲染了这个组件」这一件事）。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../api/v1DataHorizon', () => ({ getV1DataHorizon: vi.fn() }))
import { getV1DataHorizon } from '../../api/v1DataHorizon'
import { refreshV1DataHorizon, __resetV1DataHorizonForTests } from '../../composables/useV1DataHorizon'
import V1DataFrozenBanner from './V1DataFrozenBanner.vue'

const mockGet = vi.mocked(getV1DataHorizon)

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      app: {
        v1DataFrozen: {
          title: '流量数据已停更',
          titleUnavailable: '无法确认流量数据状态',
          affects: '受影响读点档位',
          gateKey: '控制开关',
          retry: '重新检查',
          failedHint: '未确认状态不等于状态正常',
        },
      },
    },
  },
})

const notice = {
  frozen: true as const,
  unknown: false as const,
  source: 'request_logs',
  gate_key: 'storage.request_logs_write_enabled',
  effect: 'EFFECT_TEXT',
  silence: 'SILENCE_TEXT',
  affects: ['silently_frozen', 'silently_empty'],
}

function mountBanner() {
  return mount(V1DataFrozenBanner, { global: { plugins: [i18n] } })
}

describe('V1DataFrozenBanner', () => {
  beforeEach(() => {
    __resetV1DataHorizonForTests()
    mockGet.mockReset()
  })

  it('unknown 态：什么都不渲染（页面刚起来闪一条比不闪更糟）', () => {
    expect(mountBanner().html()).toBe('<!--v-if-->')
  })

  it('live 态：什么都不渲染', async () => {
    mockGet.mockResolvedValue({ v1_data_horizon: null, '//': '' })
    await refreshV1DataHorizon()
    expect(mountBanner().html()).toBe('<!--v-if-->')
  })

  it('frozen 态：渲染标题 + 后端给的两句 + 开关键 + 档位', async () => {
    mockGet.mockResolvedValue({ v1_data_horizon: notice, '//': '' })
    await refreshV1DataHorizon()
    const html = mountBanner().html()
    expect(html).toContain('流量数据已停更')
    // 后端给的两句是「这个实例」的实话，必须原样出现在页面上。
    expect(html).toContain('EFFECT_TEXT')
    expect(html).toContain('SILENCE_TEXT')
    // 运维要知道改哪里。
    expect(html).toContain('storage.request_logs_write_enabled')
    expect(html).toContain('silently_frozen')
  })

  it('★ failed 态：仍渲染横幅，但**不**展示后端那两句（我们并不知道是不是停更）', async () => {
    mockGet.mockRejectedValue(new Error('down'))
    await refreshV1DataHorizon()
    const html = mountBanner().html()
    expect(html).toContain('无法确认流量数据状态')
    expect(html).toContain('未确认状态不等于状态正常')
    // 关键：failed 时不能显示「数据已停更」——那是**未验证的断言**。
    expect(html).not.toContain('流量数据已停更')
    // 也不能把后端的话挂出来（那时并没有后端响应）。
    expect(html).not.toContain('SILENCE_TEXT')
  })

  it('failed 态用更强的视觉标记（连「是不是停更」都不知道，页面数字都该被怀疑）', async () => {
    mockGet.mockRejectedValue(new Error('down'))
    await refreshV1DataHorizon()
    expect(mountBanner().html()).toContain('v1-frozen-banner--failed')
  })
})

describe('App.vue 接了横幅（读源文件）', () => {
  const __dirname = dirname(fileURLToPath(import.meta.url))
  const appVue = readFileSync(join(__dirname, '..', '..', 'App.vue'), 'utf8')

  it('App.vue 真的渲染了 V1DataFrozenBanner', () => {
    expect(appVue).toMatch(/import\s+V1DataFrozenBanner\s+from\s+['"][^'"]*V1DataFrozenBanner\.vue['"]/)
    // <V1DataFrozenBanner /> 或带属性形式。排除「只 import 不渲染」——
    // 那正是「有状态无消费者」的形态。
    expect(appVue).toMatch(/<V1DataFrozenBanner\s*\/?>/)
  })

  it('App.vue 挂载时拉一次告示（否则横幅永远停在 unknown 态）', () => {
    expect(appVue).toMatch(/refreshV1DataHorizon\(\)/)
  })
})

// D21（2026-10-06）的钉。缺陷形态是：**令牌不存在 ⇒ 靠字面兜底 ⇒ 暗色漏白**。
// 原写法 `var(--kx-warning-surface, #fff8e1)` 里那四个令牌全仓从未定义，
// 兜底于是在**两个主题下都生效**，暗色主题拿到浅奶油底配浅字，
// 实测对比度 1.10:1（失败态 1.02:1，WCAG AA 要 4.5:1）——横幅等于隐形，
// 而它承载的恰恰是「这些数字不可信」这句话。
//
// 为什么 color:check 挡不住这一类：它只断「有没有字面色」。
// 有人把令牌名改错、或顺手把兜底删了，颜色门照样全绿，背景直接消失。
// ⇒ 这里断的是**另一个失效形态**：本组件引用的每个令牌，
// 都必须在浅色块与深色块里**各自**有定义（只在浅色块里定义 ⇒ 暗色漏白重现）。
describe('D21 钉：横幅的颜色令牌在两个主题里都必须有定义', () => {
  const __dirname = dirname(fileURLToPath(import.meta.url))
  const bannerSrc = readFileSync(join(__dirname, 'V1DataFrozenBanner.vue'), 'utf8')
  const styleCss = readFileSync(join(__dirname, '..', '..', 'style.css'), 'utf8')

  /**
   * 取 `选择器 {` 起、括号配平的整块文本。
   *
   * ⚠ 选择器必须用 **行首锚定的正则** 匹配，不能用裸 `indexOf`。
   * style.css 第 5 行的注释里同时提到了两个主题名（`… html[data-theme='light'] /
   * html[data-theme='dark'] 切换…`），裸 `indexOf` 会落进那句注释，
   * 于是 light 和 dark **取到同一个块**，`dark` 集合恒等于 `light`，
   * `missingDark` 永远不可能非空 —— 断言恒真且看着一直绿。
   * 这是本条判据自己的第一版，变异 M-C（删掉深色块的 `--warning-bg`）rc=0 才暴露出来。
   */
  function blockStartingAt(css: string, selector: RegExp): string {
    const m = selector.exec(css)
    if (!m) throw new Error(`style.css 里找不到选择器 ${selector}`)
    let depth = 0
    for (let i = css.indexOf('{', m.index); i < css.length; i++) {
      if (css[i] === '{') depth++
      else if (css[i] === '}' && --depth === 0) return css.slice(m.index, i + 1)
    }
    throw new Error(`选择器 ${selector} 的花括号没配平`)
  }

  function tokenDefs(block: string): Set<string> {
    // 只认「定义」：`--name:`。`var(--name, …)` 后面跟的是逗号不是冒号，不会命中。
    return new Set([...block.matchAll(/(--[A-Za-z0-9-]+)\s*:/g)].map((m) => m[1]))
  }

  const lightBlock = blockStartingAt(styleCss, /^html\[data-theme='light'\]\s*\{/m)
  const darkBlock = blockStartingAt(styleCss, /^html\[data-theme='dark'\]\s*\{/m)
  const light = tokenDefs(lightBlock)
  const dark = tokenDefs(darkBlock)

  const styleBlock = bannerSrc.slice(bannerSrc.indexOf('<style'))
  // ⚠ 判据口径必须与 `color:check` 一致：**先剥注释再扫**（color-token-audit.mjs:124）。
  // 这不是放水 —— 注释是文档，取证原文（`var(--kx-warning-surface, #fff8e1)`）
  // 本来就该出现在注释里。第一版判据没剥，于是它先抓到了我自己写的 D21 说明。
  const declarations = styleBlock.replace(/\/\*[\s\S]*?\*\//g, '')

  it('量具自证：两个主题块真的取自**不同的**地方（否则下面那条恒真）', () => {
    // 浅色块以 color-scheme: light 收尾、深色块以 dark 收尾（style.css:147 / :272）。
    expect(lightBlock).toContain('color-scheme: light')
    expect(darkBlock).toContain('color-scheme: dark')
    expect(darkBlock).not.toContain('color-scheme: light')
    // 并且两个块的令牌集合确实不是同一份。
    expect([...dark].sort()).not.toEqual([...light].sort())
  })

  // 量具自证：这条断言必须真的读到了横幅的样式块，否则「全都已定义」是恒真的。
  it('量具自证：真的读到了横幅的 <style> 块，且剥注释后仍留着颜色声明', () => {
    expect(styleBlock).toContain('<style')
    expect(declarations).toMatch(/var\(--[A-Za-z0-9-]+\)/)
    // 剥掉的确实是注释本体（下面这行只在注释里出现过）。
    expect(styleBlock).toContain('#fff8e1')
    expect(declarations).not.toContain('#fff8e1')
  })

  it('横幅引用的每个令牌，在浅色与深色块里都各有定义', () => {
    const used = [...new Set([...declarations.matchAll(/var\((--[A-Za-z0-9-]+)/g)].map((m) => m[1]))]
    expect(used.length).toBeGreaterThan(0)
    const missingLight = used.filter((t) => !light.has(t))
    const missingDark = used.filter((t) => !dark.has(t))
    // 两个主题分别报：只在深色块缺 = 暗色漏白；只在浅色块缺 = 亮色漏白。
    expect({ missingLight, missingDark }).toEqual({ missingLight: [], missingDark: [] })
  })

  it('横幅的 <style> 里不再有字面颜色（color:check 的本地副本，失败信息更近）', () => {
    expect(declarations).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(declarations).not.toMatch(/rgba?\s*\(/)
  })
})
