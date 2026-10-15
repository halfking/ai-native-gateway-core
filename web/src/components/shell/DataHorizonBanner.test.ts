// DataHorizonBanner.test.ts — 横幅的**渲染**判据（2026-10-15 r1033 重写）。
//
// # 为什么必须有这道门（composable 的测试不够）
//
// `useDataHorizon.test.ts` 覆盖三态转换，但它**完全不需要组件存在**：
// 把 `DataHorizonBanner.vue` 整个删掉，那些用例依然全绿。
// 那样就得到一个「有状态、有测试、但没有任何消费者」的 composable。
//
// ⇒ 这里**真的挂载组件**，断言各状态下渲染出什么；
// 另有一条读源文件的断言，把 App.vue 与横幅接起来。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../api/dataHorizon', () => ({ getDataHorizon: vi.fn() }))
import { getDataHorizon } from '../../api/dataHorizon'
import { refreshDataHorizon, __resetDataHorizonForTests } from '../../composables/useDataHorizon'
import DataHorizonBanner from './DataHorizonBanner.vue'

const mockGet = vi.mocked(getDataHorizon)

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      app: {
        dataHorizon: {
          title: '流量数据记录滞后',
          titleUnavailable: '无法确认流量数据状态',
          hintLabel: '排查',
          retry: '重新检查',
          failedHint: '未确认状态不等于状态正常',
        },
      },
    },
  },
})

const staleNotice = {
  stale: true as const,
  unknown: false as const,
  source: 'session_turns_hot',
  effect: 'EFFECT_TEXT',
  silence: 'SILENCE_TEXT',
  hint: 'HINT_TEXT',
}

function mountBanner() {
  return mount(DataHorizonBanner, { global: { plugins: [i18n] } })
}

describe('DataHorizonBanner', () => {
  beforeEach(() => {
    __resetDataHorizonForTests()
    mockGet.mockReset()
  })

  it('unknown 态：什么都不渲染（页面刚起来闪一条比不闪更糟）', () => {
    expect(mountBanner().html()).toBe('<!--v-if-->')
  })

  it('healthy 态（含网关空闲）：什么都不渲染', async () => {
    mockGet.mockResolvedValue({ data_horizon: null, '//': '' })
    await refreshDataHorizon()
    expect(mountBanner().html()).toBe('<!--v-if-->')
  })

  it('stale 态：渲染标题 + 后端给的三句（effect/silence/hint）', async () => {
    mockGet.mockResolvedValue({ data_horizon: staleNotice, '//': '' })
    await refreshDataHorizon()
    const html = mountBanner().html()
    expect(html).toContain('流量数据记录滞后')
    // 后端给的三句是「这个实例」的实话，必须原样出现在页面上。
    expect(html).toContain('EFFECT_TEXT')
    expect(html).toContain('SILENCE_TEXT')
    expect(html).toContain('HINT_TEXT')
  })

  it('★ failed 态：仍渲染横幅，但**不**展示后端那三句（我们并不知道是不是断流）', async () => {
    mockGet.mockRejectedValue(new Error('down'))
    await refreshDataHorizon()
    const html = mountBanner().html()
    expect(html).toContain('无法确认流量数据状态')
    expect(html).toContain('未确认状态不等于状态正常')
    // 关键：failed 时不能显示「记录滞后」——那是**未验证的断言**。
    expect(html).not.toContain('流量数据记录滞后')
    // 也不能把后端的话挂出来（那时并没有后端响应）。
    expect(html).not.toContain('SILENCE_TEXT')
  })

  it('failed 态用更强的视觉标记（连「是不是断流」都不知道，页面数字都该被怀疑）', async () => {
    mockGet.mockRejectedValue(new Error('down'))
    await refreshDataHorizon()
    expect(mountBanner().html()).toContain('data-horizon-banner--failed')
  })

  it('★ 后端 unknown 态（读不到 session 数据）走 failed 分支文案，不走 stale 三句', async () => {
    mockGet.mockResolvedValue({
      data_horizon: { ...staleNotice, stale: false, unknown: true, effect: '无法确认流量数据状态：session_turns 读取失败。' },
      '//': '',
    })
    await refreshDataHorizon()
    const html = mountBanner().html()
    expect(html).toContain('无法确认流量数据状态')
    expect(html).toContain('未确认状态不等于状态正常')
    expect(html).not.toContain('SILENCE_TEXT')
  })
})

