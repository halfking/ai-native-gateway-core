import { describe, expect, it } from 'vitest'
import {
  formatPricePer1M,
  isBillingMode,
  normalizePrice,
  pricingSummary,
  validatePricing,
  type PriceField,
} from './modelOfferPricing'

const validPrices: Record<PriceField, number | '' | null> = {
  unit_price_in_per_1m: 1.5,
  unit_price_out_per_1m: 3,
  cache_read_price_per_1m: null,
  cache_write_price_per_1m: '',
}

describe('modelOfferPricing', () => {
  it('keeps an explicit zero price distinct from an unset price', () => {
    expect(normalizePrice(0)).toBe(0)
    expect(formatPricePer1M(0, 'Unset')).toBe('0')
    expect(formatPricePer1M(null, 'Unset')).toBe('Unset')
  })

  it('accepts only finite non-negative prices and known billing modes', () => {
    expect(validatePricing(validPrices, 'per_token')).toBeNull()
    expect(validatePricing({ ...validPrices, unit_price_out_per_1m: -1 }, 'per_token'))
      .toBe('unit_price_out_per_1m must be a non-negative finite number')
    expect(validatePricing({ ...validPrices, cache_read_price_per_1m: Number.NaN }, 'per_token'))
      .toBe('cache_read_price_per_1m must be a non-negative finite number')
    expect(validatePricing(validPrices, 'request')).toBe('Invalid billing mode')
    expect(isBillingMode('free')).toBe(true)
    expect(isBillingMode('request')).toBe(false)
  })

  it('renders the pricing summary with free and unset semantics', () => {
    expect(pricingSummary({
      unit_price_in_per_1m: 0,
      unit_price_out_per_1m: 0,
      cache_read_price_per_1m: null,
      cache_write_price_per_1m: null,
      billing_mode: 'free',
    }, { unset: 'Unset', free: 'Free', billingMode: 'Billing' }))
      .toBe('0 / 0 / Unset / Unset · Billing: Free')
  })
})
