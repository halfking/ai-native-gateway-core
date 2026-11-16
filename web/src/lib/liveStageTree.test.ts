// liveStageTree.test.ts — 请求阶段树纯函数层的判据（2026-11-16）
//
// ★ 判据纪律（沿用仓内既有做法）：
//   · 每条判据必须满足「**把被测逻辑改回缺陷形态，它立刻变红**」；
//   · 口径判据**逐字对齐生产**（domains/streaming/usage.go 的
//     CacheTokenConventionForProtocol），不按「看起来合理」写；
//   · 「未测量」与「未到达」分开钉 —— 合并成一个灰态是本轮明确禁止的缺陷。

import { describe, expect, it } from 'vitest'
import {
  cacheRateOf,
  deriveStageBand,
  deriveStages,
  failureStageOf,
  isInFlight,
  resolveModelOf,
} from './liveStageTree'
import { ALL_STAGES } from '../types/liveStageTree'
import type { ActionEvent } from '../composables/liveStreamStore'

/** 造一条动作（线上真形状：明细在 detail map 里，值是字符串）。 */
function act(action: string, seq: number, detail: Record<string, string> = {}, model?: string): ActionEvent {
  return { request_id: 'r1', seq, action, detail, model }
}

/** 造一条请求帧。 */
function req(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    ts: new Date().toISOString(),
    request_id: 'r1',
    status: 'success',
    ...over,
  }
}

describe('liveStageTree 阶段表', () => {
  it('★ 阶段顺序 = 代码真实管线顺序（脱敏在审批之前 / 压缩在路由之后 / 还原在响应侧）', () => {
    // 这条判据写死的是**核过的代码事实**，不是列举顺序：
    //   ① 脱敏是中间件（包住 serveHTTPInner）⇒ 在审批、注入检测**之前**；
    //   ② 会话压缩器 Prepare 明确 runs AFTER candidate resolution ⇒ 在路由**之后**；
    //   ③ 脱敏占位符回填挂在响应链上 ⇒ 在首字节**之后** ⇒ 属接收轨。
    expect(ALL_STAGES.map(s => s.key)).toEqual([
      'auth',
      'sanitize',
      'approval',
      'injection',
      'route',
      'compress',
      'provider',
      'first_byte',
      'restore',
      'meter',
      'reply',
    ])
  })

  it('★ 发送轨 7 段 / 接收轨 4 段（接收轨渲染时从右往左）', () => {
    const send = ALL_STAGES.filter(s => s.track === 'send')
    const recv = ALL_STAGES.filter(s => s.track === 'recv')
    expect(send.map(s => s.key)).toEqual([
      'auth',
      'sanitize',
      'approval',
      'injection',
      'route',
      'compress',
      'provider',
    ])
    expect(recv.map(s => s.key)).toEqual(['first_byte', 'restore', 'meter', 'reply'])
  })

  it('★ tools 还原**不是独立一列**（它是压缩节点的徽标：压缩之后、出站之前）', () => {
    // 主档 §5.2 附注写死：「tools 还原发生在压缩之后、出站之前，
    // 属发送轨第 6 段的子步骤，不单列一列，但会在压缩节点上挂一个「tools 回填 n」徽标」。
    const keys = ALL_STAGES.map(s => s.key as string)
    expect(keys).not.toContain('tools_restored')
    expect(keys).toContain('compress')
  })
})

