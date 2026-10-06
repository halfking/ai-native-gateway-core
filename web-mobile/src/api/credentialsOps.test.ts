import { describe, it, expect } from 'vitest'
import { unwrapDecisions, type CredentialDecisionsResponse } from './credentialsOps'

/**
 * unwrapDecisions — decisions 端点的信封解包（2026-10-06）。
 *
 * 后端 admin/credential_monitor.go:1658 的实际返回是
 *   {credential_id, decisions, total}
 * 即**信封**，不是裸数组（这与 nodes.ts 的 unwrapMonitorSummary 是同一类坑：
 * 把信封当裸载荷 ⇒ 视图在对象上调 filter/filter ⇒ TypeError ⇒ 页面进错误态）。
 *
 * 立场：**形状不符抛错，不静默返回空数组**。返回 [] 会让「解包失败」和
 * 「这个凭据最近没有流量」在 UI 上完全一样 —— 后者是真结论，前者是契约漂移，
 * 混为一谈就会把一次后端改动误读成「节点不跑了」。
 */
describe('unwrapDecisions', () => {
  const DECISION = {
    ts: '2026-10-06T13:00:00Z',
    request_id: 'r1',
    model: 'claude-sonnet-4-6',
    tier: 1,
    success: true,
    latency_ms: 820,
    error_class: null,
    chosen_provider_id: 3,
    client_model: null,
    outbound_model: null,
    sticky_hit: null,
  }

  it('取信封里的 decisions（后端真实形态）', () => {
    const resp: CredentialDecisionsResponse = { credential_id: 9, decisions: [DECISION], total: 1 }
    const out = unwrapDecisions(resp)
    expect(out).toHaveLength(1)
    expect(out[0]?.request_id).toBe('r1')
  })

  it('空信封返回空数组 —— 这是「无记录」的真实结论', () => {
    const resp: CredentialDecisionsResponse = { credential_id: 9, decisions: [], total: 0 }
    expect(unwrapDecisions(resp)).toEqual([])
  })

  it('裸数组也容忍（防御后端形态漂移）', () => {
    expect(unwrapDecisions([DECISION])).toHaveLength(1)
  })

  it.each([
    ['null', null],
    ['字符串', 'oops'],
    ['数字', 42],
    ['缺 decisions 的对象', { credential_id: 9, total: 0 }],
    ['decisions 不是数组', { credential_id: 9, decisions: null, total: 0 }],
  ])('%s 必须抛错，不能静默返空数组', (_label, bad) => {
    // ★ 反向判据：若这里改成「形状不符返 []」，本用例会红。
    //   那样一来「后端改了返回结构」会被显示成「这个凭据最近没流量」。
    expect(() => unwrapDecisions(bad as never)).toThrow(/形状不符/)
  })
})