describe('App.vue 接了横幅（读源文件）', () => {
  const __dirname = dirname(fileURLToPath(import.meta.url))
  const appVue = readFileSync(join(__dirname, '..', '..', 'App.vue'), 'utf8')

  it('App.vue 真的渲染了 DataHorizonBanner', () => {
    expect(appVue).toMatch(/import\s+DataHorizonBanner\s+from\s+['"][^'"]*DataHorizonBanner\.vue['"]/)
    // <DataHorizonBanner /> 或带属性形式。排除「只 import 不渲染」——
    // 那正是「有状态无消费者」的形态。
    expect(appVue).toMatch(/<DataHorizonBanner\s*\/?>/)
  })

  it('App.vue 挂载时拉一次告示（否则横幅永远停在 unknown 态）', () => {
    expect(appVue).toMatch(/refreshDataHorizon\(\)/)
  })
})

// D21（2026-10-06）的钉（r1033 随组件更名迁移；断言口径不变）。
// 缺陷形态是：**令牌不存在 ⇒ 靠字面兜底 ⇒ 暗色漏白**。
// ⇒ 这里断言：本组件引用的每个令牌，都必须在浅色块与深色块里**各自**
// 有定义（只在浅色块里定义 ⇒ 暗色漏白重现）。
describe('D21 钉：横幅的颜色令牌在两个主题里都必须有定义', () => {
  const __dirname = dirname(fileURLToPath(import.meta.url))
  const bannerSrc = readFileSync(join(__dirname, 'DataHorizonBanner.vue'), 'utf8')
  const styleCss = readFileSync(join(__dirname, '..', '..', 'style.css'), 'utf8')

  // ⚠ 选择器必须用 **行首锚定的正则** 匹配，不能用裸 `indexOf`
  // （style.css 第 5 行注释里同时提到两个主题名，裸 indexOf 会落进注释）。
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
    // 只认「定义」：`--name:`。
    return new Set([...block.matchAll(/(--[A-Za-z0-9-]+)\s*:/g)].map((m) => m[1]))
  }

  const lightBlock = blockStartingAt(styleCss, /^html\[data-theme='light'\]\s*\{/m)
  const darkBlock = blockStartingAt(styleCss, /^html\[data-theme='dark'\]\s*\{/m)
  const light = tokenDefs(lightBlock)
  const dark = tokenDefs(darkBlock)

  const styleBlock = bannerSrc.slice(bannerSrc.indexOf('<style'))
  // 判据口径与 `color:check` 一致：先剥注释再扫。
  const declarations = styleBlock.replace(/\/\*[\s\S]*?\*\//g, '')

  it('量具自证：两个主题块真的取自**不同的**地方（否则下面那条恒真）', () => {
    expect(lightBlock).toContain('color-scheme: light')
    expect(darkBlock).toContain('color-scheme: dark')
    expect(darkBlock).not.toContain('color-scheme: light')
    expect([...dark].sort()).not.toEqual([...light].sort())
  })

  it('量具自证：真的读到了横幅的 <style> 块，且剥注释后仍留着颜色声明', () => {
    expect(styleBlock).toContain('<style')
    expect(declarations).toMatch(/var\(--[A-Za-z0-9-]+\)/)
  })

  it('横幅引用的每个令牌，在浅色与深色块里都各有定义', () => {
    const used = [...new Set([...declarations.matchAll(/var\((--[A-Za-z0-9-]+)/g)].map((m) => m[1]))]
    expect(used.length).toBeGreaterThan(0)
    const missingLight = used.filter((t) => !light.has(t))
    const missingDark = used.filter((t) => !dark.has(t))
    expect({ missingLight, missingDark }).toEqual({ missingLight: [], missingDark: [] })
  })

  it('横幅的 <style> 里没有字面颜色（color:check 的本地副本，失败信息更近）', () => {
    expect(declarations).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(declarations).not.toMatch(/rgba?\s*\(/)
  })
})
