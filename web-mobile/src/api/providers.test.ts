import { describe, it, expect } from 'vitest'
import { unwrapProviders, providerName, type Provider } from './providers'

/**
 * providers 解包（2026-10-06）。
 *
 * ★ 这个端点的形态与本仓另外两个**恰好相反**，是踩坑高发点：
 *   /api/providers                    → **裸数组**（桌面 providers.ts:130 照收）
 *   /api/candidate-failures/alerts    → {data, count} 信封
 *   /api/credentials/monitor-summary  → {credentials, count, meta} 信封
 * 三个端点三种形态。所以这里**不用**通用解包器 —— 套一个"兼容两种"的 helper
 * 反而会掩盖真正的契约差异，让下一个维护者以为全站都是同一种形态。
 *
 * 立场同另两处：形状不符**抛错**，不返 []。返 [] 会让「后端改了返回结构」
 * 显示成「一个供应商都没有」，看着像数据被清空。
 */
describe('unwrapProviders', () => {
  const P: Provider = {
    id: 3, code: 'anthropic', display_name: 'Anthropic', catalog_code: 'anthropic',
    protocol: 'anthropic-messages', base_url: null, enabled: true,
  }

  it('裸数组是当前真实形态，直接透传', () => {
    expect(unwrapProviders([P])).toHaveLength(1)
  })

  it('空数组透传（真的一个供应商都没有）', () => {
    expect(unwrapProviders([])).toEqual([])
  })

  it.each([{ providers: [P] }, { data: [P] }])(
    '容忍信封形态（形态漂移兜底）：%o',
    (resp) => {
      expect(unwrapProviders(resp as never)).toHaveLength(1)
    },
  )

  it.each([
    ['null', null],
    ['字符串', 'oops'],
    ['空对象', {}],
    ['providers 不是数组', { providers: null }],
  ])('%s 必须抛错，不能静默返空数组', (_label, bad) => {
    expect(() => unwrapProviders(bad as never)).toThrow(/形状不符/)
  })
})

describe('providerName', () => {
  it('display_name 优先', () => {
    expect(providerName({ display_name: 'Anthropic', code: 'anthropic' } as Provider)).toBe('Anthropic')
  })

  it('display_name 为 null 时回落到 code', () => {
    expect(providerName({ display_name: null, code: 'canary' } as Provider)).toBe('canary')
  })

  it('display_name 为空串也回落（空串是「没有」，不是「名字就叫空」）', () => {
    expect(providerName({ display_name: '', code: 'canary' } as Provider)).toBe('canary')
  })
})
