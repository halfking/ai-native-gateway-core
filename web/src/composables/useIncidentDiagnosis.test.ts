// useIncidentDiagnosis.test.ts — 诊断工作台（RouteIncidentDrawer）状态 composable 单元测试
//
// 覆盖：初始状态 / handleDiagnose 打开并填充 / closeDiagnose 关闭并清空 /
//       handleRequestFromDrawer 先关闭再触发注入的 onOpenRequest 回调。

import { describe, it, expect, vi } from 'vitest'
import { useIncidentDiagnosis } from './useIncidentDiagnosis'
import type { RouteIncident } from '../types/routeIncident'

function makeIncident(): RouteIncident {
  return {
    id: 'inc_1',
    route_key: {
      endpoint_protocol: 'openai/v1',
      model: 'gpt-4o',
      provider_id: 1,
      credential_id: 42,
    },
    state: 'active',
    failure_streak: 3,
    recovery_streak: 0,
    first_failure_at: '2026-08-06T00:00:00Z',
    total_failures: 5,
    total_successes: 0,
    version: 1,
    created_at: '2026-08-06T00:00:00Z',
    updated_at: '2026-08-06T00:00:00Z',
  }
}

describe('useIncidentDiagnosis', () => {
  it('initial state: no active incident', () => {
    const onOpenRequest = vi.fn()
    const api = useIncidentDiagnosis({ onOpenRequest })
    expect(api.activeIncidentId.value).toBeNull()
    expect(api.activeIncidentPreview.value).toBeNull()
  })

  it('handleDiagnose opens the drawer and fills incident id + preview', () => {
    const api = useIncidentDiagnosis({ onOpenRequest: vi.fn() })
    const incident = makeIncident()
    api.handleDiagnose('inc_1', incident)
    expect(api.activeIncidentId.value).toBe('inc_1')
    expect(api.activeIncidentPreview.value).toStrictEqual(incident)
  })

  it('closeDiagnose clears the active incident', () => {
    const api = useIncidentDiagnosis({ onOpenRequest: vi.fn() })
    api.handleDiagnose('inc_1', makeIncident())
    api.closeDiagnose()
    expect(api.activeIncidentId.value).toBeNull()
    expect(api.activeIncidentPreview.value).toBeNull()
  })

  it('handleRequestFromDrawer closes the drawer then triggers onOpenRequest', () => {
    const onOpenRequest = vi.fn()
    const api = useIncidentDiagnosis({ onOpenRequest })
    api.handleDiagnose('inc_1', makeIncident())

    api.handleRequestFromDrawer('req_99')

    // 先关闭 drawer
    expect(api.activeIncidentId.value).toBeNull()
    expect(api.activeIncidentPreview.value).toBeNull()
    // 再触发跳转回调
    expect(onOpenRequest).toHaveBeenCalledTimes(1)
    expect(onOpenRequest).toHaveBeenCalledWith('req_99')
  })
})
