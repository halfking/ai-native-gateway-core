// probe-lane.test.ts — 探测流量物理隔离车道的读侧前端行为（2026-11-02）。
//
// 钉住两件事：
//
//  1. **来源判定**：探测流水有「当前（车道）/ 历史（业务表）」两个来源。
//     页面必须能分辨 ⇒ 否则运维会把「车道没开 ⇒ 只看得到历史行」读成
//     「最近没有探测」（这正是审计 D3 的原始症状）。
//
//  2. **映射口径**：探测不计费 ⇒ cost 一律不展示（不填 0）；
//     request_status 由 success 派生（车道表没有该列）。

import { describe, it, expect } from 'vitest'
import {
  probeLaneRowToDisplay,
  resolveProbeLaneSource,
  type ProbeLaneRequestRow,
} from './probe-lane'

function makeRow(partial: Partial<ProbeLaneRequestRow> = {}): ProbeLaneRequestRow {
  return {
    request_id: 'req-1',
    ts: '2026-11-02T01:53:39Z',
    tenant_id: 'default',
    success: true,
    created_at: '2026-11-02T01:53:40Z',
    ...partial,
  }
}

describe('resolveProbeLaneSource', () => {
  it('车道有行 ⇒ 来源是 current', () => {
    expect(resolveProbeLaneSource(3, true)).toBe('current')
    expect(resolveProbeLaneSource(1, false)).toBe('current')
  })

  it('车道开着但没行 ⇒ 仍是 current（不是历史）', () => {
    // ★ 关键区分：车道开着 + 0 行 = 「当前确实没有探测流量」；
    //   若误判成 historical，页面会提示「看的是隔离前的历史记录」——
    //   而真相是「车道开着、就是没有探测」，提示会误导运维。
    expect(resolveProbeLaneSource(0, true)).toBe('current')
  })

  it('车道没开 ⇒ 历史（探测仍在业务表里）', () => {
    expect(resolveProbeLaneSource(0, false)).toBe('historical')
  })
})

describe('probeLaneRowToDisplay', () => {
  it('success=true ⇒ request_status=success', () => {
    const d = probeLaneRowToDisplay(makeRow({ success: true }))
    expect(d.request_status).toBe('success')
    expect(d.success).toBe(true)
  })

  it('success=false ⇒ request_status=failure', () => {
    const d = probeLaneRowToDisplay(makeRow({ success: false, error_kind: 'invalid_key' }))
    expect(d.request_status).toBe('failure')
    expect(d.error_kind).toBe('invalid_key')
  })

  it('缺失的可空字段落 null（不是 0 / 空串）', () => {
    const d = probeLaneRowToDisplay(makeRow())
    expect(d.latency_ms).toBeNull()
    expect(d.prompt_tokens).toBeNull()
    expect(d.completion_tokens).toBeNull()
    expect(d.credential_label).toBeNull()
    expect(d.error_kind).toBeNull()
  })

  it('credential_id（text）落在标签位', () => {
    const d = probeLaneRowToDisplay(makeRow({ credential_id: '42' }))
    expect(d.credential_label).toBe('42')
  })

  it('★ 不产出 cost 字段（探测不计费 ⇒ 不展示假 0）', () => {
    const d = probeLaneRowToDisplay(makeRow()) as unknown as Record<string, unknown>
    // 展示形态里**没有** cost_usd ⇒ 页面渲染时显示「—」而不是 0。
    // 填 0 会被读成「探测花了 0 块」，那是假信息。
    expect('cost_usd' in d).toBe(false)
    expect('cost_display' in d).toBe(false)
  })
})
