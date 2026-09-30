import { describe, expect, it } from 'vitest'
import { buildProviderCards, fmtUsd, matchProviderPie, type ProviderPieRef, type ProviderUsageRef } from './providerCards'

const pies: ProviderPieRef[] = [
  { key: '__other__', requests: 900, tokens: 1, credits: 0, cost_usd: 0 },
  { key: '火山方舟 TokenPlan', requests: 12, tokens: 400, credits: 18, cost_usd: 0 },
]

const rows: ProviderUsageRef[] = [
  { provider_id: 1, provider_code: 'openai', provider_name: 'OpenAI', request_count: 3, total_cost_usd: 0, prompt_tokens: 10, completion_tokens: 2 },
  { provider_id: 2, provider_code: 'volc', provider_name: '火山方舟 TokenPlan', request_count: 12, total_cost_usd: 0.0001, prompt_tokens: 20, completion_tokens: 5 },
  { provider_id: 3, provider_code: 'suyun', provider_name: 'suyun', request_count: 40, total_cost_usd: 3.38, prompt_tokens: 100, completion_tokens: 20 },
  { provider_id: 4, provider_code: 'apigpt', provider_name: 'apigpt', request_count: 8, total_cost_usd: 0.19, prompt_tokens: 30, completion_tokens: 4 },
  { provider_id: 5, provider_code: 'vapeur', provider_name: 'Vapeur', request_count: 2, total_cost_usd: 0.0061, prompt_tokens: 4, completion_tokens: 1 },
  { provider_id: 6, provider_code: 'zero', provider_name: 'Zero', request_count: 1, total_cost_usd: 0, prompt_tokens: 1, completion_tokens: 0 },
]

describe('buildProviderCards', () => {
  it('按用量成本排序，不用 $0 的饼图桶占位', () => {
    const cards = buildProviderCards(rows, pies)
    expect(cards.map((card) => card.code)).toEqual(['suyun', 'apigpt', 'vapeur', 'volc', 'openai'])
    expect(cards[0]?.costUsd).toBe(3.38)
    expect(cards.some((card) => card.code === '__other__')).toBe(false)
  })

  it('饼图 key 是展示名时仍能带回积分', () => {
    const volc = buildProviderCards(rows, pies).find((card) => card.code === 'volc')
    expect(matchProviderPie(pies, 'volc', '火山方舟 TokenPlan')?.credits).toBe(18)
    expect(volc?.credits).toBe(18)
  })

  it('没有用量行时不回退到饼图', () => {
    expect(buildProviderCards([], pies)).toEqual([])
  })
})

describe('fmtUsd', () => {
  it('小于 1 美分保留四位，更小的非零值不用 $0.0000', () => {
    expect(fmtUsd(3.38)).toBe('$3.38')
    expect(fmtUsd(0.0061)).toBe('$0.0061')
    expect(fmtUsd(0.0000004)).toBe('$4.0e-7')
    expect(fmtUsd(0)).toBe('$0.00')
    expect(fmtUsd(undefined)).toBe('—')
  })
})
