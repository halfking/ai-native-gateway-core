// liveStageTree.designParity.test.ts —— 请求阶段树「设计对齐 + 线上形状」判据（2026-11-17）
//
// ★ 本文件钉的是**本轮修掉的三个真实缺陷**（全部来自 154 线上取证）：
//   ① **动作明细读错位置**：线上 SSE 把 detail **摊平到顶层**
//      （admin/live_stream_lifecycle.go 的 flattenActionEvent），
//      而实现只读 `action.detail` ⇒ 生产线上每个阶段的取值都是空
//      （审批拒绝判不出失败 / 注入命中判不出告警 / 最终模型退化成原始名）。
//   ② **后端 idle_marker 心跳被当成请求渲染** ⇒ 线上 177 行里绝大多数是
//      `no_traffic_5min` 的假行，真实请求被埋在噪声里。
//   ③ **裸 i18n 键上屏**：`t('liveStageTree.stage.' + s.key)` 对 first_byte
//      取到 `liveStageTree.stage.first_byte`（locale 里是 firstByte）⇒
//      界面上直接显示 `liveStageTree.stage.first_by…`。
//
// ★ 判据纪律：每条都必须满足「**把被测逻辑改回缺陷形态，它立刻变红**」。

import { describe, expect, it } from 'vitest'
import {
  actionDetail,
  cacheRateOf,
  deriveStageBand,
  deriveStages,
  errorSourceOf,
  failureStageOf,
  isIdleMarker,
  resolveModelOf,
} from './liveStageTree'
import type { ActionEvent } from '../composables/liveStreamStore'
import type { LiveRequest } from '../composables/liveStreamStore'

/** 造一条**线上 SSE 真形状**的动作：明细**摊平在顶层**，没有 detail 对象。 */
function flatAct(action: string, seq: number, flat: Record<string, string> = {}, model?: string): ActionEvent {
  return { request_id: 'r1', seq, action, model, ...flat } as unknown as ActionEvent
}

/** 造一条**REST 回退形状**的动作：明细在 detail 里。 */
function nestedAct(action: string, seq: number, detail: Record<string, string> = {}, model?: string): ActionEvent {
  return { request_id: 'r1', seq, action, detail, model }
}

function req(over: Record<string, unknown> = {}): LiveRequest {
  return {
    ts: new Date().toISOString(),
    request_id: 'r1',
    status: 'success',
    ...over,
  } as unknown as LiveRequest
}

describe('动作明细：两种线形状都要吃（线上 SSE 是摊平的）', () => {
  it('★ 顶层平铺（SSE 形状）与嵌套 detail（REST 形状）读出来一样', () => {
    const flat = actionDetail(flatAct('approval_decided', 1, { decision: 'block', dur_ms: '12' }))
    const nested = actionDetail(nestedAct('approval_decided', 1, { decision: 'block', dur_ms: '12' }))
    expect(flat.decision).toBe('block')
    expect(flat.dur_ms).toBe('12')
    expect(nested.decision).toBe('block')
    expect(nested.dur_ms).toBe('12')
  })

  it('★ 嵌套 detail 优先于同名顶层字段（REST 形状不被顶层覆盖）', () => {
    const a = actionDetail({
      request_id: 'r1',
      seq: 1,
      action: 'reply',
      status: 'flat-status',
      detail: { status: 'nested-status' },
    } as unknown as ActionEvent)
    expect(a.status).toBe('nested-status')
  })

  it('★ 线上形状：审批拒绝 ⇒ 失败落在审批段（摊平形状也判得出）', () => {
    const stages = deriveStages(
      req({ status: 'failure' }),
      [
        flatAct('arrive', 1, { client_protocol: 'openai' }),
        flatAct('approval_decided', 2, { decision: 'block' }),
      ],
      failureStageOf(req({ status: 'failure' }), [
        flatAct('arrive', 1, { client_protocol: 'openai' }),
        flatAct('approval_decided', 2, { decision: 'block' }),
      ]),
    )
    const approval = stages.find(s => s.key === 'approval')!
    expect(approval.state).toBe('failed')
    expect(approval.value).toBe('block')
  })

  it('★ 线上形状：注入命中 ⇒ 告警（不是失败），且得分有值', () => {
    const stages = deriveStages(req(), [
      flatAct('arrive', 1, { client_protocol: 'openai' }),
      flatAct('injection_scored', 2, { decision: 'warn', score: '0.87' }),
    ], null)
    const injection = stages.find(s => s.key === 'injection')!
    expect(injection.state).toBe('warned')
    expect(injection.value).toBe('0.87')
  })

  it('★ 线上形状：脱敏命中数 / 路由候选数 / 首字节耗时都要有值', () => {
    const stages = deriveStages(req(), [
      flatAct('arrive', 1, { client_protocol: 'openai' }),
      flatAct('sanitize_applied', 2, { mutations: '3', dur_ms: '4' }),
      flatAct('route_resolved', 3, { candidates: '5', auto_task_type: 'code' }),
      flatAct('first_byte', 4, { ttfb_ms: '820' }),
    ], null)
    expect(stages.find(s => s.key === 'sanitize')!.value).toBe('3')
    expect(stages.find(s => s.key === 'first_byte')!.value).toBe('820')
  })

  it('★ 路由格＝**最终匹配模型**（设计：路由才是「决定用哪个模型」的那一步），候选数走独立字段', () => {
    // ★ 口径来源：设计原型 bandInner 注释「路由节点的 v = 最终匹配模型（canonical）」。
    //   ⇒ 候选数不挤占主值（改走 stage.candidates ⇒ tooltip / 摘要 chip / 紧凑瓦片小数字）。
    const actions = [
      flatAct('arrive', 1, { client_protocol: 'openai' }),
      flatAct('route_resolved', 2, { candidates: '5', auto_task_type: 'code' }),
      flatAct('node_selected', 3, {}, 'gpt-4o'),
    ]
    const stages = deriveStages(req(), actions, null)
    const route = stages.find(s => s.key === 'route')!
    expect(route.value).toBe('gpt-4o')
    expect(route.candidates).toBe(5)
    expect(route.detail).toBe('auto:code')
  })

  it('★ 供应商格＝**供应商 · 凭据**（不是模型名；模型归路由格）', () => {
    const stages = deriveStages(
      { ...req(), provider_code: 'suyun', credential_label: 'kiro' },
      [
        flatAct('arrive', 1, { client_protocol: 'openai' }),
        flatAct('credential_selected', 2, { tier: '2' }, 'gpt-4o'),
        flatAct('node_selected', 3, { attempt: '1' }, 'gpt-4o'),
      ],
      null,
    )
    const provider = stages.find(s => s.key === 'provider')!
    expect(provider.value).toBe('suyun · kiro')
    // 模型名降级到 detail（tooltip 里可核对），不与路由格重复。
    expect(provider.detail).toContain('model=gpt-4o')
  })

  it('★ 线上形状：最终匹配模型取 node_selected 的 model，上游模型取 raw_model', () => {
    const model = resolveModelOf(req(), [
      flatAct('route_resolved', 1, { candidates: '2' }, 'auto'),
      flatAct('node_selected', 2, {}, 'claude-sonnet-5-5'),
      flatAct('upstream_request', 3, { raw_model: 'claude-sonnet-5-5-20260101' }),
    ])
    expect(model.final).toBe('claude-sonnet-5-5')
    expect(model.source).toBe('action')
    expect(model.upstream).toBe('claude-sonnet-5-5-20260101')
  })
})

