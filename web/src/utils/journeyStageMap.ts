import type { RequestJourneyStage } from '../api/request-journeys'
import type { WaterfallStageKey } from './waterfallTimeline'

export interface JourneyStageMapping {
  journeyStage: RequestJourneyStage
  waterfall: WaterfallStageKey | null
  traceStages: readonly string[]
  labelKey: string
  source: 'journey' | 'trace-alias'
}

const mappings: readonly JourneyStageMapping[] = [
  { journeyStage: 'received', waterfall: 'arrive', traceStages: ['received', 'ingress', 'request_received'], labelKey: 'received', source: 'journey' },
  { journeyStage: 'routing', waterfall: 'select', traceStages: ['routing', 'route_resolved'], labelKey: 'routing', source: 'journey' },
  { journeyStage: 'model_queue', waterfall: 'model', traceStages: ['model_queue', 'model_enqueued', 'model_dequeued'], labelKey: 'model_queue', source: 'journey' },
  { journeyStage: 'credential_queue', waterfall: 'cred', traceStages: ['credential_queue', 'credential_enqueued', 'credential_dequeued', 'node_enqueued'], labelKey: 'credential_queue', source: 'journey' },
  { journeyStage: 'node_selection', waterfall: 'select', traceStages: ['node_selection', 'node_selected', 'credential_selected'], labelKey: 'node_selection', source: 'journey' },
  { journeyStage: 'upstream', waterfall: 'upstream', traceStages: ['upstream', 'attempt_started', 'first_byte'], labelKey: 'upstream', source: 'journey' },
  { journeyStage: 'streaming', waterfall: 'stream', traceStages: ['streaming', 'stream', 'attempt_succeeded'], labelKey: 'streaming', source: 'journey' },
  { journeyStage: 'retrying', waterfall: null, traceStages: ['retrying', 'retry_scheduled', 'attempt_failed'], labelKey: 'retrying', source: 'journey' },
  { journeyStage: 'terminal', waterfall: 'total', traceStages: ['terminal', 'request_succeeded', 'request_failed', 'request_canceled'], labelKey: 'terminal', source: 'journey' },
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
  return getJourneyStageMapping(stage)?.labelKey ?? traceStageToJourney(stage)?.labelKey ?? null
}

export function stageSource(stage?: string | null): JourneyStageMapping['source'] | 'unknown' {
  return getJourneyStageMapping(stage)?.source ?? (traceStageToJourney(stage) ? 'trace-alias' : 'unknown')
}
