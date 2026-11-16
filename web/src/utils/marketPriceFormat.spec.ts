import { describe, expect, it } from 'vitest'
import {
  fmtDualFloors,
  fmtMarketNum,
  fmtMarketRatio,
  fmtPair,
  fmtVendorAnchor,
  marketDriftClass,
} from './marketPriceFormat'

describe('marketPriceFormat', () => {
  it('never renders null as 0', () => {
    expect(fmtMarketNum(null)).toBe('—')
    expect(fmtMarketRatio(undefined)).toBe('—')
    expect(fmtPair(null, null, '¥')).toBe('—')
  })

  it('formats vendor USD anchor with CNY paren', () => {
    expect(fmtVendorAnchor({
      vendor_list_currency: 'USD',
      vendor_list_native_in_per_1m: 1,
      vendor_list_native_out_per_1m: 2,
      vendor_list_in_cny_per_1m: 7.2,
      vendor_list_out_cny_per_1m: 14.4,
    })).toContain('$')
  })

  it('formats dual floors with platform counts', () => {
    const s = fmtDualFloors({
      overseas_min_in_per_1m: 1,
      overseas_min_out_per_1m: 2,
      overseas_min_platform_count: 4,
      domestic_min_in_per_1m: null,
      domestic_min_out_per_1m: null,
      domestic_min_platform_count: 0,
    })
    expect(s).toContain('（4）')
    expect(s).toContain('—')
  })

  it('classifies retail drift badges', () => {
    expect(marketDriftClass(null)).toBe('')
    expect(marketDriftClass(0.9)).toBe('badge-blue')
    expect(marketDriftClass(1.2)).toBe('badge-yellow')
    expect(marketDriftClass(2)).toBe('badge-red')
  })
})
