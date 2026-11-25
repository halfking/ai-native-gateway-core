// PI 分析「取证 → 修正方案 → 复测」的纯函数（2026-11-25 取证轮）。
//
// ★ 为什么单独成库：这三块的分支很多（哪些字段有值才展示、结论怎么映射到
//   徽标、方案文本怎么拼），堆在 1500 行的视图里没法钉测。这里只做
//   「数据 → 展示结构」的转换，不发请求、不碰 i18n（文案由调用方按 key 取）。

export type PiAnyRecord = Record<string, any>

/** 请求账本事实的键闭集（调用方用它取 i18n 标签）。 */
export type PiRequestFactKey =
  | 'time' | 'model' | 'protocol' | 'status' | 'errorKind' | 'failureStage'
  | 'upstreamStatus' | 'latency' | 'tokens' | 'provider' | 'credential' | 'agent'
  | 'routing' | 'interrupted' | 'routingOutcomes'

export interface PiRequestFact {
  key: PiRequestFactKey
  value: string
  /** 可选的样式类（复用视图里已有的状态类）。 */
  cls?: string
  /** 第二行细节（路由逐次摘要）。 */
  lines?: string[]
  /** 原始数字（调用方按语种拼「x in / y out」这类带单位的文案）。 */
  raw?: Record<string, number>
}

export interface PiParseDiagnostics {
  mode: string
  truncated: boolean
  repaired: boolean
  finish_reason?: string
  lost_keys: string[]
}

export interface PiFixStep {
  step: number
  title: string
  detail?: string
  kind: string
  risk: string
}

export interface PiFixPlan {
  summary?: string
  auto_actions?: PiAnyRecord[]
  manual_steps?: PiFixStep[]
  config_changes?: Array<{ target: string; current?: string; proposed: string }>
  code_changes?: Array<{ file?: string; description: string; patch?: string }>
  deploy?: { required: boolean; targets?: string[]; steps?: string[]; rollback?: string }
  verification?: { method?: string; expect?: string; steps?: string[] }
}

export type PiVerdict = 'not_reproducing' | 'reproducing' | 'inconclusive'

/** 空值统一成空数组（后端保证是数组；这里防 null/undefined 渗进模板）。 */
export function asArray<T>(v: T[] | null | undefined): T[] {
  return Array.isArray(v) ? v : []
}

function nonEmpty(v: unknown): boolean {
  return v !== null && v !== undefined && v !== ''
}

