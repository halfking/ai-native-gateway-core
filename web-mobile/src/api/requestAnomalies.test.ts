import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  fetchAnomalyList,
  fetchAnomalyCounts,
  unwrapAnomalyList,
  unwrapAnomalyCounts,
  anomalyParams,
  ANOMALY_TRIGGERS,
  ANOMALY_LIMIT_DEFAULT,
  ANOMALY_LIMIT_MAX,
  type AnomalyListResponse,
} from './requestAnomalies'

/**
 * 请求侧异常读面的契约测试（2026-10-07）。
 *
 * 五条重点：
 * 1. ★★★★ trigger 是**大小写敏感**精确匹配 ⇒ 只发后端那三个小写字面值，
 *    且**不**做 toLowerCase 归一（那会让用户以为大写也认）；
 * 2. ★★★★ `unresolved_only` 的真值判定只有 `"true"` / `"1"` 两种形态；
 * 3. ★★★ `counts` 只统计**未解决**的，与列表条数天然对不上；
 * 4. ★★★ `param` 是**逗号连接的多个**参数名，不是单值；
 * 5. ★★ 形状不符抛错；limit 越界不发（后端静默 clamp）。
 */

const fetchMock = vi.fn()

function jsonResponse(body: unknown) {
  return {
    ok: true,
    status: 200,
    json: async () => body,
    text: async () => JSON.stringify(body),
    headers: new Headers({ 'content-type': 'application/json' }),
  } as unknown as Response
}

function lastUrl(): string {
  return String(fetchMock.mock.calls.at(-1)![0])
}

function listBody(over: Record<string, unknown> = {}): AnomalyListResponse {
  return { anomalies: [], count: 0, limit: 50, offset: 0, ...over }
}

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  fetchMock.mockResolvedValue(jsonResponse(listBody()))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('路径与档位', () => {
  it('★★★ list 与 count 是两个独立路径', async () => {
    await fetchAnomalyList()
    expect(lastUrl()).toBe('/api/admin/request-anomalies')
    fetchMock.mockResolvedValueOnce(jsonResponse({ unresolved: 0, new_today: 0 }))
    await fetchAnomalyCounts()
    expect(lastUrl()).toBe('/api/admin/request-anomalies/count')
  })

  it('★ trigger 常量就是后端那三个，顺序照抄', () => {
    expect([...ANOMALY_TRIGGERS]).toEqual(['param_rejected', 'mode_mismatch', 'upstream_error'])
  })

  it('★ limit 默认 50 / 上限 500', () => {
    expect(ANOMALY_LIMIT_DEFAULT).toBe(50)
    expect(ANOMALY_LIMIT_MAX).toBe(500)
  })
})

describe('★★★★ 判据 1：trigger 大小写敏感，且不归一', () => {
  it('★★★ 三个合法 trigger 都发得出去', async () => {
    for (const t of ANOMALY_TRIGGERS) {
      await fetchAnomalyList({ trigger: t })
      expect(lastUrl()).toContain(`trigger=${t}`)
    }
  })

  it('★★★★ trigger=PARAM_REJECTED **不发**（后端大小写敏感，会静默返空）', async () => {
    await fetchAnomalyList({ trigger: 'PARAM_REJECTED' })
    expect(lastUrl()).not.toContain('trigger=')
  })

  it('★★★ 关键：也**不**把它归一成小写（后端只认小写，用户填大写本就查不到）', async () => {
    // ★ 若实现里写了 `qs.set('trigger', params.trigger.toLowerCase())`，
    //   上一条就会红 —— 那会把「用户填了大写」变成「悄悄按小写筛」，
    //   界面上看起来像是后端认了大写。
    await fetchAnomalyList({ trigger: 'Param_Rejected' })
    expect(lastUrl()).not.toContain('trigger=')
  })

  it('★★ trigger=任意未知值不发', async () => {
    await fetchAnomalyList({ trigger: 'nope' })
    expect(lastUrl()).not.toContain('trigger=')
  })
})

