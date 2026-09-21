import { describe, expect, it } from 'vitest'
import { chartColors, chartTheme, generateColors } from './useChart'

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

  it('chartTheme text/muted are hex; grid is rgba (canvas-parseable)', () => {
    expect(chartTheme.text).toMatch(HEX6)
    expect(chartTheme.muted).toMatch(HEX6)
    expect(chartTheme.grid).toMatch(/^rgba?\(/)
    expect(chartTheme.text).not.toMatch(/var\(/)
    expect(chartTheme.muted).not.toMatch(/var\(/)
    expect(chartTheme.grid).not.toMatch(/color-mix\(/)
  })
})