describe('liveStageTree 缓存命中率口径', () => {
  it('★ anthropic-messages ⇒ 并列口径（分母 = prompt + cache_read）', () => {
    const r = cacheRateOf({
      cache_read_tokens: 100,
      prompt_tokens: 300,
      upstream_protocol: 'anthropic-messages',
    } as never)
    expect(r?.convention).toBe('parallel_anthropic')
    expect(r?.pct).toBe(25) // 100 / (300+100)
  })

  it('★ openai-* ⇒ 包含口径（分母 = prompt）', () => {
    const r = cacheRateOf({
      cache_read_tokens: 100,
      prompt_tokens: 400,
      upstream_protocol: 'openai-completions',
    } as never)
    expect(r?.convention).toBe('subset_openai')
    expect(r?.pct).toBe(25) // 100 / 400
  })

  it('★ 口径不可判定 ⇒ pct=null（不是 0%）', () => {
    const r = cacheRateOf({
      cache_read_tokens: 100,
      prompt_tokens: 400,
      upstream_protocol: '',
    } as never)
    expect(r).not.toBeNull()
    expect(r?.pct).toBeNull()
    expect(r?.convention).toBe('unknown')
  })

  it('★ 包含口径下算出 >100% ⇒ 报口径异常，不显示假百分比', () => {
    const r = cacheRateOf({
      cache_read_tokens: 500,
      prompt_tokens: 400,
      upstream_protocol: 'openai-chat',
    } as never)
    expect(r?.anomaly).toBe(true)
    expect(r?.pct).toBeNull()
  })

  it('★ 上游没报 cache_read ⇒ 没有命中率（不是 0%）', () => {
    expect(
      cacheRateOf({ prompt_tokens: 400, upstream_protocol: 'openai-chat' } as never),
    ).toBeNull()
  })
})

describe('liveStageTree 最终匹配模型', () => {
  it('★ 取 node_selected 的模型（= 路由解析后的标准名），不是 route_resolved 的模型', () => {
    // route_resolved 的 Model 填的是 **clientModel**（客户端请求的模型名），
    // 拿它当「最终匹配模型」会把 auto 请求显示成「auto 选了 auto」。
    const actions = [
      act('arrive', 1, {}, 'auto'),
      act('route_resolved', 2, { candidates: '3', auto: 'true' }, 'auto'),
      act('node_selected', 3, { attempt: '1' }, 'claude-sonnet-4-5'),
    ]
    const m = resolveModelOf(req({ is_auto_request: true }) as never, actions)
    expect(m.final).toBe('claude-sonnet-4-5')
    expect(m.source).toBe('action')
    expect(m.isAuto).toBe(true)
    // auto 请求的 client_model 在生产上已被改写 ⇒ 不能当「客户端要的名字」
    expect(m.requested).toBe('')
    expect(m.candidates).toBe(3)
  })

  it('★ 没有 node_selected ⇒ 退到帧的标准模型名', () => {
    const actions = [act('arrive', 1, {}, 'gpt-4o'), act('route_resolved', 2, {}, 'gpt-4o')]
    const m = resolveModelOf(req({ canonical_name: 'gpt-4o-2024-11-20' }) as never, actions)
    expect(m.final).toBe('gpt-4o-2024-11-20')
    expect(m.source).toBe('canonical')
  })

  it('★ 再退到上游实际模型串（raw_model）', () => {
    const actions = [
      act('arrive', 1, {}, 'gpt-4o'),
      act('upstream_request', 2, { raw_model: 'gpt-4o-2024-08-06' }),
    ]
    const m = resolveModelOf(req() as never, actions)
    expect(m.final).toBe('gpt-4o-2024-08-06')
    expect(m.source).toBe('upstream_raw')
    expect(m.upstream).toBe('gpt-4o-2024-08-06')
  })

  it('★ 非 auto 请求保留客户端原始模型名（用于「解析为」提示）', () => {
    const actions = [act('node_selected', 1, {}, 'gpt-4o')]
    const m = resolveModelOf(req({ client_model: 'gpt4-alias' }) as never, actions)
    expect(m.requested).toBe('gpt4-alias')
    expect(m.isAuto).toBe(false)
  })

  it('★ 模型切换链取 model_switch 的明细', () => {
    const actions = [
      act('node_selected', 1, {}, 'gpt-4o'),
      act('model_switch', 2, { from_model: 'gpt-4o', to_model: 'gpt-4o-mini' }, 'gpt-4o-mini'),
      act('node_selected', 3, {}, 'gpt-4o-mini'),
    ]
    const m = resolveModelOf(req() as never, actions)
    expect(m.switched).toEqual({ from: 'gpt-4o', to: 'gpt-4o-mini' })
    expect(m.final).toBe('gpt-4o-mini')
  })
})