function fmtMs(ms: number): string {
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)} s`
  return `${ms} ms`
}

function fmtNum(n: number): string {
  return n.toLocaleString('en-US')
}

/** 请求状态 → 视图里已有的状态类。 */
export function requestStatusClass(status: string): string {
  switch (status) {
    case 'success': return 'status-success'
    case 'failure': return 'status-error'
    case 'rate_limited': return 'status-pending'
    case 'in_progress': return 'status-processing'
    default: return ''
  }
}

/**
 * 请求账本行 → 稳定顺序的展示事实。只保留有值的字段（空值不出空行）。
 *
 * ★ 顺序即「读的人先看什么」：什么模型、什么协议、结果如何、失败在哪一步、
 *   花了多久、多大，然后才是供应商/凭据/客户端。
 */
export function requestFacts(req: PiAnyRecord | null | undefined): PiRequestFact[] {
  if (!req || typeof req !== 'object') return []
  const facts: PiRequestFact[] = []
  const push = (key: PiRequestFactKey, value: unknown, cls?: string, lines?: string[]) => {
    if (nonEmpty(value)) facts.push({ key, value: String(value), cls, lines })
  }
  push('time', req.ts)
  if (nonEmpty(req.outbound_model) && nonEmpty(req.client_model) && req.outbound_model !== req.client_model) {
    // 出站模型与客户端模型不同（协议转换/重写时才会不同）⇒ 「客户端 → 出站」。
    push('model', `${req.client_model} → ${req.outbound_model}`)
  } else {
    push('model', req.client_model || req.outbound_model || req.canonical_model)
  }
  push('protocol', req.client_protocol)
  push('status', req.request_status, requestStatusClass(req.request_status))
  push('errorKind', req.error_kind)
  const stage = req.failure_stage
    ? String(req.failure_stage) + (nonEmpty(req.failure_detail_code) && req.failure_detail_code !== req.failure_stage ? ` / ${req.failure_detail_code}` : '')
    : req.failure_detail_code
  push('failureStage', stage)
  if (nonEmpty(req.upstream_status_code) && req.upstream_status_code !== 0) {
    push('upstreamStatus', req.upstream_status_code)
  }
  push('latency', typeof req.latency_ms === 'number' ? fmtMs(req.latency_ms) : req.latency_ms)
  if (typeof req.total_tokens === 'number') {
    const completion = typeof req.completion_tokens === 'number' ? req.completion_tokens : 0
    facts.push({
      key: 'tokens',
      value: fmtNum(req.total_tokens),
      raw: { total: req.total_tokens, prompt: typeof req.prompt_tokens === 'number' ? req.prompt_tokens : 0, completion },
    })
  }
  push('provider', req.provider_id)
  push('credential', req.credential_id)
  push('agent', req.agent_name)
  if (req.stream_interrupted === true) push('interrupted', 'true')
  if (typeof req.routing_attempts === 'number' && req.routing_attempts >= 0) {
    push('routing', req.routing_attempts)
  }
  const outcomes = asArray<string>(req.routing_outcomes)
  if (outcomes.length > 0) facts.push({ key: 'routingOutcomes', value: '', lines: outcomes })
  return facts
}

/** 从一行规范 JSON 日志里取 msg（取不到 ⇒ 原行）。 */
export function serviceLogMessage(line: string): string {
  const needle = '"msg":"'
  const i = line.indexOf(needle)
  if (i < 0) return line
  const rest = line.slice(i + needle.length)
  const end = rest.indexOf('"')
  return end > 0 ? rest.slice(0, end) : rest
}

/** 服务日志行 → 「时间 级别 内容」文本（给 <pre> 用）。 */
export function serviceLogText(lines: Array<{ time?: string; level?: string; line?: string }> | null | undefined): string {
  return asArray(lines).map((l) => {
    const t = l.time ? new Date(l.time) : null
    const hhmmss = t && !Number.isNaN(t.getTime()) ? t.toTimeString().slice(0, 8) : '--:--:--'
    return `${hhmmss} ${String(l.level || '').padEnd(5)} ${serviceLogMessage(String(l.line || ''))}`
  }).join('\n')
}

/** 详情里的解析诊断（没有 ⇒ null）。 */
export function diagnosticsOf(detail: PiAnyRecord | null | undefined): PiParseDiagnostics | null {
  const raw = detail?.analysis_details?.parse_diagnostics
  if (!raw || typeof raw !== 'object') return null
  return {
    mode: String(raw.mode || ''),
    truncated: raw.truncated === true,
    repaired: raw.repaired === true,
    finish_reason: typeof raw.finish_reason === 'string' ? raw.finish_reason : undefined,
    lost_keys: asArray<string>(raw.lost_keys),
  }
}

/** 详情里的修正方案（没有 ⇒ null）。 */
export function fixPlanOf(detail: PiAnyRecord | null | undefined): PiFixPlan | null {
  const raw = detail?.analysis_details?.fix_plan
  if (!raw || typeof raw !== 'object') return null
  return raw as PiFixPlan
}

/** 详情里模型给的分析输出（结构化留档）。 */
export function modelOutputOf(detail: PiAnyRecord | null | undefined): PiAnyRecord | null {
  const raw = detail?.analysis_details?.model_output
  return raw && typeof raw === 'object' ? raw : null
}

/** 这条结果是不是「旧解析器读不出来」的占位结论。 */
export function isParseFailed(result: PiAnyRecord | null | undefined): boolean {
  return !!result && result.root_cause === 'llm_parse_failed'
}

/** 结果列表里「解析失败」的条数（批量重解析入口的读数）。 */
export function countParseFailed(results: PiAnyRecord[] | null | undefined): number {
  return asArray(results).filter((r) => isParseFailed(r)).length
}

/** 修正方案是否为空（一个可展示的子块都没有）。 */
export function isPlanEmpty(plan: PiFixPlan | null): boolean {
  if (!plan) return true
  return !plan.summary && asArray(plan.auto_actions).length === 0
    && asArray(plan.manual_steps).length === 0 && asArray(plan.config_changes).length === 0
    && asArray(plan.code_changes).length === 0 && !plan.deploy
}

/** 步骤类别闭集 → 视图徽标类。 */
export function stepKindBadge(kind: string): string {
  switch (kind) {
    case 'config': return 'badge-warning'
    case 'code': return 'badge-error'
    case 'upstream': return 'badge-info'
    case 'client': return 'badge-success'
    default: return 'badge-info'
  }
}

export function stepRiskClass(risk: string): string {
  switch (risk) {
    case 'high': return 'severity-critical'
    case 'medium': return 'severity-medium'
    default: return 'severity-low'
  }
}

/** 风险词归一（模型偶尔给闭集外的值 ⇒ 按低风险展示，不伪装成高/中）。 */
export function normalizeRisk(risk: string): 'low' | 'medium' | 'high' {
  return risk === 'high' || risk === 'medium' ? risk : 'low'
}

export function normalizeKind(kind: string): string {
  return ['config', 'code', 'ops', 'upstream', 'client'].includes(kind) ? kind : 'ops'
}

/** 后端算出的「为什么不能自动修正」→ i18n key（在 forensics 命名空间下）。 */
export const BLOCKER_KEYS: Record<string, string> = {
  view_only_type: 'blockerViewOnlyType',
  analysis_unparsable: 'blockerUnparsable',
  no_actions: 'blockerNoActions',
  low_confidence: 'blockerLowConfidence',
  already_applied: 'blockerAlreadyApplied',
}

/** 复测检查项状态 → 视图状态类。 */
export function checkClass(status: string): string {
  switch (status) {
    case 'pass': return 'status-success'
    case 'fail': return 'status-error'
    case 'skipped': return 'status-pending'
    case 'not_applicable': return 'status-skipped'
    case 'error': return 'status-error'
    default: return 'badge-info'
  }
}

/** 复测结论 → 视图状态类。 */
export function verdictClass(verdict: string | null | undefined): string {
  switch (verdict) {
    case 'not_reproducing': return 'status-success'
    case 'reproducing': return 'status-error'
    case 'inconclusive': return 'status-pending'
    default: return ''
  }
}

/** 从一条复测留痕里取结论（没有 ⇒ null）。 */
export function verdictOf(last: PiAnyRecord | null | undefined): PiVerdict | null {
  const v = last?.verdict
  return v === 'not_reproducing' || v === 'reproducing' || v === 'inconclusive' ? v : null
}

/** 这类错误是否提供「调用一次判官」的选项（只有判官失败类才有意义）。 */
export function supportsJudgeOption(errorType: string): boolean {
  return errorType === 'llm_detection_failed'
}

/**
 * 把修正方案拼成给**人**看的文本（复制后可直接贴进工单/交接文档）。
 *
 * ★ 自动动作与人工步骤分开写：前者系统已能执行，后者要人落地——
 *   读的人要一眼分清「哪些已经做了」和「哪些等我做」。
 */
export function buildPlanText(
  plan: PiFixPlan,
  ctx: {
    title: string
    labels: Record<string, string>
    kinds: Record<string, string>
    risks: Record<string, string>
    actions: Array<{ label: string; target: string; detail?: string }>
  },
): string {
  const L = (k: string) => ctx.labels[k] ?? k
  const lines: string[] = []
  lines.push(`# ${ctx.title}`)
  if (plan.summary) lines.push('', plan.summary)

  const auto = asArray(ctx.actions)
  if (auto.length > 0) {
    lines.push('', `## ${L('planAutoTitle')}`)
    for (const a of auto) {
      lines.push(`- [ ] ${a.label} → ${a.target}${a.detail ? `（${a.detail}）` : ''}`)
    }
  }
  const steps = asArray(plan.manual_steps)
  if (steps.length > 0) {
    lines.push('', `## ${L('planManualTitle')}`)
    for (const s of steps) {
      const kind = ctx.kinds[normalizeKind(s.kind)] ?? s.kind
      const risk = ctx.risks[normalizeRisk(s.risk)] ?? s.risk
      lines.push(`${s.step}. [${kind} · ${risk}] ${s.title}${s.detail ? `\n   ${s.detail}` : ''}`)
    }
  }
  const cfg = asArray(plan.config_changes)
  if (cfg.length > 0) {
    lines.push('', `## ${L('planConfigTitle')}`)
    for (const c of cfg) {
      lines.push(`- ${c.target}: ${c.current ?? '-'} → ${c.proposed}`)
    }
  }
  const code = asArray(plan.code_changes)
  if (code.length > 0) {
    lines.push('', `## ${L('planCodeTitle')}`)
    for (const c of code) {
      lines.push(`- ${c.file ?? ''} ${c.description}`.trim())
      if (c.patch) lines.push('```', c.patch, '```')
    }
  }
  if (plan.deploy) {
    lines.push('', `## ${L('planDeployTitle')}`)
    lines.push(`- ${plan.deploy.required ? L('planDeployRequired') : L('planDeployNotRequired')}`)
    if (asArray(plan.deploy.targets).length > 0) lines.push(`- ${L('planTargets')}: ${plan.deploy.targets!.join(', ')}`)
    for (const s of asArray(plan.deploy.steps)) lines.push(`- ${s}`)
    if (plan.deploy.rollback) lines.push(`- ${L('planRollback')}: ${plan.deploy.rollback}`)
  }
  if (plan.verification) {
    lines.push('', `## ${L('planVerifyTitle')}`)
    if (plan.verification.method) lines.push(`- ${ctx.labels[`verifyMethod_${plan.verification.method}`] ?? plan.verification.method}`)
    if (plan.verification.expect) lines.push(`- ${L('verifyExpect')}: ${plan.verification.expect}`)
    for (const s of asArray(plan.verification.steps)) lines.push(`- ${s}`)
  }
  return lines.join('\n')
}

/** 规则调整动作的展示参数（severity / pattern）→ 文本。 */
export function autoActionDetail(action: PiAnyRecord): string {
  if (action.action === 'adjust_severity' && action.severity !== undefined) return `severity = ${action.severity}`
  if (action.action === 'update_pattern' && action.pattern) return action.pattern
  return ''
}
