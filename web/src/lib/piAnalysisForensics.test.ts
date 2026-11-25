// ★ 2026-11-25 取证轮：PI 分析「取证 → 修正方案 → 复测」纯函数的钉测。
// 夹具对着三台生产的真实读数写（211 上那条被守卫拦断的请求、245 上的嵌套体例、
// 后端复测结论闭集）——每一类都要有负控：空值不出空行、未知枚举不伪装成高档。
import { describe, expect, it } from 'vitest'
import {
  BLOCKER_KEYS,
  asArray,
  buildPlanText,
  checkClass,
  countParseFailed,
  diagnosticsOf,
  fixPlanOf,
  isParseFailed,
  isPlanEmpty,
  modelOutputOf,
  normalizeKind,
  normalizeRisk,
  requestFacts,
  requestStatusClass,
  serviceLogMessage,
  serviceLogText,
  stepKindBadge,
  stepRiskClass,
  supportsJudgeOption,
  verdictClass,
  verdictOf,
} from './piAnalysisForensics'

// 211 实测的那一行：15.8 万 prompt tokens 的请求，7 秒后被输出守卫拦断。
const realRequest = {
  ts: '2026-11-25T00:56:28+08:00',
  request_status: 'failure',
  success: false,
  error_kind: 'output_policy_blocked',
  failure_stage: 'upstream',
  failure_detail_code: 'output_policy_blocked',
  upstream_status_code: 0,
  client_model: 'glm-5.3-flash',
  outbound_model: 'glm-5.3-flash',
  provider_id: 32,
  credential_id: 62,
  client_protocol: 'openai-responses',
  agent_name: 'minimax-code',
  latency_ms: 7063,
  prompt_tokens: 158787,
  completion_tokens: 0,
  total_tokens: 158787,
  stream_interrupted: false,
}

describe('requestFacts', () => {
  it('按「先看什么」的顺序给出有值的事实（211 真实行）', () => {
    const facts = requestFacts(realRequest)
    expect(facts.map((f) => f.key)).toEqual([
      'time', 'model', 'protocol', 'status', 'errorKind', 'failureStage', 'latency', 'tokens', 'provider', 'credential', 'agent',
    ])
    const by = Object.fromEntries(facts.map((f) => [f.key, f]))
    expect(by.model.value).toBe('glm-5.3-flash')
    expect(by.status.cls).toBe('status-error')
    expect(by.failureStage.value).toBe('upstream / output_policy_blocked')
    expect(by.latency.value).toBe('7.06 s')
    expect(by.tokens.raw).toEqual({ total: 158787, prompt: 158787, completion: 0 })
    expect(by.tokens.value).toBe('158,787')
  })

  it('空值与 0 状态码不出空行；未中断的流不出现「已中断」', () => {
    const keys = requestFacts(realRequest).map((f) => f.key)
    expect(keys).not.toContain('upstreamStatus')
    expect(keys).not.toContain('interrupted')
    expect(requestFacts(null)).toEqual([])
    expect(requestFacts({})).toEqual([])
  })

  it('出站模型与客户端模型不同时写成「客户端 → 出站」', () => {
    const [model] = requestFacts({ client_model: 'gpt-4o', outbound_model: 'gpt-4o-2024' })
    expect(model.value).toBe('gpt-4o → gpt-4o-2024')
  })

  it('路由逐次摘要作为第二行细节带出', () => {
    const facts = requestFacts({ routing_attempts: 2, routing_outcomes: ['#1 acme → ok ', '#2 beta → fail timeout'] })
    expect(facts.find((f) => f.key === 'routing')?.value).toBe('2')
    expect(facts.find((f) => f.key === 'routingOutcomes')?.lines).toHaveLength(2)
  })
})

describe('serviceLog helpers', () => {
  it('取规范 JSON 行里的 msg；取不到就给原行', () => {
    expect(serviceLogMessage('{"timestamp":"x","msg":"blocked by guard","a":1}')).toBe('blocked by guard')
    expect(serviceLogMessage('plain text line')).toBe('plain text line')
  })
  it('渲染成「时间 级别 内容」', () => {
    const text = serviceLogText([{ time: '2026-11-25T00:56:28.000Z', level: 'ERROR', line: '{"msg":"a"}' }])
    expect(text).toContain('ERROR')
    expect(text).toContain('a')
    expect(text.split('\n')).toHaveLength(1)
    expect(serviceLogText(null)).toBe('')
  })
})

describe('diagnostics / plan readers', () => {
  it('诊断读数：缺省给 null，截断/修复标志如实', () => {
    expect(diagnosticsOf(null)).toBeNull()
    expect(diagnosticsOf({ analysis_details: {} })).toBeNull()
    const d = diagnosticsOf({ analysis_details: { parse_diagnostics: { mode: 'salvaged', truncated: true, lost_keys: ['suggested_fix'] } } })
    expect(d).toEqual({ mode: 'salvaged', truncated: true, repaired: false, finish_reason: undefined, lost_keys: ['suggested_fix'] })
  })

  it('占位结论（旧解析器读不出来）要能被认出来', () => {
    expect(isParseFailed({ root_cause: 'llm_parse_failed' })).toBe(true)
    expect(isParseFailed({ root_cause: 'x' })).toBe(false)
    expect(isParseFailed(null)).toBe(false)
    expect(countParseFailed([{ root_cause: 'llm_parse_failed' }, { root_cause: 'y' }, { root_cause: 'llm_parse_failed' }])).toBe(2)
    expect(countParseFailed(null)).toBe(0)
  })

  it('修正方案与模型原始输出的读取', () => {
    const detail = { analysis_details: { fix_plan: { summary: 's', manual_steps: [{ step: 1, title: 'a', kind: 'ops', risk: 'low' }] }, model_output: { a: 1 } } }
    expect(fixPlanOf(detail)?.summary).toBe('s')
    expect(modelOutputOf(detail)).toEqual({ a: 1 })
    expect(fixPlanOf({})).toBeNull()
    expect(modelOutputOf({})).toBeNull()
    expect(isPlanEmpty(null)).toBe(true)
    expect(isPlanEmpty({})).toBe(true)
    expect(isPlanEmpty({ summary: 's' })).toBe(false)
  })
})

