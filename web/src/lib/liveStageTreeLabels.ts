// liveStageTreeLabels.ts —— 请求阶段树的**文案映射单一真源**（2026-11-18）
//
// ★ 为什么单独一个文件：
//   阶段名 / 状态名 / 错误码释义 / 客户端类型 / 模型来源这几组映射，
//   **请求带**与**请求详情面板**都要用。
//   ⇒ 若两边各写一份 switch（像本仓上一轮「同一条规定写了两份 ⇒ 必然漂移」
//     那个 F1 缺陷一样），就会漂：新增一个错误码时带里显示了、详情里没有。
//   ⇒ 这里做**一份**，两个组件都从这儿取。
//
// ★ 纪律（沿用请求带那套）：
//   · 逐字面量 t()：动态键是引用扫描器的盲区，写错键没有任何门会报；
//   · 未知错误码回落**原始码**，不编；
//   · 未知客户端类型回落**原始值**，不编。

import type {
  ErrorSource,
  ModelSource,
  StageBand,
  StageKey,
  StageNode,
  StageState,
} from '../types/liveStageTree'

/** 与 vue-i18n 的 t() 同形（named 参数可选）。 */
export type Translate = (key: string, named?: Record<string, unknown>) => string

/** 阶段名（发送轨 + 接收轨）。 */
export function stageLabel(t: Translate, key: StageKey): string {
  switch (key) {
    case 'auth':
      return t('liveStageTree.stage.auth')
    case 'sanitize':
      return t('liveStageTree.stage.sanitize')
    case 'approval':
      return t('liveStageTree.stage.approval')
    case 'injection':
      return t('liveStageTree.stage.injection')
    case 'route':
      return t('liveStageTree.stage.route')
    case 'compress':
      return t('liveStageTree.stage.compress')
    case 'provider':
      return t('liveStageTree.stage.provider')
    case 'first_byte':
      return t('liveStageTree.stage.firstByte')
    case 'restore':
      return t('liveStageTree.stage.restore')
    case 'meter':
      return t('liveStageTree.stage.meter')
    case 'reply':
      return t('liveStageTree.stage.reply')
    default:
      return ''
  }
}

/** 阶段状态名（七态，不可合并）。 */
export function stageStateLabel(t: Translate, state: string): string {
  switch (state) {
    case 'done':
      return t('liveStageTree.state.done')
    case 'active':
      return t('liveStageTree.state.active')
    case 'failed':
      return t('liveStageTree.state.failed')
    case 'warned':
      return t('liveStageTree.state.warned')
    case 'unmeasured':
      return t('liveStageTree.state.unmeasured')
    case 'skipped':
      return t('liveStageTree.state.skipped')
    case 'pending':
      return t('liveStageTree.state.pending')
    default:
      return ''
  }
}

/** 错误码的人话释义（★ 逐字面量 t()；未知错误码回落原始码，不编）。 */
export function errorKindLabel(t: Translate, kind: string): string {
  switch (kind) {
    case 'upstream_timeout':
      return t('liveStageTree.error.kind.upstream_timeout')
    case 'upstream_rate_limited':
      return t('liveStageTree.error.kind.upstream_rate_limited')
    case 'upstream_quota_permanent':
      return t('liveStageTree.error.kind.upstream_quota_permanent')
    case 'upstream_error':
      return t('liveStageTree.error.kind.upstream_error')
    case 'upstream_connection_error':
      return t('liveStageTree.error.kind.upstream_connection_error')
    case 'upstream_status_5xx':
      return t('liveStageTree.error.kind.upstream_status_5xx')
    case 'eof_without_done':
      return t('liveStageTree.error.kind.eof_without_done')
    case 'probe_timeout':
      return t('liveStageTree.error.kind.probe_timeout')
    case 'rate_limit':
      return t('liveStageTree.error.kind.rate_limit')
    case 'rate_limit_exceeded':
      return t('liveStageTree.error.kind.rate_limit_exceeded')
    case 'key_throttled':
      return t('liveStageTree.error.kind.key_throttled')
    case 'no_route':
      return t('liveStageTree.error.kind.no_route')
    case 'model_not_found':
      return t('liveStageTree.error.kind.model_not_found')
    case 'client_cancel':
      return t('liveStageTree.error.kind.client_cancel')
    case 'prompt_injection_blocked':
      return t('liveStageTree.error.kind.prompt_injection_blocked')
    default:
      return kind
  }
}

/** 错误来源的人话名（网关 / 供应商 / 其它）。 */
export function errorSourceLabel(t: Translate, source: ErrorSource): string {
  if (source === 'gateway') return t('liveStageTree.error.source.gateway')
  if (source === 'provider') return t('liveStageTree.error.source.provider')
  if (source === 'other') return t('liveStageTree.error.source.other')
  return ''
}

