// useRouteIncidents — composable that exposes the SSE-driven
// route-incident state to Vue components.
//
// The composable listens on the existing live-stream EventSource
// for `incident_update` envelopes and folds them into a reactive
// map keyed by `incident_id`. The `Other` aggregate lane is excluded
// by design — it never gets a diagnostic entry (see spec §"Read-Only
// Diagnosis API" / "Dashboard Experience").

import { computed, reactive, type ComputedRef, onBeforeUnmount } from 'vue'
import { isSuperAdmin } from '../store'
import type {
  LaneKey,
  RouteIncident,
  RouteIncidentAffectedLane,
  RouteIncidentState,
  RouteIncidentUpdate,
} from '../types/routeIncident'

interface IncidentById {
  [incidentId: string]: RouteIncident
}

interface IncidentByLane {
  // laneKey dimension -> laneKey value -> incidentId[]
  [dimValue: string]: { [laneValue: string]: string[] }
}

const incidentState = reactive({
  byId: {} as IncidentById,
  byLane: {
    vendor: {} as Record<string, string[]>,
    provider: {} as Record<string, string[]>,
    model: {} as Record<string, string[]>,
  },
  // For testability: bumped each time an envelope is applied.
  revision: 0,
})

let listeners = 0
let attached = false

// We attach to the singleton EventSource indirectly via a
// module-level listener that the store can invoke. This avoids a
// hard dependency on the liveStreamStore (it would create a cycle
// since liveStreamStore would have to import this file to know
// about incident_update handling).
type IncidentListener = (upd: RouteIncidentUpdate) => void
const bridgeListeners: IncidentListener[] = []

export function onIncidentUpdate(fn: IncidentListener): () => void {
  bridgeListeners.push(fn)
  return () => {
    const i = bridgeListeners.indexOf(fn)
    if (i >= 0) bridgeListeners.splice(i, 1)
  }
}

/**
 * applyIncidentUpdate is the single ingress for `incident_update`
 * envelopes. It is called from the liveStreamStore after the SSE
 * envelope is parsed. The store doesn't need to know anything
 * about incidents beyond this function's signature.
 */
export function applyIncidentUpdate(upd: RouteIncidentUpdate): void {
  if (!upd || !upd.incident_id) return
  if (upd.visible) {
    const inc: RouteIncident = {
      id: upd.incident_id,
      route_key: upd.route_key,
      state: upd.state,
      failure_streak: upd.failure_streak,
      recovery_streak: upd.recovery_streak,
      first_failure_at: new Date().toISOString(),
      last_failure_at: upd.last_error ? new Date().toISOString() : null,
      total_failures: upd.failure_streak,
      total_successes: 0,
      last_error_kind: upd.last_error?.kind ?? null,
      last_failure_stage: upd.last_error?.stage ?? null,
      version: 1,
      created_at: new Date().toISOString(),
      updated_at: upd.updated_at,
    }
    incidentState.byId[upd.incident_id] = inc
    indexByLanes(inc, upd.affected_lanes)
  } else {
    // Recovered — remove from all indexes but keep byId so a
    // detail-page deep link still works for the recovered audit row.
    const prev = incidentState.byId[upd.incident_id]
    delete incidentState.byId[upd.incident_id]
    deindexByLanes(upd.incident_id, prev?.route_key, upd.affected_lanes)
  }
  incidentState.revision += 1
  for (const fn of bridgeListeners) {
    try {
      fn(upd)
    } catch {
      /* listener errors must not stop other listeners */
    }
  }
}

function indexByLanes(inc: RouteIncident, lanes?: RouteIncidentAffectedLane[]) {
  if (!lanes) return
  for (const lane of lanes) {
    const dim = lane.dimension
    if (dim !== 'vendor' && dim !== 'provider' && dim !== 'model') continue
    const bucket = incidentState.byLane[dim]
    if (!bucket[lane.value]) bucket[lane.value] = []
    if (!bucket[lane.value].includes(inc.id)) {
      bucket[lane.value].push(inc.id)
    }
  }
}

function deindexByLanes(
  incidentId: string,
  routeKey?: RouteIncident['route_key'],
  lanes?: RouteIncidentAffectedLane[],
) {
  const keys: LaneKey[] = []
  if (lanes) {
    for (const lane of lanes) {
      if (lane.dimension === 'vendor' || lane.dimension === 'provider' || lane.dimension === 'model') {
        keys.push({ dimension: lane.dimension, value: lane.value })
      }
    }
  } else if (routeKey) {
    if (routeKey.model) keys.push({ dimension: 'model', value: routeKey.model })
    if (routeKey.provider_id) {
      keys.push({ dimension: 'provider', value: `provider-${routeKey.provider_id}` })
    }
  }
  for (const k of keys) {
    const bucket = incidentState.byLane[k.dimension][k.value]
    if (!bucket) continue
    const i = bucket.indexOf(incidentId)
    if (i >= 0) bucket.splice(i, 1)
    if (bucket.length === 0) delete incidentState.byLane[k.dimension][k.value]
  }
}

export function useRouteIncidents() {
  // Touch revision so Vue recomputes dependents when a new
  // envelope lands.
  const _bump = computed(() => incidentState.revision)

  // Active incidents for a given lane (used by SwimLane to know
  // whether to show the diagnose button).
  // 2026-08-14 V3.2: 支持 'queue' 维度（返回空数组）
  function incidentsForLane(dimension: 'vendor' | 'provider' | 'model' | 'queue', value: string): RouteIncident[] {
    if (dimension === 'queue') return []
    _bump.value
    const ids = incidentState.byLane[dimension][value] || []
    return ids.map((id) => incidentState.byId[id]).filter(Boolean)
  }

  // Whether the diagnostic control should be available for a given
  // lane. Returns false for the "Other" aggregate (when the caller
  // passes `isOthers`).
  function canDiagnose(lane: { isOthers: boolean }): boolean {
    return isSuperAdmin() && !lane.isOthers
  }

  const visibleIncidents: ComputedRef<RouteIncident[]> = computed(() => {
    _bump.value
    return Object.values(incidentState.byId).filter((i) => i.state === 'active' || i.state === 'recovering')
  })

  function getById(id: string): RouteIncident | undefined {
    _bump.value
    return incidentState.byId[id]
  }

  return {
    visibleIncidents,
    incidentsForLane,
    canDiagnose,
    getById,
  }
}

// Reset clears the local state — used by the dashboard when the
// user disconnects / the SSE connection closes.
export function resetRouteIncidents() {
  incidentState.byId = {}
  incidentState.byLane = { vendor: {}, provider: {}, model: {} }
  incidentState.revision += 1
}

// attach/detach are exported for tests; production code does not
// call them directly.
export function _attach() {
  if (attached) return
  attached = true
}
export function _detach() {
  attached = false
  listeners = 0
  resetRouteIncidents()
}

// suppress unused import warning in some toolchains
export type { IncidentByLane, IncidentById, RouteIncidentState }
