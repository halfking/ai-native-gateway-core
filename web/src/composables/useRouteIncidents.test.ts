// useRouteIncidents.test.ts — unit tests for the diagnostic SSE state
// composable. Verifies the reducer logic: an `incident_update` with
// visible=false removes the incident from the lane index, and the
// "Other" lane is excluded from the diagnosis surface.

import { describe, expect, it, beforeEach } from 'vitest'
import {
  applyIncidentUpdate,
  resetRouteIncidents,
  useRouteIncidents,
} from './useRouteIncidents'
import type { RouteIncidentUpdate } from '../types/routeIncident'

function makeUpdate(over: Partial<RouteIncidentUpdate> = {}): RouteIncidentUpdate {
  return {
    type: 'incident_update',
    incident_id: 'inc-1',
    state: 'active',
    failure_streak: 3,
    recovery_streak: 0,
    visible: true,
    affected_lanes: [
      { dimension: 'model', value: 'gpt-4o' },
      { dimension: 'provider', value: 'provider-7' },
    ],
    last_error: { kind: 'rate_limited', stage: 'upstream' },
    route_key: {
      endpoint_protocol: 'openai_chat_completions',
      model: 'gpt-4o',
      provider_id: 7,
      credential_id: 99,
    },
    updated_at: new Date().toISOString(),
    ...over,
  }
}

describe('useRouteIncidents', () => {
  beforeEach(() => {
    resetRouteIncidents()
  })

  it('registers an active incident under its affected lanes', () => {
    applyIncidentUpdate(makeUpdate())
    const { incidentsForLane } = useRouteIncidents()
    const modelLane = incidentsForLane('model', 'gpt-4o')
    expect(modelLane).toHaveLength(1)
    expect(modelLane[0].id).toBe('inc-1')
    expect(modelLane[0].state).toBe('active')

    const providerLane = incidentsForLane('provider', 'provider-7')
    expect(providerLane).toHaveLength(1)
  })

  it('updates the same incident on a second envelope without duplication', () => {
    applyIncidentUpdate(makeUpdate())
    applyIncidentUpdate(
      makeUpdate({
        state: 'recovering',
        failure_streak: 0,
        recovery_streak: 1,
        last_error: undefined,
      }),
    )
    const { incidentsForLane } = useRouteIncidents()
    const modelLane = incidentsForLane('model', 'gpt-4o')
    expect(modelLane).toHaveLength(1)
    expect(modelLane[0].state).toBe('recovering')
    expect(modelLane[0].recovery_streak).toBe(1)
  })

  it('removes the incident when visible=false', () => {
    applyIncidentUpdate(makeUpdate())
    applyIncidentUpdate(makeUpdate({ state: 'recovered', visible: false }))
    const { incidentsForLane } = useRouteIncidents()
    expect(incidentsForLane('model', 'gpt-4o')).toHaveLength(0)
    expect(incidentsForLane('provider', 'provider-7')).toHaveLength(0)
  })

  it('forbids diagnosis for the "Other" aggregate lane', () => {
    const { canDiagnose } = useRouteIncidents()
    expect(canDiagnose({ isOthers: false })).toBe(true)
    expect(canDiagnose({ isOthers: true })).toBe(false)
  })

  it('returns no incidents for an unknown lane', () => {
    applyIncidentUpdate(makeUpdate())
    const { incidentsForLane } = useRouteIncidents()
    expect(incidentsForLane('model', 'unknown-model')).toHaveLength(0)
    expect(incidentsForLane('vendor', '__unknown__')).toHaveLength(0)
  })

  it('ignores envelopes with no affected_lanes (defensive)', () => {
    applyIncidentUpdate(
      makeUpdate({ affected_lanes: undefined }),
    )
    const { incidentsForLane } = useRouteIncidents()
    expect(incidentsForLane('model', 'gpt-4o')).toHaveLength(0)
  })
})
