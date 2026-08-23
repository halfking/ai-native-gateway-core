// 2026-08-23 凭据显示：共享解析器解析失败路径、TTL/去重和契约一致性。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { getCredentialMonitorSummary } = vi.hoisted(() => ({
  getCredentialMonitorSummary: vi.fn(),
}))

vi.mock('../api/credential-monitor', () => ({
  getCredentialMonitorSummary,
}))

import {
  clearCredentialLabels,
  credentialDisplayName,
  credentialIdFromSyntheticModel,
  credentialLabelForId,
  displaySyntheticCredentialModel,
  loadCredentialLabels,
} from './useCredentialLabels'

function mockSummary(credentials: Array<{ id: number; label: string }>): void {
  getCredentialMonitorSummary.mockImplementation(async () => ({
    credentials: credentials.map((c) => ({
      id: c.id,
      label: c.label,
      provider_id: c.id,
      provider_name: `prov-${c.id}`,
    })),
  }))
}

beforeEach(() => {
  clearCredentialLabels()
  getCredentialMonitorSummary.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('credentialIdFromSyntheticModel', () => {
  it('parses cred-<id> synthetic model names', () => {
    expect(credentialIdFromSyntheticModel('cred-12')).toBe(12)
    expect(credentialIdFromSyntheticModel('cred-1')).toBe(1)
  })

  it('rejects non-numeric or empty suffixes', () => {
    expect(credentialIdFromSyntheticModel('cred-')).toBeNull()
    expect(credentialIdFromSyntheticModel('cred-abc')).toBeNull()
    expect(credentialIdFromSyntheticModel('cred--1')).toBeNull()
  })

  it('passes through real upstream model names', () => {
    expect(credentialIdFromSyntheticModel('gpt-5.6-luna')).toBeNull()
    expect(credentialIdFromSyntheticModel('')).toBeNull()
  })
})

describe('credentialDisplayName fallback', () => {
  it('returns em-dash for invalid or missing ids', () => {
    expect(credentialDisplayName(null)).toBe('—')
    expect(credentialDisplayName(undefined)).toBe('—')
    expect(credentialDisplayName(0)).toBe('—')
    expect(credentialDisplayName(-1)).toBe('—')
    expect(credentialDisplayName(Number.NaN)).toBe('—')
  })

  it('returns 凭据 #ID before labels load', () => {
    expect(credentialDisplayName(99)).toBe('凭据 #99')
  })
})

describe('loadCredentialLabels', () => {
  it('returns label once monitor-summary resolves', async () => {
    mockSummary([{ id: 42, label: 'openai-prod' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(42)).toBe('openai-prod')
    expect(credentialDisplayName(42)).toBe('openai-prod')
  })

  it('keeps fallback when label is blank', async () => {
    mockSummary([{ id: 42, label: '   ' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(42)).toBeNull()
    expect(credentialDisplayName(42)).toBe('凭据 #42')
  })

  it('drops non-positive ids and ignores invalid rows', async () => {
    mockSummary([
      { id: 1, label: 'alpha' },
      { id: 0, label: 'zero' },
      { id: -2, label: 'neg' },
      { id: 9, label: '' },
    ])
    await loadCredentialLabels()
    expect(credentialLabelForId(1)).toBe('alpha')
    expect(credentialLabelForId(0)).toBeNull()
    expect(credentialLabelForId(-2)).toBeNull()
    expect(credentialLabelForId(9)).toBeNull()
  })

  it('dedupes concurrent calls and shares the in-flight promise', async () => {
    let resolveFn: ((value: { credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }) => void) | null = null
    getCredentialMonitorSummary.mockImplementation(() => new Promise((resolve) => {
      resolveFn = (value) => resolve(value)
    }))
    const a = loadCredentialLabels()
    const b = loadCredentialLabels()
    expect(getCredentialMonitorSummary).toHaveBeenCalledTimes(1)
    resolveFn?.({ credentials: [] })
    await Promise.all([a, b])
  })

  it('keeps prior labels when the API call fails', async () => {
    mockSummary([{ id: 7, label: 'preloaded' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(7)).toBe('preloaded')

    getCredentialMonitorSummary.mockImplementationOnce(async () => {
      throw new Error('boom')
    })
    await loadCredentialLabels(true) // force refresh
    expect(credentialLabelForId(7)).toBe('preloaded')
  })

  it('forces a refresh when force=true', async () => {
    mockSummary([{ id: 1, label: 'first' }])
    await loadCredentialLabels()
    expect(credentialLabelForId(1)).toBe('first')

    mockSummary([{ id: 1, label: 'updated' }])
    await loadCredentialLabels(true)
    expect(credentialLabelForId(1)).toBe('updated')
  })
})

describe('displaySyntheticCredentialModel', () => {
  it('passes real model names through unchanged', async () => {
    mockSummary([])
    await loadCredentialLabels()
    expect(displaySyntheticCredentialModel('gpt-5.6-luna')).toBe('gpt-5.6-luna')
  })

  it('substitutes label for cred-<id>', async () => {
    mockSummary([{ id: 22, label: 'glm-key' }])
    await loadCredentialLabels()
    expect(displaySyntheticCredentialModel('cred-22')).toBe('glm-key')
  })

  it('falls back to 凭据 #ID for cred-<id> when no label cached', async () => {
    mockSummary([])
    await loadCredentialLabels()
    expect(displaySyntheticCredentialModel('cred-77')).toBe('凭据 #77')
  })
})