/** 错误来源的徽标名（GW 网关 / UP 供应商 / 其它）。 */
export function errorTagLabel(t: Translate, source: ErrorSource): string {
  if (source === 'gateway') return t('liveStageTree.error.tag.gateway')
  if (source === 'provider') return t('liveStageTree.error.tag.provider')
  if (source === 'other') return t('liveStageTree.error.tag.other')
  return ''
}

/** 客户端类型的人话标签（未知类型回落原始值，不编）。 */
export function clientTypeLabel(t: Translate, clientType: string): string {
  switch (clientType) {
    case 'coding_agent':
      return t('liveStageTree.clientType.coding_agent')
    case 'sdk':
      return t('liveStageTree.clientType.sdk')
    case 'cli':
      return t('liveStageTree.clientType.cli')
    case 'app':
      return t('liveStageTree.clientType.app')
    case 'automation':
      return t('liveStageTree.clientType.automation')
    case 'bot':
      return t('liveStageTree.clientType.bot')
    case 'api':
      return t('liveStageTree.clientType.api')
    case 'web':
      return t('liveStageTree.clientType.web')
    default:
      return clientType || '—'
  }
}

/** 最终匹配模型的取值来源说明（如实标注，不假装是「路由决定」）。 */
export function modelSourceLabel(t: Translate, source: ModelSource): string {
  switch (source) {
    case 'action':
      return t('liveStageTree.band.finalModelTip.action')
    case 'canonical':
      return t('liveStageTree.band.finalModelTip.canonical')
    case 'upstream_raw':
      return t('liveStageTree.band.finalModelTip.upstreamRaw')
    case 'frame':
      return t('liveStageTree.band.finalModelTip.frame')
    case 'none':
      return t('liveStageTree.band.finalModelTip.none')
    default:
      return ''
  }
}

/** 请求状态徽标文案（success / in_progress / failure）。 */
export function bandStatusLabel(t: Translate, status: string): string {
  switch (status) {
    case 'success':
      return t('liveStageTree.band.status.success')
    case 'in_progress':
      return t('liveStageTree.band.status.in_progress')
    case 'failure':
      return t('liveStageTree.band.status.failure')
    default:
      return status
  }
}

/**
 * 请求状态（失败 > 在途 > 成功）—— 请求带与请求详情面板共用一份。
 * ★ 失败优先于在途：带里已经标了红，就不该再显示「进行中」。
 */
export function bandStatus(band: Pick<StageBand, 'failedStage' | 'inFlight'>): string {
  if (band.failedStage) return 'failure'
  if (band.inFlight) return 'in_progress'
  return 'success'
}

/** 阶段值的中文化（单位随阶段而变；空值不渲染 —— 状态字形已经说明了）。 */
export function stageValueLabel(t: Translate, s: StageNode): string {
  if (!s.value) return ''
  switch (s.key) {
    case 'sanitize':
      return t('liveStageTree.value.sanitize', { n: s.value })
    case 'route':
      // ★ 路由格的主值＝**最终匹配模型**（设计：路由才是「决定用哪个模型」的那一步）。
      return s.value
    case 'first_byte':
      // ★ 单位「ms」是技术单位，不是可翻译文案 ⇒ 直接内联，不占一个 i18n 键。
      return `${s.value}ms`
    case 'meter':
      // ★ 单位「tok」同样是技术单位缩写 ⇒ 直接内联。
      return `${s.value} tok`
    case 'compress':
      return s.value === 'yes'
        ? t('liveStageTree.value.compress.yes')
        : t('liveStageTree.value.compress.no')
    case 'approval':
      if (s.value === 'pass') return t('liveStageTree.decision.pass')
      if (s.value === 'need_approval') return t('liveStageTree.decision.need_approval')
      if (s.value === 'block') return t('liveStageTree.decision.block')
      return s.value
    case 'injection':
      // ★ 节点上显示**判定词**（短），得分与完整措辞放 tooltip。
      if (s.detail === 'safe') return t('liveStageTree.decision.safe')
      if (s.detail === 'warn' || s.detail === 'block') return t('liveStageTree.decision.warnShort')
      return t('liveStageTree.decision.unknown')
    default:
      return s.value
  }
}

/** 阶段补充说明（压缩段的 tools 回填带译文；其余原样）。 */
export function stageDetailLabel(t: Translate, s: StageNode): string {
  if (s.key !== 'compress') return s.detail
  return s.detail
    .split(' · ')
    .filter(Boolean)
    .map(part => {
      if (part.startsWith('tools:')) {
        return t('liveStageTree.band.toolsRestore', { n: part.slice('tools:'.length) })
      }
      if (part === 'restored') return t('liveStageTree.band.toolsRestored')
      return part
    })
    .join(' · ')
}
