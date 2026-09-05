export const BILLING_MODES = [
  'per_token',
  'free',
  'token_plan',
  'code_plan',
  'agent_plan',
] as const

export type BillingMode = typeof BILLING_MODES[number]
export type EditablePrice = number | '' | null

export const PRICE_FIELDS = [
  'unit_price_in_per_1m',
  'unit_price_out_per_1m',
  'cache_read_price_per_1m',
  'cache_write_price_per_1m',
] as const

export type PriceField = typeof PRICE_FIELDS[number]

export type ModelOfferPricing = Partial<Record<PriceField, number | null>> & {
  billing_mode?: string | null
}

export function isBillingMode(value: string | null | undefined): value is BillingMode {
  return typeof value === 'string' && (BILLING_MODES as readonly string[]).includes(value)
}

export function normalizePrice(value: EditablePrice): number | null {
  if (value === '' || value == null) return null
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue >= 0 ? numberValue : null
}

export function validatePricing(
  prices: Record<PriceField, EditablePrice>,
  billingMode: string | null | undefined,
): string | null {
  for (const field of PRICE_FIELDS) {
    const value = prices[field]
    if (value !== '' && value != null) {
      const numberValue = Number(value)
      if (!Number.isFinite(numberValue) || numberValue < 0) {
        return `${field} must be a non-negative finite number`
      }
    }
  }
  return isBillingMode(billingMode) ? null : 'Invalid billing mode'
}

export function formatPricePer1M(value: number | null | undefined, unsetLabel: string): string {
  if (value == null) return unsetLabel
  return Number.isFinite(value) ? String(value) : unsetLabel
}

export function pricingSummary(
  pricing: ModelOfferPricing,
  labels: { unset: string; free: string; billingMode: string },
): string {
  const prices = PRICE_FIELDS.map(field => formatPricePer1M(pricing[field], labels.unset)).join(' / ')
  const mode = pricing.billing_mode === 'free' ? labels.free : pricing.billing_mode || labels.unset
  return `${prices} · ${labels.billingMode}: ${mode}`
}
