// useTurnTitleSummary.test.ts — F2-#9 fallback lookup (2026-09-05).
import { beforeEach, describe, expect, it, vi } from 'vitest'

const listSessionTurns = vi.fn()
vi.mock('../api/sessions_v2', () => ({
  listSessionTurns: (...args: unknown[]) => listSessionTurns(...args),
}))

import {
  clearTurnTitleSummaryCache,
  lookupTurnTitleSummary,
  useTurnTitleSummary,
} from './useTurnTitleSummary'

const { ensureTurnTitleSummary } = useTurnTitleSummary()

beforeEach(() => {
  listSessionTurns.mockReset()
  clearTurnTitleSummaryCache()
})

describe('useTurnTitleSummary', () => {
  it('fetches the turns list once per session and maps trimmed title/summary', async () => {
    listSessionTurns.mockResolvedValue({
      turns: [
        { turn_no: 1, title: '  hello ', summary: 'world' },
        { turn_no: 2, title: '', summary: undefined },
      ],
    })
    await ensureTurnTitleSummary('s1')
    expect(listSessionTurns).toHaveBeenCalledWith('s1', { limit: 200 })
    expect(lookupTurnTitleSummary('s1', 1)).toEqual({ title: 'hello', summary: 'world' })
    expect(lookupTurnTitleSummary('s1', 2)).toEqual({ title: '', summary: '' })
    expect(lookupTurnTitleSummary('s1', 3)).toEqual({ title: '', summary: '' })

    // Cached per session: a second ensure must not refetch.
    await ensureTurnTitleSummary('s1')
    expect(listSessionTurns).toHaveBeenCalledTimes(1)
  })

  it('leaves failed sessions uncached so the next open retries', async () => {
    listSessionTurns.mockRejectedValueOnce(new Error('boom'))
    await ensureTurnTitleSummary('s2')
    expect(lookupTurnTitleSummary('s2', 1)).toEqual({ title: '', summary: '' })

    listSessionTurns.mockResolvedValue({ turns: [{ turn_no: 1, title: 't', summary: 's' }] })
    await ensureTurnTitleSummary('s2')
    expect(listSessionTurns).toHaveBeenCalledTimes(2)
    expect(lookupTurnTitleSummary('s2', 1)).toEqual({ title: 't', summary: 's' })
  })

  it('returns the empty fallback without fetching for null turn numbers', () => {
    expect(lookupTurnTitleSummary('s3', null)).toEqual({ title: '', summary: '' })
    expect(lookupTurnTitleSummary('', 1)).toEqual({ title: '', summary: '' })
    expect(listSessionTurns).not.toHaveBeenCalled()
  })

  it('dedupes concurrent ensures for the same session', async () => {
    let resolveFn!: (value: unknown) => void
    listSessionTurns.mockImplementation(() => new Promise((resolve) => { resolveFn = resolve }))
    const a = ensureTurnTitleSummary('s4')
    const b = ensureTurnTitleSummary('s4')
    resolveFn({ turns: [] })
    await Promise.all([a, b])
    expect(listSessionTurns).toHaveBeenCalledTimes(1)
  })

  it('clearTurnTitleSummaryCache drops a single session or everything', async () => {
    listSessionTurns.mockResolvedValue({ turns: [] })
    await ensureTurnTitleSummary('a')
    await ensureTurnTitleSummary('b')

    clearTurnTitleSummaryCache('a')
    await ensureTurnTitleSummary('a')
    expect(listSessionTurns).toHaveBeenCalledTimes(3)

    clearTurnTitleSummaryCache()
    await ensureTurnTitleSummary('b')
    expect(listSessionTurns).toHaveBeenCalledTimes(4)
  })
})
