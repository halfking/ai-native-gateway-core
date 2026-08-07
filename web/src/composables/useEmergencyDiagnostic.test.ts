// useEmergencyDiagnostic.test.ts — 应急诊断弹窗状态 composable 单元测试
//
// 覆盖：初始状态 / 打开弹窗时填充 payload / 关闭弹窗 / recovered 回调。

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { useEmergencyDiagnostic } from './useEmergencyDiagnostic'

describe('useEmergencyDiagnostic', () => {
  let api: ReturnType<typeof useEmergencyDiagnostic>

  beforeEach(() => {
    api = useEmergencyDiagnostic()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('initial state: modal hidden, empty payload fields', () => {
    expect(api.showEmergencyDiagnostic.value).toBe(false)
    expect(api.emergencyCredentialId.value).toBe(0)
    expect(api.emergencyModel.value).toBe('')
    expect(api.emergencyLaneName.value).toBe('')
  })

  it('handleEmergencyDiagnose fills payload and opens the modal', () => {
    api.handleEmergencyDiagnose({ credentialId: 42, model: 'gpt-4o', laneName: 'openai' })
    expect(api.emergencyCredentialId.value).toBe(42)
    expect(api.emergencyModel.value).toBe('gpt-4o')
    expect(api.emergencyLaneName.value).toBe('openai')
    expect(api.showEmergencyDiagnostic.value).toBe(true)
  })

  it('handleEmergencyClose hides the modal but keeps payload fields', () => {
    api.handleEmergencyDiagnose({ credentialId: 7, model: 'claude-3-5-sonnet', laneName: 'anthropic' })
    api.handleEmergencyClose()
    expect(api.showEmergencyDiagnostic.value).toBe(false)
    expect(api.emergencyCredentialId.value).toBe(7)
    expect(api.emergencyModel.value).toBe('claude-3-5-sonnet')
  })

  it('handleEmergencyRecovered logs a confirmation', () => {
    const logSpy = vi.spyOn(console, 'log').mockImplementation(() => {})
    api.handleEmergencyRecovered()
    expect(logSpy).toHaveBeenCalledWith('Credential recovered successfully')
  })
})
