import { describe, it, expect } from 'vitest'
import { CAPABILITY_NAMES, CAPABILITY_NAME_TO_FIELD, fetchCapabilities } from './featureCapabilities'
import { DRAWER_NAV, BOTTOM_NAV } from '@/config/appNav'

/**
 * 能力位的前后端契约（2026-10-21，R72f · 属主决策项 #10 的后半）。
 *
 * ★★ 这一组守的是**一条静默失效的链路**：
 *   前端按 `NavItem.requiresCapability` 决定「这一席画不画」；
 *   后端 `GET /api/admin/capabilities` 发回的 JSON 字段名决定「哪个能力开着」。
 *   ⇒ 若两边的**名字**对不上（打错一个字、加了一席没加字段、…），
 *   **不会报错**：能力位取不到该名字 ⇒ fail-closed ⇒
 *   **那一席永远不出现** ⇒ ⇒ 用户看到的是「功能不见了」，不是「功能坏了」。
 *   ⇒ ★ 所以必须有一道**名字级**的门。
 *
 * ★ 口径：
 *   · `requiresCapability` 的取值必须 ∈ `CAPABILITY_NAMES`；
 *   · `CAPABILITY_NAMES` 的每个名字必须是后端 JSON 的键
 *     （`CAPABILITY_NAME_TO_FIELD` 的类型由 `vue-tsc` 静态检查，
 *       这里再从**运行时**钉一遍「清单与映射一一对应」）。
 */

describe('能力位契约', () => {
  it('★ nav 里每一席的 requiresCapability 都在能力位清单里', () => {
    const known = new Set<string>(CAPABILITY_NAMES)
    const items = [...DRAWER_NAV, ...BOTTOM_NAV]
    const unknown = items
      .map((i) => i.requiresCapability)
      .filter((c): c is string => typeof c === 'string')
      .filter((c) => !known.has(c))
    // ★ 失败信息要点名是哪一席 —— 否则只看到「一个不认识的能力名」，无从修。
    const offenders = items
      .filter((i) => i.requiresCapability && !known.has(i.requiresCapability))
      .map((i) => `${i.key}(${i.requiresCapability})`)
    expect(offenders, `这些席要求的能力位不在 CAPABILITY_NAMES 里：${offenders.join(', ')}`).toEqual([])
    expect(unknown).toEqual([])
  })

  it('★ 能力位清单与字段映射一一对应（不多不少）', () => {
    expect(Object.keys(CAPABILITY_NAME_TO_FIELD).sort()).toEqual([...CAPABILITY_NAMES].sort())
    for (const name of CAPABILITY_NAMES) {
      expect(CAPABILITY_NAME_TO_FIELD[name], `${name} 的字段映射必须指向自己`).toBe(name)
    }
  })

  it('★ 至少有一席真的挂上了能力位（否则这组门测了个寂寞）', () => {
    const gated = [...DRAWER_NAV, ...BOTTOM_NAV].filter((i) => i.requiresCapability)
    expect(gated.length, '没有任何席要求能力位 ⇒ 「按能力隐藏入口」根本没被用上').toBeGreaterThan(0)
  })

  it('★ fetchCapabilities 打的是 /api/admin/capabilities', async () => {
    // ★ 只验「打到哪里」与「原样返回」—— 网络失败/鉴权失败由 store 的
    //   fail-closed 口径处理（见 stores/capabilities.ts）。
    const calls: Array<[string, string]> = []
    const orig = globalThis.fetch
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push([String(input), String(init?.method ?? 'GET')])
      return new Response(JSON.stringify({ cost_reconciliation: true }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    }) as typeof fetch
    try {
      const caps = await fetchCapabilities()
      expect(caps).toEqual({ cost_reconciliation: true })
      expect(calls.length).toBe(1)
      expect(calls[0]![1]).toBe('GET')
      expect(calls[0]![0]).toContain('/api/admin/capabilities')
    } finally {
      globalThis.fetch = orig
    }
  })
})
