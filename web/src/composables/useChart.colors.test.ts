import { describe, expect, it, beforeEach, afterEach } from 'vitest'
import { chartColors, getChartTheme, generateColors } from './useChart'

const HEX6 = /^#[0-9a-fA-F]{6}$/
const HEX8 = /^#[0-9a-fA-F]{8}$/

describe('useChart canvas color contracts', () => {
  it('generateColors returns only 6-digit hex (no var/color-mix)', () => {
    const colors = generateColors(16)
    expect(colors).toHaveLength(16)
    for (const c of colors) {
      expect(c).toMatch(HEX6)
      expect(c).not.toMatch(/var\(/)
      expect(c).not.toMatch(/color-mix\(/)
    }
  })

  it('chartColors values are 6-digit hex so +alpha suffix stays valid', () => {
    for (const [key, value] of Object.entries(chartColors)) {
      expect(value, key).toMatch(HEX6)
      expect(value + '80', `${key}+80`).toMatch(HEX8)
    }
  })

  it('getChartTheme returns hex/rgba that canvas can parse', () => {
    const t = getChartTheme()
    expect(t.text).toMatch(HEX6)
    expect(t.muted).toMatch(HEX6)
    expect(t.grid).toMatch(/^rgba?\(/)
    expect(t.text).not.toMatch(/var\(/)
    expect(t.muted).not.toMatch(/var\(/)
    expect(t.grid).not.toMatch(/color-mix\(/)
  })
})

// 2026-09-29：暗色修复回归。getChartTheme() 必须跟随 documentElement.data-theme 切换，
// 否则亮色皮肤下 chart 仍是暗色调色板，文字与背景几乎同色、看不清。
describe('getChartTheme is theme-aware', () => {
  let originalTheme: string | null
  // jsdom 不解析 style.css 里的 :root 自定义属性，需要手工注入。
  const setTokens = (vars: Record<string, string>) => {
    const el = document.documentElement
    for (const [k, v] of Object.entries(vars)) el.style.setProperty(k, v)
  }
  const clearTokens = () => {
    const el = document.documentElement
    for (const k of ['--kx-text', '--kx-muted', '--border']) el.style.removeProperty(k)
  }
  beforeEach(() => {
    originalTheme = document.documentElement.getAttribute('data-theme')
  })
  afterEach(() => {
    if (originalTheme === null) {
      document.documentElement.removeAttribute('data-theme')
    } else {
      document.documentElement.setAttribute('data-theme', originalTheme)
    }
    clearTokens()
  })

  it('returns visibly different palettes for dark vs light', () => {
    document.documentElement.setAttribute('data-theme', 'dark')
    setTokens({ '--kx-text': '#e8eef7', '--kx-muted': '#8b949e', '--border': '#1c2128' })
    const dark = getChartTheme()
    document.documentElement.setAttribute('data-theme', 'light')
    setTokens({ '--kx-text': '#152033', '--kx-muted': '#5a6473', '--border': '#d8dde3' })
    const light = getChartTheme()
    // 文字色与网格色必须明显不同——否则亮色皮肤下仍用暗色 hex，看不清
    expect(dark.text).not.toBe(light.text)
    expect(dark.muted).not.toBe(light.muted)
    expect(dark.grid).not.toBe(light.grid)
    // 一对不能是「同一字面量当两套皮肤」，这是历史 bug 的精确形态
    expect([dark.text, light.text].every((c) => /^#[0-9a-fA-F]{6}$/.test(c))).toBe(true)
  })

  it('text token resolved from CSS is a valid 6-digit hex', () => {
    document.documentElement.setAttribute('data-theme', 'light')
    setTokens({ '--kx-text': '#152033', '--kx-muted': '#5a6473', '--border': '#d8dde3' })
    const t = getChartTheme()
    expect(t.text).toMatch(HEX6)
    expect(t.muted).toMatch(HEX6)
  })

  it('grid color flips between dark and light (alpha differs by skin)', () => {
    document.documentElement.setAttribute('data-theme', 'dark')
    const dark = getChartTheme()
    document.documentElement.setAttribute('data-theme', 'light')
    const light = getChartTheme()
    // 暗色网格是白 6% alpha，亮色是主文字色 8% alpha。两套皮肤绝不能同一个 rgba。
    expect(dark.grid).toContain('rgba(')
    expect(light.grid).toContain('rgba(')
    expect(dark.grid).not.toBe(light.grid)
  })
})
