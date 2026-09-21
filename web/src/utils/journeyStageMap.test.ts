import { describe, expect, it } from 'vitest'
import {
  JOURNEY_STAGE_MAP,
  TRACE_STAGE_ALIASES,
  getJourneyStageMapping,
  journeyStageToWaterfall,
  stageLabelKey,
  stageSource,
  traceStageToJourney,
} from './journeyStageMap'

const knownStages = ['received', 'routing', 'model_queue', 'credential_queue', 'node_selection', 'upstream', 'streaming', 'retrying', 'terminal'] as const

describe('journeyStageMap', () => {
  it('maps every journey stage to a stable label and waterfall stage', () => {
    expect(Object.keys(JOURNEY_STAGE_MAP)).toEqual(expect.arrayContaining([...knownStages]))
    for (const stage of knownStages) {
      expect(getJourneyStageMapping(stage)?.labelKey).toBe(stage)
      expect(stageLabelKey(stage)).toBe(stage)
    }
    expect(journeyStageToWaterfall('upstream')).toBe('upstream')
    expect(journeyStageToWaterfall('retrying')).toBeNull()
  })

  it('resolves trace aliases and preserves source metadata', () => {
    expect(traceStageToJourney('route_resolved')?.journeyStage).toBe('routing')
    expect(stageSource('route_resolved')).toBe('trace-alias')
    expect(TRACE_STAGE_ALIASES.attempt_failed).toBe('retrying')
  })

  it('returns null/unknown for unsupported stages', () => {
    expect(getJourneyStageMapping('future_stage')).toBeNull()
    expect(traceStageToJourney('future_stage')).toBeNull()
    expect(stageLabelKey('future_stage')).toBeNull()
    expect(stageSource('future_stage')).toBe('unknown')
  })

  it('has unique journey keys and alias ownership', () => {
    expect(new Set(Object.keys(JOURNEY_STAGE_MAP)).size).toBe(knownStages.length)
    expect(Object.keys(TRACE_STAGE_ALIASES).length).toBeGreaterThan(knownStages.length)
  })
})