describe('★★★★ 判据 2：unresolved_only 的真值形态', () => {
  it('★★★ true ⇒ 发 `unresolved_only=true`', async () => {
    await fetchAnomalyList({ unresolvedOnly: true })
    expect(lastUrl()).toContain('unresolved_only=true')
  })

  it('★★★ false ⇒ **不发**（后端 queryBool 只认 "true"/"1"，发 "false" 反而是 false，但空值更省）', async () => {
    await fetchAnomalyList({ unresolvedOnly: false })
    expect(lastUrl()).not.toContain('unresolved_only')
  })

  it('★ 不传 ⇒ 不发', async () => {
    await fetchAnomalyList({})
    expect(lastUrl()).not.toContain('unresolved_only')
  })
})

describe('★★ 其它筛选项', () => {
  it('★★ 空白值一律不发（day/provider/model）', async () => {
    await fetchAnomalyList({ day: '  ', provider: '', model: '   ' })
    expect(lastUrl()).not.toContain('=')
  })

  it('★★ 前后空白被 trim 后再发', async () => {
    await fetchAnomalyList({ day: ' 2026-10-07 ', provider: ' openai ' })
    expect(lastUrl()).toContain('day=2026-10-07')
    expect(lastUrl()).toContain('provider=openai')
  })

  it('★ provider **不做**大小写归一（后端用 EqualFold，原样发即可）', async () => {
    await fetchAnomalyList({ provider: 'OpenAI' })
    expect(lastUrl()).toContain('provider=OpenAI')
  })
})

describe('★★ 分页边界', () => {
  it('★★ limit=501 不发；limit=500 发得出去', async () => {
    await fetchAnomalyList({ limit: 501 })
    expect(lastUrl()).not.toContain('limit=')
    await fetchAnomalyList({ limit: 500 })
    expect(lastUrl()).toContain('limit=500')
  })

  it('★★ limit=0 / 负数 / NaN 不发', async () => {
    for (const n of [0, -1, NaN]) {
      await fetchAnomalyList({ limit: n })
      expect(lastUrl()).not.toContain('limit=')
    }
  })

  it('★ offset=0 不发；offset>0 发', async () => {
    await fetchAnomalyList({ offset: 0 })
    expect(lastUrl()).not.toContain('offset=')
    await fetchAnomalyList({ offset: 30 })
    expect(lastUrl()).toContain('offset=30')
  })

  it('★ offset 负数不发（后端当 0）', async () => {
    await fetchAnomalyList({ offset: -3 })
    expect(lastUrl()).not.toContain('offset=')
  })
})

describe('★★★ 判据 4：param 是逗号连接的多个参数名', () => {
  it('★★★ 单值 ⇒ 单元素数组', () => {
    expect(anomalyParams('reasoning_effort')).toEqual(['reasoning_effort'])
  })

  it('★★★ 多值按逗号拆开并 trim', () => {
    expect(anomalyParams('reasoning_effort, top_p , max_tokens')).toEqual([
      'reasoning_effort',
      'top_p',
      'max_tokens',
    ])
  })

  it('★★ 空/空白/尾随逗号 ⇒ 空数组（不留空串）', () => {
    expect(anomalyParams(undefined)).toEqual([])
    expect(anomalyParams('')).toEqual([])
    expect(anomalyParams('a,')).toEqual(['a'])
    expect(anomalyParams(',,')).toEqual([])
  })
})

describe('形状不符抛错', () => {
  it('★★★ list 缺 anomalies ⇒ 抛错且点名「形状不符」', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ count: 0 }))
    await expect(fetchAnomalyList()).rejects.toThrow(/形状不符/)
  })

  it('★★★ list 返裸数组 ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse([]))
    await expect(fetchAnomalyList()).rejects.toThrow(/形状不符/)
  })

  it('★★★ count 缺 unresolved ⇒ 抛错', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ new_today: 3 }))
    await expect(fetchAnomalyCounts()).rejects.toThrow(/形状不符/)
  })

  it('★★ unwrap 可直接单测，且错误文案说清实得类型', () => {
    expect(() => unwrapAnomalyList(null)).toThrow(/实得 null/)
    expect(() => unwrapAnomalyList('x')).toThrow(/实得 string/)
    expect(() => unwrapAnomalyCounts([])).toThrow(/形状不符/)
  })

  it('★ 合法的对象原样放行（证明抛错不是无差别拒绝一切）', () => {
    // 本端点固定返回 `{anomalies:[…]}`；这条守的是 unwrap 不会误伤正确形状。
    const ok = listBody({ anomalies: [{ id: 1 }], count: 1 })
    expect(unwrapAnomalyList(ok)).toBe(ok)
  })
})