// useLiveStreamFilters.test.ts — 多维过滤器状态管理单元测试

import { beforeEach, describe, it, expect } from 'vitest'
import { ref } from 'vue'
import { useLiveStreamFilters } from './useLiveStreamFilters'
import { LIVE_STREAM_PREFERENCES_STORAGE_KEY } from './liveStreamPreferences'
import type { SwimLane, RequestTile } from '../types/swimlane'

// Test helper: create minimal RequestTile for testing
function makeRequest(partial: Partial<RequestTile>): RequestTile {
  return {
    request_id: '',
    timestamp: '',
    model: '',
    vendor: 'other',
    provider: '',
    status: 'success',
    ...partial,
  } as RequestTile
}

// Test helper: create minimal SwimLane for testing
function makeLane(id: string, requests: RequestTile[]): SwimLane {
  return {
    id,
    name: id,
    dimension: 'provider',
    requests,
    stats: { total: requests.length, success: 0, failure: 0 },
    isOthers: false,
  }
}

describe('useLiveStreamFilters', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('restores and persists all filter selections', () => {
    localStorage.setItem(LIVE_STREAM_PREFERENCES_STORAGE_KEY, JSON.stringify({
      version: 1,
      groupBy: 'queue',
      mode: 'small',
      filters: {
        requestTypes: ['probe'],
        statuses: ['failure'],
        models: ['gpt-4o'],
        providers: ['provider-a'],
        vendors: ['openai'],
        agents: ['zcode'],
      },
      queue: { depthOpen: false, statusFilter: { active: true, degraded: true, manualDisabled: true, exhausted: true } },
    }))

    const filters = useLiveStreamFilters({ lanes: ref<SwimLane[]>([]) })
    expect([...filters.requestTypeFilter.value]).toEqual(['probe'])
    expect([...filters.statusFilter.value]).toEqual(['failure'])
    expect([...filters.modelFilter.value]).toEqual(['gpt-4o'])
    expect([...filters.providerFilter.value]).toEqual(['provider-a'])
    expect([...filters.vendorFilter.value]).toEqual(['openai'])
    expect([...filters.agentFilter.value]).toEqual(['zcode'])

    filters.applyAgentFilter(['OpenCode'])
    const saved = JSON.parse(localStorage.getItem(LIVE_STREAM_PREFERENCES_STORAGE_KEY) || '{}')
    expect(saved.filters.agents).toEqual(['opencode'])
    expect(saved.filters.providers).toEqual(['provider-a'])
  })

  it('initializes with empty filters (show all)', () => {
    const lanes = ref<SwimLane[]>([])
    const filters = useLiveStreamFilters({ lanes })

    expect(filters.requestTypeFilter.value.size).toBe(2) // business + probe
    expect(filters.statusFilter.value.size).toBe(0)
    expect(filters.modelFilter.value.size).toBe(0)
    expect(filters.providerFilter.value.size).toBe(0)
    expect(filters.vendorFilter.value.size).toBe(0)
    expect(filters.agentFilter.value.size).toBe(0)
    expect(filters.activeFilterCount.value).toBe(0)
  })

  it('toggleRequestType deselects one type but never leaves empty', () => {
    const lanes = ref<SwimLane[]>([])
    const filters = useLiveStreamFilters({ lanes })

    expect(filters.requestTypeFilter.value.size).toBe(2)

    filters.toggleRequestType('business')
    expect(filters.requestTypeFilter.value.has('probe')).toBe(true)
    expect(filters.requestTypeFilter.value.has('business')).toBe(false)
    expect(filters.activeFilterCount.value).toBe(1)

    filters.toggleRequestType('probe')
    expect(filters.requestTypeFilter.value.size).toBe(1)

    filters.toggleRequestType('business')
    expect(filters.requestTypeFilter.value.size).toBe(2)
    expect(filters.activeFilterCount.value).toBe(0)
  })

  it('applyStatusFilter updates statusFilter', () => {
    const lanes = ref<SwimLane[]>([])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyStatusFilter(['success', 'error'])
    expect(filters.statusFilter.value.size).toBe(2)
    expect(filters.statusFilterSelected.value).toEqual(['success', 'error'])
    expect(filters.activeFilterCount.value).toBe(2)
  })

  it('applyModelFilter case-insensitive normalization', () => {
    const lanes = ref<SwimLane[]>([])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyModelFilter(['GPT-4', 'Claude-3'])
    expect(filters.normalizedModelFilter.value).toEqual(['gpt-4', 'claude-3'])
  })

  it('clearAllFilters resets all filters', () => {
    const lanes = ref<SwimLane[]>([])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyStatusFilter(['success'])
    filters.applyModelFilter(['gpt-4'])
    filters.applyProviderFilter(['openai'])
    filters.toggleRequestType('business')
    expect(filters.activeFilterCount.value).toBeGreaterThan(0)

    filters.clearAllFilters()
    expect(filters.requestTypeFilter.value.size).toBe(2)
    expect(filters.statusFilter.value.size).toBe(0)
    expect(filters.modelFilter.value.size).toBe(0)
    expect(filters.providerFilter.value.size).toBe(0)
    expect(filters.activeFilterCount.value).toBe(0)
  })

  it('availableModels extracts unique models from lanes', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', model: 'GPT-4' }),
        makeRequest({ request_id: 'r2', model: 'gpt-4' }),
        makeRequest({ request_id: 'r3', model: 'Claude-3' }),
      ]),
      makeLane('lane2', [
        makeRequest({ request_id: 'r4', model: 'GPT-4' }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    const models = filters.availableModels.value
    expect(models).toContain('GPT-4')
    expect(models).toContain('Claude-3')
    expect(models.length).toBe(2)
  })

  it('availableStatuses extracts unique statuses from lanes', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', status: 'success' }),
        makeRequest({ request_id: 'r2', status: 'error' }),
        makeRequest({ request_id: 'r3', status: 'success' }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    expect(filters.availableStatuses.value.sort()).toEqual(['error', 'success'])
  })

  it('filteredLanes filters by requestType', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', is_probe: false }),
        makeRequest({ request_id: 'r2', is_probe: true }),
        makeRequest({ request_id: 'r3', is_probe: false }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    expect(filters.filteredLanes.value[0].requests.length).toBe(3)

    filters.toggleRequestType('business')
    expect(filters.filteredLanes.value[0].requests.length).toBe(1)
    expect(filters.filteredLanes.value[0].requests[0].request_id).toBe('r2')
  })

  it('filteredLanes filters by status', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', status: 'success' }),
        makeRequest({ request_id: 'r2', status: 'error' }),
        makeRequest({ request_id: 'r3', status: 'success' }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyStatusFilter(['error'])
    expect(filters.filteredLanes.value[0].requests.length).toBe(1)
    expect(filters.filteredLanes.value[0].requests[0].request_id).toBe('r2')
  })

  it('filteredLanes filters by model (case-insensitive)', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', model: 'GPT-4' }),
        makeRequest({ request_id: 'r2', model: 'claude-3' }),
        makeRequest({ request_id: 'r3', model: 'gpt-4' }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyModelFilter(['GPT-4'])
    const filtered = filters.filteredLanes.value[0].requests
    expect(filtered.length).toBe(2)
    expect(filtered.map(r => r.request_id).sort()).toEqual(['r1', 'r3'])
  })

  it('filteredLanes filters by provider', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', provider: 'openai' }),
        makeRequest({ request_id: 'r2', provider: 'anthropic' }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyProviderFilter(['openai'])
    expect(filters.filteredLanes.value[0].requests.length).toBe(1)
    expect(filters.filteredLanes.value[0].requests[0].request_id).toBe('r1')
  })

  it('filteredLanes filters by vendor', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', vendor: 'openai' }),
        makeRequest({ request_id: 'r2', vendor: 'anthropic' }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyVendorFilter(['anthropic'])
    expect(filters.filteredLanes.value[0].requests.length).toBe(1)
    expect(filters.filteredLanes.value[0].requests[0].request_id).toBe('r2')
  })

  it('filteredLanes filters by agent (lowercase)', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', agent_name: 'zcode' }),
        makeRequest({ request_id: 'r2', agent_name: 'opencode' }),
        makeRequest({ request_id: 'r3', agent_name: 'ZCODE' }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyAgentFilter(['zcode'])
    const filtered = filters.filteredLanes.value[0].requests
    expect(filtered.length).toBe(2)
    expect(filtered.map(r => r.request_id).sort()).toEqual(['r1', 'r3'])
  })

  it('applyAgentFilter normalizes selections to lowercase', () => {
    // Regression: the extracted composable dropped the .toLowerCase() that the
    // in-component code had. Matching still works (filteredLanes lowercases the
    // request side), but the stored set must stay lowercase so the filter
    // dialog's checkbox state (draft.has(opt), opt always lowercase) stays in
    // sync. Pass mixed-case input and assert lowercase storage.
    const lanes = ref<SwimLane[]>([makeLane('lane1', [])])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyAgentFilter(['ZCODE', 'OpenCode'])
    expect([...filters.agentFilter.value]).toEqual(['zcode', 'opencode'])
  })

  it('filteredLanes removes empty lanes after filtering', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [makeRequest({ request_id: 'r1', status: 'success' })]),
      makeLane('lane2', [makeRequest({ request_id: 'r2', status: 'error' })]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyStatusFilter(['success'])
    expect(filters.filteredLanes.value.length).toBe(1)
    expect(filters.filteredLanes.value[0].id).toBe('lane1')
  })

  it('filteredLanes combines multiple filters (AND logic)', () => {
    const lanes = ref<SwimLane[]>([
      makeLane('lane1', [
        makeRequest({ request_id: 'r1', status: 'success', model: 'gpt-4', provider: 'openai' }),
        makeRequest({ request_id: 'r2', status: 'error', model: 'gpt-4', provider: 'openai' }),
        makeRequest({ request_id: 'r3', status: 'success', model: 'claude-3', provider: 'anthropic' }),
      ]),
    ])
    const filters = useLiveStreamFilters({ lanes })

    filters.applyStatusFilter(['success'])
    filters.applyModelFilter(['gpt-4'])
    filters.applyProviderFilter(['openai'])

    const filtered = filters.filteredLanes.value[0].requests
    expect(filtered.length).toBe(1)
    expect(filtered[0].request_id).toBe('r1')
  })

  it('activeFilterCount counts active dimensions correctly', () => {
    const lanes = ref<SwimLane[]>([])
    const filters = useLiveStreamFilters({ lanes })

    expect(filters.activeFilterCount.value).toBe(0)

    filters.toggleRequestType('business')
    expect(filters.activeFilterCount.value).toBe(1)

    filters.applyStatusFilter(['success', 'error'])
    expect(filters.activeFilterCount.value).toBe(3)

    filters.applyModelFilter(['gpt-4'])
    expect(filters.activeFilterCount.value).toBe(4)
  })
})
