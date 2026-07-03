// liveStreamDisplay unit tests.
import { describe, it, expect } from 'vitest'
import { errorKindLabel, errorKindBg, statusCategory, modelShortLabel, providerShortLabel, latencyLabel } from './liveStreamDisplay'

describe('errorKindLabel', () => {
  it('maps upstream 5xx to "5xx"', () => {
    expect(errorKindLabel('upstream_5xx')).toBe('5xx')
    expect(errorKindLabel('server_5xx')).toBe('5xx')
    expect(errorKindLabel('provider_overloaded')).toBe('5xx')
  })

  it('maps timeout to "timeout"', () => {
    expect(errorKindLabel('timeout')).toBe('timeout')
    // upstream_timeout is a SERVER timeout, so the priority order
    // routes it to the 5xx bucket (matches "upstream" first).
    expect(errorKindLabel('upstream_timeout')).toBe('5xx')
  })

  it('maps client_disconnect to "disc"', () => {
    expect(errorKindLabel('client_disconnect')).toBe('disc')
    expect(errorKindLabel('network_reset')).toBe('disc')
  })

  it('maps rate/quota to "rate" or "quota"', () => {
    expect(errorKindLabel('rate_limit')).toBe('rate')
    expect(errorKindLabel('quota_exceeded')).toBe('rate')
  })

  it('maps auth to "auth"', () => {
    expect(errorKindLabel('unauthorized')).toBe('auth')
    expect(errorKindLabel('forbidden')).toBe('auth')
  })

  it('maps model_not_found to "no model"', () => {
    expect(errorKindLabel('model_not_found')).toBe('no model')
    expect(errorKindLabel('no_route')).toBe('no model')
  })

  it('falls back to truncated raw kind for unknown', () => {
    // slice(0, 8) gives 8 chars max: "something" (9 chars) → "somethin"
    expect(errorKindLabel('something')).toBe('somethin')
    expect(errorKindLabel('short')).toBe('short')
    expect(errorKindLabel('')).toBe('')
    expect(errorKindLabel(undefined)).toBe('')
    expect(errorKindLabel(null)).toBe('')
  })

  it('handles underscore replacement in fallback', () => {
    // 8-char cap applies AFTER replacing underscores with spaces
    expect(errorKindLabel('weird_kind_here')).toBe('weird ki')
  })
})

describe('errorKindBg', () => {
  it('returns amber for timeout/disconnect', () => {
    expect(errorKindBg('timeout')).toBe('rgba(245, 158, 11, 0.22)')
    expect(errorKindBg('client_disconnect')).toBe('rgba(245, 158, 11, 0.22)')
  })
  it('returns red for 5xx', () => {
    expect(errorKindBg('upstream_5xx')).toBe('rgba(239, 68, 68, 0.22)')
  })
  it('returns yellow for 4xx', () => {
    expect(errorKindBg('rate_limit')).toBe('rgba(251, 191, 36, 0.22)')
  })
  it('returns purple for routing/not_found', () => {
    expect(errorKindBg('model_not_found')).toBe('rgba(167, 139, 250, 0.22)')
  })
  it('returns transparent for empty', () => {
    expect(errorKindBg('')).toBe('transparent')
    expect(errorKindBg(undefined)).toBe('transparent')
  })
})

describe('statusCategory', () => {
  it('returns success/in_progress directly', () => {
    expect(statusCategory('success', null)).toBe('success')
    expect(statusCategory('in_progress', null)).toBe('in_progress')
  })
  it('classifies failure by error_kind', () => {
    expect(statusCategory('failure', 'upstream_5xx')).toBe('failure_5xx')
    expect(statusCategory('failure', 'timeout')).toBe('failure_timeout')
    expect(statusCategory('failure', 'rate_limit')).toBe('failure_4xx')
    expect(statusCategory('failure', 'model_not_found')).toBe('failure_not_found')
  })
})

describe('modelShortLabel', () => {
  it('returns canonical codes for top vendors', () => {
    expect(modelShortLabel('gpt-4o')).toBe('GPT')
    expect(modelShortLabel('claude-3-5-sonnet')).toBe('CLD')
    expect(modelShortLabel('qwen2.5')).toBe('QWN')
    expect(modelShortLabel('glm-4')).toBe('GLM')
    expect(modelShortLabel('deepseek-v3')).toBe('DSK')
  })
  it('returns ??? for unknown', () => {
    expect(modelShortLabel('random-model')).toBe('???')
    expect(modelShortLabel('')).toBe('???')
  })
})

describe('providerShortLabel', () => {
  it('returns canonical codes for top vendors', () => {
    expect(providerShortLabel('openai')).toBe('OPEN')
    expect(providerShortLabel('anthropic')).toBe('ANTH')
    expect(providerShortLabel('azure-openai')).toBe('AZR')
  })
  it('returns ??? for unknown', () => {
    expect(providerShortLabel('')).toBe('???')
    expect(providerShortLabel(undefined)).toBe('???')
  })
})

describe('latencyLabel', () => {
  it('formats ms < 1000 as integer ms', () => {
    expect(latencyLabel(320, false)).toBe('320ms')
    expect(latencyLabel(89, false)).toBe('89ms')
  })
  it('formats ms < 10000 as fixed s', () => {
    expect(latencyLabel(1200, false)).toBe('1.2s')
  })
  it('formats >= 10000 as integer s', () => {
    expect(latencyLabel(12000, false)).toBe('12s')
  })
  it('returns … when in progress', () => {
    expect(latencyLabel(null, true)).toBe('…')
    expect(latencyLabel(500, true)).toBe('…')
  })
  it('returns — when null or negative', () => {
    expect(latencyLabel(null, false)).toBe('—')
    expect(latencyLabel(-1, false)).toBe('—')
  })
})