describe('enum normalisation', () => {
  it('未知的风险/类别不伪装成高档', () => {
    expect(normalizeRisk('critical')).toBe('low')
    expect(normalizeRisk('high')).toBe('high')
    expect(normalizeKind('部署')).toBe('ops')
    expect(normalizeKind('config')).toBe('config')
  })
  it('类别/风险 → 徽标类', () => {
    expect(stepKindBadge('code')).toBe('badge-error')
    expect(stepKindBadge('whatever')).toBe('badge-info')
    expect(stepRiskClass('high')).toBe('severity-critical')
    expect(stepRiskClass('')).toBe('severity-low')
  })
})

describe('verdict mapping', () => {
  it('检查项与结论映射到状态类；未知值不落成「通过」', () => {
    expect(checkClass('pass')).toBe('status-success')
    expect(checkClass('fail')).toBe('status-error')
    expect(checkClass('info')).toBe('badge-info')
    expect(checkClass('passs')).toBe('badge-info')
    expect(verdictClass('not_reproducing')).toBe('status-success')
    expect(verdictClass('reproducing')).toBe('status-error')
    expect(verdictClass('inconclusive')).toBe('status-pending')
    expect(verdictClass(null)).toBe('')
    expect(verdictOf({ verdict: 'reproducing' })).toBe('reproducing')
    expect(verdictOf({ verdict: 'fixed' })).toBeNull()
    expect(verdictOf(null)).toBeNull()
  })

  it('只有判官失败类才提供「调用一次判官」选项', () => {
    expect(supportsJudgeOption('llm_detection_failed')).toBe(true)
    expect(supportsJudgeOption('rule_false_positive')).toBe(false)
  })

  it('请求状态类', () => {
    expect(requestStatusClass('success')).toBe('status-success')
    expect(requestStatusClass('rate_limited')).toBe('status-pending')
    expect(requestStatusClass('')).toBe('')
  })

  it('后端闭集的阻断原因都有对应的文案键', () => {
    expect(Object.keys(BLOCKER_KEYS).sort()).toEqual(
      ['already_applied', 'analysis_unparsable', 'low_confidence', 'no_actions', 'view_only_type'],
    )
  })
})

describe('buildPlanText', () => {
  const ctx = {
    title: '修正方案 #7',
    labels: { planAutoTitle: '自动动作', planManualTitle: '人工步骤', planConfigTitle: '配置变更', planCodeTitle: '代码变更',
      planDeployTitle: '部署', planDeployRequired: '需要部署', planDeployNotRequired: '无需部署', planTargets: '目标',
      planRollback: '回滚', planVerifyTitle: '验证', verifyExpect: '期望', verifyMethod_layer_recheck: '复查出错层' },
    kinds: { ops: '运维', config: '配置', code: '代码' },
    risks: { low: '低风险', medium: '中风险', high: '高风险' },
    actions: [{ label: '禁用规则', target: 'dan_mode' }],
  }

  it('自动动作与人工步骤分开写，各小节按有无内容取舍', () => {
    const text = buildPlanText({
      summary: '放宽判官超时',
      manual_steps: [{ step: 1, title: '调大超时', detail: '到 60s', kind: 'config', risk: 'low' }, { step: 2, title: '补退避', kind: '乱填', risk: 'high' }],
      config_changes: [{ target: 'timeout', current: '30', proposed: '60' }],
      code_changes: [{ file: 'a.go', description: '改退避', patch: '+ b' }],
      deploy: { required: true, targets: ['245', '154'], steps: ['先预发布'], rollback: '回退镜像' },
      verification: { method: 'layer_recheck', expect: '复测通过', steps: ['跑一次复测'] },
    }, ctx)
    expect(text).toContain('# 修正方案 #7')
    expect(text).toContain('## 自动动作\n- [ ] 禁用规则 → dan_mode')
    expect(text).toContain('## 人工步骤\n1. [配置 · 低风险] 调大超时\n   到 60s\n2. [运维 · 高风险] 补退避')
    expect(text).toContain('timeout: 30 → 60')
    expect(text).toContain('```\n+ b\n```')
    expect(text).toContain('目标: 245, 154')
    expect(text).toContain('复查出错层')
  })

  it('没有的小节不出现；空方案只剩标题', () => {
    const text = buildPlanText({}, { ...ctx, actions: [] })
    expect(text).toBe('# 修正方案 #7')
  })
})

describe('asArray', () => {
  it('null/undefined 统一成空数组', () => {
    expect(asArray(null)).toEqual([])
    expect(asArray(undefined)).toEqual([])
    expect(asArray([1])).toEqual([1])
  })
})
