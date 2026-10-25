import type { RequestJourneyStage } from '../api/request-journeys'
import type { WaterfallStageKey } from './waterfallTimeline'

export interface JourneyStageMapping {
  journeyStage: RequestJourneyStage
  waterfall: WaterfallStageKey | null
  traceStages: readonly string[]
  /**
   * ★ journey stage 的**标识名**（`received` / `routing` / …）。
   * ⚠️ **它不是 i18n 键** —— 真键是 `requestDetail.flow.diagram.stages.<stage>`
   *   （消费方 `RequestProcessingFlowDiagram.vue` 负责拼前缀）。
   * ★ 2026-10-25 改名自 `labelKey`：原名把它伪装成 i18n 键 ⇒ 键扫描器
   *   （`i18n/scanner.ts` 的 `labelKey|titleKey|hintKey` 白名单）把它当成真键收进引用集
   *   ⇒ parity 门报出 9 个「8 语种全缺失」的**假引用**。
   * ⇒ 教训：「属性名」是扫描器判「这是不是一个 i18n 键」的**唯一信号** ⇒ 名不副实就会污染引用集。
   */
  stageKey: string
  source: 'journey' | 'trace-alias'
}

const mappings: readonly JourneyStageMapping[] = [
  { journeyStage: 'received', waterfall: 'arrive', traceStages: ['received', 'ingress', 'request_received'], stageKey: 'received', source: 'journey' },
  { journeyStage: 'routing', waterfall: 'select', traceStages: ['routing', 'route_resolved'], stageKey: 'routing', source: 'journey' },
  { journeyStage: 'model_queue', waterfall: 'model', traceStages: ['model_queue', 'model_enqueued', 'model_dequeued'], stageKey: 'model_queue', source: 'journey' },
  { journeyStage: 'credential_queue', waterfall: 'cred', traceStages: ['credential_queue', 'credential_enqueued', 'credential_dequeued', 'node_enqueued'], stageKey: 'credential_queue', source: 'journey' },
  { journeyStage: 'node_selection', waterfall: 'select', traceStages: ['node_selection', 'node_selected', 'credential_selected'], stageKey: 'node_selection', source: 'journey' },
  { journeyStage: 'upstream', waterfall: 'upstream', traceStages: ['upstream', 'attempt_started', 'first_byte'], stageKey: 'upstream', source: 'journey' },
  { journeyStage: 'streaming', waterfall: 'stream', traceStages: ['streaming', 'stream', 'attempt_succeeded'], stageKey: 'streaming', source: 'journey' },
  { journeyStage: 'retrying', waterfall: null, traceStages: ['retrying', 'retry_scheduled', 'attempt_failed'], stageKey: 'retrying', source: 'journey' },
  { journeyStage: 'terminal', waterfall: 'total', traceStages: ['terminal', 'request_succeeded', 'request_failed', 'request_canceled'], stageKey: 'terminal', source: 'journey' },
]

export const JOURNEY_STAGE_MAP: Readonly<Record<RequestJourneyStage, JourneyStageMapping>> = Object.fromEntries(
  mappings.map((mapping) => [mapping.journeyStage, mapping]),
) as Record<RequestJourneyStage, JourneyStageMapping>

export const TRACE_STAGE_ALIASES: Readonly<Record<string, RequestJourneyStage>> = Object.fromEntries(
  mappings.flatMap((mapping) => mapping.traceStages.map((stage) => [stage, mapping.journeyStage] as const)),
)

export function getJourneyStageMapping(stage?: string | null): JourneyStageMapping | null {
  if (!stage) return null
  return JOURNEY_STAGE_MAP[stage as RequestJourneyStage] ?? null
}

export function traceStageToJourney(stage?: string | null): JourneyStageMapping | null {
  if (!stage) return null
  const journeyStage = TRACE_STAGE_ALIASES[stage.trim()]
  return journeyStage ? JOURNEY_STAGE_MAP[journeyStage] : null
}

export function journeyStageToWaterfall(stage?: string | null): WaterfallStageKey | null {
  return getJourneyStageMapping(stage)?.waterfall ?? null
}

export function stageLabelKey(stage?: string | null): string | null {
  return getJourneyStageMapping(stage)?.stageKey ?? traceStageToJourney(stage)?.stageKey ?? null
}

export function stageSource(stage?: string | null): JourneyStageMapping['source'] | 'unknown' {
  return getJourneyStageMapping(stage)?.source ?? (traceStageToJourney(stage) ? 'trace-alias' : 'unknown')
}
