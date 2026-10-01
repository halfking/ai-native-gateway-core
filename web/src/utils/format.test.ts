import { describe, expect, it } from 'vitest'
import { formatNumberLocale } from './format'

describe('formatNumberLocale', () => {
  it('千分位分组（固定 locale，输出跨机稳定）', () => {
    expect(formatNumberLocale(1234567, 'en-US')).toBe('1,234,567')
    expect(formatNumberLocale(0, 'en-US')).toBe('0')
  })

  it('缺失/非有限值统一为 0（与 formatBytes 审计口径一致）', () => {
    expect(formatNumberLocale(null, 'en-US')).toBe('0')
    expect(formatNumberLocale(undefined, 'en-US')).toBe('0')
    expect(formatNumberLocale(Number.NaN, 'en-US')).toBe('0')
    expect(formatNumberLocale(Number.POSITIVE_INFINITY, 'en-US')).toBe('0')
  })

  it('locale 缺省时交给运行时默认（不抛错即可）', () => {
    expect(typeof formatNumberLocale(42)).toBe('string')
  })
})