describe('后端 idle_marker 心跳不是请求', () => {
  it('★ idle_marker 帧一律判成「不是请求」', () => {
    expect(isIdleMarker(req({ type: 'idle_marker', status: 'idle' }))).toBe(true)
    expect(isIdleMarker(req({ status: 'idle' }))).toBe(true)
  })

  it('★ 真实请求不算空闲标记', () => {
    expect(isIdleMarker(req({ status: 'success' }))).toBe(false)
    expect(isIdleMarker(req({ status: 'in_progress' }))).toBe(false)
  })

  it('★ 心跳帧推出来的带：没有任何一段「跑过」（这正是线上那 177 行的形态）', () => {
    const band = deriveStageBand(req({ type: 'idle_marker', status: 'idle', error_kind: 'no_traffic_5min' }), [])
    // 心跳不是请求 ⇒ 没有任何一段能是「已完成 / 进行中 / 失败 / 告警」；
    // 只可能是「未到达 / 未测量 / 不适用」这三种灰态。
    expect(band.stages.every(s => ['pending', 'unmeasured', 'skipped'].includes(s.state))).toBe(true)
    expect(band.clientName).toBe('—')
    expect(band.errorKind).toBe('no_traffic_5min')
  })
})

describe('错误来源归因（owner：最右边显示错误源头及类型）', () => {
  it('★ failure_stage=upstream ⇒ 供应商', () => {
    expect(errorSourceOf(req({ failure_stage: 'upstream' }), 'upstream_timeout')).toBe('provider')
  })

  it('★ 上游族错误码 ⇒ 供应商（哪怕帧没写 failure_stage）', () => {
    expect(errorSourceOf(req(), 'upstream_rate_limited')).toBe('provider')
    expect(errorSourceOf(req(), 'eof_without_done')).toBe('provider')
  })

  it('★ 网关侧错误码 ⇒ 网关', () => {
    expect(errorSourceOf(req(), 'rate_limit_exceeded')).toBe('gateway')
    expect(errorSourceOf(req({ failure_stage: 'gateway' }), 'prompt_injection_blocked')).toBe('gateway')
  })

  it('★ 没有错误码 ⇒ 没有来源（不硬塞一个）', () => {
    expect(errorSourceOf(req(), '')).toBe('')
  })
})

describe('带级派生字段（设计第 5/8 轮要的那些数）', () => {
  it('★ 排队时长 = 模型入队 → 选中节点', () => {
    const t0 = Date.parse('2026-11-17T10:00:00Z')
    const band = deriveStageBand(req(), [
      { request_id: 'r1', seq: 1, action: 'model_enqueued', ts: new Date(t0).toISOString() },
      { request_id: 'r1', seq: 2, action: 'node_selected', ts: new Date(t0 + 1490).toISOString(), model: 'm' },
    ])
    expect(band.queueMs).toBe(1490)
  })

  it('★ 出站尝试次数 = upstream_request 动作条数', () => {
    const band = deriveStageBand(req(), [
      flatAct('upstream_request', 1, { attempt: '1' }),
      flatAct('upstream_request', 2, { attempt: '2' }),
    ])
    expect(band.attempts).toBe(2)
  })

  it('★ 缓存口径仍按上游协议判（不会被本轮改动带偏）', () => {
    const a = cacheRateOf(req({ upstream_protocol: 'openai-completions', prompt_tokens: 100, cache_read_tokens: 25 }))
    expect(a?.pct).toBe(25)
    const b = cacheRateOf(req({ upstream_protocol: 'anthropic-messages', prompt_tokens: 100, cache_read_tokens: 100 }))
    expect(b?.pct).toBe(50)
  })
})
