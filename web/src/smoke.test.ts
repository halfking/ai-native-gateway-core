import { describe, expect, it } from 'vitest'

// smoke.test.ts — v6.0 audit T11 (2026-06-22)
// Verifies the vitest runner itself works on this codebase.
//
// The web/ directory historically had 0 tests, which fed the v6.0 audit
// §4 finding that "Vue TypeScript errors can ship undetected" (the
// ModelsTab.vue 6/21 single-day 6 commits). This file is the smallest
// possible test — it just confirms vitest discovers and runs .test.ts
// files. Future tests should live next to their subject file
// (e.g. src/utils/format.test.ts next to src/utils/format.ts).
describe('vitest smoke test', () => {
  it('runs a basic assertion', () => {
    expect(1 + 1).toBe(2)
  })

  it('supports async/await', async () => {
    const result = await Promise.resolve('hello')
    expect(result).toBe('hello')
  })

  it('supports describe + it nesting', () => {
    const arr = [1, 2, 3]
    expect(arr.filter((n) => n > 1)).toEqual([2, 3])
  })
})