describe('liveStageTree 失败定位', () => {
  it('★ 只有失败态才定位（在途/成功一律 null）', () => {
    expect(failureStageOf(req({ status: 'in_progress' }) as never, [])).toBeNull()
    expect(failureStageOf(req({ status: 'success' }) as never, [])).toBeNull()
  })

  it('★ 上游失败 ⇒ 供应商段；无路由 ⇒ 路由段；没进路由 ⇒ 鉴权段', () => {
    expect(
      failureStageOf(req({ status: 'failure', failure_stage: 'upstream' }) as never, [
        act('upstream_request', 1),
      ]),
    ).toBe('provider')
    expect(
      failureStageOf(req({ status: 'failure' }) as never, [act('no_route', 1)]),
    ).toBe('route')
    expect(failureStageOf(req({ status: 'failure' }) as never, [])).toBe('auth')
  })

  it('★ 审批拒绝 ⇒ 失败落在审批段', () => {
    expect(
      failureStageOf(req({ status: 'failure' }) as never, [
        act('approval_decided', 1, { decision: 'block' }),
      ]),
    ).toBe('approval')
  })
})

describe('liveStageTree 阶段状态推导', () => {
  it('★ 失败段自己红，下游一律「未到达」（不是「也失败」）', () => {
    const request = req({ status: 'failure', failure_stage: 'upstream' })
    const actions = [
      act('arrive', 1, { client_protocol: 'openai' }),
      act('route_resolved', 2, { candidates: '2' }, 'gpt-4o'),
      act('upstream_request', 3),
    ]
    const stages = deriveStages(request as never, actions, 'provider')
    const byKey = new Map(stages.map(s => [s.key, s]))
    expect(byKey.get('provider')?.state).toBe('failed')
    expect(byKey.get('first_byte')?.state).toBe('pending')
    expect(byKey.get('reply')?.state).toBe('pending')
  })

  it('★ 「未测量」与「未到达」是两个不同的态', () => {
    // 请求已经走到路由之后，但「脱敏」段没有任何信号 ⇒ 未测量（越过但没测到），
    // 而「首字节」在路由之后 ⇒ 未到达（还没跑到）。两者不可合并。
    const request = req({ status: 'in_progress' })
    const actions = [
      act('arrive', 1, { client_protocol: 'openai' }),
      act('route_resolved', 2, { candidates: '2' }, 'gpt-4o'),
    ]
    const stages = deriveStages(request as never, actions, null)
    const byKey = new Map(stages.map(s => [s.key, s]))
    expect(byKey.get('sanitize')?.state).toBe('unmeasured')
    expect(byKey.get('first_byte')?.state).toBe('pending')
    expect(byKey.get('sanitize')?.state).not.toBe(byKey.get('first_byte')?.state)
  })

  it('★ 审批 need_approval ⇒ 进行中（挂起等人工），不是「已完成」也不是「失败」', () => {
    const stages = deriveStages(req({ status: 'in_progress' }) as never, [
      act('arrive', 1),
      act('approval_decided', 2, { decision: 'need_approval', approval_id: 'ap-1' }),
    ], null)
    const approval = stages.find(s => s.key === 'approval')
    expect(approval?.state).toBe('active')
    expect(approval?.detail).toBe('ap-1')
  })

  it('★ 注入检测命中记「有告警」不记「失败」（v1 是 observe-only，不拦截）', () => {
    const stages = deriveStages(req({ status: 'success' }) as never, [
      act('arrive', 1),
      act('injection_scored', 2, { score: '0.91', decision: 'warn', dur_ms: '7' }),
    ], null)
    const injection = stages.find(s => s.key === 'injection')
    expect(injection?.state).toBe('warned')
    expect(injection?.durMs).toBe(7)
  })

  it('★ 无会话 ⇒ 压缩段「不适用」（会话压缩器块根本不进入），不是「未测量」', () => {
    const stages = deriveStages(req({ status: 'success' }) as never, [act('arrive', 1)], null)
    expect(stages.find(s => s.key === 'compress')?.state).toBe('skipped')
  })

  it('★ tools 还原挂在**压缩节点**上（徽标），不单列一列', () => {
    const stages = deriveStages(
      req({ status: 'success', gw_session_id: 's1' }) as never,
      [act('arrive', 1), act('tools_restored', 2, { restored: '1', tools_count: '3' })],
      null,
    )
    const compress = stages.find(s => s.key === 'compress')
    expect(compress?.state).toBe('done')
    expect(compress?.detail).toContain('tools:3')
    expect(compress?.detail).toContain('restored')
    // 还原那一列是**响应侧**的脱敏占位符回填，与 tools 还原不是同一件事。
    expect(stages.find(s => s.key === 'restore')?.state).not.toBe('done')
  })

  it('★ 输入侧没有脱敏 ⇒ 响应侧回填「不适用」；有脱敏但没打点 ⇒ 「未测量」', () => {
    const withSanitize0 = deriveStages(
      req({ status: 'success', gw_session_id: 's1' }) as never,
      [act('arrive', 1), act('sanitize_applied', 2, { mutations: '0' })],
      null,
    )
    expect(withSanitize0.find(s => s.key === 'restore')?.state).toBe('skipped')

    const withSanitizeN = deriveStages(
      req({ status: 'success', gw_session_id: 's1' }) as never,
      [act('arrive', 1), act('sanitize_applied', 2, { mutations: '4' })],
      null,
    )
    expect(withSanitizeN.find(s => s.key === 'restore')?.state).toBe('unmeasured')
  })

  it('★ 有会话的终态请求 ⇒ 压缩段「已完成」，缺省即未改写出站体', () => {
    const stages = deriveStages(
      req({ status: 'success', gw_session_id: 's1' }) as never,
      [act('arrive', 1)],
      null,
    )
    const compress = stages.find(s => s.key === 'compress')
    expect(compress?.state).toBe('done')
    expect(compress?.value).toBe('no')
  })

  it('★ 终态但没上报 token ⇒ 计量段「未测量」，不是 0', () => {
    const stages = deriveStages(req({ status: 'success' }) as never, [act('arrive', 1)], null)
    expect(stages.find(s => s.key === 'meter')?.state).toBe('unmeasured')
  })

  it('★ 鉴权段：arrive 动作证明已过鉴权（它在鉴权中间件之内侧发射）', () => {
    const stages = deriveStages(req({ status: 'success' }) as never, [
      act('arrive', 1, { client_protocol: 'openai', method: 'POST' }),
    ], null)
    const auth = stages.find(s => s.key === 'auth')
    expect(auth?.state).toBe('done')
    expect(auth?.value).toBe('openai')
  })
})

describe('liveStageTree 请求带', () => {
  it('★ auto 请求的入口显示 auto → 最终模型（不出现「请求 auto」）', () => {
    const band = deriveStageBand(
      req({
        status: 'success',
        is_auto_request: true,
        client_model: 'claude-sonnet-4-5',
        provider_code: 'deepseek',
        credential_label: 'cred-1',
      }) as never,
      [act('arrive', 1, {}, 'auto'), act('node_selected', 2, {}, 'claude-sonnet-4-5')],
    )
    expect(band.model.isAuto).toBe(true)
    expect(band.model.final).toBe('claude-sonnet-4-5')
    expect(band.model.requested).toBe('')
    expect(band.providerLabel).toBe('deepseek · cred-1')
  })

  it('★ 在途判定：in_progress / 无状态 = 在途', () => {
    expect(isInFlight(req({ status: 'in_progress' }) as never)).toBe(true)
    expect(isInFlight(req({ status: 'success' }) as never)).toBe(false)
    expect(isInFlight(req({ status: 'failure' }) as never)).toBe(false)
  })
})
