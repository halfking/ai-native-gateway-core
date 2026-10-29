import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchProvidersToday,
  fetchModelsToday,
  fetchClientsToday,
  liveStatRowName,
  liveStatRowSuccessRate,
} from './liveTopology'
import { resetUnauthorizedForTests } from './client'

/**
 * liveTopology（/api/admin/topology/{providers,models,clients}-today）契约护栏。
 *
 * ① 三条端点的路径与 ?window= 参数形状（5m/1h 直查；缺省 = day 日桶）。
 * ② ★★★★ window 乱写被后端静默落回 day ⇒ 响应的 window 回显键**可选**；
 *    调用方（LiveView）以回显为准 —— 本文件只钉「发出去的请求形状」。
 * ③ ★★★★ degraded 是三组共有的契约键（信封层），行级键并集进 LiveStatRow。
 * ④ liveStatRowName 展示链：display_name → canonical_name → raw_model_name →
 *    client_key（三个后端结构体名字键不同源，视图不自己写取值链）。
 * ⑤ liveStatRowSuccessRate 分母 0 ⇒ null（不许 0% 冒充）。
 */

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

afterEach(() => {
  vi.unstubAllGlobals()
  resetUnauthorizedForTests()
})

describe('liveTopology 三组区间统计契约', () => {
  it('① 缺省 = day（不带 window 参数）；5m/1h 显式传参', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      calls.push(String(input))
      return jsonResponse(200, { items: [] })
    }))
    await fetchProvidersToday()
    await fetchModelsToday('5m')
    await fetchClientsToday('1h')
    expect(calls[0]).toBe('/api/admin/topology/providers-today')
    expect(calls[1]).toBe('/api/admin/topology/models-today?window=5m')
    expect(calls[2]).toBe('/api/admin/topology/clients-today?window=1h')
  })

  it('②③ 信封形状：window 回显可选、watermark 可选、degraded 可选；items 行承载并集', async () => {
    const envelope = {
      day_utc: '2026-10-29',
      source_high_watermark: '2026-10-29T09:00:00Z',
      window: undefined,
      degraded: true,
      error_code: 'rollup_missing',
      items: [{
        raw_model_name: 'glm-5.3-flash',
        requests: 3,
        success: 2,
        failure: 1,
        rate_limited: 0,
        prompt_tokens: 1,
        completion_tokens: 1,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        credits_charged: 0,
        cost_usd: 0.1,
        share: 1,
      }],
    }
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(200, envelope)))
    const got = await fetchModelsToday()
    expect(got.degraded).toBe(true)
    expect(got.source_high_watermark).toBe('2026-10-29T09:00:00Z')
    expect(got.window).toBeUndefined()
    expect(got.items?.[0]?.raw_model_name).toBe('glm-5.3-flash')
  })

  it('④ liveStatRowName 展示链（display → canonical → raw → client_key → 空串）', () => {
    expect(liveStatRowName({ requests: 1, success: 1, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, credits_charged: 0, cost_usd: 0, share: 1, display_name: 'MiniMax', client_key: 'ck' })).toBe('MiniMax')
    expect(liveStatRowName({ requests: 1, success: 1, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, credits_charged: 0, cost_usd: 0, share: 1, canonical_name: 'glm-5.3-flash', raw_model_name: 'glm-5.3-flash-alias', client_key: 'ck' })).toBe('glm-5.3-flash')
    expect(liveStatRowName({ requests: 1, success: 1, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, credits_charged: 0, cost_usd: 0, share: 1, raw_model_name: 'glm-5.3-flash-alias' })).toBe('glm-5.3-flash-alias')
    expect(liveStatRowName({ requests: 1, success: 1, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, credits_charged: 0, cost_usd: 0, share: 1, client_key: 'claude-code' })).toBe('claude-code')
    expect(liveStatRowName({ requests: 1, success: 1, failure: 0, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, credits_charged: 0, cost_usd: 0, share: 1 })).toBe('')
  })

  it('⑤ liveStatRowSuccessRate：分母 0 ⇒ null；正常行百分比', () => {
    const base = { success: 7, failure: 3, rate_limited: 0, prompt_tokens: 0, completion_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, credits_charged: 0, cost_usd: 0, share: 1 }
    expect(liveStatRowSuccessRate({ ...base, requests: 0 })).toBeNull()
    expect(liveStatRowSuccessRate({ ...base, requests: 10 })).toBe(70)
  })
})
